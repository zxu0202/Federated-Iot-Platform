"""Single-concurrency Worker control loop over the PostgreSQL function boundary."""

from __future__ import annotations

import secrets
from dataclasses import dataclass
from pathlib import Path
from time import monotonic
from typing import Callable

from .contracts import parse_worker_task
from .errors import ContractFailure, WorkerFailure
from .postgres_repository import ClaimedJob, LeaseLostFailure, PostgresWorkerRepository, RepositoryOperationError
from .runner import WorkerRunner


@dataclass(frozen=True)
class WorkerLoopSettings:
    """Bounded timing controls for the one-instance M1 Worker loop."""

    idle_poll_interval_seconds: float = 2.0
    idle_heartbeat_interval_seconds: float = 10.0

    def __post_init__(self) -> None:
        if self.idle_poll_interval_seconds <= 0 or self.idle_heartbeat_interval_seconds <= 0:
            raise ValueError("Worker loop intervals must be positive.")


class WorkerService:
    """Claim, execute, and terminally report one complete task at a time."""

    def __init__(
        self,
        repository: PostgresWorkerRepository,
        storage_root: Path,
        *,
        settings: WorkerLoopSettings | None = None,
        clock: Callable[[], float] = monotonic,
        liveness_callback: Callable[[], None] | None = None,
        liveness_interval_seconds: float = 5.0,
    ) -> None:
        if liveness_interval_seconds <= 0:
            raise ValueError("Worker liveness interval must be positive.")
        self._repository = repository
        self._storage_root = storage_root
        self._settings = settings or WorkerLoopSettings()
        self._clock = clock
        self._liveness_callback = liveness_callback
        self._liveness_interval_seconds = liveness_interval_seconds
        self._registered = False
        self._next_idle_heartbeat = 0.0

    @property
    def idle_poll_interval_seconds(self) -> float:
        """Expose the bounded wait requested by the M0 Worker lifecycle."""

        return self._settings.idle_poll_interval_seconds

    def register(self) -> None:
        """Register the single Worker before it claims any task."""

        self._repository.register_instance()
        self._registered = True
        self._next_idle_heartbeat = self._clock() + self._settings.idle_heartbeat_interval_seconds

    def run_one_poll(self) -> bool:
        """Run at most one full claimed task and return whether one was claimed."""

        if not self._registered:
            self.register()
        if self._clock() >= self._next_idle_heartbeat:
            self._repository.heartbeat_instance()
            self._next_idle_heartbeat = self._clock() + self._settings.idle_heartbeat_interval_seconds

        claim = self._repository.claim_next_job(_attempt_id(), _lease_token())
        if claim is None:
            return False
        self._run_claim(claim)
        return True

    def _run_claim(self, claim: ClaimedJob) -> None:
        try:
            task = parse_worker_task(claim.envelope)
        except ContractFailure as failure:
            self._repository.fail_claim(
                claim,
                {
                    "code": failure.code,
                    "message": failure.message,
                    "stage": None,
                    "agent": None,
                    "diagnostic_id": f"{claim.job_id}:worker-contract",
                    "recoverable": False,
                },
            )
            return
        if (
            task.job_id != claim.job_id
            or task.job_type != claim.job_type
            or task.run_id != claim.run_id
            or task.attempt_id != claim.attempt_id
            or task.lease_token != claim.lease_token
        ):
            self._repository.fail_claim(
                claim,
                {
                    "code": "WORKER_CONTRACT_MISMATCH",
                    "message": "Claimed Worker envelope identity does not match the lease result.",
                    "stage": None,
                    "agent": None,
                    "diagnostic_id": f"{claim.job_id}:claim-identity",
                    "recoverable": False,
                },
            )
            return
        try:
            WorkerRunner(
                self._storage_root,
                self._repository,
                liveness_callback=self._liveness_callback,
                liveness_interval_seconds=self._liveness_interval_seconds,
            ).run(claim.envelope)
        except LeaseLostFailure:
            # A stale attempt must stop; no additional job can be claimed on
            # this process after lease authority has been lost.
            raise
        except WorkerFailure:
            # WorkerRunner reported its own stable failure through the lease.
            return
        except RepositoryOperationError:
            # The caller owns process restart policy. Continuing would allow a
            # second claim while an unknown first attempt might still be live.
            raise


def _attempt_id() -> str:
    return f"attempt-{secrets.token_hex(16)}"


def _lease_token() -> str:
    return secrets.token_urlsafe(32)
