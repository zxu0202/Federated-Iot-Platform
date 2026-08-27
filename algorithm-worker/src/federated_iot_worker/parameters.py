"""Frozen custom-parameter parsing for the M1 simulation preparation boundary."""

from __future__ import annotations

import math
from dataclasses import dataclass
from types import MappingProxyType
from typing import Any, Mapping

from .contracts import WorkerTask
from .errors import ContractFailure
from .preprocessing import PreprocessingConfig


@dataclass(frozen=True)
class _FieldSpec:
    """One leaf in the source-compatible custom parameter tree."""

    kind: str
    minimum: float | None = None
    maximum: float | None = None
    exclusive_minimum: bool = False
    allowed: frozenset[Any] | None = None
    nullable: bool = False


_INTEGER = "integer"
_NUMBER = "number"
_BOOLEAN = "boolean"
_STRING = "string"

# The Worker has no profile-store client. This closed view validates only the
# immutable parameter tree supplied by worker.task.v1 and has no default filler.
_PARAMETER_SPEC: Mapping[str, Mapping[str, _FieldSpec]] = {
    "feature_state": {
        "nLag": _FieldSpec(_INTEGER, minimum=5),
        "speed_threshold": _FieldSpec(_NUMBER, minimum=0.0),
        "current_threshold": _FieldSpec(_NUMBER, minimum=0.0),
    },
    "cleaning": {
        "median_window": _FieldSpec(_INTEGER, minimum=1),
        "mad_factor": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "smoothing_window": _FieldSpec(_INTEGER, minimum=1),
    },
    "split": {
        "training_ratio": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "calibration_ratio": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "minimum_training": _FieldSpec(_INTEGER, minimum=1),
        "minimum_calibration": _FieldSpec(_INTEGER, minimum=1),
        "minimum_testing": _FieldSpec(_INTEGER, minimum=1),
        "agent_count": _FieldSpec(_INTEGER, minimum=3, maximum=3),
    },
    "local_gp": {
        "kNN": _FieldSpec(_INTEGER, minimum=1),
        "adaptive_ratio": _FieldSpec(_NUMBER, minimum=0.0, maximum=1.0, exclusive_minimum=True),
        "ell": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "sigma_f": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "sigma_n": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "minimum_regularization": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
    },
    "trend": {
        "threshold": _FieldSpec(_NUMBER, minimum=0.0),
        "maximum_mixing": _FieldSpec(_NUMBER, minimum=0.0, maximum=1.0),
        "gain": _FieldSpec(_NUMBER),
        "maximum_step_change": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
    },
    "interval": {
        "confidence": _FieldSpec(_NUMBER, minimum=0.0, maximum=1.0, exclusive_minimum=True),
        "calibration_window": _FieldSpec(_INTEGER, minimum=1),
        "minimum_scores": _FieldSpec(_INTEGER, minimum=1),
        "std_floor": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "calibration_scale_min": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "calibration_scale_max": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "half_width_min": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "half_width_max": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "coverage_window": _FieldSpec(_INTEGER, minimum=1),
        "update_mode": _FieldSpec(_STRING, allowed=frozenset({"all_finite"})),
        "variance_floor": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
    },
    "anchors": {
        "base_centers": _FieldSpec(_INTEGER, minimum=1),
        "transition_centers": _FieldSpec(_INTEGER, minimum=1),
        "boundary_centers": _FieldSpec(_INTEGER, minimum=1),
        "transition_quantile": _FieldSpec(_NUMBER, minimum=0.0, maximum=1.0, exclusive_minimum=True),
        "public_anchors": _FieldSpec(_INTEGER, minimum=10),
        "iterations": _FieldSpec(_INTEGER, minimum=1),
        "random_seed": _FieldSpec(_INTEGER, minimum=0),
    },
    "support": {
        "scale_multiple": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "minimum_weight": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "minimum_query_support": _FieldSpec(_NUMBER, minimum=0.0, maximum=1.0),
        "full_weight_reference": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
    },
    "global_surrogate": {
        "ell": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "minimum_regularization": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "noise_ratio": _FieldSpec(_NUMBER, minimum=0.0),
        "cholesky_attempts": _FieldSpec(_INTEGER, minimum=1),
        "leave_one_out": _FieldSpec(_BOOLEAN, allowed=frozenset({True})),
    },
    "fusion": {
        "maximum_global_weight": _FieldSpec(_NUMBER, minimum=0.0, maximum=1.0),
        "initial_improvement": _FieldSpec(_NUMBER, minimum=0.0),
        "error_window": _FieldSpec(_INTEGER, minimum=1),
        "minimum_samples": _FieldSpec(_INTEGER, minimum=1),
        "win_margin": _FieldSpec(_NUMBER, minimum=0.0, maximum=1.0),
        "variance_weight": _FieldSpec(_NUMBER, minimum=0.0),
        "winsor_quantile": _FieldSpec(_NUMBER, minimum=0.0, maximum=1.0, exclusive_minimum=True),
        "global_clear_threshold": _FieldSpec(_NUMBER, minimum=0.0, maximum=1.0),
        "neutral_upper_limit": _FieldSpec(_NUMBER, minimum=0.0, maximum=1.0),
        "persistence": _FieldSpec(_INTEGER, minimum=1),
        "rise_smoothing": _FieldSpec(_NUMBER, minimum=0.0, maximum=1.0),
        "fall_smoothing": _FieldSpec(_NUMBER, minimum=0.0, maximum=1.0),
        "disagreement_kappa": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
        "maximum_variance_ratio": _FieldSpec(_NUMBER, minimum=0.0, exclusive_minimum=True),
    },
    "alarms": {
        "imbalance_threshold": _FieldSpec(_NUMBER, minimum=0.0, maximum=1.0),
        "notice_count": _FieldSpec(_INTEGER, minimum=1),
        "warning_count": _FieldSpec(_INTEGER, minimum=1),
        "alarm_count": _FieldSpec(_INTEGER, minimum=1),
        "absolute_current_threshold": _FieldSpec(_NUMBER, minimum=0.0, nullable=True),
        "absolute_tension_threshold": _FieldSpec(_NUMBER, minimum=0.0, nullable=True),
    },
}


def accepted_shared_parameter_paths() -> frozenset[str]:
    """Return the closed flattened path set accepted from a task snapshot."""

    return frozenset(
        f"{group}.{leaf}"
        for group, leaves in _PARAMETER_SPEC.items()
        for leaf in leaves
    )


@dataclass(frozen=True)
class AgentPreparationParameters:
    """One Agent's immutable, merged custom parameters for implemented stages."""

    effective_parameters: Mapping[str, Any]
    preprocessing: PreprocessingConfig
    n_lag: int
    training_ratio: float
    calibration_ratio: float
    minimum_training: int
    minimum_calibration: int
    minimum_testing: int
    trend_gain: float
    base_center_seed: int

    @property
    def minimum_partition_rows(self) -> int:
        """Match the reference guard before supervised-row creation."""

        return self.n_lag + self.minimum_training + self.minimum_calibration + self.minimum_testing + 5

    @property
    def preprocessing_cache_key(self) -> tuple[object, ...]:
        """Return the complete preprocessing identity for one task-local cache."""

        return preprocessing_cache_key(self.preprocessing)


@dataclass(frozen=True)
class SimulationPreparationParameters:
    """The three immutable Agent-effective configurations of one task envelope."""

    by_agent: Mapping[int, AgentPreparationParameters]

    def for_agent(self, agent: int) -> AgentPreparationParameters:
        """Return the complete frozen configuration of one logical Agent."""

        try:
            return self.by_agent[agent]
        except KeyError as error:
            raise ContractFailure("S1 simulation parameters are missing one Agent.") from error


def simulation_preparation_parameters(task: WorkerTask) -> SimulationPreparationParameters:
    """Parse only the immutable task-envelope parameters for one simulation.

    The Worker has no profile repository and cannot observe profile edits after
    this call. Sparse Agent parameter objects are recursively merged onto the
    complete frozen shared tree before validation and context construction.
    """

    snapshot = _object(task.raw.get("parameter_snapshot"), "parameter_snapshot")
    shared = _validated_parameter_tree(
        _object(snapshot.get("shared_parameters"), "parameter_snapshot.shared_parameters"),
        "parameter_snapshot.shared_parameters",
    )
    seeds = _base_center_seeds(task)
    by_agent: dict[int, AgentPreparationParameters] = {}
    for agent_snapshot in task.parameter_agents:
        agent = int(agent_snapshot["agent"])
        override = _object(agent_snapshot.get("parameters"), f"parameter_snapshot.agents[{agent}].parameters")
        merged = _deep_merge(shared, override, f"parameter_snapshot.agents[{agent}].parameters")
        effective = _validated_parameter_tree(merged, f"parameter_snapshot.agents[{agent}].parameters")
        by_agent[agent] = _agent_preparation_parameters(effective, seeds[agent])

    if set(by_agent) != {1, 2, 3}:
        raise ContractFailure("S1 simulation parameters must contain exactly Agent 1, Agent 2, and Agent 3.")
    return SimulationPreparationParameters(MappingProxyType(by_agent))


def preprocessing_cache_key(config: PreprocessingConfig) -> tuple[object, ...]:
    """Create a local-only cache key without using mutable profile state."""

    return (
        config.contract_version,
        config.speed_stop_threshold,
        config.current_stop_threshold,
        config.median_window,
        config.mad_factor,
        config.smooth_window,
        config.median_filter_path,
        config.smoothing_path,
    )


def _agent_preparation_parameters(parameters: Mapping[str, Any], base_center_seed: int) -> AgentPreparationParameters:
    feature_state = parameters["feature_state"]
    split = parameters["split"]
    trend = parameters["trend"]
    return AgentPreparationParameters(
        effective_parameters=_freeze_mapping(parameters),
        preprocessing=_preprocessing_config(parameters),
        n_lag=int(feature_state["nLag"]),
        training_ratio=float(split["training_ratio"]),
        calibration_ratio=float(split["calibration_ratio"]),
        minimum_training=int(split["minimum_training"]),
        minimum_calibration=int(split["minimum_calibration"]),
        minimum_testing=int(split["minimum_testing"]),
        trend_gain=float(trend["gain"]),
        base_center_seed=base_center_seed,
    )


def _preprocessing_config(parameters: Mapping[str, Any]) -> PreprocessingConfig:
    feature_state = parameters["feature_state"]
    cleaning = parameters["cleaning"]
    return PreprocessingConfig(
        speed_stop_threshold=float(feature_state["speed_threshold"]),
        current_stop_threshold=float(feature_state["current_threshold"]),
        median_window=int(cleaning["median_window"]),
        mad_factor=float(cleaning["mad_factor"]),
        smooth_window=int(cleaning["smoothing_window"]),
    )


def _base_center_seeds(task: WorkerTask) -> Mapping[int, int]:
    runtime = _object(task.raw.get("runtime"), "runtime")
    streams = _object(runtime.get("random_streams"), "runtime.random_streams")
    seed_values = _object(
        streams.get("base_center_seed_by_agent"),
        "runtime.random_streams.base_center_seed_by_agent",
    )
    return MappingProxyType(
        {
            agent: _integer(
                seed_values.get(str(agent)),
                f"runtime.random_streams.base_center_seed_by_agent.{agent}",
                minimum=0,
            )
            for agent in (1, 2, 3)
        }
    )


def _validated_parameter_tree(value: Mapping[str, Any], field: str) -> Mapping[str, Any]:
    _exact_keys(value, _PARAMETER_SPEC, field)
    validated: dict[str, Any] = {}
    for group, group_spec in _PARAMETER_SPEC.items():
        group_value = _object(value[group], f"{field}.{group}")
        _exact_keys(group_value, group_spec, f"{field}.{group}")
        validated[group] = {
            name: _validate_value(group_value[name], spec, f"{field}.{group}.{name}")
            for name, spec in group_spec.items()
        }
    _validate_combinations(validated, field)
    return validated


def _validate_combinations(parameters: Mapping[str, Any], field: str) -> None:
    split = parameters["split"]
    if split["training_ratio"] + split["calibration_ratio"] >= 1.0:
        raise ContractFailure(f"{field}.split training and calibration ratios must leave a test partition.")
    interval = parameters["interval"]
    if interval["calibration_window"] < interval["minimum_scores"]:
        raise ContractFailure(f"{field}.interval.calibration_window must be at least minimum_scores.")
    if interval["calibration_scale_min"] > interval["calibration_scale_max"]:
        raise ContractFailure(f"{field}.interval calibration scale bounds are invalid.")
    if interval["half_width_min"] > interval["half_width_max"]:
        raise ContractFailure(f"{field}.interval half-width bounds are invalid.")
    fusion = parameters["fusion"]
    if fusion["error_window"] < fusion["minimum_samples"]:
        raise ContractFailure(f"{field}.fusion.error_window must be at least minimum_samples.")
    if fusion["neutral_upper_limit"] > fusion["global_clear_threshold"]:
        raise ContractFailure(f"{field}.fusion neutral_upper_limit must not exceed global_clear_threshold.")
    alarms = parameters["alarms"]
    if not alarms["notice_count"] <= alarms["warning_count"] <= alarms["alarm_count"]:
        raise ContractFailure(f"{field}.alarms counts must be ordered notice, warning, alarm.")


def _deep_merge(base: Mapping[str, Any], override: Mapping[str, Any], field: str) -> Mapping[str, Any]:
    unknown = sorted(set(override) - set(base))
    if unknown:
        raise ContractFailure(f"{field} contains unknown parameter paths: {', '.join(unknown)}.")
    merged: dict[str, Any] = {}
    for key, base_value in base.items():
        if key not in override:
            if isinstance(base_value, Mapping):
                merged[key] = _deep_merge(base_value, {}, f"{field}.{key}")
            else:
                merged[key] = base_value
            continue
        override_value = override[key]
        if isinstance(base_value, Mapping):
            if not isinstance(override_value, Mapping):
                raise ContractFailure(f"{field}.{key} must be an object for a sparse override.")
            merged[key] = _deep_merge(base_value, override_value, f"{field}.{key}")
        else:
            merged[key] = override_value
    return merged


def _freeze_mapping(value: Mapping[str, Any]) -> Mapping[str, Any]:
    return MappingProxyType(
        {
            key: _freeze_mapping(item) if isinstance(item, Mapping) else item
            for key, item in value.items()
        }
    )


def _exact_keys(value: Mapping[str, Any], expected: Mapping[str, Any], field: str) -> None:
    missing = sorted(set(expected) - set(value))
    unknown = sorted(set(value) - set(expected))
    if missing:
        raise ContractFailure(f"{field} is missing required parameter paths: {', '.join(missing)}.")
    if unknown:
        raise ContractFailure(f"{field} contains unknown parameter paths: {', '.join(unknown)}.")


def _validate_value(value: Any, spec: _FieldSpec, field: str) -> Any:
    if value is None:
        if spec.nullable:
            return None
        raise ContractFailure(f"{field} must not be null.")
    if spec.kind == _BOOLEAN:
        if not isinstance(value, bool):
            raise ContractFailure(f"{field} must be a boolean.")
        if spec.allowed is not None and value not in spec.allowed:
            raise ContractFailure(f"{field} has an unsupported value.")
        return value
    if spec.kind == _STRING:
        if not isinstance(value, str) or not value:
            raise ContractFailure(f"{field} must be a non-empty string.")
        if spec.allowed is not None and value not in spec.allowed:
            raise ContractFailure(f"{field} has an unsupported value.")
        return value
    if spec.kind == _INTEGER:
        return _integer(value, field, minimum=spec.minimum, maximum=spec.maximum)
    if spec.kind == _NUMBER:
        return _number(
            value,
            field,
            minimum=spec.minimum,
            maximum=spec.maximum,
            exclusive_minimum=spec.exclusive_minimum,
        )
    raise AssertionError(f"Unsupported parameter spec kind: {spec.kind}")


def _object(value: Any, field: str) -> Mapping[str, Any]:
    if not isinstance(value, Mapping):
        raise ContractFailure(f"{field} must be an object for simulation preparation.")
    return value


def _integer(value: Any, field: str, *, minimum: float | None = None, maximum: float | None = None) -> int:
    if not isinstance(value, int) or isinstance(value, bool):
        raise ContractFailure(f"{field} must be an integer.")
    if minimum is not None and value < minimum:
        raise ContractFailure(f"{field} must be greater than or equal to {minimum}.")
    if maximum is not None and value > maximum:
        raise ContractFailure(f"{field} must be less than or equal to {maximum}.")
    return value


def _number(
    value: Any,
    field: str,
    *,
    minimum: float | None = None,
    maximum: float | None = None,
    exclusive_minimum: bool = False,
) -> float:
    if not isinstance(value, (int, float)) or isinstance(value, bool) or not math.isfinite(float(value)):
        raise ContractFailure(f"{field} must be a finite number.")
    number = float(value)
    if minimum is not None and (number <= minimum if exclusive_minimum else number < minimum):
        comparison = "greater than" if exclusive_minimum else "greater than or equal to"
        raise ContractFailure(f"{field} must be {comparison} {minimum}.")
    if maximum is not None and number > maximum:
        raise ContractFailure(f"{field} must be less than or equal to {maximum}.")
    return number
