"""Worker job entry point with contract validation, events, cancellation, and preflight."""

from __future__ import annotations

import argparse
import csv
import hashlib
import json
import os
import re
import signal
from dataclasses import dataclass
from datetime import datetime, timezone
from io import StringIO
from pathlib import Path
from threading import Event
from time import monotonic
from typing import Any, Callable, Mapping, Sequence

from .agents import AgentContext, AgentExecutor, validate_s1_contexts
from .artifacts import ArtifactItem, AtomicArtifactWriter
from .cancellation import check_cancel
from .contracts import WorkerRepository, WorkerTask, parse_worker_task
from .errors import CancelledFailure, ContractFailure, WorkerFailure
from .features import build_transition_dataset, chronological_split, partition_contiguous_indices, training_standardization
from .parameters import SimulationPreparationParameters, simulation_preparation_parameters
from .postgres_repository import (
    LeaseLostFailure,
    PostgresSettings,
    PostgresWorkerRepository,
    RepositoryConfigurationError,
    RepositoryOperationError,
    WorkerIdentity,
)
from .preprocessing import AlgorithmCore, PreprocessedDataset
from .simulation import SimulationOutcome, execute_simulation


HEARTBEAT_SCHEMA_VERSION = "worker.heartbeat.v1"
_DEFAULT_HEARTBEAT_FILE = Path("/var/lib/iot/runs/.worker-heartbeat.json")
_POSTGRES_PROFILE_RE = re.compile(r"(?m)^\s*profile:\s*postgres\s*$")
_POOL_CAPACITY_RE = re.compile(r"(?m)^\s*worker_pool_capacity:\s*1\s*$")
_CONFIG_VERSION_RE = re.compile(r"(?m)^\s*config_version:\s*platform-config\.v1\s*$")

# ``point-result.v1`` is a frozen artifact header.  Keeping it here avoids a
# row-dependent CSV header when a numerical implementation regresses.
_RESULT_CSV_FIELDNAMES = (
    "OriginalRunningIndex", "Time", "Agent", "RunMode", "ParameterProfileVersion", "LoadMappingVersion",
    "MappedLoadEstimate", "MappedLoadUnit", "I1", "I2", "I3", "I4", "TrueAverageCurrentSmoothed",
    "TrueAverageCurrentRaw", "Isum_3motors", "zl", "sd", "ImbalanceRate_3motors", "LocalPrediction",
    "GlobalPrediction", "FusedPrediction", "LocalGPStd", "GlobalGPStd", "GlobalSupport", "LocalCalibrationQ",
    "GlobalCalibrationQ", "FusedCalibrationQ", "LocalHalfWidth", "GlobalHalfWidth", "FusedHalfWidth",
    "LocalCalibratedVariance", "GlobalCalibratedVariance", "FusedCalibratedVariance", "FusionAlphaRaw",
    "FusionAlpha", "RecentLocalRMSE", "RecentGlobalRMSE", "LocalReliability", "GlobalReliability",
    "PredictionDisagreement", "GlobalClearlyBetter", "LocalClearlyBetter", "GlobalBetterCount", "LocalBetterCount",
    "FusedLowerBound", "FusedUpperBound", "FusedInsideInterval", "LoadStatus", "IsSpikeSample",
    "MotorZeroWarning", "SpikeReason", "HighCount", "LowCount", "LoadAlarmLevel", "LoadAlarmReason",
    "DataQualityAlarmLevel", "DataQualityAlarmReason", "OverallAlarmLevel", "OverallAlarmReason",
)
_RESULT_CSV_FIELDSET = frozenset(_RESULT_CSV_FIELDNAMES)


@dataclass(frozen=True)
class SimulationPreparation:
    """Validated frozen inputs for the complete M1 simulation pipeline."""

    datasets_by_agent: Mapping[int, PreprocessedDataset]
    agents: tuple[AgentContext, ...]


class WorkerRunner:
    """Run exactly one leased task through the narrow PostgreSQL repository surface."""

    def __init__(
        self,
        storage_root: Path,
        repository: WorkerRepository,
        core: AlgorithmCore | None = None,
        *,
        liveness_callback: Callable[[], None] | None = None,
        liveness_interval_seconds: float = 5.0,
    ) -> None:
        if liveness_interval_seconds <= 0:
            raise ValueError("Worker liveness interval must be positive.")
        self._storage_root = storage_root.resolve()
        self._repository = repository
        self._core = core or AlgorithmCore()
        self._agent_executor = AgentExecutor()
        self._liveness_callback = liveness_callback
        self._liveness_interval_seconds = liveness_interval_seconds

    def run(self, raw_task: Mapping[str, Any]) -> PreprocessedDataset | SimulationOutcome:
        """Validate a task, verify the input hash, and execute its authorized M1 work."""

        task = parse_worker_task(raw_task)
        repository_cancellation = self._repository.cancellation_context(task)
        start_task_heartbeat = getattr(repository_cancellation, "start_task_heartbeat", None)
        stop_task_heartbeat = getattr(repository_cancellation, "stop_task_heartbeat", None)
        task_heartbeat_stopped = False

        def stop_task_heartbeat_once() -> None:
            nonlocal task_heartbeat_stopped
            if callable(stop_task_heartbeat) and not task_heartbeat_stopped:
                stop_task_heartbeat()
                task_heartbeat_stopped = True

        cancellation = repository_cancellation
        if self._liveness_callback is not None:
            cancellation = _LivenessCancellationContext(
                cancellation,
                self._liveness_callback,
                minimum_interval_seconds=self._liveness_interval_seconds,
            )
        try:
            if callable(start_task_heartbeat):
                start_task_heartbeat()
            source = self._resolve_storage_path(task.dataset_relative_path)
            self._verify_dataset_hash(task, source, cancellation)
            if task.job_type == "DATASET_PREFLIGHT":
                return self._run_preflight(task, source, cancellation)
            parameters = simulation_preparation_parameters(task)
            return self._run_simulation(task, source, cancellation, parameters)
        except CancelledFailure as failure:
            # A bounded checkpoint already observed durable cancellation. Stop
            # renewal before the terminal confirmation so a CANCELLING state
            # cannot be mistaken for a stale RUNNING lease.
            stop_task_heartbeat_once()
            self._repository.commit_failure(task, _failure_payload(task, failure))
            raise
        except LeaseLostFailure:
            raise
        except WorkerFailure as failure:
            self._repository.commit_failure(task, _failure_payload(task, failure))
            raise
        finally:
            stop_task_heartbeat_once()

    def prepare_simulation(
        self,
        task: WorkerTask,
        datasets_by_agent: Mapping[int, PreprocessedDataset],
        parameters: SimulationPreparationParameters | None = None,
    ) -> SimulationPreparation:
        """Create generic contexts from Agent-effective preprocessing results."""

        parameters = parameters or simulation_preparation_parameters(task)
        agents: list[AgentContext] = []
        for snapshot in sorted(task.parameter_agents, key=lambda item: int(item["agent"])):
            agent = int(snapshot["agent"])
            agent_parameters = parameters.for_agent(agent)
            try:
                dataset = datasets_by_agent[agent]
            except KeyError as error:
                raise ContractFailure(f"SIMULATION preprocessing result is missing Agent {agent}.") from error
            partitions = partition_contiguous_indices(
                len(dataset.running_rows),
                int(agent_parameters.effective_parameters["split"]["agent_count"]),
                agent_parameters.minimum_partition_rows,
            )
            partition = partitions[agent - 1]
            feature_dataset = build_transition_dataset(
                [dataset.running_rows[index] for index in partition],
                n_lag=agent_parameters.n_lag,
                trend_gain=agent_parameters.trend_gain,
            )
            split = chronological_split(
                len(feature_dataset.values),
                train_ratio=agent_parameters.training_ratio,
                calibration_ratio=agent_parameters.calibration_ratio,
            )
            if (
                len(split.train) < agent_parameters.minimum_training
                or len(split.calibration) < agent_parameters.minimum_calibration
                or len(split.test) < agent_parameters.minimum_testing
            ):
                raise WorkerFailure("INSUFFICIENT_SAMPLES", "Agent partition does not satisfy frozen split minima.", "PREPROCESSING", agent, True)
            training_rows = [feature_dataset.values[index] for index in split.train]
            standardization = training_standardization(training_rows)
            agents.append(
                AgentContext(
                    agent=agent,
                    segment=str(snapshot["segment"]),
                    parameters=agent_parameters.effective_parameters,
                    feature_dataset=feature_dataset,
                    split=split,
                    standardization=standardization,
                    random_seed=agent_parameters.base_center_seed,
                    output_namespace=f"agent_{agent}",
                )
            )
        validate_s1_contexts(agents)
        return SimulationPreparation(datasets_by_agent=dict(datasets_by_agent), agents=tuple(agents))

    def _run_preflight(self, task: WorkerTask, source: Path, cancellation: Any) -> PreprocessedDataset:
        check_cancel(cancellation, "PREPROCESSING")
        self._emit(task, status="RUNNING", stage="PREPROCESSING", diagnostics={"job_type": task.job_type})
        dataset = self._core.preprocess_csv(source, cancellation=cancellation)
        check_cancel(cancellation, "PREPROCESSING")
        summary = self._core.preflight_summary(dataset)
        destination = self._resolve_storage_path(task.tmp_relative_directory).joinpath("preflight_summary.json")
        self._core.write_preflight_summary(summary, destination)
        check_cancel(cancellation, "PREPROCESSING")
        self._emit(
            task,
            status="RUNNING",
            stage="PREPROCESSING",
            diagnostics={"running_rows": len(dataset.running_rows), "summary_sha256": summary.summary_sha256},
        )
        self._repository.commit_preflight(task, summary.to_dict())
        return dataset

    def _run_simulation(
        self,
        task: WorkerTask,
        source: Path,
        cancellation: Any,
        parameters: SimulationPreparationParameters,
    ) -> SimulationOutcome:
        check_cancel(cancellation, "PREPROCESSING")
        self._emit(task, status="RUNNING", stage="PREPROCESSING", diagnostics={"job_type": task.job_type})
        datasets_by_agent = self._preprocess_agent_datasets(source, cancellation, parameters)
        check_cancel(cancellation, "PREPROCESSING")
        preparation = self.prepare_simulation(task, datasets_by_agent, parameters)
        stage_started = monotonic()
        current_stage = "PREPROCESSING"
        durations_ms: dict[str, int] = {}

        def progress(stage: str) -> None:
            nonlocal stage_started, current_stage
            now = monotonic()
            durations_ms[current_stage.lower()] = durations_ms.get(current_stage.lower(), 0) + int((now - stage_started) * 1000)
            current_stage = stage
            stage_started = now
            self._emit(
                task,
                status="RUNNING",
                stage=stage,
                diagnostics={"agent_count": len(preparation.agents), "feature_shapes": [agent.feature_dataset.shape for agent in preparation.agents]},
            )

        outcome = execute_simulation(
            preparation.agents,
            cancellation=cancellation,
            progress=progress,
            agent_executor=self._agent_executor,
        )
        now = monotonic()
        durations_ms[current_stage.lower()] = durations_ms.get(current_stage.lower(), 0) + int((now - stage_started) * 1000)
        repository_alarms = _repository_alarms(task, outcome)
        artifact_started = monotonic()
        items, manifest_sha256 = self._write_simulation_artifacts(task, preparation, outcome, durations_ms)
        durations_ms["generating_artifacts"] = int((monotonic() - artifact_started) * 1000)
        check_cancel(cancellation, "GENERATING_ARTIFACTS")
        self._repository.commit_simulation(
            task,
            manifest_sha256=manifest_sha256,
            artifacts=_repository_artifacts(task, items),
            alarms=repository_alarms,
            stage_durations_ms=durations_ms,
        )
        return outcome

    def _write_simulation_artifacts(
        self,
        task: WorkerTask,
        preparation: SimulationPreparation,
        outcome: SimulationOutcome,
        stage_durations_ms: Mapping[str, int],
    ) -> tuple[tuple[ArtifactItem, ...], str]:
        """Write and verify every required result before a single directory rename."""

        if task.run_id is None:
            raise WorkerFailure("INPUT_INVALID", "SIMULATION task has no run identifier.", "GENERATING_ARTIFACTS")
        tmp_directory = self._resolve_storage_path(task.tmp_relative_directory)
        committed_directory = self._resolve_storage_path(f"runs/{task.run_id}/committed")
        writer = AtomicArtifactWriter(tmp_directory, committed_directory)
        try:
            parameter_snapshot = task.raw["parameter_snapshot"]
            writer.write_json(
                "run_manifest.json",
                {
                    "schema_version": "run-manifest.v1",
                    "run_id": task.run_id,
                    "attempt_id": task.attempt_id,
                    "run_mode": task.raw["run_mode"],
                    "dataset": task.raw["dataset"],
                    "parameter_snapshot": parameter_snapshot,
                    "mapping_snapshot": task.raw["mapping_snapshot"],
                    "field_standard_snapshot": task.raw["field_standard_snapshot"],
                    "runtime": task.raw["runtime"],
                },
            )
            writer.write_json(
                "preprocessing_summary.json",
                {
                    "schema_version": "simulation-preprocessing-summary.v1",
                    "by_agent": {
                        str(agent): AlgorithmCore(dataset.config).preflight_summary(dataset).to_dict()
                        for agent, dataset in sorted(preparation.datasets_by_agent.items())
                    },
                },
            )
            writer.write_bytes("agent_partition_summary.csv", _csv_bytes(outcome.partition_rows), media_type="text/csv")
            writer.write_json("feature_schema.json", outcome.feature_schema)
            writer.write_json("anchor_summary.json", outcome.anchor_summary)
            writer.write_bytes("metrics.csv", _csv_bytes([result.metrics for result in outcome.agent_results]), media_type="text/csv")
            for result in outcome.agent_results:
                enriched = [_enrich_result_row(row, task) for row in result.rows]
                writer.write_bytes(f"results_agent_{result.agent}.csv", _result_csv_bytes(enriched, result.agent), media_type="text/csv")
            writer.write_bytes("alarms.csv", _csv_bytes(_artifact_alarms(outcome)), media_type="text/csv")
            writer.write_json(
                "diagnostics.json",
                {
                    **outcome.diagnostics,
                    "stage_durations_ms": dict(stage_durations_ms),
                    "agents": [result.diagnostics for result in outcome.agent_results],
                },
            )
            frozen_parameter_snapshot_sha256 = str(parameter_snapshot["sha256"])
            manifest = writer.build_manifest(
                run_id=task.run_id,
                run_mode=str(task.raw["run_mode"]),
                snapshot_sha256=frozen_parameter_snapshot_sha256,
            )
            if manifest["snapshot_sha256"] != frozen_parameter_snapshot_sha256:
                raise WorkerFailure(
                    "ARTIFACT_WRITE_FAILED",
                    "Artifact manifest snapshot SHA-256 must equal the frozen task parameter snapshot SHA-256.",
                    "GENERATING_ARTIFACTS",
                )
            items = writer.commit()
            manifest_item = next(item for item in items if item.name == "artifact_manifest.json")
            return items, manifest_item.sha256
        except Exception:
            writer.abandon()
            raise

    def _preprocess_agent_datasets(
        self,
        source: Path,
        cancellation: Any,
        parameters: SimulationPreparationParameters,
    ) -> dict[int, PreprocessedDataset]:
        """Reuse matching effective configurations only inside one frozen task."""

        cache: dict[tuple[object, ...], PreprocessedDataset] = {}
        datasets_by_agent: dict[int, PreprocessedDataset] = {}
        for agent in sorted(parameters.by_agent):
            check_cancel(cancellation, "PREPROCESSING", agent)
            agent_parameters = parameters.for_agent(agent)
            key = agent_parameters.preprocessing_cache_key
            dataset = cache.get(key)
            if dataset is None:
                dataset = AlgorithmCore(agent_parameters.preprocessing).preprocess_csv(source, cancellation=cancellation)
                cache[key] = dataset
            datasets_by_agent[agent] = dataset
        return datasets_by_agent

    def _emit(self, task: WorkerTask, *, status: str, stage: str, diagnostics: Mapping[str, Any], agent: int | None = None) -> None:
        self._repository.report_event(
            task,
            {
                "schema_version": "worker.event.v1",
                "job_id": task.job_id,
                "run_id": task.run_id,
                "attempt_id": task.attempt_id,
                "status": status,
                "stage": stage,
                "agent": agent,
                "occurred_at": datetime.now(timezone.utc).isoformat(timespec="milliseconds"),
                "diagnostics": dict(diagnostics),
            },
        )

    def _verify_dataset_hash(self, task: WorkerTask, source: Path, cancellation: Any) -> None:
        check_cancel(cancellation, "PREPROCESSING")
        if not source.is_file():
            raise WorkerFailure("INPUT_INVALID", "Dataset source file is missing.", "PREPROCESSING", recoverable=False)
        digest = hashlib.sha256()
        with source.open("rb") as handle:
            for block in iter(lambda: handle.read(1024 * 1024), b""):
                digest.update(block)
                if handle.tell() % (16 * 1024 * 1024) == 0:
                    check_cancel(cancellation, "PREPROCESSING")
        if digest.hexdigest() != task.dataset_sha256:
            raise WorkerFailure("INPUT_INVALID", "Dataset SHA-256 does not match the task snapshot.", "PREPROCESSING", recoverable=False)

    def _resolve_storage_path(self, relative_path: str) -> Path:
        candidate = (self._storage_root / Path(*relative_path.split("/"))).resolve()
        try:
            candidate.relative_to(self._storage_root)
        except ValueError as error:
            raise WorkerFailure("INPUT_INVALID", "Worker task escaped its configured storage root.", "PREPROCESSING") from error
        return candidate


class _LivenessCancellationContext:
    """Refresh local liveness only after a bounded repository check succeeds."""

    def __init__(
        self,
        delegate: Any,
        callback: Callable[[], None],
        *,
        minimum_interval_seconds: float,
        clock: Callable[[], float] = monotonic,
    ) -> None:
        self._delegate = delegate
        self._callback = callback
        self._minimum_interval_seconds = minimum_interval_seconds
        self._clock = clock
        self._next_update = 0.0

    def is_cancel_requested(self) -> bool:
        """Check durable cancellation first so liveness never masks lease loss."""

        cancelled = self._delegate.is_cancel_requested()
        now = self._clock()
        if now >= self._next_update:
            self._callback()
            self._next_update = now + self._minimum_interval_seconds
        return cancelled

def _failure_payload(task: WorkerTask, failure: WorkerFailure) -> dict[str, Any]:
    return {
        "code": failure.code,
        "message": failure.message,
        "stage": failure.stage,
        "agent": failure.agent,
        "diagnostic_id": f"{task.job_id}:{task.attempt_id}:{failure.code}",
        "recoverable": failure.recoverable,
    }


def _csv_bytes(rows: Sequence[Mapping[str, Any]], *, fieldnames: Sequence[str] | None = None) -> bytes:
    """Encode deterministic UTF-8 CSV including a header for an empty table."""

    defaults = {
        "Agent": "",
        "OriginalRunningIndex": "",
        "Time": "",
        "OverallAlarmLevel": "",
    }
    fieldnames = list(fieldnames) if fieldnames is not None else (list(rows[0]) if rows else list(defaults))
    destination = StringIO(newline="")
    writer = csv.DictWriter(destination, fieldnames=fieldnames, extrasaction="raise", lineterminator="\n")
    writer.writeheader()
    for row in rows:
        writer.writerow({name: _csv_value(row.get(name)) for name in fieldnames})
    return destination.getvalue().encode("utf-8")


def _csv_value(value: Any) -> Any:
    if value is None:
        return ""
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, float):
        if not value == value or value in {float("inf"), float("-inf")}:
            return ""
        return format(value, ".17g")
    if isinstance(value, (list, tuple, dict)):
        return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True, allow_nan=False)
    return value


def _enrich_result_row(row: Mapping[str, Any], task: WorkerTask) -> Mapping[str, Any]:
    """Attach immutable task facts after numerical prediction is complete."""

    output = dict(row)
    output["RunMode"] = task.raw["run_mode"]
    output["ParameterProfileVersion"] = task.raw["parameter_snapshot"]["version_id"]
    output["LoadMappingVersion"] = task.raw["mapping_snapshot"]["version_id"]
    return output


def _result_csv_bytes(rows: Sequence[Mapping[str, Any]], agent: int) -> bytes:
    """Encode only complete, frozen ``point-result.v1`` rows for one Agent."""

    if not rows:
        raise WorkerFailure(
            "ARTIFACT_WRITE_FAILED",
            "A simulation Agent produced no point-result.v1 rows.",
            "GENERATING_ARTIFACTS",
            agent,
        )
    for index, row in enumerate(rows):
        actual = frozenset(row)
        if actual != _RESULT_CSV_FIELDSET:
            missing = ", ".join(sorted(_RESULT_CSV_FIELDSET - actual))
            unknown = ", ".join(sorted(actual - _RESULT_CSV_FIELDSET))
            details = "; ".join(item for item in (f"missing: {missing}" if missing else "", f"unknown: {unknown}" if unknown else "") if item)
            raise WorkerFailure(
                "ARTIFACT_WRITE_FAILED",
                f"Result row {index} does not match the frozen point-result.v1 header ({details}).",
                "GENERATING_ARTIFACTS",
                agent,
            )
        if row["Agent"] != agent:
            raise WorkerFailure(
                "ARTIFACT_WRITE_FAILED",
                f"Result row {index} has a mismatched Agent value.",
                "GENERATING_ARTIFACTS",
                agent,
            )
    return _csv_bytes(rows, fieldnames=_RESULT_CSV_FIELDNAMES)


def _artifact_alarms(outcome: SimulationOutcome) -> tuple[Mapping[str, Any], ...]:
    return tuple(
        {
            "Agent": item["agent"],
            "OriginalRunningIndex": item["original_running_index"],
            "Time": item["time"],
            "OverallAlarmLevel": item["overall_alarm_level"],
            "AlarmType": item["alarm_type"],
            "Reasons": item["reasons"],
            "LoadStatus": item["load_status"],
            "ResultIndex": item["result_index"],
        }
        for result in outcome.agent_results
        for item in result.alarms
    )


def _repository_artifacts(task: WorkerTask, items: Sequence[ArtifactItem]) -> tuple[Mapping[str, Any], ...]:
    if task.run_id is None:
        raise WorkerFailure("INPUT_INVALID", "SIMULATION task has no run identifier.", "GENERATING_ARTIFACTS")
    return tuple(
        {
            "name": item.name,
            "relative_path": f"runs/{task.run_id}/committed/{item.name}",
            "media_type": item.media_type,
            "size_bytes": item.size_bytes,
            "sha256": item.sha256,
            "required": item.required,
        }
        for item in items
    )


def _repository_alarms(_task: WorkerTask, outcome: SimulationOutcome) -> tuple[Mapping[str, Any], ...]:
    """Forward every source alarm without changing its local ``Time`` text.

    ``alarms.csv`` and the terminal Worker payload retain the source timestamp.
    The controlled Backend commit boundary owns database-index timezone
    normalization using the immutable dataset snapshot.
    """

    indexed: list[Mapping[str, Any]] = []
    for result in outcome.agent_results:
        for alarm in result.alarms:
            agent = int(alarm["agent"])
            indexed.append(
                {
                    "agent": agent,
                    "original_running_index": alarm["original_running_index"],
                    "time_value": alarm["time"],
                    "overall_alarm_level": alarm["overall_alarm_level"],
                    "alarm_type": alarm["alarm_type"],
                    "reasons_json": alarm["reasons"],
                    "load_status": alarm["load_status"],
                    "result_locator": {
                        "agent": agent,
                        "original_running_index": alarm["original_running_index"],
                    },
                }
            )
    return tuple(indexed)
def default_heartbeat_file() -> Path:
    """Return the local heartbeat path without opening a configuration file."""

    return Path(os.environ.get("WORKER_HEARTBEAT_FILE", str(_DEFAULT_HEARTBEAT_FILE)))


def validate_runtime_configuration(config_path: Path) -> None:
    """Validate only startup invariants that need no YAML or database client."""

    repository: PostgresWorkerRepository | None = None
    try:
        contents = config_path.read_text(encoding="utf-8")
    except OSError as error:
        raise RuntimeConfigurationError("Worker configuration cannot be read.") from error
    if not _CONFIG_VERSION_RE.search(contents):
        raise RuntimeConfigurationError("Worker configuration must declare platform-config.v1.")
    if not _POSTGRES_PROFILE_RE.search(contents):
        raise RuntimeConfigurationError("Worker configuration must select the PostgreSQL profile.")
    if not _POOL_CAPACITY_RE.search(contents):
        raise RuntimeConfigurationError("Worker configuration must retain worker_pool_capacity: 1.")


class RuntimeConfigurationError(Exception):
    """A local process-startup failure that must not claim a Worker task."""


@dataclass(frozen=True)
class WorkerRuntimeSettings:
    """The small worker-only subset of the mounted platform configuration."""

    postgres: PostgresSettings
    storage_root: Path
    lease_seconds: int
    heartbeat_seconds: float
    poll_seconds: float


def load_worker_runtime_settings(config_path: Path) -> WorkerRuntimeSettings:
    """Read only the PostgreSQL, storage, and bounded Worker settings needed here."""

    try:
        contents = config_path.read_text(encoding="utf-8")
    except OSError as error:
        raise RuntimeConfigurationError("Worker configuration cannot be read.") from error
    database = _yaml_block(contents, "database")
    storage = _yaml_block(contents, "storage")
    limits = _yaml_block(contents, "limits")
    postgres_host = _yaml_value(database, "host")
    worker_username = _yaml_value(database, "worker_username")
    dataset_root = Path(_yaml_value(storage, "dataset_root"))
    artifact_root = Path(_yaml_value(storage, "artifact_root"))
    if dataset_root.parent != artifact_root.parent:
        raise RuntimeConfigurationError("Dataset and artifact roots must share the Worker storage root.")
    try:
        port = int(_yaml_value(database, "port"))
        connect_timeout = int(_yaml_value(database, "connect_timeout_seconds"))
        lease_seconds = int(_yaml_value(limits, "worker_lease_seconds"))
        heartbeat_seconds = int(_yaml_value(limits, "worker_heartbeat_seconds"))
        poll_seconds = int(_yaml_value(limits, "worker_poll_interval_ms")) / 1000.0
    except ValueError as error:
        raise RuntimeConfigurationError("Worker numeric configuration is invalid.") from error
    if postgres_host != "postgres" or port != 5432 or worker_username != "algorithm_worker":
        raise RuntimeConfigurationError("Worker must use the controlled postgres:5432 algorithm_worker connection.")
    if connect_timeout <= 0 or not 1 <= lease_seconds <= 600:
        raise RuntimeConfigurationError("Worker PostgreSQL connection or lease configuration is invalid.")
    if heartbeat_seconds <= 0 or heartbeat_seconds > lease_seconds or poll_seconds <= 0:
        raise RuntimeConfigurationError("Worker heartbeat or poll interval is invalid.")
    return WorkerRuntimeSettings(
        postgres=PostgresSettings(
            host=postgres_host,
            port=port,
            database=_yaml_value(database, "name"),
            username=worker_username,
            password_file=Path(_yaml_value(database, "worker_password_file")),
            connect_timeout_seconds=connect_timeout,
        ),
        storage_root=dataset_root.parent,
        lease_seconds=lease_seconds,
        heartbeat_seconds=float(heartbeat_seconds),
        poll_seconds=poll_seconds,
    )


def _yaml_block(contents: str, name: str) -> str:
    match = re.search(rf"(?m)^{re.escape(name)}:[ \t]*\r?$\r?\n((?:^[ \t]+[^\r\n]*(?:\r?\n|$))*)", contents)
    if not match:
        raise RuntimeConfigurationError(f"Worker configuration is missing {name}.")
    return match.group(1)


def _yaml_value(block: str, key: str) -> str:
    match = re.search(rf"(?m)^\s+{re.escape(key)}:\s*([^#\r\n]+?)\s*$", block)
    if not match:
        raise RuntimeConfigurationError(f"Worker configuration is missing {key}.")
    value = match.group(1).strip().strip("\"'")
    if not value or value.lower() == "null":
        raise RuntimeConfigurationError(f"Worker configuration has an invalid {key}.")
    return value


def write_heartbeat(path: Path) -> None:
    """Publish a small local liveness record using one atomic replacement."""

    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f"{path.name}.writing.{os.getpid()}")
    payload = {
        "schema_version": HEARTBEAT_SCHEMA_VERSION,
        "pid": os.getpid(),
        "updated_at": datetime.now(timezone.utc).isoformat(timespec="milliseconds"),
    }
    try:
        temporary.write_text(json.dumps(payload, sort_keys=True) + "\n", encoding="utf-8")
        os.replace(temporary, path)
    except OSError as error:
        try:
            temporary.unlink()
        except OSError:
            pass
        raise RuntimeConfigurationError("Worker heartbeat cannot be written.") from error


def main(argv: Sequence[str] | None = None) -> int:
    """Run the one-instance PostgreSQL Worker host without any Agent service split."""

    parser = argparse.ArgumentParser(description="Run the single-capacity Algorithm Worker host.")
    parser.add_argument("--config", required=True, type=Path, help="Read-only platform configuration path.")
    parser.add_argument(
        "--heartbeat-file",
        type=Path,
        default=default_heartbeat_file(),
        help="Override the local heartbeat path for a controlled test environment.",
    )
    parser.add_argument(
        "--heartbeat-interval-seconds",
        type=float,
        default=None,
        help="Override the configuration heartbeat interval for a controlled local test.",
    )
    parser.add_argument(
        "--once",
        action="store_true",
        help="Register and perform at most one complete FIFO poll before exit.",
    )
    parser.add_argument(
        "--check-config",
        action="store_true",
        help="Validate only configuration and local heartbeat output without a database connection.",
    )
    arguments = parser.parse_args(argv)
    if arguments.heartbeat_interval_seconds is not None and arguments.heartbeat_interval_seconds <= 0:
        parser.error("--heartbeat-interval-seconds must be positive")

    try:
        validate_runtime_configuration(arguments.config)
        if arguments.check_config:
            write_heartbeat(arguments.heartbeat_file)
            return 0
        settings = load_worker_runtime_settings(arguments.config)
        heartbeat_seconds = arguments.heartbeat_interval_seconds or settings.heartbeat_seconds
        identity = WorkerIdentity(
            worker_id=os.environ.get("WORKER_INSTANCE_ID", "algorithm-worker-1"),
            worker_version=os.environ.get("IOT_WORKER_VERSION", "1.0.0-m1"),
        )
        repository = PostgresWorkerRepository.connect(
            settings.postgres,
            identity,
            lease_seconds=settings.lease_seconds,
            heartbeat_interval_seconds=heartbeat_seconds,
        )
        from .worker_service import WorkerLoopSettings, WorkerService

        service = WorkerService(
            repository,
            settings.storage_root,
            settings=WorkerLoopSettings(
                idle_poll_interval_seconds=settings.poll_seconds,
                idle_heartbeat_interval_seconds=heartbeat_seconds,
            ),
            liveness_callback=lambda: write_heartbeat(arguments.heartbeat_file),
            liveness_interval_seconds=min(5.0, heartbeat_seconds),
        )
        service.register()
        write_heartbeat(arguments.heartbeat_file)
    except (RuntimeConfigurationError, RepositoryConfigurationError, RepositoryOperationError, LeaseLostFailure) as error:
        if repository is not None:
            try:
                repository.close()
            except Exception:
                pass
        print(f"worker startup failed: {error}", file=os.sys.stderr)
        return 1
    if arguments.once:
        try:
            service.run_one_poll()
            write_heartbeat(arguments.heartbeat_file)
            return 0
        except (LeaseLostFailure, RepositoryOperationError, RuntimeConfigurationError) as error:
            print(f"worker runtime stopped: {error}", file=os.sys.stderr)
            return 1
        finally:
            repository.close()

    stop_requested = Event()

    def request_stop(_signal_number: int, _frame: object) -> None:
        stop_requested.set()

    signal.signal(signal.SIGINT, request_stop)
    signal.signal(signal.SIGTERM, request_stop)
    try:
        while not stop_requested.is_set():
            service.run_one_poll()
            write_heartbeat(arguments.heartbeat_file)
            stop_requested.wait(service.idle_poll_interval_seconds)
    except LeaseLostFailure as error:
        print(f"worker runtime stopped: {error}", file=os.sys.stderr)
        return 1
    except (RepositoryOperationError, RuntimeConfigurationError) as error:
        print(f"worker runtime stopped: {error}", file=os.sys.stderr)
        return 1
    finally:
        try:
            repository.close()
        except Exception:
            pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
