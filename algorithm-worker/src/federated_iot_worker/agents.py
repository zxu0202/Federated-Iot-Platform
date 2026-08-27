"""Generic Agent runtime ownership and aggregation boundary for S1."""

from __future__ import annotations

import random
from dataclasses import dataclass, field
from typing import Any, Callable, Mapping, Protocol, Sequence

from .cancellation import CancellationContext, NeverCancelled, check_cancel
from .features import ChronologicalSplit, FeatureDataset, Standardization


@dataclass(frozen=True)
class AgentContribution:
    """Immutable contribution that may cross the aggregation boundary."""

    contract_version: str
    agent: int
    representative_centers: tuple[tuple[float, ...], ...]
    anchor_means: tuple[float, ...]
    anchor_variances: tuple[float, ...]
    anchor_support: tuple[float, ...]


class AggregationPort(Protocol):
    """Versioned, read-only aggregation input surface."""

    def aggregate(self, contributions: Sequence[AgentContribution]) -> Mapping[str, Any]:
        """Aggregate immutable contributions without accessing AgentContext state."""


@dataclass
class AgentRuntimeState:
    """All mutable state that must remain private to one logical Agent."""

    local_model: Any | None = None
    global_model: Any | None = None
    local_residual_window: list[float] = field(default_factory=list)
    global_residual_window: list[float] = field(default_factory=list)
    fused_residual_window: list[float] = field(default_factory=list)
    local_error_window: list[float] = field(default_factory=list)
    global_error_window: list[float] = field(default_factory=list)
    fusion_alpha: float = 0.0
    global_better_count: int = 0
    local_better_count: int = 0


@dataclass
class AgentContext:
    """A complete per-Agent ownership boundary used by the reusable executor."""

    agent: int
    segment: str
    parameters: Mapping[str, Any]
    feature_dataset: FeatureDataset
    split: ChronologicalSplit
    standardization: Standardization
    random_seed: int
    output_namespace: str
    runtime: AgentRuntimeState = field(default_factory=AgentRuntimeState)
    random_stream: random.Random = field(init=False, repr=False)

    def __post_init__(self) -> None:
        self.random_stream = random.Random(self.random_seed)


class AgentExecutor:
    """Execute an arbitrary ordered context collection without identity branches."""

    def execute(
        self,
        contexts: Sequence[AgentContext],
        action: Callable[[AgentContext], Any],
        *,
        stage: str,
        cancellation: CancellationContext | None = None,
    ) -> tuple[Any, ...]:
        cancellation = cancellation or NeverCancelled()
        results: list[Any] = []
        for context in contexts:
            check_cancel(cancellation, stage, context.agent)
            results.append(action(context))
        return tuple(results)


def validate_s1_contexts(contexts: Sequence[AgentContext]) -> None:
    """Enforce S1 cardinality at the task boundary, not in the execution loop."""

    expected = {1: "EARLY", 2: "MIDDLE", 3: "LATE"}
    actual = {context.agent: context.segment for context in contexts}
    if len(contexts) != 3 or actual != expected:
        raise ValueError("S1 requires exactly Agent 1/EARLY, Agent 2/MIDDLE, and Agent 3/LATE.")
    if len({id(context.runtime) for context in contexts}) != len(contexts):
        raise ValueError("Agent runtime state must not be shared.")
    if len({id(context.random_stream) for context in contexts}) != len(contexts):
        raise ValueError("Agent random streams must not be shared.")
    if len({context.output_namespace for context in contexts}) != len(contexts):
        raise ValueError("Agent output namespaces must not be shared.")
