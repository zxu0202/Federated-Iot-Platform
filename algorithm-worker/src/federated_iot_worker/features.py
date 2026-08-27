"""Reference-ordered transition features, chronological splitting, and scaling."""

from __future__ import annotations

import math
import statistics
from dataclasses import dataclass
from typing import Sequence

from .preprocessing import RunningRow, sys_float_epsilon


@dataclass(frozen=True)
class FeatureDataset:
    """A dense, row-major feature matrix with its aligned target values."""

    values: tuple[tuple[float, ...], ...]
    targets: tuple[float, ...]
    trends: tuple[float, ...]
    transition_scores: tuple[float, ...]
    source_rows: tuple[RunningRow, ...]

    @property
    def shape(self) -> tuple[int, int]:
        return (len(self.values), len(self.values[0]) if self.values else 0)


@dataclass(frozen=True)
class ChronologicalSplit:
    """Non-overlapping train, calibration, and test row indices."""

    train: tuple[int, ...]
    calibration: tuple[int, ...]
    test: tuple[int, ...]


@dataclass(frozen=True)
class Standardization:
    """Training-only sample statistics for one AgentContext."""

    means: tuple[float, ...]
    sample_stds: tuple[float, ...]

    def transform(self, matrix: Sequence[Sequence[float]]) -> tuple[tuple[float, ...], ...]:
        return tuple(
            tuple((value - self.means[index]) / self.sample_stds[index] for index, value in enumerate(row))
            for row in matrix
        )


def feature_names(n_lag: int) -> tuple[str, ...]:
    """Return the frozen ordered feature layout without aliases."""

    if n_lag < 5:
        raise ValueError("n_lag must be at least 5.")
    names = ["current_sd", "current_zl"]
    names.extend(f"sd_lag_{index}" for index in range(1, n_lag + 1))
    names.extend(f"zl_lag_{index}" for index in range(1, n_lag + 1))
    names.extend(f"Iavg_lag_{index}" for index in range(1, n_lag + 1))
    names.extend(f"Isum_lag_{index}" for index in range(1, n_lag + 1))
    names.extend(
        (
            "Iimb_last",
            "dIavg_last",
            "dzl_last",
            "dsd_last",
            "dIsum_last",
            "Iavg_slope",
            "Isum_slope",
            "zl_slope",
            "sd_slope",
            "Iavg_acc",
            "Isum_acc",
            "zl_acc",
            "sd_acc",
            "Iavg_maxdiff",
            "Isum_maxdiff",
            "zl_maxdiff",
            "sd_maxdiff",
            "Iavg_std",
            "Isum_std",
            "zl_std",
            "sd_std",
            "Iavg_range",
            "Isum_range",
            "zl_range",
            "sd_range",
            "Iavg_trend",
            "Isum_trend",
            "current_zl_jump",
            "current_sd_jump",
            "transition_score",
        )
    )
    return tuple(names)


def build_transition_dataset(rows: Sequence[RunningRow], n_lag: int, trend_gain: float = 1.0) -> FeatureDataset:
    """Construct the exact `4*nLag+32` feature sequence inside one partition."""

    names = feature_names(n_lag)
    if len(rows) <= n_lag:
        raise ValueError("A partition must contain more rows than n_lag.")
    sd = [row.sd for row in rows]
    zl = [row.zl for row in rows]
    iavg = [row.iavg for row in rows]
    isum = [row.isum for row in rows]
    iimb = [row.iimb for row in rows]
    matrix: list[tuple[float, ...]] = []
    targets: list[float] = []
    trends: list[float] = []
    scores: list[float] = []
    aligned_rows: list[RunningRow] = []
    for current in range(n_lag, len(rows)):
        past_sd = [sd[current - offset] for offset in range(1, n_lag + 1)]
        past_zl = [zl[current - offset] for offset in range(1, n_lag + 1)]
        past_iavg = [iavg[current - offset] for offset in range(1, n_lag + 1)]
        past_isum = [isum[current - offset] for offset in range(1, n_lag + 1)]
        window_iavg = iavg[current - n_lag : current]
        window_isum = isum[current - n_lag : current]
        window_zl = zl[current - n_lag : current]
        window_sd = sd[current - n_lag : current]
        d_iavg = iavg[current - 1] - iavg[current - 2]
        d_isum = isum[current - 1] - isum[current - 2]
        d_zl = zl[current - 1] - zl[current - 2]
        d_sd = sd[current - 1] - sd[current - 2]
        slope_iavg = _window_slope(window_iavg)
        max_diff_iavg = _max_adjacent_difference(window_iavg)
        trend = iavg[current - 1] + trend_gain * d_iavg
        score = abs(d_iavg) + abs(slope_iavg) + max_diff_iavg + 0.5 * _sample_std(window_iavg)
        base = [sd[current], zl[current], *past_sd, *past_zl, *past_iavg, *past_isum, iimb[current - 1], d_iavg, d_zl, d_sd]
        transition = [
            d_isum,
            slope_iavg,
            _window_slope(window_isum),
            _window_slope(window_zl),
            _window_slope(window_sd),
            iavg[current - 1] - 2.0 * iavg[current - 2] + iavg[current - 3],
            isum[current - 1] - 2.0 * isum[current - 2] + isum[current - 3],
            zl[current - 1] - 2.0 * zl[current - 2] + zl[current - 3],
            sd[current - 1] - 2.0 * sd[current - 2] + sd[current - 3],
            max_diff_iavg,
            _max_adjacent_difference(window_isum),
            _max_adjacent_difference(window_zl),
            _max_adjacent_difference(window_sd),
            _sample_std(window_iavg),
            _sample_std(window_isum),
            _sample_std(window_zl),
            _sample_std(window_sd),
            max(window_iavg) - min(window_iavg),
            max(window_isum) - min(window_isum),
            max(window_zl) - min(window_zl),
            max(window_sd) - min(window_sd),
            trend,
            isum[current - 1] + trend_gain * d_isum,
            zl[current] - zl[current - 1],
            sd[current] - sd[current - 1],
            score,
        ]
        row = tuple(base + transition)
        if len(row) != len(names):
            raise AssertionError("Transition feature dimension diverged from the frozen schema.")
        matrix.append(row)
        targets.append(iavg[current])
        trends.append(trend)
        scores.append(score)
        aligned_rows.append(rows[current])
    return FeatureDataset(tuple(matrix), tuple(targets), tuple(trends), tuple(scores), tuple(aligned_rows))


def partition_contiguous_indices(total_rows: int, parts: int, minimum_part_length: int) -> tuple[tuple[int, ...], ...]:
    """Partition by equally spaced endpoints with round-half-away-from-zero semantics."""

    if parts <= 0:
        raise ValueError("parts must be positive.")
    edges = tuple(_round_half_away_from_zero(1.0 + index * total_rows / parts) for index in range(parts + 1))
    partitions = tuple(tuple(range(edges[index] - 1, edges[index + 1] - 1)) for index in range(parts))
    for index, partition in enumerate(partitions, start=1):
        if len(partition) < minimum_part_length:
            raise ValueError(f"Partition {index} has fewer rows than its minimum length.")
    return partitions


def chronological_split(total_samples: int, train_ratio: float = 0.70, calibration_ratio: float = 0.15) -> ChronologicalSplit:
    """Use source-compatible floors and preserve chronological order."""

    if total_samples <= 0 or train_ratio <= 0 or calibration_ratio <= 0 or train_ratio + calibration_ratio >= 1:
        raise ValueError("Invalid chronological split input.")
    train_count = math.floor(train_ratio * total_samples)
    calibration_count = math.floor(calibration_ratio * total_samples)
    return ChronologicalSplit(
        train=tuple(range(train_count)),
        calibration=tuple(range(train_count, train_count + calibration_count)),
        test=tuple(range(train_count + calibration_count, total_samples)),
    )


def training_standardization(matrix: Sequence[Sequence[float]]) -> Standardization:
    """Calculate sample standard deviations on training rows only."""

    if not matrix:
        raise ValueError("Training matrix is empty.")
    width = len(matrix[0])
    if any(len(row) != width for row in matrix):
        raise ValueError("Training matrix is ragged.")
    means = tuple(sum(row[column] for row in matrix) / len(matrix) for column in range(width))
    stds = tuple(
        max(_sample_std([row[column] for row in matrix]), 1.0) if _sample_std([row[column] for row in matrix]) < sys_float_epsilon() else _sample_std([row[column] for row in matrix])
        for column in range(width)
    )
    return Standardization(means, stds)


def _round_half_away_from_zero(value: float) -> int:
    return math.floor(value + 0.5) if value >= 0 else math.ceil(value - 0.5)


def _window_slope(values: Sequence[float]) -> float:
    return (values[-1] - values[0]) / (len(values) - 1)


def _max_adjacent_difference(values: Sequence[float]) -> float:
    return max(abs(values[index] - values[index - 1]) for index in range(1, len(values)))


def _sample_std(values: Sequence[float]) -> float:
    return statistics.stdev(values) if len(values) >= 2 else 0.0
