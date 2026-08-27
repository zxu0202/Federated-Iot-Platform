"""Single preprocessing pipeline shared by preflight and simulation jobs."""

from __future__ import annotations

import csv
import hashlib
import json
import math
import os
import statistics
from dataclasses import asdict, dataclass
from datetime import datetime
from pathlib import Path
from typing import Iterable, Sequence

from .cancellation import CancellationContext, NeverCancelled, check_cancel
from .contracts import PREFLIGHT_SUMMARY_VERSION
from .errors import WorkerFailure


EXPECTED_COLUMNS = ("Time_base", "dzdl_1", "dzdl_2", "dzdl_3", "dzdl_4", "zl", "sd")
PREPROCESSING_CONTRACT_VERSION = "preprocessing.v1"
_PREFLIGHT_ATTEMPT_DIRECTORY_MODE = 0o770
_PREFLIGHT_SUMMARY_FILE_MODE = 0o640


@dataclass(frozen=True)
class PreprocessingConfig:
    """Frozen reference parameter values for the M1 preprocessing surface."""

    contract_version: str = PREPROCESSING_CONTRACT_VERSION
    speed_stop_threshold: float = 0.01
    current_stop_threshold: float = 1.0
    median_window: int = 21
    mad_factor: float = 5.0
    smooth_window: int = 5
    median_filter_path: str = "median_filter_zero_padded_v1"
    smoothing_path: str = "zero_phase_fir_v1"


@dataclass(frozen=True)
class RawRow:
    """One parsed CSV row after numeric conversion and before state removal."""

    original_index: int
    time_raw: str
    i1: float
    i2: float
    i3: float
    i4: float
    zl: float
    sd: float


@dataclass(frozen=True)
class RunningRow:
    """One globally preprocessed running row retaining raw and repaired values."""

    original_running_index: int
    source_row_index: int
    time_raw: str
    i1_raw: float
    i2_raw: float
    i3_raw: float
    i4_raw: float
    zl_raw: float
    sd_raw: float
    iavg_raw: float
    isum_raw: float
    iimb_raw: float
    is_spike_sample: bool
    motor_zero_warning: bool
    spike_reason: str
    i1: float
    i2: float
    i3: float
    i4: float
    zl: float
    sd: float
    iavg: float
    isum: float
    iimb: float


@dataclass(frozen=True)
class PreprocessedDataset:
    """The immutable result of the unique preprocessing pipeline."""

    input_sha256: str
    config: PreprocessingConfig
    running_rows: tuple[RunningRow, ...]
    invalid_rows: tuple[RawRow, ...]
    stop_count: int
    suspicious_count: int
    time_parse_failed_count: int
    time_non_monotonic_count: int
    time_start: str | None
    time_end: str | None
    sampling_period_ms_median: float | None
    sampling_period_ms_min: float | None
    sampling_period_ms_max: float | None
    spike_count: int


@dataclass(frozen=True)
class PreflightSummary:
    """Versioned, JSON-serializable preflight result committed by the Worker."""

    payload: dict[str, object]

    def to_dict(self) -> dict[str, object]:
        return dict(self.payload)

    @property
    def summary_sha256(self) -> str:
        return str(self.payload["summary_sha256"])


class AlgorithmCore:
    """Algorithm Core entry point for deterministic preprocessing and features."""

    def __init__(self, config: PreprocessingConfig | None = None) -> None:
        self._config = config or PreprocessingConfig()

    @property
    def config(self) -> PreprocessingConfig:
        return self._config

    def preprocess_csv(
        self,
        source_path: Path,
        *,
        cancellation: CancellationContext | None = None,
    ) -> PreprocessedDataset:
        """Parse, classify, repair, smooth, and retain source row order."""

        cancellation = cancellation or NeverCancelled()
        check_cancel(cancellation, "PREPROCESSING")
        source_hash = _sha256_file(source_path, cancellation)
        rows, invalid_rows, time_stats = _read_csv(source_path, cancellation)
        check_cancel(cancellation, "PREPROCESSING")

        stop_count = 0
        suspicious_count = 0
        modeling_rows: list[RawRow] = []
        motor_zero_warning: list[bool] = []
        for row in rows:
            all_motor_near_zero = all(value <= self._config.current_stop_threshold for value in (row.i1, row.i2, row.i3, row.i4))
            any_motor_current = any(value > self._config.current_stop_threshold for value in (row.i1, row.i2, row.i3, row.i4))
            is_stop = row.sd <= self._config.speed_stop_threshold and all_motor_near_zero
            is_running = row.sd > self._config.speed_stop_threshold or any_motor_current
            run_without_current = row.sd > self._config.speed_stop_threshold and all_motor_near_zero
            zero_speed_with_current = row.sd <= self._config.speed_stop_threshold and any_motor_current
            if is_stop:
                stop_count += 1
            if run_without_current or zero_speed_with_current:
                suspicious_count += 1
            if is_running and not run_without_current and not zero_speed_with_current:
                modeling_rows.append(row)
                motor_zero_warning.append(
                    row.i1 <= self._config.current_stop_threshold
                    or row.i2 <= self._config.current_stop_threshold
                    or row.i3 <= self._config.current_stop_threshold
                )

        if len(modeling_rows) <= 0:
            raise WorkerFailure("INSUFFICIENT_SAMPLES", "No running rows are available after state filtering.", "PREPROCESSING", recoverable=True)
        check_cancel(cancellation, "PREPROCESSING")

        raw_signals = _derive_signals(modeling_rows)
        repaired: dict[str, list[float]] = {}
        spike_flags: dict[str, list[bool]] = {}
        for signal_name, signal in raw_signals.items():
            check_cancel(cancellation, "PREPROCESSING")
            repaired[signal_name], spike_flags[signal_name] = _despike_and_repair(
                signal, self._config.median_window, self._config.mad_factor, self._config.median_filter_path
            )
        spike_any = [any(spike_flags[name][index] for name in spike_flags) for index in range(len(modeling_rows))]
        spike_reason = [
            _join_reasons(
                _spike_reason_for_signal(name)
                for name in spike_flags
                if spike_flags[name][index]
            )
            for index in range(len(modeling_rows))
        ]
        smoothed = {
            name: _smooth_signal(repaired[name], self._config.smooth_window, self._config.smoothing_path)
            for name in repaired
        }

        running_rows = tuple(
            RunningRow(
                original_running_index=index + 1,
                source_row_index=row.original_index,
                time_raw=row.time_raw,
                i1_raw=row.i1,
                i2_raw=row.i2,
                i3_raw=row.i3,
                i4_raw=row.i4,
                zl_raw=row.zl,
                sd_raw=row.sd,
                iavg_raw=raw_signals["iavg"][index],
                isum_raw=raw_signals["isum"][index],
                iimb_raw=raw_signals["iimb"][index],
                is_spike_sample=spike_any[index],
                motor_zero_warning=motor_zero_warning[index],
                spike_reason=spike_reason[index],
                i1=smoothed["i1"][index],
                i2=smoothed["i2"][index],
                i3=smoothed["i3"][index],
                i4=smoothed["i4"][index],
                zl=smoothed["zl"][index],
                sd=smoothed["sd"][index],
                iavg=smoothed["iavg"][index],
                isum=smoothed["isum"][index],
                iimb=smoothed["iimb"][index],
            )
            for index, row in enumerate(modeling_rows)
        )
        return PreprocessedDataset(
            input_sha256=source_hash,
            config=self._config,
            running_rows=running_rows,
            invalid_rows=tuple(invalid_rows),
            stop_count=stop_count,
            suspicious_count=suspicious_count,
            time_parse_failed_count=time_stats.parse_failed,
            time_non_monotonic_count=time_stats.non_monotonic,
            time_start=time_stats.start,
            time_end=time_stats.end,
            sampling_period_ms_median=time_stats.period_median,
            sampling_period_ms_min=time_stats.period_min,
            sampling_period_ms_max=time_stats.period_max,
            spike_count=sum(spike_any),
        )

    def preflight_summary(self, dataset: PreprocessedDataset) -> PreflightSummary:
        """Create a canonical preflight payload with a self-excluded hash."""

        payload: dict[str, object] = {
            "schema_version": PREFLIGHT_SUMMARY_VERSION,
            "preprocessing_contract_version": dataset.config.contract_version,
            "input_sha256": dataset.input_sha256,
            "counts": {
                "raw_rows": len(dataset.running_rows) + len(dataset.invalid_rows) + dataset.stop_count + dataset.suspicious_count,
                "invalid_numeric_rows": len(dataset.invalid_rows),
                "stop_rows": dataset.stop_count,
                "suspicious_rows": dataset.suspicious_count,
                "running_rows": len(dataset.running_rows),
                "spike_rows": dataset.spike_count,
            },
            "time": {
                "start": dataset.time_start,
                "end": dataset.time_end,
                "parse_failed_count": dataset.time_parse_failed_count,
                "non_monotonic_count": dataset.time_non_monotonic_count,
                "sampling_period_ms": {
                    "median": dataset.sampling_period_ms_median,
                    "min": dataset.sampling_period_ms_min,
                    "max": dataset.sampling_period_ms_max,
                },
            },
            "filter_path": {
                "median": dataset.config.median_filter_path,
                "smoothing": dataset.config.smoothing_path,
            },
            "parameters": asdict(dataset.config),
        }
        payload["summary_sha256"] = _canonical_sha256(payload)
        return PreflightSummary(payload)

    def write_preflight_summary(self, summary: PreflightSummary, destination: Path) -> None:
        """Atomically write a completed preflight summary file."""

        _atomic_write_json(destination, summary.to_dict())


@dataclass(frozen=True)
class _TimeStats:
    parse_failed: int
    non_monotonic: int
    start: str | None
    end: str | None
    period_median: float | None
    period_min: float | None
    period_max: float | None


def _read_csv(path: Path, cancellation: CancellationContext) -> tuple[list[RawRow], list[RawRow], _TimeStats]:
    valid_rows: list[RawRow] = []
    invalid_rows: list[RawRow] = []
    parsed_times: list[datetime] = []
    parse_failed = 0
    non_monotonic = 0
    previous_time: datetime | None = None
    with path.open("r", encoding="utf-8-sig", newline="") as handle:
        reader = csv.reader(handle)
        try:
            header = next(reader)
        except StopIteration as error:
            raise WorkerFailure("INPUT_INVALID", "CSV input is empty.", "PREPROCESSING", recoverable=True) from error
        if tuple(header) != EXPECTED_COLUMNS:
            raise WorkerFailure("INPUT_INVALID", "CSV header must contain the seven frozen columns in order.", "PREPROCESSING", recoverable=True)
        for row_number, fields in enumerate(reader, start=1):
            if row_number % 256 == 0:
                check_cancel(cancellation, "PREPROCESSING")
            if len(fields) != len(EXPECTED_COLUMNS):
                raise WorkerFailure("INPUT_INVALID", f"CSV row {row_number} has an unexpected field count.", "PREPROCESSING", recoverable=True)
            row = RawRow(
                original_index=row_number,
                time_raw=fields[0],
                i1=_to_double(fields[1]),
                i2=_to_double(fields[2]),
                i3=_to_double(fields[3]),
                i4=_to_double(fields[4]),
                zl=_to_double(fields[5]),
                sd=_to_double(fields[6]),
            )
            parsed = _parse_time(row.time_raw)
            if parsed is None:
                parse_failed += 1
            else:
                if previous_time is not None and parsed < previous_time:
                    non_monotonic += 1
                previous_time = parsed
                parsed_times.append(parsed)
            if any(math.isnan(value) for value in (row.i1, row.i2, row.i3, row.i4, row.zl, row.sd)):
                invalid_rows.append(row)
            else:
                valid_rows.append(row)
    if not valid_rows and not invalid_rows:
        raise WorkerFailure("INPUT_INVALID", "CSV does not contain data rows.", "PREPROCESSING", recoverable=True)
    periods = [(parsed_times[index] - parsed_times[index - 1]).total_seconds() * 1000.0 for index in range(1, len(parsed_times))]
    return valid_rows, invalid_rows, _TimeStats(
        parse_failed=parse_failed,
        non_monotonic=non_monotonic,
        start=parsed_times[0].isoformat(timespec="milliseconds") if parsed_times else None,
        end=parsed_times[-1].isoformat(timespec="milliseconds") if parsed_times else None,
        period_median=statistics.median(periods) if periods else None,
        period_min=min(periods) if periods else None,
        period_max=max(periods) if periods else None,
    )


def _derive_signals(rows: Sequence[RawRow]) -> dict[str, list[float]]:
    iavg = [(row.i1 + row.i2 + row.i3) / 3.0 for row in rows]
    isum = [row.i1 + row.i2 + row.i3 for row in rows]
    iimb = [(max(row.i1, row.i2, row.i3) - min(row.i1, row.i2, row.i3)) / (iavg[index] + sys_float_epsilon()) for index, row in enumerate(rows)]
    return {
        "i1": [row.i1 for row in rows],
        "i2": [row.i2 for row in rows],
        "i3": [row.i3 for row in rows],
        "i4": [row.i4 for row in rows],
        "zl": [row.zl for row in rows],
        "sd": [row.sd for row in rows],
        "iavg": iavg,
        "isum": isum,
        "iimb": iimb,
    }


def _despike_and_repair(
    values: Sequence[float], window: int, factor: float, median_filter_path: str
) -> tuple[list[float], list[bool]]:
    source = list(values)
    count = len(source)
    spikes = [False] * count
    if count < window + 5:
        return source, spikes
    if window % 2 == 0:
        window += 1
    baseline = _median_filter(source, window, median_filter_path)
    residual = [source[index] - baseline[index] for index in range(count)]
    residual_median = statistics.median(residual)
    mad = statistics.median(abs(value - residual_median) for value in residual)
    if mad < sys_float_epsilon():
        mad = _sample_std(residual)
    if mad < sys_float_epsilon():
        return source, spikes
    threshold = factor * 1.4826 * mad
    initial = [abs(value) > threshold for value in residual]
    for index, marked in enumerate(initial):
        if marked:
            spikes[index] = True
            if index > 0:
                spikes[index - 1] = True
            if index + 1 < count:
                spikes[index + 1] = True
    if sum(not marked for marked in spikes) < 2 or not any(spikes):
        return source, spikes
    return _linear_repair(source, spikes), spikes


def _moving_median(values: Sequence[float], window: int) -> list[float]:
    half = window // 2
    return [statistics.median(values[max(0, index - half) : min(len(values), index + half + 1)]) for index in range(len(values))]


def _median_filter(values: Sequence[float], window: int, path: str) -> list[float]:
    if path == "moving_median_compat_v1":
        return _moving_median(values, window)
    if path == "median_filter_zero_padded_v1":
        half = window // 2
        padded = [0.0] * half + list(values) + [0.0] * half
        return [statistics.median(padded[index : index + window]) for index in range(len(values))]
    raise WorkerFailure("INPUT_INVALID", f"Unsupported explicit median filter path: {path}", "PREPROCESSING", recoverable=False)


def _linear_repair(values: Sequence[float], spikes: Sequence[bool]) -> list[float]:
    repaired = list(values)
    good = [index for index, marked in enumerate(spikes) if not marked]
    nearest_left: list[int | None] = [None] * len(values)
    nearest_right: list[int | None] = [None] * len(values)
    last_good: int | None = None
    for index, marked in enumerate(spikes):
        nearest_left[index] = last_good
        if not marked:
            last_good = index
    next_good: int | None = None
    for index in range(len(values) - 1, -1, -1):
        nearest_right[index] = next_good
        if not spikes[index]:
            next_good = index
    for index, marked in enumerate(spikes):
        if not marked:
            continue
        left = nearest_left[index]
        right = nearest_right[index]
        if left is not None and right is not None:
            repaired[index] = values[left] + (values[right] - values[left]) * (index - left) / (right - left)
        elif left is None:
            first, second = good[0], good[1]
            repaired[index] = values[first] + (values[second] - values[first]) * (index - first) / (second - first)
        else:
            first, second = good[-2], good[-1]
            repaired[index] = values[second] + (values[second] - values[first]) * (index - second) / (second - first)
    return repaired


def _smooth_signal(values: Sequence[float], window: int, path: str) -> list[float]:
    source = list(values)
    if window <= 1 or len(source) < window + 5:
        return source
    if window % 2 == 0:
        window += 1
    if window < 3:
        window = 3
    if path == "filter_fir_v1":
        return _forward_fir_filter(source, window)
    if path == "zero_phase_fir_v1":
        return _zero_phase_fir(source, window)
    raise WorkerFailure("INPUT_INVALID", f"Unsupported explicit smoothing path: {path}", "PREPROCESSING", recoverable=False)


def _forward_fir_filter(source: Sequence[float], window: int) -> list[float]:
    coefficient = 1.0 / window
    history = [0.0] * window
    output: list[float] = []
    for value in source:
        history.pop()
        history.insert(0, value)
        output.append(sum(history) * coefficient)
    return output


def _zero_phase_fir(source: Sequence[float], window: int) -> list[float]:
    """Apply the frozen moving-average FIR filter in forward and reverse order."""

    edge = 3 * (window - 1)
    if len(source) <= edge:
        raise WorkerFailure("INSUFFICIENT_SAMPLES", "Signal is too short for the frozen zero-phase FIR path.", "PREPROCESSING", recoverable=True)
    coefficient = 1.0 / window
    # This is the odd-reflection extension and steady-state lfilter initial
    # condition used by the frozen FIR forward/backward path.
    extended = (
        [2.0 * source[0] - source[index] for index in range(edge, 0, -1)]
        + list(source)
        + [2.0 * source[-1] - source[index] for index in range(len(source) - 2, len(source) - edge - 2, -1)]
    )
    initial_state = [coefficient * (window - index - 1) for index in range(window - 1)]
    forward = _fir_filter_with_state(extended, coefficient, [state * extended[0] for state in initial_state])
    backward_reversed = _fir_filter_with_state(list(reversed(forward)), coefficient, [state * forward[-1] for state in initial_state])
    return list(reversed(backward_reversed))[edge:-edge]


def _fir_filter_with_state(values: Sequence[float], coefficient: float, state: Sequence[float]) -> list[float]:
    output: list[float] = []
    delay = list(state)
    for value in values:
        filtered = coefficient * value + delay[0]
        output.append(filtered)
        delay = [coefficient * value + delay[index + 1] for index in range(len(delay) - 1)] + [coefficient * value]
    return output


def _to_double(value: str) -> float:
    stripped = value.strip().strip("'\"")
    if not stripped or stripped.upper() == "NULL":
        return math.nan
    try:
        return float(stripped)
    except ValueError:
        return math.nan


def _parse_time(value: str) -> datetime | None:
    cleaned = value.strip()
    if not cleaned:
        return None
    try:
        return datetime.fromisoformat(cleaned.replace("Z", "+00:00"))
    except ValueError:
        for layout in ("%Y-%m-%d %H:%M:%S.%f", "%Y-%m-%d %H:%M:%S", "%d-%b-%Y %H:%M:%S"):
            try:
                return datetime.strptime(cleaned, layout)
            except ValueError:
                continue
    return None


def _sample_std(values: Sequence[float]) -> float:
    return statistics.stdev(values) if len(values) >= 2 else 0.0


def _spike_reason_for_signal(name: str) -> str:
    return {
        "i1": "I1 spike",
        "i2": "I2 spike",
        "i3": "I3 spike",
        "i4": "I4 spike",
        "zl": "Tension spike",
        "sd": "Speed spike",
        "iavg": "Iavg spike",
        "isum": "Isum spike",
        "iimb": "Imbalance spike",
    }[name]


def _join_reasons(reasons: Iterable[str]) -> str:
    return "; ".join(reasons)


def _sha256_file(path: Path, cancellation: CancellationContext | None = None) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
            if cancellation is not None and handle.tell() % (16 * 1024 * 1024) == 0:
                check_cancel(cancellation, "PREPROCESSING")
    return digest.hexdigest()


def _canonical_sha256(payload: dict[str, object]) -> str:
    serialized = json.dumps(payload, ensure_ascii=False, separators=(",", ":"), sort_keys=True, allow_nan=False).encode("utf-8")
    return hashlib.sha256(serialized).hexdigest()


def _atomic_write_json(destination: Path, payload: dict[str, object]) -> None:
    _ensure_preflight_attempt_directory(destination.parent)
    temp_path = _reserve_temp_path(destination)
    file_descriptor: int | None = None
    temp_created = False
    try:
        file_descriptor = os.open(
            temp_path,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL,
            _PREFLIGHT_SUMMARY_FILE_MODE,
        )
        temp_created = True
        if hasattr(os, "fchmod"):
            os.fchmod(file_descriptor, _PREFLIGHT_SUMMARY_FILE_MODE)
        else:
            os.chmod(temp_path, _PREFLIGHT_SUMMARY_FILE_MODE)
        handle = os.fdopen(file_descriptor, "w", encoding="utf-8", newline="\n")
        file_descriptor = None
        with handle:
            json.dump(payload, handle, ensure_ascii=False, indent=2, sort_keys=True)
            handle.write("\n")
            handle.flush()
            # The supported Windows filesystem layer can block indefinitely on
            # fsync for a newly-created local file. Rename is still atomic on
            # the target volume; durable write guarantees are delegated to the
            # platform volume policy and validated by recovery tests.
            if os.name != "nt":
                os.fsync(handle.fileno())
        os.replace(temp_path, destination)
        os.chmod(destination, _PREFLIGHT_SUMMARY_FILE_MODE)
    except Exception:
        if file_descriptor is not None:
            os.close(file_descriptor)
        try:
            if temp_created:
                temp_path.unlink()
        except FileNotFoundError:
            pass
        raise


def _ensure_preflight_attempt_directory(destination: Path) -> None:
    """Create only the task attempt directory with a controlled group mode."""

    destination.mkdir(parents=True, exist_ok=True, mode=_PREFLIGHT_ATTEMPT_DIRECTORY_MODE)
    # mkdir applies the process umask. The container's worker user supplies
    # ownership (UID 10002 and primary platform GID 10000); mode is explicit.
    os.chmod(destination, _PREFLIGHT_ATTEMPT_DIRECTORY_MODE)


def _reserve_temp_path(destination: Path) -> Path:
    """Create a bounded deterministic temp-name sequence without tempfile RNG."""

    for sequence in range(100):
        candidate = destination.with_name(f"{destination.name}.writing.{os.getpid()}.{sequence}")
        if not candidate.exists():
            return candidate
    raise WorkerFailure("ARTIFACT_WRITE_FAILED", "Could not reserve a local temporary file name.", "PREPROCESSING")


def sys_float_epsilon() -> float:
    return 2.220446049250313e-16
