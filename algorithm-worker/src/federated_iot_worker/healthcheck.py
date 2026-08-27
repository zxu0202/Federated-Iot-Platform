"""Non-network liveness check for the single M1 Algorithm Worker process."""

from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Sequence

from .runner import HEARTBEAT_SCHEMA_VERSION, RuntimeConfigurationError, default_heartbeat_file, validate_runtime_configuration


def main(argv: Sequence[str] | None = None) -> int:
    """Validate the configured single Worker host and its recent local heartbeat."""

    parser = argparse.ArgumentParser(description="Check Algorithm Worker liveness without network access.")
    parser.add_argument("--config", required=True, type=Path, help="Read-only platform configuration path.")
    parser.add_argument(
        "--max-heartbeat-age-seconds",
        required=True,
        type=float,
        help="Maximum accepted age for the Worker heartbeat.",
    )
    parser.add_argument(
        "--heartbeat-file",
        type=Path,
        default=default_heartbeat_file(),
        help="Override the local heartbeat file for a controlled test environment.",
    )
    arguments = parser.parse_args(argv)
    if arguments.max_heartbeat_age_seconds <= 0:
        parser.error("--max-heartbeat-age-seconds must be positive")

    try:
        validate_runtime_configuration(arguments.config)
        heartbeat = _read_heartbeat(arguments.heartbeat_file)
        age_seconds = _heartbeat_age_seconds(heartbeat)
        if age_seconds > arguments.max_heartbeat_age_seconds:
            raise HealthcheckFailure("Worker heartbeat is stale.")
    except (HealthcheckFailure, RuntimeConfigurationError) as error:
        print(f"worker healthcheck failed: {error}", file=sys.stderr)
        return 1

    print(json.dumps({"status": "ok", "heartbeat_age_seconds": round(age_seconds, 3)}, sort_keys=True))
    return 0


class HealthcheckFailure(Exception):
    """A stable non-network healthcheck failure."""


def _read_heartbeat(path: Path) -> dict[str, object]:
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise HealthcheckFailure("Worker heartbeat file is unavailable.") from error
    if not isinstance(payload, dict) or payload.get("schema_version") != HEARTBEAT_SCHEMA_VERSION:
        raise HealthcheckFailure("Worker heartbeat has an unsupported schema.")
    if not isinstance(payload.get("updated_at"), str):
        raise HealthcheckFailure("Worker heartbeat is missing updated_at.")
    return payload


def _heartbeat_age_seconds(heartbeat: dict[str, object]) -> float:
    timestamp = heartbeat["updated_at"]
    assert isinstance(timestamp, str)
    try:
        updated_at = datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    except ValueError as error:
        raise HealthcheckFailure("Worker heartbeat timestamp is invalid.") from error
    if updated_at.tzinfo is None:
        raise HealthcheckFailure("Worker heartbeat timestamp has no timezone.")
    age_seconds = (datetime.now(timezone.utc) - updated_at.astimezone(timezone.utc)).total_seconds()
    if age_seconds < -1:
        raise HealthcheckFailure("Worker heartbeat timestamp is in the future.")
    return max(age_seconds, 0.0)


if __name__ == "__main__":
    raise SystemExit(main())
