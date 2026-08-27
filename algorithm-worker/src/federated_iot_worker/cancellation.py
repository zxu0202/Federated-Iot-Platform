"""Bounded cancellation checkpoints used by long worker loops."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Protocol

from .errors import CancelledFailure


class CancellationContext(Protocol):
    """Read-only cancellation access supplied by the Worker Repository."""

    def is_cancel_requested(self) -> bool:
        """Return the persisted cancellation intent for the active attempt."""


@dataclass(frozen=True)
class NeverCancelled:
    """Default context for direct core calls and deterministic tests."""

    def is_cancel_requested(self) -> bool:
        return False


def check_cancel(context: CancellationContext, stage: str, agent: int | None = None) -> None:
    """Raise a stable cancellation failure before a new bounded work unit."""

    if context.is_cancel_requested():
        raise CancelledFailure(stage=stage, agent=agent)
