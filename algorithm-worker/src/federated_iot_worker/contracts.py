"""Executable strict validation for ``worker.task.v1`` and repository protocols."""

from __future__ import annotations

import re
from dataclasses import dataclass
from pathlib import PurePosixPath
from typing import Any, Mapping, Protocol, Sequence

from .cancellation import CancellationContext
from .errors import ContractFailure


WORKER_TASK_VERSION = "worker.task.v1"
PREFLIGHT_SUMMARY_VERSION = "dataset-preflight.summary.v1"
ARTIFACT_MANIFEST_VERSION = "artifact.manifest.v1"
POINT_RESULT_VERSION = "point-result.v1"
SUPPORTED_JOB_TYPES = frozenset({"DATASET_PREFLIGHT", "SIMULATION"})
S1_AGENT_SEGMENTS = {1: "EARLY", 2: "MIDDLE", 3: "LATE"}
DATASET_COLUMNS = ("Time_base", "dzdl_1", "dzdl_2", "dzdl_3", "dzdl_4", "zl", "sd")
_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]*$")
_SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
_OCI_DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")


@dataclass(frozen=True)
class WorkerTask:
    """Validated, immutable subset of the persistent Worker task envelope."""

    raw: Mapping[str, Any]
    contract_version: str
    job_id: str
    job_type: str
    run_id: str | None
    attempt_id: str
    lease_token: str
    dataset_relative_path: str
    dataset_sha256: str
    timezone: str
    tmp_relative_directory: str

    @property
    def parameter_agents(self) -> Sequence[Mapping[str, Any]]:
        snapshot = self.raw.get("parameter_snapshot", {})
        return snapshot.get("agents", [])


class WorkerRepository(Protocol):
    """The only database-facing surface used by the Worker process."""

    def cancellation_context(self, task: WorkerTask) -> CancellationContext:
        """Return an attempt-scoped cancellation view."""

    def report_event(self, task: WorkerTask, event: Mapping[str, Any]) -> None:
        """Persist an event after lease validation."""

    def heartbeat(self, task: WorkerTask) -> None:
        """Renew the current PostgreSQL lease after lease validation."""

    def commit_preflight(self, task: WorkerTask, summary: Mapping[str, Any]) -> None:
        """Persist a preflight terminal payload after artifact integrity checks."""

    def commit_failure(self, task: WorkerTask, failure: Mapping[str, Any]) -> None:
        """Persist a stable terminal failure after lease validation."""

    def commit_simulation(
        self,
        task: WorkerTask,
        *,
        manifest_sha256: str,
        artifacts: Sequence[Mapping[str, Any]],
        alarms: Sequence[Mapping[str, Any]],
        stage_durations_ms: Mapping[str, int],
    ) -> None:
        """Commit only an atomically published and verified simulation artifact set."""


def parse_worker_task(raw: Mapping[str, Any]) -> WorkerTask:
    """Validate all semantic invariants before filesystem or algorithm access."""

    if not isinstance(raw, Mapping):
        raise ContractFailure("worker.task.v1 must be an object.")
    _require_exact_keys(
        raw,
        {
            "contract_version", "job_id", "job_type", "run_id", "attempt_id", "lease_token",
            "dataset", "run_mode", "parameter_snapshot", "mapping_snapshot",
            "field_standard_snapshot", "runtime", "preprocessing", "output", "limits",
        },
        {"contract_version", "job_id", "job_type", "run_id", "attempt_id", "lease_token", "dataset", "output", "limits"},
        "worker.task.v1",
    )
    if raw["contract_version"] != WORKER_TASK_VERSION:
        raise ContractFailure("Unsupported worker task contract version.")
    job_type = raw["job_type"]
    if job_type not in SUPPORTED_JOB_TYPES:
        raise ContractFailure("Unsupported worker job type.")

    job_id = _opaque_id(raw["job_id"], "job_id")
    attempt_id = _opaque_id(raw["attempt_id"], "attempt_id")
    lease_token = _nonempty_string(raw["lease_token"], "lease_token", minimum_length=16)
    dataset = _validate_dataset(raw["dataset"], job_type)
    _validate_limits(raw["limits"])
    output = raw["output"]
    if not isinstance(output, Mapping):
        raise ContractFailure("output must be an object.")

    run_id = raw["run_id"]
    if job_type == "DATASET_PREFLIGHT":
        _validate_preflight_branch(raw, dataset, output, attempt_id)
    else:
        _validate_simulation_branch(raw, dataset, output, attempt_id)
        run_id = _opaque_id(run_id, "run_id")

    return WorkerTask(
        raw=raw,
        contract_version=WORKER_TASK_VERSION,
        job_id=job_id,
        job_type=job_type,
        run_id=run_id,
        attempt_id=attempt_id,
        lease_token=lease_token,
        dataset_relative_path=dataset["relative_path"],
        dataset_sha256=dataset["sha256"],
        timezone=dataset["timezone"],
        tmp_relative_directory=_safe_relative_path(output["relative_tmp_directory"], "output.relative_tmp_directory"),
    )


def _validate_preflight_branch(raw: Mapping[str, Any], dataset: Mapping[str, Any], output: Mapping[str, Any], attempt_id: str) -> None:
    if raw["run_id"] is not None:
        raise ContractFailure("DATASET_PREFLIGHT requires run_id=null.")
    forbidden = {"run_mode", "parameter_snapshot", "mapping_snapshot", "field_standard_snapshot", "runtime"}
    present = sorted(field for field in forbidden if field in raw)
    if present:
        raise ContractFailure(f"DATASET_PREFLIGHT forbids simulation fields: {', '.join(present)}.")
    _validate_preprocessing(raw.get("preprocessing"))
    _require_exact_keys(output, {"relative_tmp_directory", "required_summary_schema"}, {"relative_tmp_directory", "required_summary_schema"}, "output")
    if output["required_summary_schema"] != PREFLIGHT_SUMMARY_VERSION:
        raise ContractFailure("DATASET_PREFLIGHT requires dataset-preflight.summary.v1 output.")
    expected = f"datasets/{dataset['dataset_id']}/preflight/tmp/{attempt_id}"
    if _safe_relative_path(output["relative_tmp_directory"], "output.relative_tmp_directory") != expected:
        raise ContractFailure("DATASET_PREFLIGHT output directory does not match its dataset and attempt.")


def _validate_simulation_branch(raw: Mapping[str, Any], dataset: Mapping[str, Any], output: Mapping[str, Any], attempt_id: str) -> None:
    run_id = _opaque_id(raw["run_id"], "run_id")
    if "preprocessing" in raw:
        raise ContractFailure("SIMULATION forbids preprocessing.")
    for field in ("run_mode", "parameter_snapshot", "mapping_snapshot", "field_standard_snapshot", "runtime"):
        if field not in raw:
            raise ContractFailure(f"SIMULATION is missing {field}.")
    if raw["run_mode"] not in {"REFERENCE", "CUSTOM"}:
        raise ContractFailure("SIMULATION run_mode must be REFERENCE or CUSTOM.")
    if tuple(dataset.get("columns", ())) != DATASET_COLUMNS:
        raise ContractFailure("SIMULATION dataset.columns must match the frozen seven-column header.")
    _validate_parameter_snapshot(raw["parameter_snapshot"])
    _validate_mapping_snapshot(raw["mapping_snapshot"])
    _validate_field_standard_snapshot(raw["field_standard_snapshot"])
    _validate_runtime(raw["runtime"])
    _require_exact_keys(output, {"relative_tmp_directory", "required_artifact_schema"}, {"relative_tmp_directory", "required_artifact_schema"}, "output")
    if output["required_artifact_schema"] != ARTIFACT_MANIFEST_VERSION:
        raise ContractFailure("SIMULATION requires artifact.manifest.v1 output.")
    expected = f"runs/{run_id}/tmp/{attempt_id}"
    if _safe_relative_path(output["relative_tmp_directory"], "output.relative_tmp_directory") != expected:
        raise ContractFailure("SIMULATION output directory does not match its run and attempt.")


def _validate_dataset(value: Any, job_type: str) -> Mapping[str, Any]:
    if not isinstance(value, Mapping):
        raise ContractFailure("dataset must be an object.")
    required = {"dataset_id", "relative_path", "sha256", "timezone"}
    allowed = required | {"columns"}
    _require_exact_keys(value, allowed, required, "dataset")
    dataset_id = _opaque_id(value["dataset_id"], "dataset.dataset_id")
    relative_path = _safe_relative_path(value["relative_path"], "dataset.relative_path")
    if relative_path != f"datasets/{dataset_id}/source.csv":
        raise ContractFailure("dataset.relative_path does not match the controlled dataset layout.")
    _sha256(value["sha256"], "dataset.sha256")
    if value["timezone"] != "Asia/Shanghai":
        raise ContractFailure("dataset.timezone must be Asia/Shanghai.")
    if job_type == "SIMULATION" and "columns" not in value:
        raise ContractFailure("SIMULATION dataset must include the frozen columns snapshot.")
    if "columns" in value and tuple(value["columns"]) != DATASET_COLUMNS:
        raise ContractFailure("dataset.columns must match the frozen seven-column header.")
    return value


def _validate_parameter_snapshot(value: Any) -> None:
    if not isinstance(value, Mapping):
        raise ContractFailure("parameter_snapshot must be an object.")
    _require_exact_keys(value, {"version_id", "sha256", "shared_parameters", "agents", "fixed_items"}, {"version_id", "sha256", "shared_parameters", "agents", "fixed_items"}, "parameter_snapshot")
    _opaque_id(value["version_id"], "parameter_snapshot.version_id")
    _sha256(value["sha256"], "parameter_snapshot.sha256")
    if not isinstance(value["shared_parameters"], Mapping) or not isinstance(value["fixed_items"], Mapping):
        raise ContractFailure("parameter_snapshot parameter groups must be objects.")
    _validate_s1_agent_collection(value)


def _validate_s1_agent_collection(snapshot: Any) -> None:
    if not isinstance(snapshot, Mapping):
        raise ContractFailure("SIMULATION requires parameter_snapshot.")
    agents = snapshot.get("agents")
    if not isinstance(agents, list) or len(agents) != 3:
        raise ContractFailure("S1 SIMULATION requires exactly three agent contexts.")
    seen: set[int] = set()
    for item in agents:
        if not isinstance(item, Mapping):
            raise ContractFailure("parameter_snapshot.agents must contain objects.")
        _require_exact_keys(item, {"agent", "segment", "parameters"}, {"agent", "segment", "parameters"}, "parameter_snapshot.agents[]")
        agent = item["agent"]
        if not _is_integer(agent) or agent not in S1_AGENT_SEGMENTS or item["segment"] != S1_AGENT_SEGMENTS[agent] or agent in seen:
            raise ContractFailure("S1 agents must be unique 1/EARLY, 2/MIDDLE, 3/LATE.")
        if not isinstance(item["parameters"], Mapping):
            raise ContractFailure("parameter_snapshot.agents[].parameters must be an object.")
        seen.add(agent)
    if seen != set(S1_AGENT_SEGMENTS):
        raise ContractFailure("S1 agents must contain 1/EARLY, 2/MIDDLE, and 3/LATE exactly once.")


def _validate_mapping_snapshot(value: Any) -> None:
    if not isinstance(value, Mapping):
        raise ContractFailure("mapping_snapshot must be an object.")
    _require_exact_keys(value, {"version_id", "mapping_type", "parameters", "result_unit", "sha256"}, {"version_id", "mapping_type", "parameters", "result_unit", "sha256"}, "mapping_snapshot")
    _opaque_id(value["version_id"], "mapping_snapshot.version_id")
    _sha256(value["sha256"], "mapping_snapshot.sha256")
    if value["mapping_type"] != "identity" or value["result_unit"] != "A" or value["parameters"] != {}:
        raise ContractFailure("S1 only permits an empty identity mapping with unit A.")


def _validate_field_standard_snapshot(value: Any) -> None:
    if not isinstance(value, Mapping):
        raise ContractFailure("field_standard_snapshot must be an object.")
    _require_exact_keys(value, {"configuration_version", "sha256", "zl", "sd", "sampling"}, {"configuration_version", "sha256", "zl", "sd", "sampling"}, "field_standard_snapshot")
    _opaque_id(value["configuration_version"], "field_standard_snapshot.configuration_version")
    _sha256(value["sha256"], "field_standard_snapshot.sha256")
    for name in ("zl", "sd"):
        field = value[name]
        if not isinstance(field, Mapping):
            raise ContractFailure(f"field_standard_snapshot.{name} must be an object.")
        _require_exact_keys(field, {"unit_symbol", "validation_enabled"}, {"unit_symbol", "validation_enabled"}, f"field_standard_snapshot.{name}")
        if field["unit_symbol"] is not None and not isinstance(field["unit_symbol"], str):
            raise ContractFailure(f"field_standard_snapshot.{name}.unit_symbol must be string or null.")
        if not isinstance(field["validation_enabled"], bool):
            raise ContractFailure(f"field_standard_snapshot.{name}.validation_enabled must be boolean.")
    sampling = value["sampling"]
    if not isinstance(sampling, Mapping):
        raise ContractFailure("field_standard_snapshot.sampling must be an object.")
    _require_exact_keys(sampling, {"expected_period_ms", "tolerance_ms"}, {"expected_period_ms", "tolerance_ms"}, "field_standard_snapshot.sampling")
    for name in ("expected_period_ms", "tolerance_ms"):
        number = sampling[name]
        if number is not None and (not _is_integer(number) or number < 1):
            raise ContractFailure(f"field_standard_snapshot.sampling.{name} must be positive integer or null.")


def _validate_runtime(value: Any) -> None:
    if not isinstance(value, Mapping):
        raise ContractFailure("runtime must be an object.")
    _require_exact_keys(value, {"algorithm_version", "worker_version", "image_digest", "numeric_runtime", "master_seed", "random_streams"}, {"algorithm_version", "worker_version", "image_digest", "numeric_runtime", "master_seed", "random_streams"}, "runtime")
    _opaque_id(value["algorithm_version"], "runtime.algorithm_version")
    _opaque_id(value["worker_version"], "runtime.worker_version")
    if not isinstance(value["image_digest"], str) or not _OCI_DIGEST_RE.fullmatch(value["image_digest"]):
        raise ContractFailure("runtime.image_digest must be a lowercase immutable sha256 OCI digest.")
    _nonempty_string(value["numeric_runtime"], "runtime.numeric_runtime")
    if not _is_integer(value["master_seed"]) or value["master_seed"] < 0:
        raise ContractFailure("runtime.master_seed must be a non-negative integer.")
    streams = value["random_streams"]
    if not isinstance(streams, Mapping):
        raise ContractFailure("runtime.random_streams must be an object.")
    required = {"generator", "seed_mapping_version", "base_center_seed_by_agent", "transition_center_seed_by_agent", "boundary_seed_by_agent", "public_anchor_seed"}
    _require_exact_keys(streams, required, required, "runtime.random_streams")
    if streams["generator"] != "MT19937_TWISTER_COMPAT":
        raise ContractFailure("runtime.random_streams.generator is not the frozen generator.")
    _opaque_id(streams["seed_mapping_version"], "runtime.random_streams.seed_mapping_version")
    for name in ("base_center_seed_by_agent", "transition_center_seed_by_agent", "boundary_seed_by_agent"):
        _validate_seed_map(streams[name], f"runtime.random_streams.{name}")
    if not _is_integer(streams["public_anchor_seed"]) or streams["public_anchor_seed"] < 0:
        raise ContractFailure("runtime.random_streams.public_anchor_seed must be a non-negative integer.")


def _validate_seed_map(value: Any, field: str) -> None:
    if not isinstance(value, Mapping) or set(value) != {"1", "2", "3"}:
        raise ContractFailure(f"{field} must contain exactly agent seed keys 1, 2, 3.")
    if any(not _is_integer(value[key]) or value[key] < 0 for key in value):
        raise ContractFailure(f"{field} values must be non-negative integers.")


def _validate_preprocessing(value: Any) -> None:
    if not isinstance(value, Mapping):
        raise ContractFailure("DATASET_PREFLIGHT requires preprocessing.")
    required = {"contract_version", "field_standard_configuration_sha256"}
    _require_exact_keys(value, required, required, "preprocessing")
    if value["contract_version"] != "preprocessing.v1":
        raise ContractFailure("preprocessing.contract_version must be preprocessing.v1.")
    _sha256(value["field_standard_configuration_sha256"], "preprocessing.field_standard_configuration_sha256")


def _validate_limits(value: Any) -> None:
    if not isinstance(value, Mapping):
        raise ContractFailure("limits must be an object.")
    required = {"memory_bytes", "cancel_check_target_ms"}
    _require_exact_keys(value, required, required, "limits")
    if not _is_integer(value["memory_bytes"]) or value["memory_bytes"] < 1:
        raise ContractFailure("limits.memory_bytes must be a positive integer.")
    if not _is_integer(value["cancel_check_target_ms"]) or not 1 <= value["cancel_check_target_ms"] <= 5000:
        raise ContractFailure("limits.cancel_check_target_ms must be between 1 and 5000.")


def _require_exact_keys(value: Mapping[str, Any], allowed: set[str], required: set[str], field: str) -> None:
    missing = sorted(required - set(value))
    unknown = sorted(set(value) - allowed)
    if missing:
        raise ContractFailure(f"{field} is missing required fields: {', '.join(missing)}.")
    if unknown:
        raise ContractFailure(f"{field} contains forbidden fields: {', '.join(unknown)}.")


def _safe_relative_path(value: Any, field: str) -> str:
    if not isinstance(value, str) or not value or len(value) > 512:
        raise ContractFailure(f"{field} must be a non-empty relative path.")
    if "\\" in value or "//" in value or value.endswith("/"):
        raise ContractFailure(f"{field} is not a normalized relative path.")
    path = PurePosixPath(value)
    if path.is_absolute() or any(part in {".", ".."} for part in path.parts) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._/-]*", value):
        raise ContractFailure(f"{field} is not a safe relative path.")
    return path.as_posix()


def _sha256(value: Any, field: str) -> str:
    if not isinstance(value, str) or not _SHA256_RE.fullmatch(value):
        raise ContractFailure(f"{field} must be a 64-character lowercase SHA-256 string.")
    return value


def _opaque_id(value: Any, field: str) -> str:
    if not isinstance(value, str) or not 1 <= len(value) <= 128 or not _ID_RE.fullmatch(value):
        raise ContractFailure(f"{field} must be a safe opaque identifier.")
    return value


def _nonempty_string(value: Any, field: str, *, minimum_length: int = 1) -> str:
    if not isinstance(value, str) or len(value) < minimum_length:
        raise ContractFailure(f"{field} must be a non-empty string.")
    return value


def _is_integer(value: Any) -> bool:
    return isinstance(value, int) and not isinstance(value, bool)
