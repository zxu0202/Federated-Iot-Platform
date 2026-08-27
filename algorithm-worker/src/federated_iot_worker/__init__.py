"""Stable M1 public surface for the generic three-context Algorithm Worker.

Only reusable preprocessing, feature, and generic Agent runtime types are
exported here. Database adapters and task-loop implementation details remain
internal so they cannot become alternate algorithm entry points.
"""

from typing import TYPE_CHECKING, Any

from .agents import AgentContext, AgentExecutor, AggregationPort
from .features import FeatureDataset, build_transition_dataset, feature_names
from .preprocessing import AlgorithmCore, PreflightSummary, PreprocessingConfig
from .version import ALGORITHM_VERSION, WORKER_VERSION

if TYPE_CHECKING:
    from .runner import WorkerRunner

__all__ = [
    "AgentContext",
    "AgentExecutor",
    "AggregationPort",
    "AlgorithmCore",
    "ALGORITHM_VERSION",
    "FeatureDataset",
    "PreflightSummary",
    "PreprocessingConfig",
    "WorkerRunner",
    "WORKER_VERSION",
    "build_transition_dataset",
    "feature_names",
]


def __getattr__(name: str) -> Any:
    """Load runner types lazily so module entry points are not pre-imported."""

    if name == "WorkerRunner":
        from .runner import WorkerRunner

        return WorkerRunner
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")
