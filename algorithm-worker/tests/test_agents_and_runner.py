from __future__ import annotations

import csv
import hashlib
import json
import math
import re
import sys
import tempfile
import unittest
from copy import deepcopy
from dataclasses import replace
from datetime import datetime, timedelta
from pathlib import Path
from types import SimpleNamespace
from typing import Any, Mapping
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from federated_iot_worker.agents import AgentContext, AgentExecutor, validate_s1_contexts
from federated_iot_worker.cancellation import NeverCancelled
from federated_iot_worker.contracts import parse_worker_task
from federated_iot_worker.features import ChronologicalSplit, FeatureDataset, training_standardization
from federated_iot_worker.errors import CancelledFailure, ContractFailure, WorkerFailure
from federated_iot_worker.parameters import accepted_shared_parameter_paths, simulation_preparation_parameters
from federated_iot_worker.preprocessing import AlgorithmCore, PreprocessedDataset, PreprocessingConfig, RunningRow
from federated_iot_worker.runner import WorkerRunner, _artifact_alarms, _repository_alarms, _result_csv_bytes, write_heartbeat
from federated_iot_worker.simulation import execute_simulation


class _Repository:
    def __init__(self) -> None:
        self.events: list[Mapping[str, Any]] = []
        self.preflight: list[Mapping[str, Any]] = []
        self.failures: list[Mapping[str, Any]] = []
        self.operations: list[str] = []
        self.simulation_commits: list[Mapping[str, Any]] = []

    def cancellation_context(self, task: Any) -> NeverCancelled:
        return NeverCancelled()

    def report_event(self, task: Any, event: Mapping[str, Any]) -> None:
        self.events.append(event)
        self.operations.append("event")

    def heartbeat(self, task: Any) -> None:
        return None

    def commit_preflight(self, task: Any, summary: Mapping[str, Any]) -> None:
        self.preflight.append(summary)
        self.operations.append("preflight")

    def commit_failure(self, task: Any, failure: Mapping[str, Any]) -> None:
        self.failures.append(failure)

    def commit_simulation(self, task: Any, **kwargs: Any) -> None:
        self.simulation_commits.append(kwargs)
        self.operations.append("simulation")


def _context(agent: int, segment: str) -> AgentContext:
    features = FeatureDataset(((1.0, 2.0), (2.0, 3.0), (3.0, 4.0)), (1.0, 2.0, 3.0), (1.0, 2.0, 3.0), (0.0, 0.0, 0.0), ())
    return AgentContext(agent, segment, {}, features, ChronologicalSplit((0,), (1,), (2,)), training_standardization(((1.0, 2.0), (3.0, 4.0))), 2026 + agent, f"agent_{agent}")


def _reference_shared_parameters() -> dict[str, object]:
    return {
        "feature_state": {"nLag": 8, "speed_threshold": 0.01, "current_threshold": 1.0},
        "cleaning": {"median_window": 21, "mad_factor": 5.0, "smoothing_window": 5},
        "split": {"training_ratio": 0.70, "calibration_ratio": 0.15, "minimum_training": 80, "minimum_calibration": 30, "minimum_testing": 30, "agent_count": 3},
        "local_gp": {"kNN": 100, "adaptive_ratio": 0.10, "ell": 5.0, "sigma_f": 1.0, "sigma_n": 0.10, "minimum_regularization": 0.01},
        "trend": {"threshold": 1.0, "maximum_mixing": 0.75, "gain": 1.0, "maximum_step_change": 2.5},
        "interval": {"confidence": 0.95, "calibration_window": 300, "minimum_scores": 20, "std_floor": 0.20, "calibration_scale_min": 0.5, "calibration_scale_max": 10.0, "half_width_min": 1.0, "half_width_max": 8.0, "coverage_window": 200, "update_mode": "all_finite", "variance_floor": 1e-8},
        "anchors": {"base_centers": 100, "transition_centers": 30, "boundary_centers": 20, "transition_quantile": 0.75, "public_anchors": 300, "iterations": 60, "random_seed": 2026},
        "support": {"scale_multiple": 2.5, "minimum_weight": 1e-5, "minimum_query_support": 0.03, "full_weight_reference": 0.35},
        "global_surrogate": {"ell": 5.0, "minimum_regularization": 1e-4, "noise_ratio": 0.25, "cholesky_attempts": 10, "leave_one_out": True},
        "fusion": {"maximum_global_weight": 0.98, "initial_improvement": 0.001, "error_window": 50, "minimum_samples": 20, "win_margin": 0.05, "variance_weight": 0.25, "winsor_quantile": 0.90, "global_clear_threshold": 0.85, "neutral_upper_limit": 0.70, "persistence": 5, "rise_smoothing": 0.85, "fall_smoothing": 0.55, "disagreement_kappa": 2.5, "maximum_variance_ratio": 2.0},
        "alarms": {"imbalance_threshold": 0.15, "notice_count": 1, "warning_count": 3, "alarm_count": 5, "absolute_current_threshold": None, "absolute_tension_threshold": None},
    }


def _flatten_parameter_paths(parameters: Mapping[str, object]) -> frozenset[str]:
    """Flatten the two-level frozen parameter tree used by worker.task.v1."""

    return frozenset(
        f"{group}.{leaf}"
        for group, leaves in parameters.items()
        for leaf in leaves
    )


def _simulation_task() -> dict[str, object]:
    raw = json.loads(
        (ROOT.parent / "contracts" / "worker" / "fixtures" / "simulation-task.v1.json").read_text(encoding="utf-8")
    )
    raw["parameter_snapshot"]["shared_parameters"] = _reference_shared_parameters()
    return raw


def _running_rows(count: int) -> tuple[RunningRow, ...]:
    rows: list[RunningRow] = []
    for index in range(count):
        iavg = 10.0 + index
        timestamp = (datetime(2026, 7, 1) + timedelta(seconds=index)).isoformat(timespec="milliseconds")
        rows.append(
            RunningRow(
                index + 1,
                index + 1,
                timestamp,
                iavg - 1,
                iavg,
                iavg + 1,
                0.0,
                5.0 + index,
                2.0 + index,
                iavg,
                iavg * 3,
                0.2,
                False,
                False,
                "",
                iavg - 1,
                iavg,
                iavg + 1,
                0.0,
                5.0 + index,
                2.0 + index,
                iavg,
                iavg * 3,
                0.2,
            )
        )
    return tuple(rows)


class AgentAndRunnerTests(unittest.TestCase):
    def test_repository_alarm_handoff_preserves_source_time_and_safe_locator_for_all_agents(self) -> None:
        task = parse_worker_task(_simulation_task())
        source_times = {
            1: ("2026-07-04 02:02:06.621", "2026-07-04T02:02:07.621"),
            2: ("2026-07-03T18:02:06.621Z", "2026-07-03T18:02:07.621+00:00"),
            3: ("2026-07-04T03:02:06.621+09:00", "2026-07-04T03:02:07.621+09:00"),
        }
        results = []
        for agent, values in source_times.items():
            alarms = tuple(
                {
                    "agent": agent,
                    "original_running_index": agent * 100 + result_index,
                    "time": raw_time,
                    "overall_alarm_level": "Warning",
                    "alarm_type": "OVERALL",
                    "reasons": [f"reason-{agent}-{result_index}"],
                    "load_status": "High",
                    "result_index": result_index,
                }
                for result_index, raw_time in enumerate(values)
            )
            results.append(SimpleNamespace(alarms=alarms))
        outcome = SimpleNamespace(agent_results=tuple(results))

        artifact_rows = _artifact_alarms(outcome)
        indexed_rows = _repository_alarms(task, outcome)

        self.assertEqual(len(artifact_rows), 6)
        self.assertEqual(len(indexed_rows), len(artifact_rows))
        self.assertEqual([row["Time"] for row in artifact_rows], [time for pair in source_times.values() for time in pair])
        self.assertEqual({row["agent"] for row in indexed_rows}, {1, 2, 3})
        for source, indexed in zip(artifact_rows, indexed_rows):
            self.assertEqual(indexed["time_value"], source["Time"])
            self.assertEqual(indexed["reasons_json"], source["Reasons"])
            self.assertEqual(
                indexed["result_locator"],
                {"agent": source["Agent"], "original_running_index": source["OriginalRunningIndex"]},
            )
            self.assertEqual(set(indexed["result_locator"]), {"agent", "original_running_index"})
            self.assertNotIn("artifact", indexed["result_locator"])
            self.assertNotIn("result_index", indexed["result_locator"])
            self.assertEqual(
                set(indexed),
                {
                    "agent",
                    "original_running_index",
                    "time_value",
                    "overall_alarm_level",
                    "alarm_type",
                    "reasons_json",
                    "load_status",
                    "result_locator",
                },
            )
            self.assertNotIn("result_locator_json", indexed)

    def test_repository_alarm_handoff_does_not_discard_an_invalid_time_before_backend_normalization(self) -> None:
        task = parse_worker_task(_simulation_task())
        outcome = SimpleNamespace(
            agent_results=(
                SimpleNamespace(
                    alarms=(
                        {
                            "agent": 2,
                            "original_running_index": 201,
                            "time": "not-a-timestamp",
                            "overall_alarm_level": "Warning",
                            "alarm_type": "OVERALL",
                            "reasons": ["invalid-time"],
                            "load_status": "High",
                            "result_index": 1,
                        },
                    )
                ),
            )
        )

        indexed = _repository_alarms(task, outcome)
        self.assertEqual(len(indexed), 1)
        self.assertEqual(indexed[0]["time_value"], "not-a-timestamp")
        self.assertEqual(indexed[0]["reasons_json"], ["invalid-time"])
        self.assertEqual(indexed[0]["result_locator"], {"agent": 2, "original_running_index": 201})
        self.assertNotIn("result_locator_json", indexed[0])

    def test_result_csv_rejects_missing_or_unknown_frozen_header_fields(self) -> None:
        malformed_rows = (
            {"Agent": 1},
            {"Agent": 1, "UnexpectedField": "unexpected"},
        )
        for row in malformed_rows:
            with self.subTest(fields=tuple(row)):
                with self.assertRaises(WorkerFailure) as raised:
                    _result_csv_bytes([row], 1)
                self.assertEqual(raised.exception.code, "ARTIFACT_WRITE_FAILED")
                self.assertEqual(raised.exception.stage, "GENERATING_ARTIFACTS")
                self.assertEqual(raised.exception.agent, 1)

    def test_shared_parameter_paths_match_backend_constraints_exactly(self) -> None:
        constraints = json.loads(
            (ROOT.parent / "backend" / "config" / "parameter-constraints.v1.json").read_text(encoding="utf-8")
        )
        backend_paths = frozenset(constraints["paths"])
        worker_paths = accepted_shared_parameter_paths()

        self.assertEqual(len(backend_paths), 69)
        self.assertEqual(sum(item["editable"] for item in constraints["paths"].values()), 67)
        self.assertEqual(worker_paths, backend_paths)
        self.assertEqual(_flatten_parameter_paths(_reference_shared_parameters()), backend_paths)
        self.assertIn("split.agent_count", worker_paths)
        self.assertIn("global_surrogate.leave_one_out", worker_paths)
        self.assertNotIn("output.save_intermediate_files", worker_paths)
        self.assertNotIn("output.debug_top_n", worker_paths)

        parameters = simulation_preparation_parameters(parse_worker_task(_simulation_task()))
        self.assertEqual(
            [_flatten_parameter_paths(parameters.for_agent(agent).effective_parameters) for agent in (1, 2, 3)],
            [backend_paths, backend_paths, backend_paths],
        )

    def test_parameter_snapshot_rejects_closed_paths_fixed_values_and_invalid_numbers(self) -> None:
        def remove_shared_leaf(task: dict[str, object]) -> None:
            del task["parameter_snapshot"]["shared_parameters"]["trend"]["gain"]

        def add_shared_leaf(task: dict[str, object]) -> None:
            task["parameter_snapshot"]["shared_parameters"]["trend"]["undeclared"] = 1.0

        def add_output_group(task: dict[str, object]) -> None:
            task["parameter_snapshot"]["shared_parameters"]["output"] = {"debug_top_n": 50}

        def add_unknown_agent_leaf(task: dict[str, object]) -> None:
            task["parameter_snapshot"]["agents"][0]["parameters"] = {"trend": {"undeclared": 1.0}}

        mutations = (
            ("missing", remove_shared_leaf),
            ("extra", add_shared_leaf),
            ("output", add_output_group),
            ("fixed-agent-count", lambda task: task["parameter_snapshot"]["shared_parameters"]["split"].update({"agent_count": 2})),
            ("fixed-leave-one-out", lambda task: task["parameter_snapshot"]["shared_parameters"]["global_surrogate"].update({"leave_one_out": False})),
            ("unknown-agent", add_unknown_agent_leaf),
            ("type", lambda task: task["parameter_snapshot"]["agents"][0].update({"parameters": {"cleaning": {"median_window": "seven"}}})),
            ("bool-as-number", lambda task: task["parameter_snapshot"]["agents"][1].update({"parameters": {"trend": {"threshold": True}}})),
            ("nan", lambda task: task["parameter_snapshot"]["agents"][1].update({"parameters": {"trend": {"gain": math.nan}}})),
            ("infinity", lambda task: task["parameter_snapshot"]["agents"][2].update({"parameters": {"fusion": {"variance_weight": math.inf}}})),
        )
        for name, mutate in mutations:
            with self.subTest(name=name):
                raw_task = _simulation_task()
                mutate(raw_task)
                with self.assertRaises(ContractFailure):
                    simulation_preparation_parameters(parse_worker_task(raw_task))

    def test_generic_executor_and_context_ownership(self) -> None:
        contexts = (_context(1, "EARLY"), _context(2, "MIDDLE"), _context(3, "LATE"))
        validate_s1_contexts(contexts)
        result = AgentExecutor().execute(contexts, lambda context: context.agent * 10, stage="LOCAL_TRAINING")
        self.assertEqual(result, (10, 20, 30))
        contexts[0].runtime.local_error_window.append(1.0)
        self.assertEqual(contexts[1].runtime.local_error_window, [])

    def test_preflight_runner_verifies_hash_and_persists_contract_payload(self) -> None:
        fixture = Path(__file__).parent / "fixtures" / "preprocessing_fixture.csv"
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            source = root / "datasets" / "ds_1" / "source.csv"
            source.parent.mkdir(parents=True)
            source.write_bytes(fixture.read_bytes())
            digest = hashlib.sha256(source.read_bytes()).hexdigest()
            repository = _Repository()
            task = {
                "contract_version": "worker.task.v1",
                "job_id": "job_1",
                "job_type": "DATASET_PREFLIGHT",
                "run_id": None,
                "attempt_id": "attempt_1",
                "lease_token": "not-logged-in-tests",
                "dataset": {
                    "dataset_id": "ds_1",
                    "relative_path": "datasets/ds_1/source.csv",
                    "sha256": digest,
                    "timezone": "Asia/Shanghai",
                },
                "preprocessing": {
                    "contract_version": "preprocessing.v1",
                    "field_standard_configuration_sha256": "0" * 64,
                },
                "output": {
                    "relative_tmp_directory": "datasets/ds_1/preflight/tmp/attempt_1",
                    "required_summary_schema": "dataset-preflight.summary.v1",
                },
                "limits": {"memory_bytes": 1_073_741_824, "cancel_check_target_ms": 5000},
            }
            result = WorkerRunner(root, repository).run(task)
            summary_path = root / "datasets" / "ds_1" / "preflight" / "tmp" / "attempt_1" / "preflight_summary.json"
            self.assertEqual(len(result.running_rows), 35)
            self.assertTrue(summary_path.is_file())
            self.assertEqual(len(repository.preflight), 1)
            self.assertEqual(repository.failures, [])
            self.assertEqual(repository.events[0]["stage"], "PREPROCESSING")
            self.assertEqual(repository.operations, ["event", "event", "preflight"])

    def test_runner_starts_and_stops_a_task_scoped_lease_heartbeat_context(self) -> None:
        class ManagedCancellation:
            def __init__(self) -> None:
                self.started = 0
                self.stopped = 0

            def start_task_heartbeat(self) -> None:
                self.started += 1

            def stop_task_heartbeat(self) -> None:
                self.stopped += 1

            def is_cancel_requested(self) -> bool:
                return False

        managed = ManagedCancellation()
        repository = _Repository()
        repository.cancellation_context = lambda _task: managed  # type: ignore[method-assign]
        runner = WorkerRunner(Path("."), repository)
        task = SimpleNamespace(job_type="DATASET_PREFLIGHT", dataset_relative_path="datasets/ds_1/source.csv")
        with (
            patch("federated_iot_worker.runner.parse_worker_task", return_value=task),
            patch.object(runner, "_verify_dataset_hash"),
            patch.object(runner, "_resolve_storage_path", return_value=Path("frozen.csv")),
            patch.object(runner, "_run_preflight", return_value=SimpleNamespace()),
        ):
            runner.run({})
        self.assertEqual((managed.started, managed.stopped), (1, 1))

    def test_preprocessing_cancel_stops_renewal_before_the_cancel_terminal_write(self) -> None:
        class ManagedCancellation:
            def __init__(self) -> None:
                self.started = 0
                self.stopped = 0

            def start_task_heartbeat(self) -> None:
                self.started += 1

            def stop_task_heartbeat(self) -> None:
                self.stopped += 1

            def is_cancel_requested(self) -> bool:
                return True

        managed = ManagedCancellation()

        class CancellingRepository(_Repository):
            def __init__(self) -> None:
                super().__init__()
                self.stopped_at_terminal_write: int | None = None

            def cancellation_context(self, _task: Any) -> ManagedCancellation:
                return managed

            def commit_failure(self, task: Any, failure: Mapping[str, Any]) -> None:
                self.stopped_at_terminal_write = managed.stopped
                super().commit_failure(task, failure)

        repository = CancellingRepository()
        task = {
            "contract_version": "worker.task.v1",
            "job_id": "job_1",
            "job_type": "DATASET_PREFLIGHT",
            "run_id": None,
            "attempt_id": "attempt_1",
            "lease_token": "not-logged-in-tests",
            "dataset": {
                "dataset_id": "ds_1",
                "relative_path": "datasets/ds_1/source.csv",
                "sha256": "0" * 64,
                "timezone": "Asia/Shanghai",
            },
            "preprocessing": {
                "contract_version": "preprocessing.v1",
                "field_standard_configuration_sha256": "0" * 64,
            },
            "output": {
                "relative_tmp_directory": "datasets/ds_1/preflight/tmp/attempt_1",
                "required_summary_schema": "dataset-preflight.summary.v1",
            },
            "limits": {"memory_bytes": 1_073_741_824, "cancel_check_target_ms": 5000},
        }
        with self.assertRaises(CancelledFailure) as raised:
            WorkerRunner(Path("."), repository).run(task)
        self.assertEqual(raised.exception.stage, "PREPROCESSING")
        self.assertEqual((managed.started, managed.stopped), (1, 1))
        self.assertEqual(repository.stopped_at_terminal_write, 1)
        self.assertEqual(repository.events, [])
        self.assertEqual(repository.preflight, [])
        self.assertEqual(len(repository.failures), 1)
        self.assertEqual(repository.failures[0]["code"], "CANCELLED")

    def test_preflight_liveness_refreshes_only_from_bounded_cancellation_checks(self) -> None:
        fixture = Path(__file__).parent / "fixtures" / "preprocessing_fixture.csv"
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            source = root / "datasets" / "ds_1" / "source.csv"
            source.parent.mkdir(parents=True)
            source.write_bytes(fixture.read_bytes())
            digest = hashlib.sha256(source.read_bytes()).hexdigest()
            repository = _Repository()
            updates: list[str] = []
            heartbeat = root / "worker-heartbeat.json"
            task = {
                "contract_version": "worker.task.v1",
                "job_id": "job_1",
                "job_type": "DATASET_PREFLIGHT",
                "run_id": None,
                "attempt_id": "attempt_1",
                "lease_token": "not-logged-in-tests",
                "dataset": {"dataset_id": "ds_1", "relative_path": "datasets/ds_1/source.csv", "sha256": digest, "timezone": "Asia/Shanghai"},
                "preprocessing": {"contract_version": "preprocessing.v1", "field_standard_configuration_sha256": "0" * 64},
                "output": {"relative_tmp_directory": "datasets/ds_1/preflight/tmp/attempt_1", "required_summary_schema": "dataset-preflight.summary.v1"},
                "limits": {"memory_bytes": 1_073_741_824, "cancel_check_target_ms": 5000},
            }
            WorkerRunner(
                root,
                repository,
                liveness_callback=lambda: (write_heartbeat(heartbeat), updates.append("alive")),
                liveness_interval_seconds=3600,
            ).run(task)
            self.assertEqual(updates, ["alive"])
            self.assertTrue(heartbeat.is_file())

    def test_simulation_executes_generic_three_agent_reference_path(self) -> None:
        raw_task = _simulation_task()
        shared = raw_task["parameter_snapshot"]["shared_parameters"]
        shared["split"].update({"minimum_training": 10, "minimum_calibration": 5, "minimum_testing": 5})
        shared["local_gp"].update({"kNN": 3, "adaptive_ratio": 0.10})
        shared["interval"].update({"calibration_window": 5, "minimum_scores": 5, "coverage_window": 5})
        shared["anchors"].update({"base_centers": 3, "transition_centers": 1, "boundary_centers": 1, "public_anchors": 10, "iterations": 2})
        shared["fusion"].update({"error_window": 5, "minimum_samples": 5})
        task = parse_worker_task(raw_task)
        preparation = WorkerRunner(Path("."), _Repository()).prepare_simulation(
            task,
            {agent: SimpleNamespace(running_rows=_running_rows(300)) for agent in (1, 2, 3)},
        )
        outcome = execute_simulation(preparation.agents)
        self.assertEqual([result.agent for result in outcome.agent_results], [1, 2, 3])
        self.assertTrue(all(result.rows for result in outcome.agent_results))
        self.assertTrue(all(math.isfinite(result.metrics["FusedRMSE"]) for result in outcome.agent_results))
        self.assertTrue(outcome.diagnostics["predict_then_update"])

    def test_diagnostic_top_n_and_fallback_counts_are_deterministic_for_one_frozen_seed(self) -> None:
        def execute_once() -> tuple[object, object]:
            raw_task = _simulation_task()
            shared = raw_task["parameter_snapshot"]["shared_parameters"]
            shared["split"].update({"minimum_training": 10, "minimum_calibration": 5, "minimum_testing": 5})
            shared["local_gp"].update({"kNN": 3, "adaptive_ratio": 0.10})
            shared["interval"].update({"calibration_window": 5, "minimum_scores": 5, "coverage_window": 5})
            shared["anchors"].update({"base_centers": 3, "transition_centers": 1, "boundary_centers": 1, "public_anchors": 10, "iterations": 2})
            shared["fusion"].update({"error_window": 5, "minimum_samples": 5})
            task = parse_worker_task(raw_task)
            preparation = WorkerRunner(Path("."), _Repository()).prepare_simulation(
                task,
                {agent: SimpleNamespace(running_rows=_running_rows(300)) for agent in (1, 2, 3)},
            )
            outcome = execute_simulation(preparation.agents)
            return (
                outcome.diagnostics["local_fallbacks"],
                tuple(
                    (
                        result.agent,
                        result.diagnostics["local_fallbacks"],
                        tuple(result.diagnostics["top_fused_error_indices"]),
                    )
                    for result in outcome.agent_results
                ),
            )

        self.assertEqual(execute_once(), execute_once())

    def test_runner_atomically_publishes_all_simulation_artifacts_before_repository_commit(self) -> None:
        raw_task = _simulation_task()
        shared = raw_task["parameter_snapshot"]["shared_parameters"]
        shared["split"].update({"minimum_training": 10, "minimum_calibration": 5, "minimum_testing": 5})
        shared["local_gp"].update({"kNN": 3, "adaptive_ratio": 0.10})
        shared["interval"].update({"calibration_window": 5, "minimum_scores": 5, "coverage_window": 5})
        shared["anchors"].update({"base_centers": 3, "transition_centers": 1, "boundary_centers": 1, "public_anchors": 10, "iterations": 2})
        shared["fusion"].update({"error_window": 5, "minimum_samples": 5})
        dataset = PreprocessedDataset("0" * 64, PreprocessingConfig(), _running_rows(300), (), 0, 0, 0, 0, None, None, None, None, None, 0)
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            source = root / "datasets" / "ds_fixture_001" / "source.csv"
            source.parent.mkdir(parents=True)
            source.write_text("frozen source\n", encoding="utf-8")
            raw_task["dataset"]["sha256"] = hashlib.sha256(source.read_bytes()).hexdigest()
            repository = _Repository()
            runner = WorkerRunner(root, repository)
            with patch.object(runner, "_preprocess_agent_datasets", return_value={1: dataset, 2: dataset, 3: dataset}):
                outcome = runner.run(raw_task)
            committed = root / "runs" / str(raw_task["run_id"]) / "committed"
            names = {path.name for path in committed.iterdir()}
            self.assertEqual(len(outcome.agent_results), 3)
            self.assertIn("artifact_manifest.json", names)
            self.assertEqual(len(repository.simulation_commits), 1)
            self.assertEqual(len(repository.simulation_commits[0]["artifacts"]), 12)
            with (committed / "alarms.csv").open("r", encoding="utf-8", newline="") as handle:
                artifact_alarms = list(csv.DictReader(handle))
            expected_alarms = [alarm for result in outcome.agent_results for alarm in result.alarms]
            self.assertEqual([row["Time"] for row in artifact_alarms], [alarm["time"] for alarm in expected_alarms])
            indexed_alarms = repository.simulation_commits[0]["alarms"]
            self.assertEqual(len(indexed_alarms), len(expected_alarms))
            self.assertEqual([item["time_value"] for item in indexed_alarms], [alarm["time"] for alarm in expected_alarms])
            self.assertEqual(
                [item["result_locator"] for item in indexed_alarms],
                [
                    {"agent": alarm["agent"], "original_running_index": alarm["original_running_index"]}
                    for alarm in expected_alarms
                ],
            )
            persisted_manifest = json.loads((committed / "artifact_manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(
                persisted_manifest["snapshot_sha256"],
                raw_task["parameter_snapshot"]["sha256"],
            )
            self.assertEqual(repository.operations[-1], "simulation")

    def test_simulation_preparation_consumes_frozen_shared_parameters_and_runtime_seed(self) -> None:
        raw_task = json.loads(
            (ROOT.parent / "contracts" / "worker" / "fixtures" / "simulation-task.v1.json").read_text(encoding="utf-8")
        )
        shared = _reference_shared_parameters()
        shared["feature_state"]["nLag"] = 6
        shared["trend"]["gain"] = 2.0
        raw_task["parameter_snapshot"]["shared_parameters"] = shared
        raw_task["runtime"]["random_streams"]["base_center_seed_by_agent"] = {"1": 9101, "2": 9102, "3": 9103}
        task = parse_worker_task(raw_task)

        parameters = simulation_preparation_parameters(task)
        self.assertEqual((parameters.for_agent(1).n_lag, parameters.for_agent(1).trend_gain), (6, 2.0))
        prepared = WorkerRunner(Path("."), _Repository()).prepare_simulation(
            task,
            {agent: SimpleNamespace(running_rows=_running_rows(660)) for agent in (1, 2, 3)},
            parameters,
        )
        self.assertEqual([agent.feature_dataset.shape[1] for agent in prepared.agents], [56, 56, 56])
        self.assertEqual([agent.random_seed for agent in prepared.agents], [9101, 9102, 9103])
        self.assertEqual(prepared.agents[0].feature_dataset.trends[0], 17.0)

    def test_agent_sparse_overrides_are_deep_merged_and_consumed_per_context(self) -> None:
        raw_task = _simulation_task()
        raw_task["parameter_snapshot"]["agents"][0]["parameters"] = {
            "cleaning": {"median_window": 7},
            "feature_state": {"nLag": 5},
            "split": {"training_ratio": 0.50, "calibration_ratio": 0.20},
            "trend": {"gain": 1.5},
            "local_gp": {"kNN": 81},
        }
        raw_task["parameter_snapshot"]["agents"][1]["parameters"] = {
            "feature_state": {"nLag": 6, "speed_threshold": 0.02},
            "split": {"training_ratio": 0.55, "calibration_ratio": 0.15},
            "trend": {"gain": 2.0},
            "local_gp": {"kNN": 82},
        }
        raw_task["parameter_snapshot"]["agents"][2]["parameters"] = {
            "cleaning": {"smoothing_window": 3},
            "feature_state": {"nLag": 7},
            "split": {"training_ratio": 0.60, "calibration_ratio": 0.11},
            "trend": {"gain": 3.0},
            "local_gp": {"kNN": 83},
        }
        raw_task["runtime"]["random_streams"]["base_center_seed_by_agent"] = {"1": 9101, "2": 9102, "3": 9103}
        task = parse_worker_task(raw_task)
        parameters = simulation_preparation_parameters(task)

        self.assertEqual([parameters.for_agent(agent).n_lag for agent in (1, 2, 3)], [5, 6, 7])
        self.assertEqual([parameters.for_agent(agent).trend_gain for agent in (1, 2, 3)], [1.5, 2.0, 3.0])
        self.assertEqual([parameters.for_agent(agent).base_center_seed for agent in (1, 2, 3)], [9101, 9102, 9103])
        self.assertEqual([parameters.for_agent(agent).effective_parameters["local_gp"]["kNN"] for agent in (1, 2, 3)], [81, 82, 83])
        self.assertEqual([len(_flatten_parameter_paths(parameters.for_agent(agent).effective_parameters)) for agent in (1, 2, 3)], [69, 69, 69])
        self.assertEqual(parameters.for_agent(1).preprocessing.median_window, 7)
        self.assertEqual(parameters.for_agent(2).preprocessing.speed_stop_threshold, 0.02)
        self.assertEqual(parameters.for_agent(3).preprocessing.smooth_window, 3)

        prepared = WorkerRunner(Path("."), _Repository()).prepare_simulation(
            task,
            {agent: SimpleNamespace(running_rows=_running_rows(900)) for agent in (1, 2, 3)},
            parameters,
        )
        self.assertEqual([agent.feature_dataset.shape[1] for agent in prepared.agents], [52, 56, 60])
        self.assertEqual([len(agent.split.train) for agent in prepared.agents], [147, 161, 175])
        self.assertEqual([len(agent.split.calibration) for agent in prepared.agents], [59, 44, 32])
        self.assertEqual([agent.random_seed for agent in prepared.agents], [9101, 9102, 9103])
        self.assertEqual([agent.parameters["trend"]["gain"] for agent in prepared.agents], [1.5, 2.0, 3.0])
        self.assertEqual([agent.parameters["global_surrogate"]["leave_one_out"] for agent in prepared.agents], [True, True, True])
        self.assertTrue(all("output" not in agent.parameters for agent in prepared.agents))
        self.assertNotEqual(id(prepared.agents[0].parameters), id(prepared.agents[1].parameters))

    def test_preprocessing_cache_is_task_local_and_uses_agent_effective_parameters(self) -> None:
        shared_raw = _simulation_task()
        shared_parameters = simulation_preparation_parameters(parse_worker_task(shared_raw))
        calls: list[object] = []

        def preprocess(core: AlgorithmCore, _source: Path, *, cancellation: object) -> object:
            calls.append(core.config)
            return SimpleNamespace(running_rows=_running_rows(900))

        runner = WorkerRunner(Path("."), _Repository())
        with patch.object(AlgorithmCore, "preprocess_csv", autospec=True, side_effect=preprocess):
            shared_datasets = runner._preprocess_agent_datasets(Path("frozen.csv"), NeverCancelled(), shared_parameters)
        self.assertEqual(len(calls), 1)
        self.assertIs(shared_datasets[1], shared_datasets[2])
        self.assertIs(shared_datasets[2], shared_datasets[3])

        custom_raw = deepcopy(shared_raw)
        custom_raw["parameter_snapshot"]["agents"][0]["parameters"] = {"cleaning": {"median_window": 7}}
        custom_raw["parameter_snapshot"]["agents"][1]["parameters"] = {"feature_state": {"speed_threshold": 0.02}}
        custom_raw["parameter_snapshot"]["agents"][2]["parameters"] = {"cleaning": {"smoothing_window": 3}}
        custom_parameters = simulation_preparation_parameters(parse_worker_task(custom_raw))
        calls.clear()
        with patch.object(AlgorithmCore, "preprocess_csv", autospec=True, side_effect=preprocess):
            custom_datasets = runner._preprocess_agent_datasets(Path("frozen.csv"), NeverCancelled(), custom_parameters)
        self.assertEqual(len(calls), 3)
        self.assertEqual([config.median_window for config in calls], [7, 21, 21])
        self.assertEqual([config.speed_stop_threshold for config in calls], [0.01, 0.02, 0.01])
        self.assertEqual([config.smooth_window for config in calls], [5, 5, 3])
        self.assertNotEqual(id(custom_datasets[1]), id(custom_datasets[2]))
        self.assertNotEqual(id(custom_datasets[2]), id(custom_datasets[3]))

    def test_parameter_snapshot_isolated_from_later_task_or_profile_changes(self) -> None:
        first_raw = _simulation_task()
        first_raw["parameter_snapshot"]["agents"][0]["parameters"] = {"cleaning": {"median_window": 7}}
        first_parameters = simulation_preparation_parameters(parse_worker_task(first_raw))
        first_raw["parameter_snapshot"]["shared_parameters"]["cleaning"]["median_window"] = 99

        second_raw = _simulation_task()
        second_raw["parameter_snapshot"]["agents"][0]["parameters"] = {"cleaning": {"median_window": 9}}
        second_parameters = simulation_preparation_parameters(parse_worker_task(second_raw))

        self.assertEqual(first_parameters.for_agent(1).preprocessing.median_window, 7)
        self.assertEqual(second_parameters.for_agent(1).preprocessing.median_window, 9)

    def test_simulation_preparation_rejects_an_empty_shared_parameter_snapshot(self) -> None:
        raw_task = json.loads(
            (ROOT.parent / "contracts" / "worker" / "fixtures" / "simulation-task.v1.json").read_text(encoding="utf-8")
        )

        with self.assertRaises(ContractFailure) as raised:
            simulation_preparation_parameters(parse_worker_task(raw_task))

        self.assertIn("parameter_snapshot.shared_parameters", str(raised.exception))

    def test_dataset_hash_honors_cancellation_at_the_sixteen_mebibyte_checkpoint(self) -> None:
        class CancellationRequested:
            def __init__(self) -> None:
                self.checks = 0

            def is_cancel_requested(self) -> bool:
                self.checks += 1
                return True

        with tempfile.TemporaryDirectory() as temporary_directory:
            source = Path(temporary_directory) / "source.csv"
            source.write_bytes(b"x" * (17 * 1024 * 1024))
            cancellation = CancellationRequested()
            with self.assertRaises(CancelledFailure) as raised:
                WorkerRunner(Path(temporary_directory), _Repository())._verify_dataset_hash(
                    SimpleNamespace(dataset_sha256="0" * 64), source, cancellation
                )
        self.assertEqual(raised.exception.stage, "PREPROCESSING")
        self.assertEqual(cancellation.checks, 1)


if __name__ == "__main__":
    unittest.main()
