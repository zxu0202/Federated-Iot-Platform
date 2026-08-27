"""Narrow PostgreSQL Worker Repository adapter using function calls only."""

from __future__ import annotations

import importlib
import json
import re
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from threading import Event, Lock, RLock, Thread
from time import monotonic
from typing import Any, Mapping, Protocol, Sequence

from .cancellation import CancellationContext
from .contracts import WORKER_TASK_VERSION, WorkerRepository, WorkerTask
from .errors import WorkerFailure


_IDENTIFIER_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
_WORKER_FUNCTION_RE = re.compile(r"\b(worker_[a-z_]+)\s*\(", re.ASCII)


class PostgresCursor(Protocol):
    """Minimal cursor shape used by the adapter and its offline unit tests."""

    def fetchone(self) -> Sequence[Any] | Mapping[str, Any] | None:
        """Return one function result row."""


class PostgresConnection(Protocol):
    """Minimal autocommit connection shape; no table operation is exposed."""

    def execute(self, query: str, parameters: Sequence[Any] = ()) -> PostgresCursor:
        """Execute one positional Worker Repository function call."""

    def close(self) -> None:
        """Release the database connection."""


@dataclass(frozen=True)
class PostgresSettings:
    """Secret-file PostgreSQL settings for the Algorithm Worker credential."""

    host: str
    port: int
    database: str
    username: str
    password_file: Path
    connect_timeout_seconds: int


@dataclass(frozen=True)
class WorkerIdentity:
    """Durable identity for the single generic Worker instance."""

    worker_id: str
    worker_version: str

    def __post_init__(self) -> None:
        if not _IDENTIFIER_RE.fullmatch(self.worker_id):
            raise RepositoryConfigurationError("Worker identifier is invalid.")
        if not _IDENTIFIER_RE.fullmatch(self.worker_version):
            raise RepositoryConfigurationError("Worker version is invalid.")


@dataclass(frozen=True)
class ClaimedJob:
    """A function-issued task envelope held by one Worker lease."""

    job_id: str
    job_type: str
    run_id: str | None
    attempt_id: str
    lease_token: str
    envelope: Mapping[str, Any]
    lease_expires_at: datetime | str | None


@dataclass(frozen=True)
class CancellationState:
    """Persisted cancellation and lease result for one active attempt."""

    cancel_requested: bool
    lease_valid: bool


class RepositoryConfigurationError(Exception):
    """A local configuration or offline dependency failure without a secret."""


class RepositoryOperationError(Exception):
    """A database function could not complete a Worker Repository operation."""


class LeaseLostFailure(WorkerFailure):
    """Stop computation when a persisted attempt is no longer authoritative."""

    def __init__(self) -> None:
        super().__init__("LEASE_LOST", "Worker lease is no longer valid.", recoverable=True)


class PostgresCancellationContext(CancellationContext):
    """Cancellation checks with a task-scoped, independently scheduled lease renewal."""

    def __init__(self, repository: "PostgresWorkerRepository", task: WorkerTask, heartbeat_interval_seconds: float) -> None:
        self._repository = repository
        self._task = task
        self._heartbeat_interval_seconds = heartbeat_interval_seconds
        self._next_heartbeat = monotonic() + heartbeat_interval_seconds
        self._heartbeat_stop = Event()
        self._heartbeat_failure: LeaseLostFailure | RepositoryOperationError | None = None
        self._heartbeat_failure_lock = Lock()
        self._heartbeat_thread: Thread | None = None

    def start_task_heartbeat(self) -> None:
        """Start one bounded renewal loop before numerical work can occupy the CPU."""

        if self._heartbeat_thread is not None:
            return
        if self._cancellation_requested():
            return
        try:
            self._renew_lease()
        except LeaseLostFailure:
            if self._cancellation_requested():
                return
            raise
        self._next_heartbeat = monotonic() + self._heartbeat_interval_seconds
        thread = Thread(
            target=self._heartbeat_loop,
            name="worker-task-lease-heartbeat",
            daemon=True,
        )
        self._heartbeat_thread = thread
        thread.start()

    def stop_task_heartbeat(self) -> None:
        """Stop and join the task renewal loop before the Worker can claim again."""

        self._heartbeat_stop.set()
        thread = self._heartbeat_thread
        if thread is not None and thread.is_alive():
            thread.join(timeout=max(1.0, self._heartbeat_interval_seconds * 2.0))
            if thread.is_alive():
                error = RepositoryOperationError("PostgreSQL Worker Repository task heartbeat did not stop cleanly.")
                self._record_heartbeat_failure(error)
                raise error

    def is_cancel_requested(self) -> bool:
        """Prioritize observed cancellation over a concurrent lease-state transition."""

        state = self._repository.cancellation_state(self._task)
        if state.cancel_requested:
            return True
        if not state.lease_valid:
            raise LeaseLostFailure()
        self._raise_heartbeat_failure()
        if self._heartbeat_thread is None and monotonic() >= self._next_heartbeat:
            try:
                self._renew_lease()
            except LeaseLostFailure:
                if self._cancellation_requested():
                    return True
                raise
            self._next_heartbeat = monotonic() + self._heartbeat_interval_seconds
        return False

    def _heartbeat_loop(self) -> None:
        while not self._heartbeat_stop.wait(self._heartbeat_interval_seconds):
            try:
                self._renew_lease()
            except (LeaseLostFailure, RepositoryOperationError) as error:
                self._record_heartbeat_failure(error)
                self._heartbeat_stop.set()
                return

    def _renew_lease(self) -> None:
        self._repository.heartbeat(self._task)

    def _cancellation_requested(self) -> bool:
        return self._repository.cancellation_state(self._task).cancel_requested

    def _record_heartbeat_failure(self, error: LeaseLostFailure | RepositoryOperationError) -> None:
        with self._heartbeat_failure_lock:
            if self._heartbeat_failure is None:
                self._heartbeat_failure = error

    def _raise_heartbeat_failure(self) -> None:
        with self._heartbeat_failure_lock:
            error = self._heartbeat_failure
        if error is not None:
            raise error


class PostgresWorkerRepository(WorkerRepository):
    """Function-only PostgreSQL adapter for one active Worker process."""

    def __init__(
        self,
        connection: PostgresConnection,
        identity: WorkerIdentity,
        *,
        lease_seconds: int,
        heartbeat_interval_seconds: float = 10.0,
    ) -> None:
        if not 1 <= lease_seconds <= 600:
            raise RepositoryConfigurationError("Worker lease duration must be between 1 and 600 seconds.")
        if heartbeat_interval_seconds <= 0:
            raise RepositoryConfigurationError("Worker heartbeat interval must be positive.")
        self._connection = connection
        self._identity = identity
        self._lease_seconds = lease_seconds
        self._heartbeat_interval_seconds = heartbeat_interval_seconds
        self._connection_lock = RLock()

    @classmethod
    def connect(cls, settings: PostgresSettings, identity: WorkerIdentity, *, lease_seconds: int, heartbeat_interval_seconds: float = 10.0) -> "PostgresWorkerRepository":
        """Connect with an approved local psycopg wheel and a mounted secret file."""

        password = _read_secret(settings.password_file)
        try:
            psycopg = importlib.import_module("psycopg")
        except ModuleNotFoundError as error:
            raise RepositoryConfigurationError(
                "PostgreSQL driver psycopg is unavailable; install only the approved offline wheelhouse package."
            ) from error
        try:
            connection = psycopg.connect(
                host=settings.host,
                port=settings.port,
                dbname=settings.database,
                user=settings.username,
                password=password,
                connect_timeout=settings.connect_timeout_seconds,
                sslmode="disable",
                application_name="federated-iot-algorithm-worker",
                autocommit=True,
            )
        except Exception as error:
            raise RepositoryOperationError("Could not connect to the PostgreSQL Worker Repository.") from error
        return cls(connection, identity, lease_seconds=lease_seconds, heartbeat_interval_seconds=heartbeat_interval_seconds)

    def close(self) -> None:
        """Close the single Worker connection after it stops polling."""

        with self._connection_lock:
            self._connection.close()

    def register_instance(self) -> None:
        """Persist compatible Worker observation before the first claim."""

        row = self._call_one(
            "SELECT worker_register_instance(%s, %s, %s)",
            (self._identity.worker_id, WORKER_TASK_VERSION, self._identity.worker_version),
        )
        if row is None:
            raise RepositoryOperationError("Worker registration returned no observation timestamp.")

    def heartbeat_instance(self) -> None:
        """Refresh idle Worker observation without inspecting tables."""

        accepted = self._boolean_call(
            "SELECT worker_heartbeat_instance(%s, %s)",
            (self._identity.worker_id, WORKER_TASK_VERSION),
        )
        if not accepted:
            raise LeaseLostFailure()

    def claim_next_job(self, attempt_id: str, lease_token: str) -> ClaimedJob | None:
        """Claim exactly one FIFO task through the controlled function boundary."""

        _require_identifier(attempt_id, "Attempt identifier")
        if not isinstance(lease_token, str) or len(lease_token) < 16:
            raise RepositoryConfigurationError("Lease token is invalid.")
        row = self._call_one(
            "SELECT job_id, job_type, run_id, envelope_json, lease_expires_at "
            "FROM worker_claim_next_job_for_worker(%s, %s, %s, %s)",
            (self._identity.worker_id, attempt_id, lease_token, self._lease_seconds),
        )
        if row is None:
            return None
        job_id, job_type, run_id, envelope, lease_expires_at = _row_values(
            row, ("job_id", "job_type", "run_id", "envelope_json", "lease_expires_at")
        )
        decoded = _decode_envelope(envelope)
        return ClaimedJob(
            job_id=_require_identifier(job_id, "Claimed job identifier"),
            job_type=_require_job_type(job_type),
            run_id=run_id if isinstance(run_id, str) else None,
            attempt_id=attempt_id,
            lease_token=lease_token,
            envelope=decoded,
            lease_expires_at=lease_expires_at if isinstance(lease_expires_at, (datetime, str)) else None,
        )

    def cancellation_context(self, task: WorkerTask) -> CancellationContext:
        """Return a function-backed bounded context for one claimed attempt."""

        return PostgresCancellationContext(self, task, self._heartbeat_interval_seconds)

    def cancellation_state(self, task: WorkerTask) -> CancellationState:
        """Read cancellation intent only through the sanctioned function."""

        row = self._call_one(
            "SELECT cancel_requested, cancel_requested_at, lease_valid "
            "FROM worker_cancellation_context(%s, %s, %s)",
            (task.job_id, task.attempt_id, task.lease_token),
        )
        if row is None:
            return CancellationState(cancel_requested=False, lease_valid=False)
        cancel_requested, _requested_at, lease_valid = _row_values(row, ("cancel_requested", "cancel_requested_at", "lease_valid"))
        return CancellationState(cancel_requested=bool(cancel_requested), lease_valid=bool(lease_valid))

    def heartbeat(self, task: WorkerTask) -> None:
        """Renew both the durable Worker observation and active lease."""

        accepted = self._boolean_call(
            "SELECT worker_heartbeat_for_worker(%s, %s, %s, %s, %s)",
            (self._identity.worker_id, task.job_id, task.attempt_id, task.lease_token, self._lease_seconds),
        )
        if not accepted:
            raise LeaseLostFailure()

    def report_event(self, task: WorkerTask, event: Mapping[str, Any]) -> None:
        """Persist an exact ``worker.event.v1`` envelope through one function."""

        accepted = self._boolean_call(
            "SELECT worker_report_event(%s, %s, %s, %s::jsonb)",
            (task.job_id, task.attempt_id, task.lease_token, _json_parameter(event)),
        )
        if not accepted:
            raise LeaseLostFailure()

    def commit_preflight(self, task: WorkerTask, summary: Mapping[str, Any]) -> None:
        """Commit only a matching preflight summary and its frozen SHA-256."""

        summary_sha256 = summary.get("summary_sha256")
        if not isinstance(summary_sha256, str) or not re.fullmatch(r"[0-9a-f]{64}", summary_sha256):
            raise RepositoryOperationError("Preflight summary SHA-256 is invalid.")
        accepted = self._boolean_call(
            "SELECT worker_complete_preflight(%s, %s, %s, %s, %s::jsonb, %s)",
            (task.job_id, task.attempt_id, task.lease_token, task.dataset_sha256, _json_parameter(summary), summary_sha256),
        )
        if not accepted:
            raise LeaseLostFailure()

    def confirm_cancelled(self, task: WorkerTask) -> None:
        """Commit cancellation only while the original attempt remains current."""

        accepted = self._boolean_call(
            "SELECT worker_confirm_cancel(%s, %s, %s)",
            (task.job_id, task.attempt_id, task.lease_token),
        )
        if not accepted:
            raise LeaseLostFailure()

    def commit_failure(self, task: WorkerTask, failure: Mapping[str, Any]) -> None:
        """Commit a stable failure, or persisted cancellation, without table access."""

        if failure.get("code") == "CANCELLED":
            self.confirm_cancelled(task)
            return
        accepted = self._boolean_call(
            "SELECT worker_fail_job(%s, %s, %s, %s::jsonb, %s)",
            (
                task.job_id,
                task.attempt_id,
                task.lease_token,
                _json_parameter(failure),
                bool(failure.get("recoverable", False)),
            ),
        )
        if not accepted:
            raise LeaseLostFailure()

    def commit_simulation(
        self,
        task: WorkerTask,
        *,
        manifest_sha256: str,
        artifacts: Sequence[Mapping[str, Any]],
        alarms: Sequence[Mapping[str, Any]],
        stage_durations_ms: Mapping[str, int],
    ) -> None:
        """Record a pre-verified local artifact commit through the exact DB function."""

        if not re.fullmatch(r"[0-9a-f]{64}", manifest_sha256):
            raise RepositoryOperationError("Simulation artifact manifest SHA-256 is invalid.")
        accepted = self._boolean_call(
            "SELECT worker_commit_simulation(%s, %s, %s, %s, %s::jsonb, %s::jsonb, %s::jsonb)",
            (
                task.job_id,
                task.attempt_id,
                task.lease_token,
                manifest_sha256,
                _json_parameter_list(artifacts),
                _json_parameter_list(alarms),
                _json_parameter(stage_durations_ms),
            ),
        )
        if not accepted:
            raise LeaseLostFailure()

    def fail_claim(self, claim: ClaimedJob, failure: Mapping[str, Any], *, recoverable: bool = False) -> None:
        """Fail an unparseable claimed envelope using its injected lease identity."""

        accepted = self._boolean_call(
            "SELECT worker_fail_job(%s, %s, %s, %s::jsonb, %s)",
            (claim.job_id, claim.attempt_id, claim.lease_token, _json_parameter(failure), recoverable),
        )
        if not accepted:
            raise LeaseLostFailure()

    def _boolean_call(self, query: str, parameters: Sequence[Any]) -> bool:
        row = self._call_one(query, parameters)
        if row is None:
            return False
        value = _row_values(row, ("accepted",))[0]
        return value is True

    def _call_one(self, query: str, parameters: Sequence[Any]) -> Sequence[Any] | Mapping[str, Any] | None:
        if "worker_" not in query or re.search(r"\b(?:FROM|JOIN|UPDATE|INSERT|DELETE)\s+(?!worker_)", query, re.IGNORECASE):
            raise RepositoryOperationError("Repository adapter attempted a forbidden database operation.")
        operation = _worker_function_name(query)
        try:
            with self._connection_lock:
                cursor = self._connection.execute(query, parameters)
                return cursor.fetchone()
        except LeaseLostFailure:
            raise
        except Exception as error:
            raise RepositoryOperationError(f"PostgreSQL Worker Repository {operation} operation failed.") from error


def _read_secret(path: Path) -> str:
    try:
        value = path.read_text(encoding="utf-8").strip()
    except OSError as error:
        raise RepositoryConfigurationError("Worker database secret file cannot be read.") from error
    if not value or "\n" in value or "\r" in value:
        raise RepositoryConfigurationError("Worker database secret file is invalid.")
    return value


def _json_parameter(value: Mapping[str, Any]) -> str:
    try:
        return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True, allow_nan=False)
    except (TypeError, ValueError) as error:
        raise RepositoryOperationError("Worker Repository JSON payload is invalid.") from error


def _json_parameter_list(value: Sequence[Mapping[str, Any]]) -> str:
    try:
        return json.dumps(list(value), ensure_ascii=False, separators=(",", ":"), sort_keys=True, allow_nan=False)
    except (TypeError, ValueError) as error:
        raise RepositoryOperationError("Worker Repository JSON payload is invalid.") from error


def _decode_envelope(value: Any) -> Mapping[str, Any]:
    if isinstance(value, Mapping):
        return value
    if isinstance(value, (str, bytes, bytearray)):
        try:
            decoded = json.loads(value)
        except (TypeError, ValueError) as error:
            raise RepositoryOperationError("Claimed Worker envelope is not valid JSON.") from error
        if isinstance(decoded, Mapping):
            return decoded
    raise RepositoryOperationError("Claimed Worker envelope is not an object.")


def _row_values(row: Sequence[Any] | Mapping[str, Any], names: Sequence[str]) -> tuple[Any, ...]:
    if isinstance(row, Mapping):
        try:
            return tuple(row[name] for name in names)
        except KeyError as error:
            raise RepositoryOperationError("Worker Repository result has missing fields.") from error
    if len(row) != len(names):
        raise RepositoryOperationError("Worker Repository result has an unexpected shape.")
    return tuple(row)


def _worker_function_name(query: str) -> str:
    """Extract only a fixed Worker function name for diagnostics, never parameters."""

    match = _WORKER_FUNCTION_RE.search(query)
    return match.group(1) if match is not None else "operation"


def _require_identifier(value: Any, label: str) -> str:
    if not isinstance(value, str) or not _IDENTIFIER_RE.fullmatch(value):
        raise RepositoryOperationError(f"{label} is invalid.")
    return value


def _require_job_type(value: Any) -> str:
    if value not in {"DATASET_PREFLIGHT", "SIMULATION"}:
        raise RepositoryOperationError("Claimed Worker job type is invalid.")
    return str(value)
