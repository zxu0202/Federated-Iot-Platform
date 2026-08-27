"""Atomic artifact staging and semantic validation for ``artifact.manifest.v1``."""

from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path, PurePosixPath
from typing import Any, Mapping

from .contracts import ARTIFACT_MANIFEST_VERSION, POINT_RESULT_VERSION
from .errors import ContractFailure, WorkerFailure


REQUIRED_ARTIFACT_NAMES = (
    "run_manifest.json",
    "preprocessing_summary.json",
    "agent_partition_summary.csv",
    "feature_schema.json",
    "anchor_summary.json",
    "metrics.csv",
    "results_agent_1.csv",
    "results_agent_2.csv",
    "results_agent_3.csv",
    "alarms.csv",
    "diagnostics.json",
)
_ATTEMPT_DIRECTORY_MODE = 0o770
_ARTIFACT_FILE_MODE = 0o640
_SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]*$")
_MEDIA_TYPE_RE = re.compile(r"^[A-Za-z0-9!#$&^_.+-]+/[A-Za-z0-9!#$&^_.+-]+(?:;[A-Za-z0-9!#$&^_.+-]+=[A-Za-z0-9!#$&^_.+-]+)*$")
_CREATED_AT_RE = re.compile(r"^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}(Z|[+-][0-9]{2}:[0-9]{2})$")


@dataclass(frozen=True)
class ArtifactItem:
    """A committed artifact inventory entry."""

    name: str
    media_type: str
    size_bytes: int
    sha256: str
    required: bool

    def to_dict(self) -> dict[str, Any]:
        """Return the manifest representation with its stable field names."""

        return {
            "name": self.name,
            "media_type": self.media_type,
            "size_bytes": self.size_bytes,
            "sha256": self.sha256,
            "required": self.required,
        }


def validate_artifact_manifest(manifest: Mapping[str, Any], committed_root: Path | None = None) -> tuple[ArtifactItem, ...]:
    """Reject malformed manifests and, optionally, verify their committed files.

    JSON Schema enforces shape; this validator owns invariants that depend on
    sibling entries and filesystem state. ``committed_root`` is intentionally
    optional so consumers can validate an envelope before its atomic publish.
    """

    if not isinstance(manifest, Mapping):
        raise ContractFailure("artifact.manifest.v1 must be an object.")
    required = {"schema_version", "run_id", "run_mode", "snapshot_sha256", "result_schema_version", "created_at", "items"}
    _require_exact_keys(manifest, required, required, "artifact.manifest.v1")
    if manifest["schema_version"] != ARTIFACT_MANIFEST_VERSION:
        raise ContractFailure("Unsupported artifact manifest schema version.")
    _opaque_id(manifest["run_id"], "artifact.manifest.v1.run_id")
    if manifest["run_mode"] not in {"REFERENCE", "CUSTOM"}:
        raise ContractFailure("artifact.manifest.v1.run_mode must be REFERENCE or CUSTOM.")
    _sha256(manifest["snapshot_sha256"], "artifact.manifest.v1.snapshot_sha256")
    if manifest["result_schema_version"] != POINT_RESULT_VERSION:
        raise ContractFailure("artifact.manifest.v1.result_schema_version must be point-result.v1.")
    if not isinstance(manifest["created_at"], str) or not _CREATED_AT_RE.fullmatch(manifest["created_at"]):
        raise ContractFailure("artifact.manifest.v1.created_at must be an ISO-8601 timestamp with milliseconds.")
    if not isinstance(manifest["items"], list):
        raise ContractFailure("artifact.manifest.v1.items must be an array.")

    items = tuple(_parse_item(item, index) for index, item in enumerate(manifest["items"]))
    names = [item.name for item in items]
    if len(names) != len(set(names)):
        raise ContractFailure("artifact.manifest.v1 item names must be unique.")
    if "artifact_manifest.json" in names:
        raise ContractFailure("artifact.manifest.v1 must not list artifact_manifest.json itself.")
    by_name = {item.name: item for item in items}
    absent = [name for name in REQUIRED_ARTIFACT_NAMES if name not in by_name or not by_name[name].required]
    if absent:
        raise ContractFailure(f"artifact.manifest.v1 is missing required artifacts: {', '.join(absent)}.")
    if committed_root is not None:
        _verify_committed_files(items, committed_root)
    return items


class AtomicArtifactWriter:
    """Stage one private attempt tree, verify it, and publish it by one rename.

    The writer explicitly sets directory mode ``0770`` and artifact mode
    ``0640`` after creation so the leased tree remains private regardless of
    the process umask. It never advertises an artifact manifest until all
    required files have been written and validated.
    """

    def __init__(self, tmp_directory: Path, committed_directory: Path) -> None:
        self._tmp_directory = tmp_directory
        self._committed_directory = committed_directory
        self._items: dict[str, ArtifactItem] = {}
        self._manifest_payload: dict[str, Any] | None = None
        self._closed = False
        self._tmp_directory.mkdir(parents=True, exist_ok=False, mode=_ATTEMPT_DIRECTORY_MODE)
        # The container's worker UID and platform primary GID own this tree.
        # Explicit chmod prevents a permissive or restrictive process umask
        # from changing the leased attempt boundary.
        os.chmod(self._tmp_directory, _ATTEMPT_DIRECTORY_MODE)

    def write_json(self, name: str, payload: Mapping[str, Any], *, required: bool = True) -> ArtifactItem:
        return self.write_bytes(
            name,
            (json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n").encode("utf-8"),
            media_type="application/json",
            required=required,
        )

    def write_bytes(self, name: str, data: bytes, *, media_type: str, required: bool = True) -> ArtifactItem:
        if self._closed:
            raise WorkerFailure("ARTIFACT_WRITE_FAILED", "Artifact writer is already closed.", "GENERATING_ARTIFACTS")
        path = self._safe_target(name)
        path.parent.mkdir(parents=True, exist_ok=True, mode=_ATTEMPT_DIRECTORY_MODE)
        os.chmod(path.parent, _ATTEMPT_DIRECTORY_MODE)
        temp_path = _reserve_temp_path(path)
        file_descriptor: int | None = None
        temp_created = False
        try:
            file_descriptor = os.open(temp_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, _ARTIFACT_FILE_MODE)
            temp_created = True
            if hasattr(os, "fchmod"):
                os.fchmod(file_descriptor, _ARTIFACT_FILE_MODE)
            else:
                os.chmod(temp_path, _ARTIFACT_FILE_MODE)
            with os.fdopen(file_descriptor, "wb") as handle:
                file_descriptor = None
                handle.write(data)
                handle.flush()
                # See the matching preflight writer: Windows durability is a
                # volume-policy concern because fsync may block indefinitely.
                if os.name != "nt":
                    os.fsync(handle.fileno())
            os.replace(temp_path, path)
            os.chmod(path, _ARTIFACT_FILE_MODE)
        except Exception as error:
            if file_descriptor is not None:
                os.close(file_descriptor)
            try:
                if temp_created:
                    temp_path.unlink()
            except FileNotFoundError:
                pass
            raise WorkerFailure("ARTIFACT_WRITE_FAILED", f"Could not write artifact {name}.", "GENERATING_ARTIFACTS") from error
        item = ArtifactItem(name, media_type, path.stat().st_size, _sha256_file(path), required)
        self._items[name] = item
        return item

    def build_manifest(self, *, run_id: str, run_mode: str, snapshot_sha256: str) -> dict[str, Any]:
        """Write a self-excluded, semantically valid manifest in the temp tree."""

        if "artifact_manifest.json" in self._items:
            raise WorkerFailure("ARTIFACT_WRITE_FAILED", "Artifact manifest was already written.", "GENERATING_ARTIFACTS")
        payload = {
            "schema_version": ARTIFACT_MANIFEST_VERSION,
            "run_id": run_id,
            "run_mode": run_mode,
            "snapshot_sha256": snapshot_sha256,
            "result_schema_version": POINT_RESULT_VERSION,
            "created_at": datetime.now(timezone.utc).isoformat(timespec="milliseconds"),
            "items": [item.to_dict() for item in sorted(self._items.values(), key=lambda entry: entry.name)],
        }
        try:
            validate_artifact_manifest(payload, self._tmp_directory)
        except ContractFailure as error:
            raise WorkerFailure("ARTIFACT_WRITE_FAILED", error.message, "GENERATING_ARTIFACTS") from error
        self.write_json("artifact_manifest.json", payload, required=True)
        self._manifest_payload = payload
        return payload

    def commit(self) -> tuple[ArtifactItem, ...]:
        """Verify staged files and atomically publish the directory once."""

        if self._closed:
            raise WorkerFailure("ARTIFACT_WRITE_FAILED", "Artifact writer is already closed.", "GENERATING_ARTIFACTS")
        if "artifact_manifest.json" not in self._items or self._manifest_payload is None:
            raise WorkerFailure("ARTIFACT_WRITE_FAILED", "Artifact manifest is required before commit.", "GENERATING_ARTIFACTS")
        if self._committed_directory.exists():
            raise WorkerFailure("ARTIFACT_WRITE_FAILED", "Committed artifact directory already exists.", "GENERATING_ARTIFACTS")
        try:
            validate_artifact_manifest(self._manifest_payload, self._tmp_directory)
        except ContractFailure as error:
            raise WorkerFailure("ARTIFACT_WRITE_FAILED", error.message, "GENERATING_ARTIFACTS") from error
        self._committed_directory.parent.mkdir(parents=True, exist_ok=True)
        try:
            os.replace(self._tmp_directory, self._committed_directory)
        except OSError as error:
            raise WorkerFailure("ARTIFACT_WRITE_FAILED", "Artifact directory rename was not atomic or failed.", "GENERATING_ARTIFACTS") from error
        self._closed = True
        return tuple(sorted(self._items.values(), key=lambda entry: entry.name))

    def abandon(self) -> None:
        """Leave no partial artifact directory advertised as committed."""

        if self._closed:
            return
        if self._tmp_directory.exists():
            shutil.rmtree(self._tmp_directory)
        self._closed = True

    def _safe_target(self, name: str) -> Path:
        try:
            _safe_logical_path(name, "artifact name")
        except ContractFailure as error:
            raise WorkerFailure("ARTIFACT_WRITE_FAILED", error.message, "GENERATING_ARTIFACTS") from error
        return self._tmp_directory.joinpath(*PurePosixPath(name).parts)


def _parse_item(value: Any, index: int) -> ArtifactItem:
    field = f"artifact.manifest.v1.items[{index}]"
    if not isinstance(value, Mapping):
        raise ContractFailure(f"{field} must be an object.")
    required = {"name", "media_type", "size_bytes", "sha256", "required"}
    _require_exact_keys(value, required, required, field)
    name = _safe_logical_path(value["name"], f"{field}.name")
    media_type = value["media_type"]
    if not isinstance(media_type, str) or not _MEDIA_TYPE_RE.fullmatch(media_type):
        raise ContractFailure(f"{field}.media_type must be a valid media type.")
    size = value["size_bytes"]
    if not isinstance(size, int) or isinstance(size, bool) or size < 0:
        raise ContractFailure(f"{field}.size_bytes must be a non-negative integer.")
    digest = _sha256(value["sha256"], f"{field}.sha256")
    if not isinstance(value["required"], bool):
        raise ContractFailure(f"{field}.required must be boolean.")
    return ArtifactItem(name=name, media_type=media_type, size_bytes=size, sha256=digest, required=value["required"])


def _verify_committed_files(items: tuple[ArtifactItem, ...], committed_root: Path) -> None:
    root = committed_root.resolve()
    if not root.is_dir():
        raise ContractFailure("artifact committed root does not exist or is not a directory.")
    for item in items:
        candidate = root.joinpath(*PurePosixPath(item.name).parts)
        try:
            resolved = candidate.resolve(strict=True)
            resolved.relative_to(root)
        except (OSError, ValueError) as error:
            raise ContractFailure(f"artifact {item.name} escaped the committed root.") from error
        if candidate.is_symlink() or not resolved.is_file():
            raise ContractFailure(f"artifact {item.name} is not a regular committed file.")
        if resolved.stat().st_size != item.size_bytes:
            raise ContractFailure(f"artifact {item.name} size does not match the manifest.")
        if _sha256_file(resolved) != item.sha256:
            raise ContractFailure(f"artifact {item.name} SHA-256 does not match the manifest.")


def _safe_logical_path(value: Any, field: str) -> str:
    if not isinstance(value, str) or not value or len(value) > 512 or "\\" in value or "//" in value or value.endswith("/"):
        raise ContractFailure(f"{field} is not a normalized safe relative path.")
    path = PurePosixPath(value)
    if path.is_absolute() or any(part in {".", ".."} for part in path.parts) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._/-]*", value):
        raise ContractFailure(f"{field} is not a safe relative path.")
    return path.as_posix()


def _require_exact_keys(value: Mapping[str, Any], allowed: set[str], required: set[str], field: str) -> None:
    missing = sorted(required - set(value))
    unknown = sorted(set(value) - allowed)
    if missing:
        raise ContractFailure(f"{field} is missing required fields: {', '.join(missing)}.")
    if unknown:
        raise ContractFailure(f"{field} contains forbidden fields: {', '.join(unknown)}.")


def _opaque_id(value: Any, field: str) -> str:
    if not isinstance(value, str) or not 1 <= len(value) <= 128 or not _ID_RE.fullmatch(value):
        raise ContractFailure(f"{field} must be a safe opaque identifier.")
    return value


def _sha256(value: Any, field: str) -> str:
    if not isinstance(value, str) or not _SHA256_RE.fullmatch(value):
        raise ContractFailure(f"{field} must be a 64-character lowercase SHA-256 string.")
    return value


def _reserve_temp_path(destination: Path) -> Path:
    for sequence in range(100):
        candidate = destination.with_name(f"{destination.name}.writing.{os.getpid()}.{sequence}")
        if not candidate.exists():
            return candidate
    raise WorkerFailure("ARTIFACT_WRITE_FAILED", "Could not reserve a local temporary file name.", "GENERATING_ARTIFACTS")


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()
