"""Deterministic single-process implementation of the M1 federated reference path.

The module deliberately owns no process-global model, mutable parameter, or
Agent-specific service.  A caller supplies the three generic ``AgentContext``
objects that were built from the frozen task envelope.  All mutable online
state remains in the context's ``runtime`` member or in a call-local result.
"""

from __future__ import annotations

import hashlib
import math
import statistics
from dataclasses import dataclass
from typing import Any, Callable, Mapping, Sequence

from .agents import AgentContext, AgentContribution, AgentExecutor, AggregationPort
from .cancellation import CancellationContext, NeverCancelled, check_cancel
from .errors import WorkerFailure
from .features import Standardization

try:
    import numpy as _numpy
except ModuleNotFoundError:  # The host oracle intentionally runs without the Linux wheel.
    _numpy = None


_EPSILON = 2.220446049250313e-16
_DIAGNOSTIC_TOP_N = 50


class _ReferenceTwister:
    """The seeded MT19937 uniform stream used by ``rng(seed, 'twister')``.

    The frozen stream consumes a 53-bit double from two MT output words for a
    uniform sample. Integer selection uses ``floor(uniform*n)`` rather than
    Python's unrelated ``randrange`` mapping.
    """

    def __init__(self, seed: int) -> None:
        self._state = [seed & 0xFFFFFFFF]
        for index in range(1, 624):
            previous = self._state[index - 1]
            self._state.append((1812433253 * (previous ^ (previous >> 30)) + index) & 0xFFFFFFFF)
        self._index = 624

    def _word(self) -> int:
        if self._index >= 624:
            for index in range(624):
                value = (self._state[index] & 0x80000000) | (self._state[(index + 1) % 624] & 0x7FFFFFFF)
                self._state[index] = self._state[(index + 397) % 624] ^ (value >> 1) ^ (0x9908B0DF if value & 1 else 0)
            self._index = 0
        value = self._state[self._index]
        self._index += 1
        value ^= value >> 11
        value ^= (value << 7) & 0x9D2C5680
        value ^= (value << 15) & 0xEFC60000
        return (value ^ (value >> 18)) & 0xFFFFFFFF

    def uniform(self) -> float:
        return ((self._word() >> 5) * 67108864.0 + (self._word() >> 6)) / 9007199254740992.0

    def randi(self, upper: int) -> int:
        if upper < 1:
            raise ValueError("Random-index upper bound must be positive.")
        return min(int(math.floor(self.uniform() * upper)), upper - 1)


@dataclass(frozen=True)
class LocalGPModel:
    """A local kNN Gaussian-process training view in standardized space."""

    x_train: tuple[tuple[float, ...], ...]
    y_train: tuple[float, ...]
    k_neighbors: int
    ell: float
    sigma_f: float
    sigma_n: float
    regularization: float


@dataclass(frozen=True)
class SurrogateGPModel:
    """A heteroscedastic global GP trained on public-anchor pseudo targets."""

    mean_x: tuple[float, ...]
    std_x: tuple[float, ...]
    x_train: tuple[tuple[float, ...], ...]
    y_center: float
    sigma_f: float
    ell: float
    cholesky: tuple[tuple[float, ...], ...]
    alpha: tuple[float, ...]
    observation_noise: float
    support_scale: float
    jitter: float


@dataclass(frozen=True)
class AgentSimulationResult:
    """The immutable point results and metric row of one generic Agent."""

    agent: int
    rows: tuple[Mapping[str, Any], ...]
    metrics: Mapping[str, Any]
    alarms: tuple[Mapping[str, Any], ...]
    diagnostics: Mapping[str, Any]


@dataclass(frozen=True)
class SimulationOutcome:
    """All algorithm-owned products used by the artifact writer and repository."""

    agent_results: tuple[AgentSimulationResult, ...]
    partition_rows: tuple[Mapping[str, Any], ...]
    feature_schema: Mapping[str, Any]
    anchor_summary: Mapping[str, Any]
    diagnostics: Mapping[str, Any]


class FederatedAggregator(AggregationPort):
    """Explicit gPoE aggregation port; it receives immutable contributions only."""

    def __init__(self, variance_floor: float) -> None:
        self._variance_floor = variance_floor

    def aggregate(self, contributions: Sequence[AgentContribution]) -> Mapping[str, Any]:
        if len(contributions) != 3:
            raise ValueError("S1 aggregation requires exactly three contributions.")
        anchors = len(contributions[0].anchor_means)
        if any(len(item.anchor_means) != anchors for item in contributions):
            raise ValueError("Agent contributions must use the same public anchors.")
        means: list[float] = []
        variances: list[float] = []
        beta_rows: list[tuple[float, ...]] = []
        loo_means: list[tuple[float, float, float]] = []
        loo_variances: list[tuple[float, float, float]] = []
        for anchor in range(anchors):
            support = [max(0.0, item.anchor_support[anchor]) for item in contributions]
            if sum(support) <= 0.0:
                support[_stable_nearest_contribution(contributions, anchor)] = 1.0
            beta = _normalize(support)
            item_means = [item.anchor_means[anchor] for item in contributions]
            item_vars = [item.anchor_variances[anchor] for item in contributions]
            mean, variance = gpoe_aggregate(item_means, item_vars, beta, self._variance_floor)
            means.append(mean)
            variances.append(variance)
            beta_rows.append(tuple(beta))
            current_means: list[float] = []
            current_vars: list[float] = []
            for leave_out in range(3):
                other = [index for index in range(3) if index != leave_out]
                other_support = [support[index] for index in other]
                if sum(other_support) <= 0.0:
                    nearest = min(other, key=lambda index: (1.0 - contributions[index].anchor_support[anchor], index))
                    other_support[other.index(nearest)] = 1.0
                loo_mean, loo_variance = gpoe_aggregate(
                    [item_means[index] for index in other],
                    [item_vars[index] for index in other],
                    _normalize(other_support),
                    self._variance_floor,
                )
                current_means.append(loo_mean)
                current_vars.append(loo_variance)
            loo_means.append(tuple(current_means))
            loo_variances.append(tuple(current_vars))
        return {
            "anchor_means": tuple(means),
            "anchor_variances": tuple(variances),
            "anchor_betas": tuple(beta_rows),
            "loo_means": tuple(loo_means),
            "loo_variances": tuple(loo_variances),
        }


def execute_simulation(
    contexts: Sequence[AgentContext],
    *,
    cancellation: CancellationContext | None = None,
    progress: Callable[[str], None] | None = None,
    agent_executor: AgentExecutor | None = None,
) -> SimulationOutcome:
    """Execute local models, public aggregation, and online fusion once.

    The result follows the frozen execution order: local calibration before
    anchor exchange, leave-one-agent-out surrogate calibration, then each test
    point is predicted before its label can update any window.
    """

    cancellation = cancellation or NeverCancelled()
    ordered = tuple(sorted(contexts, key=lambda context: context.agent))
    if tuple(context.agent for context in ordered) != (1, 2, 3):
        raise WorkerFailure("INPUT_INVALID", "SIMULATION requires Agent 1, Agent 2, and Agent 3.", "LOCAL_TRAINING")
    _validate_feature_widths(ordered)
    if progress is not None:
        progress("LOCAL_TRAINING")
    if agent_executor is None:
        _fit_local_contexts(ordered, cancellation)
    else:
        agent_executor.execute(
            ordered,
            lambda context: _fit_local_contexts((context,), cancellation),
            stage="LOCAL_TRAINING",
            cancellation=cancellation,
        )
    if progress is not None:
        progress("ANCHOR_AGGREGATING")
    contributions, public_anchors, anchor_metadata = _build_contributions(ordered, cancellation)
    aggregation = FederatedAggregator(_shared_floor(ordered)).aggregate(contributions)
    if progress is not None:
        progress("GLOBAL_DISTILLING")
    _fit_global_contexts(ordered, public_anchors, aggregation, cancellation)
    if progress is not None:
        progress("CALIBRATING")
    if progress is not None:
        progress("TESTING")
    results = tuple(_online_fuse(context, cancellation) for context in ordered)
    partition_rows = tuple(_partition_row(context) for context in ordered)
    diagnostics = {
        "algorithm": "anchor-federated-gpr-adaptive-fusion-v3",
        "agent_count": len(ordered),
        "local_fallbacks": {str(context.agent): int(getattr(context.runtime, "local_fallbacks", 0)) for context in ordered},
        "global_jitter": {str(context.agent): float(context.runtime.global_model.jitter) for context in ordered},
        "public_anchor_count": len(public_anchors),
        "predict_then_update": True,
    }
    return SimulationOutcome(
        agent_results=results,
        partition_rows=partition_rows,
        feature_schema={"schema_version": "feature-schema.v1", "names": _feature_names(ordered[0]), "width": len(ordered[0].feature_dataset.values[0])},
        anchor_summary={**anchor_metadata, **aggregation},
        diagnostics=diagnostics,
    )


def gpoe_aggregate(
    means: Sequence[float], variances: Sequence[float], beta: Sequence[float], variance_floor: float
) -> tuple[float, float]:
    """Apply the reference generalized product-of-experts formula."""

    weights = _normalize(beta)
    guarded = [max(float(value), variance_floor) for value in variances]
    precision = sum(weight / variance for weight, variance in zip(weights, guarded))
    variance = max(1.0 / max(precision, _EPSILON), variance_floor)
    return variance * sum(weight * mean / value for weight, mean, value in zip(weights, means, guarded)), variance


def local_gp_predict(
    model: LocalGPModel,
    queries: Sequence[Sequence[float]],
    *,
    cancellation: CancellationContext | None = None,
    agent: int | None = None,
) -> tuple[tuple[float, ...], tuple[float, ...], int]:
    """Predict a deterministic local kNN GP with the documented fallback."""

    if _numpy is not None:
        return _local_gp_predict_numpy(model, queries, cancellation=cancellation, agent=agent)
    return _local_gp_predict_python(model, queries, cancellation=cancellation, agent=agent)


def _local_gp_predict_python(
    model: LocalGPModel,
    queries: Sequence[Sequence[float]],
    *,
    cancellation: CancellationContext | None = None,
    agent: int | None = None,
) -> tuple[tuple[float, ...], tuple[float, ...], int]:
    """Reference-equivalent fallback for host-only oracle environments."""

    cancellation = cancellation or NeverCancelled()
    means: list[float] = []
    stds: list[float] = []
    fallbacks = 0
    for query_index, query in enumerate(queries):
        if query_index % 8 == 0:
            check_cancel(cancellation, "LOCAL_TRAINING", agent)
        distances = [(_squared_distance(query, point), index) for index, point in enumerate(model.x_train)]
        distances.sort(key=lambda item: (item[0], item[1]))
        chosen = distances[: model.k_neighbors]
        x_local = [model.x_train[index] for _, index in chosen]
        y_local = [model.y_train[index] for _, index in chosen]
        distance_values = [distance for distance, _ in chosen]
        weighted_mean, weighted_variance = _weighted_local_fallback(y_local, distance_values, model.ell, model.regularization)
        predicted = weighted_mean
        variance = weighted_variance
        try:
            kernel = _kernel_matrix(x_local, x_local, model.sigma_f, model.ell)
            for index in range(len(kernel)):
                kernel[index][index] += model.regularization
            cholesky = _cholesky(kernel)
            centered = [value - _mean(y_local) for value in y_local]
            alpha = _solve_cholesky(cholesky, centered)
            vector = [model.sigma_f ** 2 * math.exp(-0.5 * distance / (model.ell ** 2)) for distance in distance_values]
            beta = _solve_cholesky(cholesky, vector)
            candidate = _mean(y_local) + _dot(vector, alpha)
            candidate_variance = max(model.sigma_f ** 2 - _dot(vector, beta) + model.regularization, _EPSILON)
            low, high = _local_bounds(y_local)
            diagonal_condition = (min(cholesky[index][index] for index in range(len(cholesky))) / max(cholesky[index][index] for index in range(len(cholesky)))) ** 2
            if math.isfinite(candidate) and low <= candidate <= high and diagonal_condition >= 1e-10:
                predicted, variance = candidate, candidate_variance
            else:
                fallbacks += 1
        except (ArithmeticError, ValueError):
            fallbacks += 1
        means.append(predicted)
        stds.append(math.sqrt(max(variance, _EPSILON)))
    return tuple(means), tuple(stds), fallbacks


def _local_gp_predict_numpy(
    model: LocalGPModel,
    queries: Sequence[Sequence[float]],
    *,
    cancellation: CancellationContext | None = None,
    agent: int | None = None,
) -> tuple[tuple[float, ...], tuple[float, ...], int]:
    """Vectorize kNN distances and dense local solves without changing formulas."""

    assert _numpy is not None
    cancellation = cancellation or NeverCancelled()
    training = _numpy.asarray(model.x_train, dtype=_numpy.float64)
    targets = _numpy.asarray(model.y_train, dtype=_numpy.float64)
    means: list[float] = []
    stds: list[float] = []
    fallbacks = 0
    for query_index, query in enumerate(queries):
        if query_index % 8 == 0:
            check_cancel(cancellation, "LOCAL_TRAINING", agent)
        query_vector = _numpy.asarray(query, dtype=_numpy.float64)
        distances = _numpy.sum((training - query_vector) ** 2, axis=1)
        # NumPy's stable ordering retains the original row order for ties,
        # matching the stable nearest-neighbour ordering used by the oracle.
        chosen = _numpy.argsort(distances, kind="stable")[: model.k_neighbors]
        x_local = training[chosen]
        y_local = targets[chosen]
        distance_values = distances[chosen]
        weighted_mean, weighted_variance = _weighted_local_fallback(
            tuple(float(value) for value in y_local),
            tuple(float(value) for value in distance_values),
            model.ell,
            model.regularization,
        )
        predicted = weighted_mean
        variance = weighted_variance
        try:
            deltas = x_local[:, _numpy.newaxis, :] - x_local[_numpy.newaxis, :, :]
            kernel = model.sigma_f ** 2 * _numpy.exp(-0.5 * _numpy.sum(deltas * deltas, axis=2) / (model.ell ** 2))
            kernel[_numpy.diag_indices_from(kernel)] += model.regularization
            cholesky = _numpy.linalg.cholesky(kernel)
            centered = y_local - _numpy.mean(y_local)
            alpha = _numpy.linalg.solve(cholesky.T, _numpy.linalg.solve(cholesky, centered))
            vector = model.sigma_f ** 2 * _numpy.exp(-0.5 * distance_values / (model.ell ** 2))
            beta = _numpy.linalg.solve(cholesky.T, _numpy.linalg.solve(cholesky, vector))
            candidate = float(_numpy.mean(y_local) + _numpy.dot(vector, alpha))
            candidate_variance = max(float(model.sigma_f ** 2 - _numpy.dot(vector, beta) + model.regularization), _EPSILON)
            low, high = _local_bounds(tuple(float(value) for value in y_local))
            diagonal = _numpy.diag(cholesky)
            diagonal_condition = float((_numpy.min(diagonal) / _numpy.max(diagonal)) ** 2)
            if math.isfinite(candidate) and low <= candidate <= high and diagonal_condition >= 1e-10:
                predicted, variance = candidate, candidate_variance
            else:
                fallbacks += 1
        except (ArithmeticError, ValueError, _numpy.linalg.LinAlgError):
            fallbacks += 1
        means.append(predicted)
        stds.append(math.sqrt(max(variance, _EPSILON)))
    return tuple(means), tuple(stds), fallbacks


def _fit_local_contexts(contexts: Sequence[AgentContext], cancellation: CancellationContext) -> None:
    for context in contexts:
        check_cancel(cancellation, "LOCAL_TRAINING", context.agent)
        parameters = context.parameters
        split = context.split
        dataset = context.feature_dataset
        x_train = context.standardization.transform([dataset.values[index] for index in split.train])
        y_train = tuple(dataset.targets[index] for index in split.train)
        gp = parameters["local_gp"]
        requested = int(gp["kNN"])
        adaptive = max(3, math.floor(float(gp["adaptive_ratio"]) * len(x_train)))
        k_neighbors = min(requested, len(x_train))
        if len(x_train) < 5 * k_neighbors:
            k_neighbors = min(len(x_train), adaptive)
        model = LocalGPModel(
            x_train=tuple(x_train), y_train=y_train, k_neighbors=k_neighbors,
            ell=float(gp["ell"]), sigma_f=float(gp["sigma_f"]), sigma_n=float(gp["sigma_n"]),
            regularization=max(float(gp["sigma_n"]) ** 2, float(gp["minimum_regularization"])),
        )
        context.runtime.local_model = model
        setattr(context.runtime, "local_fallbacks", 0)
        calibration_x = context.standardization.transform([dataset.values[index] for index in split.calibration])
        test_x = context.standardization.transform([dataset.values[index] for index in split.test])
        calibration_mean, calibration_std, fallback_a = local_gp_predict(model, calibration_x, cancellation=cancellation, agent=context.agent)
        test_mean, test_std, fallback_b = local_gp_predict(model, test_x, cancellation=cancellation, agent=context.agent)
        setattr(context.runtime, "local_fallbacks", fallback_a + fallback_b)
        trend = parameters["trend"]
        calibration_mean = _apply_transition_blend(
            calibration_mean, [dataset.trends[index] for index in split.calibration],
            [dataset.transition_scores[index] for index in split.calibration], trend,
        )
        test_mean = _apply_transition_blend(
            test_mean, [dataset.trends[index] for index in split.test],
            [dataset.transition_scores[index] for index in split.test], trend,
        )
        calibration_mean = _apply_rate_limit(calibration_mean, float(trend["maximum_step_change"]))
        test_mean = _apply_rate_limit(test_mean, float(trend["maximum_step_change"]))
        interval = parameters["interval"]
        calibration_target = tuple(dataset.targets[index] for index in split.calibration)
        calibration_std_effective = tuple(max(value, float(interval["std_floor"])) for value in calibration_std)
        scores = tuple(abs(target - prediction) / std for target, prediction, std in zip(calibration_target, calibration_mean, calibration_std_effective))
        local_window = _initial_window(scores, parameters, context.agent, "local")
        context.runtime.local_calibration_mean = calibration_mean
        context.runtime.local_calibration_std = calibration_std_effective
        context.runtime.local_test_mean = test_mean
        context.runtime.local_test_std = tuple(max(value, float(interval["std_floor"])) for value in test_std)
        context.runtime.local_score_window = local_window
        context.runtime.local_initial_q = _score_quantile(local_window, parameters)


def _build_contributions(
    contexts: Sequence[AgentContext], cancellation: CancellationContext
) -> tuple[tuple[AgentContribution, ...], tuple[tuple[float, ...], ...], Mapping[str, Any]]:
    local_centers: list[tuple[tuple[float, ...], ...]] = []
    for context in contexts:
        check_cancel(cancellation, "ANCHOR_AGGREGATING", context.agent)
        parameters = context.parameters
        train = context.runtime.local_model.x_train
        scores = [context.feature_dataset.transition_scores[index] for index in context.split.train]
        anchors = parameters["anchors"]
        seed = int(anchors["random_seed"]) + context.agent
        base = _simple_kmeans(train, int(anchors["base_centers"]), int(anchors["iterations"]), seed)
        threshold = _empirical_quantile(scores, float(anchors["transition_quantile"]))
        transition_input = [point for point, score in zip(train, scores) if math.isfinite(score) and score >= threshold]
        transition = _simple_kmeans(transition_input, min(int(anchors["transition_centers"]), len(transition_input)), int(anchors["iterations"]), seed + 20) if transition_input else ()
        boundary = _farthest_point_subset(train, int(anchors["boundary_centers"]), seed + 40)
        unique = _stable_unique_rows((*base, *transition, *boundary))
        local_centers.append(tuple(_unstandardize(point, context.standardization) for point in unique))
    merged = tuple(point for item in local_centers for point in item)
    if not merged:
        raise WorkerFailure("NUMERICAL_FAILURE", "No local representative centers were produced.", "ANCHOR_AGGREGATING")
    center_mean, center_std = _standardization(merged)
    standardized = tuple(_standardize_row(point, center_mean, center_std) for point in merged)
    public_count = max(int(context.parameters["anchors"]["public_anchors"]) for context in contexts)
    seed = _public_anchor_seed(contexts)
    public_standardized = _simple_kmeans(standardized, min(public_count, len(standardized)), int(max(context.parameters["anchors"]["iterations"] for context in contexts)), seed)
    public_anchors = tuple(_unstandardize(point, Standardization(center_mean, center_std)) for point in public_standardized)
    contributions: list[AgentContribution] = []
    for context in contexts:
        check_cancel(cancellation, "ANCHOR_AGGREGATING", context.agent)
        model: LocalGPModel = context.runtime.local_model
        standardized_for_agent = tuple(_standardize_row(point, context.standardization.means, context.standardization.sample_stds) for point in public_anchors)
        means, stds, fallbacks = local_gp_predict(model, standardized_for_agent, cancellation=cancellation, agent=context.agent)
        context.runtime.local_fallbacks += fallbacks
        trend_column, score_column = _trend_and_score_columns(context)
        means = _apply_transition_blend(
            means, [point[trend_column] for point in public_anchors], [point[score_column] for point in public_anchors], context.parameters["trend"],
        )
        interval = context.parameters["interval"]
        q = context.runtime.local_initial_q
        calibrated_variances = tuple(
            max((min(max(q * max(std, float(interval["std_floor"])), float(interval["half_width_min"])), float(interval["half_width_max"])) / _normal_multiplier(float(interval["confidence"]))) ** 2, float(interval["variance_floor"]))
            for std in stds
        )
        scale = _support_scale(model.x_train, 300)
        context.runtime.local_support_scale = scale
        distances = _nearest_distances(model.x_train, standardized_for_agent)
        public_budget = int(context.parameters["anchors"]["public_anchors"])
        support = tuple(
            0.0 if index >= public_budget or value < float(context.parameters["support"]["minimum_weight"]) else value
            for index, value in enumerate(math.exp(-0.5 * (distance / max(float(context.parameters["support"]["scale_multiple"]) * scale, 1e-8)) ** 2) for distance in distances)
        )
        contributions.append(AgentContribution("agent-contribution.v1", context.agent, local_centers[context.agent - 1], tuple(means), calibrated_variances, support))
    return tuple(contributions), public_anchors, {
        "schema_version": "anchor-summary.v1", "public_anchor_count": len(public_anchors), "public_anchor_seed": seed,
        "agent_representative_center_counts": {str(context.agent): len(local_centers[context.agent - 1]) for context in contexts},
    }


def _fit_global_contexts(
    contexts: Sequence[AgentContext], public_anchors: Sequence[Sequence[float]], aggregation: Mapping[str, Any], cancellation: CancellationContext
) -> None:
    for context in contexts:
        check_cancel(cancellation, "GLOBAL_DISTILLING", context.agent)
        position = context.agent - 1
        loo_means = tuple(row[position] for row in aggregation["loo_means"])
        loo_variances = tuple(row[position] for row in aggregation["loo_variances"])
        model = _train_surrogate(public_anchors, loo_means, loo_variances, context.parameters["global_surrogate"], float(context.parameters["interval"]["variance_floor"]))
        context.runtime.global_model = model
        dataset = context.feature_dataset
        calibration = tuple(dataset.values[index] for index in context.split.calibration)
        test = tuple(dataset.values[index] for index in context.split.test)
        global_calibration_mean, global_calibration_std = _surrogate_predict(model, calibration, cancellation=cancellation, agent=context.agent)
        global_test_mean, global_test_std = _surrogate_predict(model, test, cancellation=cancellation, agent=context.agent)
        trend = context.parameters["trend"]
        global_calibration_mean = _apply_rate_limit(global_calibration_mean, float(trend["maximum_step_change"]))
        global_test_mean = _apply_rate_limit(global_test_mean, float(trend["maximum_step_change"]))
        interval = context.parameters["interval"]
        calibration_std_effective = tuple(max(value, float(interval["std_floor"])) for value in global_calibration_std)
        global_test_std_effective = tuple(max(value, float(interval["std_floor"])) for value in global_test_std)
        calibration_targets = tuple(dataset.targets[index] for index in context.split.calibration)
        global_scores = tuple(abs(target - prediction) / std for target, prediction, std in zip(calibration_targets, global_calibration_mean, calibration_std_effective))
        local_mean = context.runtime.local_calibration_mean
        alpha = _optimize_alpha(calibration_targets, local_mean, global_calibration_mean, float(context.parameters["fusion"]["maximum_global_weight"]))
        support_calibration = _surrogate_support(model, calibration, context.parameters["support"])
        support_gate = tuple(min(1.0, value / max(float(context.parameters["support"]["full_weight_reference"]), _EPSILON)) for value in support_calibration)
        blended = tuple((1.0 - alpha * gate) * local + alpha * gate * global_value for local, global_value, gate in zip(local_mean, global_calibration_mean, support_gate))
        local_rmse = _rmse(calibration_targets, local_mean)
        if _rmse(calibration_targets, blended) >= local_rmse * (1.0 - float(context.parameters["fusion"]["initial_improvement"])):
            alpha = 0.0
            blended = local_mean
        local_q = context.runtime.local_initial_q
        global_window = _initial_window(global_scores, context.parameters, context.agent, "global")
        global_q = _score_quantile(global_window, context.parameters)
        local_var = _calibrated_variances(context.runtime.local_calibration_std, local_q, context.parameters)
        global_var = _calibrated_variances(calibration_std_effective, global_q, context.parameters)
        fused_std = tuple(math.sqrt(_mixture_variance(local, global_value, local_value, global_value, alpha * gate, float(interval["variance_floor"]))) for local, global_value, local_value, global_value, gate in zip(local_mean, global_calibration_mean, local_var, global_var, support_gate))
        fused_scores = tuple(abs(target - prediction) / std for target, prediction, std in zip(calibration_targets, blended, fused_std))
        context.runtime.global_calibration_mean = global_calibration_mean
        context.runtime.global_calibration_std = calibration_std_effective
        context.runtime.global_test_mean = global_test_mean
        context.runtime.global_test_std = global_test_std_effective
        context.runtime.global_test_support = _surrogate_support(model, test, context.parameters["support"])
        context.runtime.global_score_window = global_window
        context.runtime.fused_score_window = _initial_window(fused_scores, context.parameters, context.agent, "fused")
        context.runtime.local_error_window = _initial_errors(calibration_targets, local_mean, context.parameters, context.agent, "local")
        context.runtime.global_error_window = _initial_errors(calibration_targets, global_calibration_mean, context.parameters, context.agent, "global")
        context.runtime.fusion_alpha = alpha


def _online_fuse(context: AgentContext, cancellation: CancellationContext) -> AgentSimulationResult:
    parameters = context.parameters
    interval = parameters["interval"]
    fusion = parameters["fusion"]
    alarms = parameters["alarms"]
    dataset = context.feature_dataset
    test_indices = context.split.test
    local_window = list(context.runtime.local_score_window)
    global_window = list(context.runtime.global_score_window)
    fused_window = list(context.runtime.fused_score_window)
    local_errors = list(context.runtime.local_error_window)
    global_errors = list(context.runtime.global_error_window)
    alpha_previous = context.runtime.fusion_alpha * min(1.0, context.runtime.global_test_support[0] / max(float(parameters["support"]["full_weight_reference"]), _EPSILON))
    local_better, global_better = _initial_winner_counts(local_errors, global_errors, fusion)
    rows: list[Mapping[str, Any]] = []
    alarm_rows: list[Mapping[str, Any]] = []
    high_count = 0
    low_count = 0
    for point, source_index in enumerate(test_indices):
        if point % 8 == 0:
            check_cancel(cancellation, "TESTING", context.agent)
        local_prediction = context.runtime.local_test_mean[point]
        global_prediction = context.runtime.global_test_mean[point]
        local_std = context.runtime.local_test_std[point]
        global_std = context.runtime.global_test_std[point]
        support = context.runtime.global_test_support[point]
        local_q = _score_quantile(local_window, parameters)
        global_q = _score_quantile(global_window, parameters)
        fused_q = _score_quantile(fused_window, parameters)
        local_band = _half_width(local_q, local_std, interval)
        global_band = _half_width(global_q, global_std, interval)
        local_variance = max((local_band / _normal_multiplier(float(interval["confidence"]))) ** 2, float(interval["variance_floor"]))
        global_variance = max((global_band / _normal_multiplier(float(interval["confidence"]))) ** 2, float(interval["variance_floor"]))
        recent_local = _robust_recent_mean(local_errors, float(fusion["winsor_quantile"]))
        recent_global = _robust_recent_mean(global_errors, float(fusion["winsor_quantile"]))
        global_clear = recent_global <= (1.0 - float(fusion["win_margin"])) * recent_local and support >= float(parameters["support"]["minimum_query_support"])
        local_clear = recent_local <= (1.0 - float(fusion["win_margin"])) * recent_global or support < float(parameters["support"]["minimum_query_support"])
        if global_clear:
            global_better, local_better = global_better + 1, 0
        elif local_clear:
            local_better, global_better = local_better + 1, 0
        else:
            global_better, local_better = max(global_better - 1, 0), max(local_better - 1, 0)
        local_reliability = 1.0 / max(recent_local + float(fusion["variance_weight"]) * local_variance, _EPSILON)
        global_reliability = support / max(recent_global + float(fusion["variance_weight"]) * global_variance, _EPSILON)
        alpha_reliability = global_reliability / max(local_reliability + global_reliability, _EPSILON)
        alpha_raw = min(max(0.2 * context.runtime.fusion_alpha + 0.8 * alpha_reliability, 0.0), float(fusion["maximum_global_weight"]))
        disagreement = abs(global_prediction - local_prediction) / math.sqrt(max(local_variance + global_variance, float(interval["variance_floor"])))
        support_gate = min(1.0, support / max(float(parameters["support"]["full_weight_reference"]), _EPSILON))
        if local_better >= int(fusion["persistence"]):
            alpha_raw = 0.0
        elif global_better >= int(fusion["persistence"]):
            alpha_raw = max(alpha_raw, float(fusion["global_clear_threshold"]))
            support_gate = max(support_gate, 0.75)
        else:
            disagreement_gate = math.exp(-0.5 * (disagreement / max(float(fusion["disagreement_kappa"]), _EPSILON)) ** 2)
            variance_gate = min(1.0, float(fusion["maximum_variance_ratio"]) * local_variance / max(global_variance, _EPSILON))
            alpha_raw = min(alpha_raw, float(fusion["neutral_upper_limit"])) * disagreement_gate * variance_gate
        alpha_raw = min(max(alpha_raw * support_gate, 0.0), float(fusion["maximum_global_weight"]))
        smoothing = float(fusion["rise_smoothing"] if alpha_raw >= alpha_previous else fusion["fall_smoothing"])
        alpha = min(max(smoothing * alpha_previous + (1.0 - smoothing) * alpha_raw, 0.0), float(fusion["maximum_global_weight"]))
        fused_prediction = (1.0 - alpha) * local_prediction + alpha * global_prediction
        fused_std_raw = math.sqrt(_mixture_variance(local_prediction, global_prediction, local_variance, global_variance, alpha, float(interval["variance_floor"])))
        fused_band = _half_width(fused_q, fused_std_raw, interval)
        fused_variance = max((fused_band / _normal_multiplier(float(interval["confidence"]))) ** 2, float(interval["variance_floor"]))
        target = dataset.targets[source_index]
        local_inside = local_prediction - local_band <= target <= local_prediction + local_band
        global_inside = global_prediction - global_band <= target <= global_prediction + global_band
        fused_lower, fused_upper = fused_prediction - fused_band, fused_prediction + fused_band
        fused_inside = fused_lower <= target <= fused_upper
        load_status = "Heavy load" if target > fused_upper else "Light load" if target < fused_lower else "Normal load"
        high_count = high_count + 1 if load_status == "Heavy load" else 0
        low_count = low_count + 1 if load_status == "Light load" else 0
        source_row = dataset.source_rows[source_index]
        level, load_reason, data_level, data_reason, overall_level, overall_reason = _alarm_fields(load_status, high_count, low_count, source_row, alarms)
        row = {
            "OriginalRunningIndex": source_row.original_running_index, "Time": source_row.time_raw, "Agent": context.agent,
            "RunMode": None, "ParameterProfileVersion": None, "LoadMappingVersion": None, "MappedLoadEstimate": fused_prediction, "MappedLoadUnit": "A",
            "I1": source_row.i1_raw, "I2": source_row.i2_raw, "I3": source_row.i3_raw, "I4": source_row.i4_raw,
            "TrueAverageCurrentSmoothed": target, "TrueAverageCurrentRaw": source_row.iavg_raw, "Isum_3motors": source_row.isum_raw,
            "zl": source_row.zl_raw, "sd": source_row.sd_raw, "ImbalanceRate_3motors": source_row.iimb_raw,
            "LocalPrediction": local_prediction, "GlobalPrediction": global_prediction, "FusedPrediction": fused_prediction,
            "LocalGPStd": local_std, "GlobalGPStd": global_std, "GlobalSupport": support,
            "LocalCalibrationQ": local_q, "GlobalCalibrationQ": global_q, "FusedCalibrationQ": fused_q,
            "LocalHalfWidth": local_band, "GlobalHalfWidth": global_band, "FusedHalfWidth": fused_band,
            "LocalCalibratedVariance": local_variance, "GlobalCalibratedVariance": global_variance, "FusedCalibratedVariance": fused_variance,
            "FusionAlphaRaw": alpha_raw, "FusionAlpha": alpha, "RecentLocalRMSE": math.sqrt(max(recent_local, 0.0)), "RecentGlobalRMSE": math.sqrt(max(recent_global, 0.0)),
            "LocalReliability": local_reliability, "GlobalReliability": global_reliability, "PredictionDisagreement": disagreement,
            "GlobalClearlyBetter": global_clear, "LocalClearlyBetter": local_clear, "GlobalBetterCount": global_better, "LocalBetterCount": local_better,
            "FusedLowerBound": fused_lower, "FusedUpperBound": fused_upper, "FusedInsideInterval": fused_inside, "LoadStatus": load_status,
            "IsSpikeSample": source_row.is_spike_sample, "MotorZeroWarning": source_row.motor_zero_warning, "SpikeReason": source_row.spike_reason,
            "HighCount": high_count, "LowCount": low_count, "LoadAlarmLevel": level, "LoadAlarmReason": load_reason,
            "DataQualityAlarmLevel": data_level, "DataQualityAlarmReason": data_reason, "OverallAlarmLevel": overall_level, "OverallAlarmReason": overall_reason,
        }
        rows.append(row)
        if overall_level != "None":
            alarm_rows.append({"agent": context.agent, "original_running_index": source_row.original_running_index, "time": source_row.time_raw, "overall_alarm_level": overall_level, "alarm_type": "OVERALL", "reasons": [item for item in (load_reason, data_reason) if item], "load_status": load_status, "result_index": point})
        # Prediction, uncertainty, and alarm are fixed before observing target.
        local_error, global_error, fused_error = target - local_prediction, target - global_prediction, target - fused_prediction
        if math.isfinite(local_error):
            _update_window(local_errors, local_error ** 2, int(fusion["error_window"]))
        if math.isfinite(global_error):
            _update_window(global_errors, global_error ** 2, int(fusion["error_window"]))
        if interval["update_mode"] == "all_finite":
            if math.isfinite(local_error):
                _update_window(local_window, abs(local_error) / local_std, int(interval["calibration_window"]))
            if math.isfinite(global_error):
                _update_window(global_window, abs(global_error) / global_std, int(interval["calibration_window"]))
            if math.isfinite(fused_error):
                _update_window(fused_window, abs(fused_error) / fused_std_raw, int(interval["calibration_window"]))
        alpha_previous = alpha
    return AgentSimulationResult(context.agent, tuple(rows), _metrics(context, rows), tuple(alarm_rows), _agent_diagnostics(context, rows))


def _train_surrogate(x: Sequence[Sequence[float]], y: Sequence[float], noise: Sequence[float], parameters: Mapping[str, Any], variance_floor: float) -> SurrogateGPModel:
    if _numpy is not None:
        return _train_surrogate_numpy(x, y, noise, parameters, variance_floor)
    return _train_surrogate_python(x, y, noise, parameters, variance_floor)


def _train_surrogate_python(x: Sequence[Sequence[float]], y: Sequence[float], noise: Sequence[float], parameters: Mapping[str, Any], variance_floor: float) -> SurrogateGPModel:
    mean_x, std_x = _standardization(x)
    standardized = tuple(_standardize_row(row, mean_x, std_x) for row in x)
    y_center = _mean(y)
    sigma_f = max(1.0, _sample_std(y))
    base = _kernel_matrix(standardized, standardized, sigma_f, float(parameters["ell"]))
    diagonal = [max(value, variance_floor) + float(parameters["minimum_regularization"]) for value in noise]
    jitter = 0.0
    cholesky: list[list[float]] | None = None
    for _ in range(int(parameters["cholesky_attempts"])):
        candidate = [row[:] for row in base]
        for index, value in enumerate(diagonal):
            candidate[index][index] += value + jitter
        try:
            cholesky = _cholesky(candidate)
            break
        except ValueError:
            jitter = max(1e-10, float(parameters["minimum_regularization"]) * 1e-3) if jitter == 0.0 else jitter * 10.0
    if cholesky is None:
        raise WorkerFailure("NUMERICAL_FAILURE", "Global surrogate GP Cholesky factorization failed.", "GLOBAL_DISTILLING", recoverable=True)
    alpha = _solve_cholesky(cholesky, [value - y_center for value in y])
    return SurrogateGPModel(mean_x, std_x, standardized, y_center, sigma_f, float(parameters["ell"]), tuple(tuple(row) for row in cholesky), tuple(alpha), max(float(parameters["noise_ratio"]) * _median(noise), variance_floor), _support_scale(standardized, min(300, len(standardized))), jitter)


def _train_surrogate_numpy(x: Sequence[Sequence[float]], y: Sequence[float], noise: Sequence[float], parameters: Mapping[str, Any], variance_floor: float) -> SurrogateGPModel:
    """Train the same heteroscedastic GP with LAPACK Cholesky operations."""

    assert _numpy is not None
    values = _numpy.asarray(x, dtype=_numpy.float64)
    target = _numpy.asarray(y, dtype=_numpy.float64)
    noise_values = _numpy.maximum(_numpy.asarray(noise, dtype=_numpy.float64), variance_floor)
    mean_x = _numpy.mean(values, axis=0)
    std_x = _numpy.std(values, axis=0, ddof=1)
    std_x = _numpy.where(std_x < _EPSILON, 1.0, std_x)
    standardized = (values - mean_x) / std_x
    y_center = float(_numpy.mean(target))
    sigma_f = max(1.0, float(_numpy.std(target, ddof=1)))
    base = sigma_f ** 2 * _numpy.exp(-0.5 * _squared_distance_matrix_numpy(standardized, standardized) / (float(parameters["ell"]) ** 2))
    jitter = 0.0
    cholesky: Any | None = None
    for _ in range(int(parameters["cholesky_attempts"])):
        candidate = base.copy()
        candidate[_numpy.diag_indices_from(candidate)] += noise_values + float(parameters["minimum_regularization"]) + jitter
        try:
            cholesky = _numpy.linalg.cholesky(candidate)
            break
        except _numpy.linalg.LinAlgError:
            jitter = max(1e-10, float(parameters["minimum_regularization"]) * 1e-3) if jitter == 0.0 else jitter * 10.0
    if cholesky is None:
        raise WorkerFailure("NUMERICAL_FAILURE", "Global surrogate GP Cholesky factorization failed.", "GLOBAL_DISTILLING", recoverable=True)
    alpha = _numpy.linalg.solve(cholesky.T, _numpy.linalg.solve(cholesky, target - y_center))
    return SurrogateGPModel(
        mean_x,
        std_x,
        standardized,
        y_center,
        sigma_f,
        float(parameters["ell"]),
        cholesky,
        alpha,
        max(float(parameters["noise_ratio"]) * float(_numpy.median(noise_values)), variance_floor),
        _support_scale(standardized, min(300, len(standardized))),
        jitter,
    )


def _surrogate_predict(model: SurrogateGPModel, x: Sequence[Sequence[float]], *, cancellation: CancellationContext, agent: int) -> tuple[tuple[float, ...], tuple[float, ...]]:
    if _numpy is not None:
        return _surrogate_predict_numpy(model, x, cancellation=cancellation, agent=agent)
    return _surrogate_predict_python(model, x, cancellation=cancellation, agent=agent)


def _surrogate_predict_python(model: SurrogateGPModel, x: Sequence[Sequence[float]], *, cancellation: CancellationContext, agent: int) -> tuple[tuple[float, ...], tuple[float, ...]]:
    means: list[float] = []
    stds: list[float] = []
    for index, query in enumerate(x):
        if index % 8 == 0:
            check_cancel(cancellation, "CALIBRATING", agent)
        standardized = _standardize_row(query, model.mean_x, model.std_x)
        vector = [model.sigma_f ** 2 * math.exp(-0.5 * _squared_distance(point, standardized) / (model.ell ** 2)) for point in model.x_train]
        means.append(model.y_center + _dot(vector, model.alpha))
        forward = _forward_solve(model.cholesky, vector)
        stds.append(math.sqrt(max(model.sigma_f ** 2 - _dot(forward, forward), 0.0) + model.observation_noise))
    return tuple(means), tuple(stds)


def _surrogate_predict_numpy(model: SurrogateGPModel, x: Sequence[Sequence[float]], *, cancellation: CancellationContext, agent: int) -> tuple[tuple[float, ...], tuple[float, ...]]:
    assert _numpy is not None
    queries = _numpy.asarray(x, dtype=_numpy.float64)
    training = _numpy.asarray(model.x_train, dtype=_numpy.float64)
    mean_x = _numpy.asarray(model.mean_x, dtype=_numpy.float64)
    std_x = _numpy.asarray(model.std_x, dtype=_numpy.float64)
    cholesky = _numpy.asarray(model.cholesky, dtype=_numpy.float64)
    alpha = _numpy.asarray(model.alpha, dtype=_numpy.float64)
    means = _numpy.empty(len(queries), dtype=_numpy.float64)
    stds = _numpy.empty(len(queries), dtype=_numpy.float64)
    for start in range(0, len(queries), 200):
        check_cancel(cancellation, "CALIBRATING", agent)
        block = (queries[start : start + 200] - mean_x) / std_x
        vector = model.sigma_f ** 2 * _numpy.exp(-0.5 * _squared_distance_matrix_numpy(training, block) / (model.ell ** 2))
        means[start : start + len(block)] = model.y_center + vector.T @ alpha
        forward = _numpy.linalg.solve(cholesky, vector)
        latent = _numpy.maximum(model.sigma_f ** 2 - _numpy.sum(forward * forward, axis=0), 0.0)
        stds[start : start + len(block)] = _numpy.sqrt(latent + model.observation_noise)
    return tuple(float(value) for value in means), tuple(float(value) for value in stds)


def _apply_transition_blend(values: Sequence[float], trend: Sequence[float], score: Sequence[float], parameters: Mapping[str, Any]) -> tuple[float, ...]:
    threshold = max(float(parameters["threshold"]), _EPSILON)
    maximum = float(parameters["maximum_mixing"])
    return tuple((1.0 - min(max(value / threshold, 0.0), maximum)) * prediction + min(max(value / threshold, 0.0), maximum) * trend_value for prediction, trend_value, value in zip(values, trend, score))


def _apply_rate_limit(values: Sequence[float], maximum_step: float) -> tuple[float, ...]:
    if not values:
        return ()
    output = [values[0]]
    for value in values[1:]:
        output.append(min(max(value, output[-1] - maximum_step), output[-1] + maximum_step))
    return tuple(output)


def _simple_kmeans(points: Sequence[Sequence[float]], count: int, iterations: int, seed: int) -> tuple[tuple[float, ...], ...]:
    if _numpy is not None:
        return _simple_kmeans_numpy(points, count, iterations, seed)
    return _simple_kmeans_python(points, count, iterations, seed)


def _simple_kmeans_python(points: Sequence[Sequence[float]], count: int, iterations: int, seed: int) -> tuple[tuple[float, ...], ...]:
    if not points or count <= 0:
        return ()
    count = min(count, len(points))
    stream = _ReferenceTwister(seed)
    centers = [tuple(points[stream.randi(len(points))])]
    minimum = [_squared_distance(point, centers[0]) for point in points]
    while len(centers) < count:
        index = max(range(len(points)), key=lambda item: (minimum[item], -item))
        centers.append(tuple(points[index]))
        minimum = [min(value, _squared_distance(point, centers[-1])) for point, value in zip(points, minimum)]
    assignment: list[int] | None = None
    for _ in range(iterations):
        distances = [[_squared_distance(point, center) for center in centers] for point in points]
        current = [min(range(count), key=lambda item: (row[item], item)) for row in distances]
        if assignment == current:
            break
        assignment = current
        for center_index in range(count):
            members = [point for point, chosen in zip(points, assignment) if chosen == center_index]
            if members:
                centers[center_index] = tuple(_mean([point[column] for point in members]) for column in range(len(centers[0])))
            else:
                farthest = max(range(len(points)), key=lambda item: (min(distances[item]), -item))
                centers[center_index] = tuple(points[farthest])
    return tuple(centers)


def _simple_kmeans_numpy(points: Sequence[Sequence[float]], count: int, iterations: int, seed: int) -> tuple[tuple[float, ...], ...]:
    """Use dense BLAS-backed assignment while retaining reference tie handling."""

    assert _numpy is not None
    if not points or count <= 0:
        return ()
    values = _numpy.asarray(points, dtype=_numpy.float64)
    count = min(count, len(values))
    stream = _ReferenceTwister(seed)
    centers = _numpy.empty((count, values.shape[1]), dtype=_numpy.float64)
    centers[0] = values[stream.randi(len(values))]
    minimum = _numpy.sum((values - centers[0]) ** 2, axis=1)
    for center_index in range(1, count):
        selected = int(_numpy.argmax(minimum))
        centers[center_index] = values[selected]
        minimum = _numpy.minimum(minimum, _numpy.sum((values - centers[center_index]) ** 2, axis=1))
    assignment: Any | None = None
    for _ in range(iterations):
        distances = _squared_distance_matrix_numpy(values, centers)
        current = _numpy.argmin(distances, axis=1)
        if assignment is not None and _numpy.array_equal(current, assignment):
            break
        assignment = current
        nearest = _numpy.min(distances, axis=1)
        for center_index in range(count):
            members = values[current == center_index]
            if len(members):
                centers[center_index] = _numpy.mean(members, axis=0)
            else:
                centers[center_index] = values[int(_numpy.argmax(nearest))]
    return tuple(tuple(float(value) for value in row) for row in centers)


def _farthest_point_subset(points: Sequence[Sequence[float]], count: int, seed: int) -> tuple[tuple[float, ...], ...]:
    if _numpy is not None:
        return _farthest_point_subset_numpy(points, count, seed)
    return _farthest_point_subset_python(points, count, seed)


def _farthest_point_subset_python(points: Sequence[Sequence[float]], count: int, seed: int) -> tuple[tuple[float, ...], ...]:
    if not points or count <= 0:
        return ()
    count = min(count, len(points))
    stream = _ReferenceTwister(seed)
    chosen = [stream.randi(len(points))]
    minimum = [_squared_distance(point, points[chosen[0]]) for point in points]
    while len(chosen) < count:
        index = max(range(len(points)), key=lambda item: (minimum[item], -item))
        chosen.append(index)
        minimum = [min(value, _squared_distance(point, points[index])) for point, value in zip(points, minimum)]
    return tuple(tuple(points[index]) for index in chosen)


def _farthest_point_subset_numpy(points: Sequence[Sequence[float]], count: int, seed: int) -> tuple[tuple[float, ...], ...]:
    assert _numpy is not None
    if not points or count <= 0:
        return ()
    values = _numpy.asarray(points, dtype=_numpy.float64)
    count = min(count, len(values))
    stream = _ReferenceTwister(seed)
    chosen = [stream.randi(len(values))]
    minimum = _numpy.sum((values - values[chosen[0]]) ** 2, axis=1)
    while len(chosen) < count:
        index = int(_numpy.argmax(minimum))
        chosen.append(index)
        minimum = _numpy.minimum(minimum, _numpy.sum((values - values[index]) ** 2, axis=1))
    return tuple(tuple(float(value) for value in values[index]) for index in chosen)


def _stable_unique_rows(rows: Sequence[Sequence[float]]) -> tuple[tuple[float, ...], ...]:
    output: list[tuple[float, ...]] = []
    seen: set[tuple[float, ...]] = set()
    for row in rows:
        key = tuple(row)
        if key not in seen:
            seen.add(key)
            output.append(key)
    return tuple(output)


def _support_scale(points: Sequence[Sequence[float]], maximum: int) -> float:
    if _numpy is not None:
        return _support_scale_numpy(points, maximum)
    return _support_scale_python(points, maximum)


def _support_scale_python(points: Sequence[Sequence[float]], maximum: int) -> float:
    if len(points) < 2:
        return 1.0
    count = min(len(points), maximum)
    indices = _linspace_indices(len(points), count)
    selected = [points[index] for index in indices]
    nearest = []
    for index, point in enumerate(selected):
        values = [math.sqrt(_squared_distance(point, other)) for other_index, other in enumerate(selected) if other_index != index]
        if values and min(values) > 0.0:
            nearest.append(min(values))
    return _median(nearest) if nearest else 1.0


def _support_scale_numpy(points: Sequence[Sequence[float]], maximum: int) -> float:
    assert _numpy is not None
    if len(points) < 2:
        return 1.0
    indices = _linspace_indices(len(points), min(len(points), maximum))
    selected = _numpy.asarray([points[index] for index in indices], dtype=_numpy.float64)
    distances = _squared_distance_matrix_numpy(selected, selected)
    distances[_numpy.diag_indices_from(distances)] = _numpy.inf
    nearest = _numpy.sqrt(_numpy.min(distances, axis=1))
    finite = nearest[_numpy.isfinite(nearest) & (nearest > 0.0)]
    return float(_numpy.median(finite)) if len(finite) else 1.0


def _nearest_distances(training: Sequence[Sequence[float]], queries: Sequence[Sequence[float]]) -> tuple[float, ...]:
    if _numpy is not None:
        return _nearest_distances_numpy(training, queries)
    return tuple(math.sqrt(min(_squared_distance(query, point) for point in training)) for query in queries)


def _nearest_distances_numpy(training: Sequence[Sequence[float]], queries: Sequence[Sequence[float]]) -> tuple[float, ...]:
    assert _numpy is not None
    train = _numpy.asarray(training, dtype=_numpy.float64)
    query = _numpy.asarray(queries, dtype=_numpy.float64)
    train_norm = _numpy.sum(train * train, axis=1)
    result = _numpy.empty(len(query), dtype=_numpy.float64)
    for start in range(0, len(query), 200):
        block = query[start : start + 200]
        squared = _numpy.sum(block * block, axis=1)[:, _numpy.newaxis] + train_norm[_numpy.newaxis, :] - 2.0 * block @ train.T
        result[start : start + len(block)] = _numpy.sqrt(_numpy.maximum(_numpy.min(squared, axis=1), 0.0))
    return tuple(float(value) for value in result)


def _squared_distance_matrix_numpy(left: Any, right: Any) -> Any:
    """Compute deterministic squared distances in bounded dense blocks."""

    assert _numpy is not None
    left_values = _numpy.asarray(left, dtype=_numpy.float64)
    right_values = _numpy.asarray(right, dtype=_numpy.float64)
    output = _numpy.empty((len(left_values), len(right_values)), dtype=_numpy.float64)
    for start in range(0, len(left_values), 256):
        block = left_values[start : start + 256]
        delta = block[:, _numpy.newaxis, :] - right_values[_numpy.newaxis, :, :]
        output[start : start + len(block)] = _numpy.einsum("ijk,ijk->ij", delta, delta, optimize=True)
    return output


def _surrogate_support(model: SurrogateGPModel, queries: Sequence[Sequence[float]], parameters: Mapping[str, Any]) -> tuple[float, ...]:
    standardized = tuple(_standardize_row(query, model.mean_x, model.std_x) for query in queries)
    denominator = max(float(parameters["scale_multiple"]) * model.support_scale, 1e-8)
    return tuple(0.0 if value < float(parameters["minimum_weight"]) else min(max(value, 0.0), 1.0) for value in (math.exp(-0.5 * (distance / denominator) ** 2) for distance in _nearest_distances(model.x_train, standardized)))


def _metrics(context: AgentContext, rows: Sequence[Mapping[str, Any]]) -> Mapping[str, Any]:
    truth = [float(row["TrueAverageCurrentSmoothed"]) for row in rows]
    local = [float(row["LocalPrediction"]) for row in rows]
    global_values = [float(row["GlobalPrediction"]) for row in rows]
    fused = [float(row["FusedPrediction"]) for row in rows]
    local_rmse, global_rmse, fused_rmse = _rmse(truth, local), _rmse(truth, global_values), _rmse(truth, fused)
    local_error = [abs(a - b) for a, b in zip(truth, local)]
    global_error = [abs(a - b) for a, b in zip(truth, global_values)]
    fused_error = [abs(a - b) for a, b in zip(truth, fused)]
    oracle = [global_value if global_abs < local_abs else local_value for global_value, local_value, global_abs, local_abs in zip(global_values, local, global_error, local_error)]
    return {
        "Agent": f"Agent {context.agent}", "GlobalModelType": "Leave-one-agent-out", "LocalRMSE": local_rmse, "GlobalRMSE": global_rmse, "FusedRMSE": fused_rmse,
        "LocalMAE": _mean(local_error), "GlobalMAE": _mean(global_error), "FusedMAE": _mean(fused_error), "LocalR2": _r2(truth, local), "GlobalR2": _r2(truth, global_values), "FusedR2": _r2(truth, fused),
        "LocalCoverage": _mean([1.0 if row["LocalPrediction"] - row["LocalHalfWidth"] <= row["TrueAverageCurrentSmoothed"] <= row["LocalPrediction"] + row["LocalHalfWidth"] else 0.0 for row in rows]),
        "GlobalCoverage": _mean([1.0 if row["GlobalPrediction"] - row["GlobalHalfWidth"] <= row["TrueAverageCurrentSmoothed"] <= row["GlobalPrediction"] + row["GlobalHalfWidth"] else 0.0 for row in rows]),
        "FusedCoverage": _mean([1.0 if row["FusedInsideInterval"] else 0.0 for row in rows]), "FusionImprovementPercent": 100.0 * (local_rmse - fused_rmse) / local_rmse if local_rmse > _EPSILON else 0.0,
        "CalibrationGlobalWeight": context.runtime.fusion_alpha, "MeanOnlineGlobalWeight": _mean([float(row["FusionAlpha"]) for row in rows]), "FusionActiveRate": _mean([1.0 if row["FusionAlpha"] > 1e-3 else 0.0 for row in rows]),
        "NegativeTransferRate": _mean([1.0 if fused_abs > local_abs + 1e-12 else 0.0 for fused_abs, local_abs in zip(fused_error, local_error)]), "GlobalBetterPointRate": _mean([1.0 if global_abs < local_abs else 0.0 for global_abs, local_abs in zip(global_error, local_error)]),
        "FusedBetterThanBothRate": _mean([1.0 if fused_abs < min(local_abs, global_abs) else 0.0 for fused_abs, local_abs, global_abs in zip(fused_error, local_error, global_error)]), "OracleBestExpertRMSE": _rmse(truth, oracle), "MeanGlobalSupport": _mean([float(row["GlobalSupport"]) for row in rows]),
    }


def _agent_diagnostics(context: AgentContext, rows: Sequence[Mapping[str, Any]]) -> Mapping[str, Any]:
    errors = sorted(((abs(float(row["FusedPrediction"]) - float(row["TrueAverageCurrentSmoothed"])), index) for index, row in enumerate(rows)), reverse=True)
    coverage_window = int(context.parameters["interval"]["coverage_window"])
    recent_coverage = [1.0 if row["FusedInsideInterval"] else 0.0 for row in rows[-coverage_window:]]
    return {"agent": context.agent, "local_fallbacks": context.runtime.local_fallbacks, "support_scale": context.runtime.local_support_scale, "final_rolling_fused_coverage": _mean(recent_coverage), "top_fused_error_indices": [index for _, index in errors[:_DIAGNOSTIC_TOP_N]]}


def _partition_row(context: AgentContext) -> Mapping[str, Any]:
    rows = context.feature_dataset.source_rows
    return {"Agent": f"Agent {context.agent}", "RunningRows": len(rows) + int(context.parameters["feature_state"]["nLag"]), "SupervisedSamples": len(context.feature_dataset.values), "TrainingSamples": len(context.split.train), "CalibrationSamples": len(context.split.calibration), "TestingSamples": len(context.split.test), "StartTime": rows[0].time_raw, "EndTime": rows[-1].time_raw}


def _feature_names(context: AgentContext) -> tuple[str, ...]:
    from .features import feature_names
    return feature_names(int(context.parameters["feature_state"]["nLag"]))


def _trend_and_score_columns(context: AgentContext) -> tuple[int, int]:
    names = _feature_names(context)
    return names.index("Iavg_trend"), names.index("transition_score")


def _alarm_fields(status: str, high_count: int, low_count: int, row: Any, parameters: Mapping[str, Any]) -> tuple[str, str, str, str, str, str]:
    count = high_count if status == "Heavy load" else low_count if status == "Light load" else 0
    load_level, load_reason = "None", ""
    if count >= int(parameters["alarm_count"]):
        load_level = "Alarm"
        load_reason = "Continuous heavy load / possible overload" if status == "Heavy load" else "Continuous light load / possible empty running"
    elif count >= int(parameters["warning_count"]):
        load_level = "Warning"
        load_reason = "Continuous heavy-load warning" if status == "Heavy load" else "Continuous light-load warning"
    elif count >= int(parameters["notice_count"]):
        load_level = "Notice"
        load_reason = "Single-point heavy-load trend" if status == "Heavy load" else "Single-point light-load trend"
    if parameters["absolute_current_threshold"] is not None and row.iavg_raw > float(parameters["absolute_current_threshold"]):
        load_level = "Alarm"
        load_reason = _append_reason(load_reason, "Absolute current threshold exceeded")
    if parameters["absolute_tension_threshold"] is not None and row.zl_raw > float(parameters["absolute_tension_threshold"]):
        load_level = "Alarm"
        load_reason = _append_reason(load_reason, "Absolute tension threshold exceeded")
    data_level, data_reason = "None", ""
    if row.is_spike_sample:
        data_level, data_reason = "Warning", _append_reason(data_reason, "Spike/data-quality abnormality")
    if row.motor_zero_warning:
        data_level, data_reason = "Warning", _append_reason(data_reason, "Motor zero warning")
    if row.iimb_raw > float(parameters["imbalance_threshold"]):
        data_level, data_reason = "Warning", _append_reason(data_reason, "Motor imbalance warning")
    rank = {"None": 0, "Notice": 1, "Warning": 2, "Alarm": 3}
    overall = load_level if rank[load_level] >= rank[data_level] else data_level
    return load_level, load_reason, data_level, data_reason, overall, _append_reason(load_reason, data_reason)


def _append_reason(current: str, addition: str) -> str:
    return addition if not current else current if not addition else f"{current}; {addition}"


def _initial_window(values: Sequence[float], parameters: Mapping[str, Any], agent: int, label: str) -> tuple[float, ...]:
    finite = [value for value in values if math.isfinite(value)]
    window = finite[-int(parameters["interval"]["calibration_window"]):]
    if len(window) < int(parameters["interval"]["minimum_scores"]):
        raise WorkerFailure("INSUFFICIENT_SAMPLES", f"Agent {agent} has insufficient {label} calibration scores.", "CALIBRATING", agent, True)
    return tuple(window)


def _initial_errors(target: Sequence[float], prediction: Sequence[float], parameters: Mapping[str, Any], agent: int, label: str) -> tuple[float, ...]:
    values = [(actual - estimated) ** 2 for actual, estimated in zip(target, prediction) if math.isfinite(actual - estimated)]
    window = values[-int(parameters["fusion"]["error_window"]):]
    if len(window) < int(parameters["fusion"]["minimum_samples"]):
        raise WorkerFailure("INSUFFICIENT_SAMPLES", f"Agent {agent} has insufficient {label} calibration errors.", "CALIBRATING", agent, True)
    return tuple(window)


def _score_quantile(values: Sequence[float], parameters: Mapping[str, Any]) -> float:
    filtered = sorted(value for value in values if math.isfinite(value))
    if not filtered:
        raise WorkerFailure("NUMERICAL_FAILURE", "Calibration score window is empty.", "TESTING", recoverable=True)
    index = min(max(math.ceil((len(filtered) + 1) * float(parameters["interval"]["confidence"])) - 1, 0), len(filtered) - 1)
    return min(max(filtered[index], float(parameters["interval"]["calibration_scale_min"])), float(parameters["interval"]["calibration_scale_max"]))


def _calibrated_variances(stds: Sequence[float], q: float, parameters: Mapping[str, Any]) -> tuple[float, ...]:
    interval = parameters["interval"]
    multiplier = _normal_multiplier(float(interval["confidence"]))
    return tuple(max((_half_width(q, std, interval) / multiplier) ** 2, float(interval["variance_floor"])) for std in stds)


def _half_width(q: float, std: float, parameters: Mapping[str, Any]) -> float:
    return min(max(q * std, float(parameters["half_width_min"])), float(parameters["half_width_max"]))


def _normal_multiplier(confidence: float) -> float:
    # NormalDist is stable and is the direct Python equivalent of sqrt(2)*erfinv.
    return statistics.NormalDist().inv_cdf((1.0 + confidence) / 2.0)


def _initial_winner_counts(local: Sequence[float], global_values: Sequence[float], parameters: Mapping[str, Any]) -> tuple[int, int]:
    local_mean, global_mean = _robust_recent_mean(local, float(parameters["winsor_quantile"])), _robust_recent_mean(global_values, float(parameters["winsor_quantile"]))
    persistence = int(parameters["persistence"])
    margin = float(parameters["win_margin"])
    if global_mean <= (1.0 - margin) * local_mean:
        return 0, persistence
    if local_mean <= (1.0 - margin) * global_mean:
        return persistence, 0
    return 0, 0


def _update_window(values: list[float], value: float, maximum: int) -> None:
    if not math.isfinite(value):
        return
    values.append(value)
    if len(values) > maximum:
        del values[:-maximum]


def _robust_recent_mean(values: Sequence[float], quantile: float) -> float:
    filtered = sorted(value for value in values if math.isfinite(value) and value >= 0.0)
    if not filtered:
        return math.inf
    index = min(max(math.ceil(min(max(quantile, 0.0), 1.0) * len(filtered)) - 1, 0), len(filtered) - 1)
    cap = filtered[index]
    return _mean([min(value, cap) for value in filtered])


def _optimize_alpha(target: Sequence[float], local: Sequence[float], global_values: Sequence[float], maximum: float) -> float:
    delta = [global_value - local_value for global_value, local_value in zip(global_values, local)]
    residual = [value - local_value for value, local_value in zip(target, local)]
    denominator = _dot(delta, delta)
    return min(max(_dot(delta, residual) / denominator, 0.0), maximum) if denominator > _EPSILON else 0.0


def _mixture_variance(local_mean: float, global_mean: float, local_variance: float, global_variance: float, alpha: float, floor: float) -> float:
    alpha = min(max(alpha, 0.0), 1.0)
    return max((1.0 - alpha) * max(local_variance, floor) + alpha * max(global_variance, floor) + alpha * (1.0 - alpha) * (local_mean - global_mean) ** 2, floor)


def _kernel_matrix(left: Sequence[Sequence[float]], right: Sequence[Sequence[float]], sigma_f: float, ell: float) -> list[list[float]]:
    return [[sigma_f ** 2 * math.exp(-0.5 * _squared_distance(a, b) / (ell ** 2)) for b in right] for a in left]


def _cholesky(matrix: Sequence[Sequence[float]]) -> list[list[float]]:
    count = len(matrix)
    lower = [[0.0] * count for _ in range(count)]
    for row in range(count):
        for column in range(row + 1):
            value = matrix[row][column] - sum(lower[row][index] * lower[column][index] for index in range(column))
            if row == column:
                if not math.isfinite(value) or value <= 0.0:
                    raise ValueError("matrix is not positive definite")
                lower[row][column] = math.sqrt(value)
            else:
                lower[row][column] = value / lower[column][column]
    return lower


def _forward_solve(lower: Sequence[Sequence[float]], values: Sequence[float]) -> list[float]:
    output: list[float] = []
    for row, value in enumerate(values):
        output.append((value - sum(lower[row][index] * output[index] for index in range(row))) / lower[row][row])
    return output


def _solve_cholesky(lower: Sequence[Sequence[float]], values: Sequence[float]) -> list[float]:
    forward = _forward_solve(lower, values)
    output = [0.0] * len(values)
    for row in range(len(values) - 1, -1, -1):
        output[row] = (forward[row] - sum(lower[index][row] * output[index] for index in range(row + 1, len(values)))) / lower[row][row]
    return output


def _weighted_local_fallback(values: Sequence[float], distances: Sequence[float], ell: float, regularization: float) -> tuple[float, float]:
    weights = [math.exp(-0.5 * value / (ell ** 2)) for value in distances]
    total = max(sum(weights), _EPSILON)
    mean = sum(weight * value for weight, value in zip(weights, values)) / total
    return mean, max(sum(weight * (value - mean) ** 2 for weight, value in zip(weights, values)) / total + regularization, _EPSILON)


def _local_bounds(values: Sequence[float]) -> tuple[float, float]:
    ordered = sorted(values)
    low = ordered[min(max(_round_half_away_from_zero(0.01 * len(values)) - 1, 0), len(values) - 1)]
    high = ordered[min(max(_round_half_away_from_zero(0.99 * len(values)) - 1, 0), len(values) - 1)]
    margin = max(2.0, 0.5 * (max(values) - min(values)))
    return low - margin, high + margin


def _standardization(rows: Sequence[Sequence[float]]) -> tuple[tuple[float, ...], tuple[float, ...]]:
    mean = tuple(_mean([row[index] for row in rows]) for index in range(len(rows[0])))
    std = tuple(max(_sample_std([row[index] for row in rows]), 1.0) if _sample_std([row[index] for row in rows]) < _EPSILON else _sample_std([row[index] for row in rows]) for index in range(len(rows[0])))
    return mean, std


def _standardize_row(row: Sequence[float], mean: Sequence[float], std: Sequence[float]) -> tuple[float, ...]:
    return tuple((value - center) / scale for value, center, scale in zip(row, mean, std))


def _unstandardize(row: Sequence[float], parameters: Standardization) -> tuple[float, ...]:
    return tuple(value * scale + center for value, center, scale in zip(row, parameters.means, parameters.sample_stds))


def _validate_feature_widths(contexts: Sequence[AgentContext]) -> None:
    if any(not context.feature_dataset.values for context in contexts):
        raise WorkerFailure("INSUFFICIENT_SAMPLES", "Agent feature data is empty.", "PREPROCESSING", recoverable=True)
    if any(len(context.feature_dataset.values[0]) != 4 * int(context.parameters["feature_state"]["nLag"]) + 32 for context in contexts):
        raise WorkerFailure("INPUT_INVALID", "Feature dimension does not match frozen nLag.", "PREPROCESSING")


def _shared_floor(contexts: Sequence[AgentContext]) -> float:
    return max(float(context.parameters["interval"]["variance_floor"]) for context in contexts)


def _public_anchor_seed(contexts: Sequence[AgentContext]) -> int:
    seeds = [int(context.parameters["anchors"]["random_seed"]) for context in contexts]
    if len(set(seeds)) == 1:
        return seeds[0] + 100
    digest = hashlib.sha256(",".join(str(seed) for seed in seeds).encode("ascii")).digest()
    return int.from_bytes(digest[:4], "big")


def _stable_nearest_contribution(contributions: Sequence[AgentContribution], anchor: int) -> int:
    return max(range(len(contributions)), key=lambda index: (contributions[index].anchor_support[anchor], -index))


def _linspace_indices(length: int, count: int) -> tuple[int, ...]:
    if count == 1:
        return (0,)
    return tuple(_round_half_away_from_zero(index * (length - 1) / (count - 1)) for index in range(count))


def _empirical_quantile(values: Sequence[float], probability: float) -> float:
    ordered = sorted(value for value in values if math.isfinite(value))
    if not ordered:
        return math.nan
    index = min(max(math.ceil(min(max(probability, 0.0), 1.0) * len(ordered)) - 1, 0), len(ordered) - 1)
    return ordered[index]


def _normalise_numeric(value: Any) -> float:
    number = float(value)
    if not math.isfinite(number):
        raise WorkerFailure("NUMERICAL_FAILURE", "Simulation produced a non-finite value.", "TESTING", recoverable=True)
    return number


def _r2(actual: Sequence[float], predicted: Sequence[float]) -> float:
    baseline = _mean(actual)
    denominator = sum((value - baseline) ** 2 for value in actual)
    return 0.0 if denominator <= _EPSILON else 1.0 - sum((value - estimate) ** 2 for value, estimate in zip(actual, predicted)) / denominator


def _rmse(actual: Sequence[float], predicted: Sequence[float]) -> float:
    return math.sqrt(_mean([(value - estimate) ** 2 for value, estimate in zip(actual, predicted)]))


def _mean(values: Sequence[float]) -> float:
    return sum(values) / len(values)


def _median(values: Sequence[float]) -> float:
    return statistics.median(values)


def _sample_std(values: Sequence[float]) -> float:
    return statistics.stdev(values) if len(values) >= 2 else 0.0


def _dot(left: Sequence[float], right: Sequence[float]) -> float:
    return sum(a * b for a, b in zip(left, right))


def _squared_distance(left: Sequence[float], right: Sequence[float]) -> float:
    return sum((a - b) ** 2 for a, b in zip(left, right))


def _normalize(values: Sequence[float]) -> list[float]:
    total = sum(values)
    if total <= 0.0:
        raise ValueError("Aggregation weights must contain a positive value.")
    return [value / total for value in values]


def _round_half_away_from_zero(value: float) -> int:
    return math.floor(value + 0.5) if value >= 0 else math.ceil(value - 0.5)
