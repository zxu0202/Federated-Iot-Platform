from __future__ import annotations

import json
import sys
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path
from threading import Event
from time import sleep
from types import SimpleNamespace
from unittest.mock import Mock, patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from federated_iot_worker import worker_service
from federated_iot_worker.contracts import parse_worker_task
from federated_iot_worker.postgres_repository import (
    ClaimedJob,
    CancellationState,
    LeaseLostFailure,
    PostgresCancellationContext,
    PostgresSettings,
    PostgresWorkerRepository,
    RepositoryOperationError,
    WorkerIdentity,
)
from federated_iot_worker.worker_service import WorkerService
from federated_iot_worker.runner import load_worker_runtime_settings


class _Cursor:
    def __init__(self, row: object) -> None:
        self._row = row

    def fetchone(self) -> object:
        return self._row


class _Connection:
    def __init__(self, responses: list[object]) -> None:
        self._responses = responses
        self.calls: list[tuple[str, tuple[object, ...]]] = []
        self.closed = False

    def execute(self, query: str, parameters: tuple[object, ...] = ()) -> _Cursor:
        self.calls.append((query, tuple(parameters)))
        if not self._responses:
            raise AssertionError(f"unexpected query: {query}")
        return _Cursor(self._responses.pop(0))

    def close(self) -> None:
        self.closed = True


class _HeartbeatRepository:
    """Thread-safe test double for task-heartbeat lifecycle coverage."""

    def __init__(self, *, fail_after: int | None = None) -> None:
        self.calls = 0
        self._fail_after = fail_after
        self.cancel_requested = False
        self.lease_valid = True
        self.renewed = Event()
        self.failed = Event()

    def heartbeat(self, _task: object) -> None:
        self.calls += 1
        self.renewed.set()
        if self._fail_after is not None and self.calls >= self._fail_after:
            self.failed.set()
            raise RepositoryOperationError("PostgreSQL Worker Repository worker_heartbeat_for_worker operation failed.")

    def cancellation_state(self, _task: object) -> CancellationState:
        return CancellationState(cancel_requested=self.cancel_requested, lease_valid=self.lease_valid)


def _preflight_envelope(*, attempt_id: str = "attempt_1", lease_token: str = "lease-token-at-least-16") -> dict[str, object]:
    return {
        "contract_version": "worker.task.v1",
        "job_id": "job_1",
        "job_type": "DATASET_PREFLIGHT",
        "run_id": None,
        "attempt_id": attempt_id,
        "lease_token": lease_token,
        "dataset": {
            "dataset_id": "ds_1",
            "relative_path": "datasets/ds_1/source.csv",
            "sha256": "0" * 64,
            "timezone": "Asia/Shanghai",
        },
        "preprocessing": {
            "contract_version": "preprocessing.v1",
            "field_standard_configuration_sha256": "1" * 64,
        },
        "output": {
            "relative_tmp_directory": f"datasets/ds_1/preflight/tmp/{attempt_id}",
            "required_summary_schema": "dataset-preflight.summary.v1",
        },
        "limits": {"memory_bytes": 1_073_741_824, "cancel_check_target_ms": 5000},
    }


class PostgresRepositoryTests(unittest.TestCase):
    def _repository(self, responses: list[object]) -> tuple[PostgresWorkerRepository, _Connection]:
        connection = _Connection(responses)
        repository = PostgresWorkerRepository(
            connection,
            WorkerIdentity("algorithm-worker-1", "1.0.0-m1"),
            lease_seconds=60,
            heartbeat_interval_seconds=10,
        )
        return repository, connection

    def test_uses_only_controlled_worker_functions_for_full_preflight_lifecycle(self) -> None:
        envelope = _preflight_envelope()
        repository, connection = self._repository(
            [
                (datetime.now(timezone.utc),),
                (True,),
                ("job_1", "DATASET_PREFLIGHT", None, json.dumps(envelope), datetime.now(timezone.utc)),
                (False, None, True),
                (True,),
                (True,),
            ]
        )
        repository.register_instance()
        repository.heartbeat_instance()
        claim = repository.claim_next_job("attempt_1", "lease-token-at-least-16")
        assert claim is not None
        self.assertEqual((claim.attempt_id, claim.lease_token), ("attempt_1", "lease-token-at-least-16"))
        task = parse_worker_task(claim.envelope)
        self.assertFalse(repository.cancellation_context(task).is_cancel_requested())
        repository.report_event(
            task,
            {
                "schema_version": "worker.event.v1",
                "job_id": task.job_id,
                "run_id": None,
                "attempt_id": task.attempt_id,
                "status": "RUNNING",
                "stage": "PREPROCESSING",
                "agent": None,
                "occurred_at": datetime.now(timezone.utc).isoformat(),
                "diagnostics": {},
            },
        )
        repository.commit_preflight(task, {"summary_sha256": "a" * 64})

        statements = [query for query, _parameters in connection.calls]
        self.assertEqual(
            [statement.split("(")[0].split()[-1] for statement in statements],
            [
                "worker_register_instance",
                "worker_heartbeat_instance",
                "worker_claim_next_job_for_worker",
                "worker_cancellation_context",
                "worker_report_event",
                "worker_complete_preflight",
            ],
        )
        self.assertTrue(all("worker_jobs" not in statement and "worker_instances" not in statement for statement in statements))
        self.assertEqual(connection.calls[-1][1][-1], "a" * 64)

    def test_false_cancellation_lease_stops_without_terminal_write(self) -> None:
        repository, connection = self._repository([(False, None, False)])
        task = parse_worker_task(_preflight_envelope())
        with self.assertRaises(LeaseLostFailure):
            repository.cancellation_context(task).is_cancel_requested()
        self.assertEqual(len(connection.calls), 1)

    def test_cancellation_intent_wins_over_a_concurrent_invalid_lease(self) -> None:
        repository, connection = self._repository([(True, datetime.now(timezone.utc), False)])
        task = parse_worker_task(_preflight_envelope())
        self.assertTrue(repository.cancellation_context(task).is_cancel_requested())
        self.assertEqual(len(connection.calls), 1)

    def test_initial_cancellation_does_not_start_a_renewal_that_can_race_confirmation(self) -> None:
        repository = _HeartbeatRepository()
        repository.cancel_requested = True
        repository.lease_valid = False
        context = PostgresCancellationContext(
            repository,
            parse_worker_task(_preflight_envelope()),
            heartbeat_interval_seconds=0.01,
        )
        context.start_task_heartbeat()
        self.assertEqual(repository.calls, 0)
        self.assertTrue(context.is_cancel_requested())

    def test_cancelled_failure_uses_the_controlled_cancel_confirmation(self) -> None:
        repository, connection = self._repository([(True,)])
        task = parse_worker_task(_preflight_envelope())
        repository.commit_failure(task, {"code": "CANCELLED", "recoverable": True})
        statement, parameters = connection.calls[0]
        self.assertIn("worker_confirm_cancel", statement)
        self.assertNotIn("worker_fail_job", statement)
        self.assertEqual(parameters, (task.job_id, task.attempt_id, task.lease_token))

    def test_task_lease_heartbeat_runs_without_waiting_for_a_cancellation_poll(self) -> None:
        repository = _HeartbeatRepository()
        context = PostgresCancellationContext(
            repository,
            parse_worker_task(_preflight_envelope()),
            heartbeat_interval_seconds=0.01,
        )
        context.start_task_heartbeat()
        try:
            self.assertTrue(repository.renewed.wait(timeout=0.2))
            first_count = repository.calls
            sleep(0.04)
            self.assertGreater(repository.calls, first_count)
            self.assertFalse(context.is_cancel_requested())
        finally:
            context.stop_task_heartbeat()

    def test_background_lease_heartbeat_failure_is_reported_at_the_next_bounded_check(self) -> None:
        repository = _HeartbeatRepository(fail_after=2)
        context = PostgresCancellationContext(
            repository,
            parse_worker_task(_preflight_envelope()),
            heartbeat_interval_seconds=0.01,
        )
        context.start_task_heartbeat()
        try:
            self.assertTrue(repository.failed.wait(timeout=0.2))
            with self.assertRaisesRegex(RepositoryOperationError, "worker_heartbeat_for_worker"):
                context.is_cancel_requested()
        finally:
            context.stop_task_heartbeat()

    def test_observed_cancellation_outranks_a_pending_heartbeat_lease_failure(self) -> None:
        repository = _HeartbeatRepository(fail_after=2)
        context = PostgresCancellationContext(
            repository,
            parse_worker_task(_preflight_envelope()),
            heartbeat_interval_seconds=0.01,
        )
        context.start_task_heartbeat()
        try:
            self.assertTrue(repository.failed.wait(timeout=0.2))
            repository.cancel_requested = True
            repository.lease_valid = False
            self.assertTrue(context.is_cancel_requested())
        finally:
            context.stop_task_heartbeat()

    def test_repository_error_identifies_the_controlled_function_without_parameters(self) -> None:
        repository, _connection = self._repository([])
        task = SimpleNamespace(job_id="job_1", attempt_id="attempt_1", lease_token="secret-not-for-logs")
        with self.assertRaisesRegex(RepositoryOperationError, "worker_heartbeat_for_worker") as raised:
            repository.heartbeat(task)
        self.assertNotIn("secret-not-for-logs", str(raised.exception))

    def test_terminal_commit_error_identifies_worker_commit_simulation_without_payload_data(self) -> None:
        repository, _connection = self._repository([])
        task = SimpleNamespace(job_id="job_1", attempt_id="attempt_1", lease_token="secret-not-for-logs")
        with self.assertRaisesRegex(RepositoryOperationError, "worker_commit_simulation") as raised:
            repository.commit_simulation(
                task,
                manifest_sha256="a" * 64,
                artifacts=[{"name": "artifact_manifest.json", "required": True}],
                alarms=[{"result_locator": {"agent": 1, "original_running_index": 14059}}],
                stage_durations_ms={"testing": 1},
            )
        self.assertNotIn("secret-not-for-logs", str(raised.exception))

    def test_invalid_claim_can_be_failed_with_generated_lease_identity(self) -> None:
        repository, connection = self._repository([(True,)])
        claim = ClaimedJob(
            job_id="job_1",
            job_type="DATASET_PREFLIGHT",
            run_id=None,
            attempt_id="attempt_1",
            lease_token="lease-token-at-least-16",
            envelope={},
            lease_expires_at=None,
        )
        repository.fail_claim(claim, {"code": "WORKER_CONTRACT_MISMATCH"})
        self.assertEqual(connection.calls[0][1][:3], ("job_1", "attempt_1", "lease-token-at-least-16"))

    def test_simulation_commit_uses_only_the_exact_terminal_function(self) -> None:
        repository, connection = self._repository([(True,)])
        task = SimpleNamespace(job_id="job_1", attempt_id="attempt_1", lease_token="lease-token-at-least-16")
        repository.commit_simulation(
            task,
            manifest_sha256="a" * 64,
            artifacts=[{"name": "artifact_manifest.json", "required": True}],
            alarms=[],
            stage_durations_ms={"testing": 1},
        )
        statement, parameters = connection.calls[0]
        self.assertIn("worker_commit_simulation", statement)
        self.assertNotIn("worker_jobs", statement)
        self.assertEqual(parameters[:4], ("job_1", "attempt_1", "lease-token-at-least-16", "a" * 64))
        self.assertEqual(json.loads(parameters[4])[0]["name"], "artifact_manifest.json")

    def test_simulation_commit_retry_reuses_one_unchanged_terminal_payload_without_worker_events(self) -> None:
        repository, connection = self._repository([(True,), (True,)])
        task = SimpleNamespace(job_id="job_1", attempt_id="attempt_1", lease_token="lease-token-at-least-16")
        artifacts = [
            {"name": "artifact_manifest.json", "required": True, "sha256": "a" * 64},
            {"name": "results_agent_1.csv", "required": True, "sha256": "b" * 64},
        ]
        alarms = [
            {
                "agent": 1,
                "original_running_index": 14059,
                "time_value": "2026-07-04 02:01:51.113",
                "overall_alarm_level": "Warning",
                "alarm_type": "OVERALL",
                "reasons_json": ["threshold"],
                "load_status": "Heavy load",
                "result_locator": {"agent": 1, "original_running_index": 14059},
            }
        ]
        for _ in range(2):
            repository.commit_simulation(
                task,
                manifest_sha256="c" * 64,
                artifacts=artifacts,
                alarms=alarms,
                stage_durations_ms={"testing": 1},
            )

        self.assertEqual(len(connection.calls), 2)
        first_statement, first_parameters = connection.calls[0]
        second_statement, second_parameters = connection.calls[1]
        self.assertEqual(first_statement, second_statement)
        self.assertIn("worker_commit_simulation", first_statement)
        self.assertNotIn("worker_report_event", first_statement)
        self.assertEqual(first_parameters, second_parameters)
        self.assertEqual(json.loads(first_parameters[4]), artifacts)
        self.assertEqual(json.loads(first_parameters[5]), alarms)
        terminal_alarm = json.loads(first_parameters[5])[0]
        self.assertEqual(
            set(terminal_alarm),
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
        self.assertNotIn("result_locator_json", terminal_alarm)

    def test_connect_uses_secret_file_and_fixed_internal_tls_mode(self) -> None:
        connection = _Connection([])
        driver = SimpleNamespace(connect=Mock(return_value=connection))
        with tempfile.TemporaryDirectory() as temporary_directory:
            secret = Path(temporary_directory) / "worker-db-password"
            secret.write_text("not-logged\n", encoding="utf-8")
            settings = PostgresSettings("postgres", 5432, "federated_iot", "algorithm_worker", secret, 10)
            with patch("federated_iot_worker.postgres_repository.importlib.import_module", return_value=driver):
                repository = PostgresWorkerRepository.connect(
                    settings,
                    WorkerIdentity("algorithm-worker-1", "1.0.0-m1"),
                    lease_seconds=60,
                )
        self.assertIsInstance(repository, PostgresWorkerRepository)
        self.assertEqual(driver.connect.call_args.kwargs["sslmode"], "disable")
        self.assertEqual(driver.connect.call_args.kwargs["host"], "postgres")

    def test_runtime_settings_read_the_controlled_postgres_dns_and_single_worker_limits(self) -> None:
        contents = """config_version: platform-config.v1
database:
  profile: postgres
  host: postgres
  port: 5432
  name: federated_iot
  worker_username: algorithm_worker
  worker_password_file: /run/secrets/worker_db_password
  connect_timeout_seconds: 10
storage:
  dataset_root: /var/lib/iot/datasets
  artifact_root: /var/lib/iot/runs
limits:
  worker_pool_capacity: 1
  worker_poll_interval_ms: 2000
  worker_lease_seconds: 60
  worker_heartbeat_seconds: 10
"""
        with tempfile.TemporaryDirectory() as temporary_directory:
            config = Path(temporary_directory) / "platform.yaml"
            config.write_text(contents, encoding="utf-8")
            settings = load_worker_runtime_settings(config)
        self.assertEqual(settings.postgres.host, "postgres")
        self.assertEqual(settings.postgres.port, 5432)
        self.assertEqual(settings.storage_root, Path("/var/lib/iot"))
        self.assertEqual((settings.poll_seconds, settings.lease_seconds, settings.heartbeat_seconds), (2.0, 60, 10.0))

    def test_database_error_during_claim_execution_is_propagated_to_stop_the_service(self) -> None:
        claim = ClaimedJob(
            job_id="job_1",
            job_type="DATASET_PREFLIGHT",
            run_id=None,
            attempt_id="attempt_1",
            lease_token="lease-token-at-least-16",
            envelope={"not": "parsed in this isolated propagation test"},
            lease_expires_at=None,
        )
        task = SimpleNamespace(
            job_id="job_1",
            job_type="DATASET_PREFLIGHT",
            run_id=None,
            attempt_id="attempt_1",
            lease_token="lease-token-at-least-16",
        )
        with (
            patch.object(worker_service, "parse_worker_task", return_value=task),
            patch.object(worker_service.WorkerRunner, "run", side_effect=RepositoryOperationError("database unavailable")),
        ):
            with self.assertRaisesRegex(RepositoryOperationError, "database unavailable"):
                WorkerService(SimpleNamespace(), Path("."))._run_claim(claim)


if __name__ == "__main__":
    unittest.main()
