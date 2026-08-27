"""Stable Algorithm Worker error types."""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class WorkerFailure(Exception):
    """A failure whose code is safe to persist in the worker contract."""

    code: str
    message: str
    stage: str | None = None
    agent: int | None = None
    recoverable: bool = False

    def __str__(self) -> str:
        return self.message


class CancelledFailure(WorkerFailure):
    """Raised at a bounded cancellation checkpoint."""

    def __init__(self, stage: str | None = None, agent: int | None = None) -> None:
        super().__init__("CANCELLED", "Worker cancellation was observed.", stage, agent, True)


class ContractFailure(WorkerFailure):
    """Raised when a worker task cannot be safely interpreted."""

    def __init__(self, message: str) -> None:
        super().__init__("WORKER_CONTRACT_MISMATCH", message, recoverable=False)
