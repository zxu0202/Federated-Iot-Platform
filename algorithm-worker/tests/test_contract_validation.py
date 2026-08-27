from __future__ import annotations

import copy
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
CONTRACT_ROOT = ROOT.parent / "contracts" / "worker"
sys.path.insert(0, str(ROOT / "src"))

from federated_iot_worker.artifacts import AtomicArtifactWriter, validate_artifact_manifest
from federated_iot_worker.contracts import parse_worker_task
from federated_iot_worker.errors import ContractFailure, WorkerFailure


def _read_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def _apply_task_case(task: dict[str, Any], case: dict[str, Any]) -> dict[str, Any]:
    mutated = copy.deepcopy(task)
    path = case["path"]
    cursor: Any = mutated
    for component in path[:-1]:
        cursor = cursor[component]
    if case["operation"] == "remove":
        cursor.pop(path[-1])
    else:
        cursor[path[-1]] = case["value"]
    return mutated


def _complete_artifact_writer(root: Path) -> AtomicArtifactWriter:
    """Create the exact pre-manifest required artifact set for filesystem checks."""

    writer = AtomicArtifactWriter(root / "tmp", root / "committed")
    writer.write_json("run_manifest.json", {"schema_version": "run-manifest.v1"})
    writer.write_json("preprocessing_summary.json", {"status": "ok"})
    writer.write_bytes("agent_partition_summary.csv", b"agent,count\n1,1\n", media_type="text/csv")
    writer.write_json("feature_schema.json", {"shape": [1, 64]})
    writer.write_json("anchor_summary.json", {"count": 1})
    writer.write_bytes("metrics.csv", b"agent,rmse\n1,0.0\n", media_type="text/csv")
    for agent in range(1, 4):
        writer.write_bytes(f"results_agent_{agent}.csv", f"index,value\n1,{agent}.0\n".encode(), media_type="text/csv")
    writer.write_bytes("alarms.csv", b"index,level\n", media_type="text/csv")
    writer.write_json("diagnostics.json", {"status": "ok"})
    return writer


class WorkerTaskContractTests(unittest.TestCase):
    def test_two_frozen_branch_fixtures_are_semantically_valid(self) -> None:
        for name in ("preflight-task.v1.json", "simulation-task.v1.json"):
            parsed = parse_worker_task(_read_json(CONTRACT_ROOT / "fixtures" / name))
            self.assertIn(parsed.job_type, {"DATASET_PREFLIGHT", "SIMULATION"})

    def test_worker_negative_fixtures_are_rejected(self) -> None:
        cases = _read_json(CONTRACT_ROOT / "fixtures" / "negative" / "worker-task.v1.cases.json")
        for case in cases:
            with self.subTest(case=case["case"]):
                base = _read_json(CONTRACT_ROOT / "fixtures" / case["base"])
                with self.assertRaises(ContractFailure):
                    parse_worker_task(_apply_task_case(base, case))

    def test_schema_declares_closed_objects_and_branches(self) -> None:
        schema = _read_json(CONTRACT_ROOT / "worker.task.v1.schema.json")
        self.assertEqual(schema["$schema"], "https://json-schema.org/draft/2020-12/schema")
        self.assertFalse(schema["additionalProperties"])
        self.assertEqual(len(schema["oneOf"]), 2)
        self.assertFalse(schema["$defs"]["dataset"]["additionalProperties"])
        self.assertFalse(schema["$defs"]["runtime"]["additionalProperties"])


class ArtifactManifestContractTests(unittest.TestCase):
    def test_valid_manifest_fixture_is_semantically_valid_without_filesystem(self) -> None:
        manifest = _read_json(CONTRACT_ROOT / "fixtures" / "artifact-manifest.v1.json")
        items = validate_artifact_manifest(manifest)
        self.assertEqual(len(items), 11)

    def test_artifact_negative_fixtures_are_rejected(self) -> None:
        base = _read_json(CONTRACT_ROOT / "fixtures" / "artifact-manifest.v1.json")
        cases = _read_json(CONTRACT_ROOT / "fixtures" / "negative" / "artifact-manifest.v1.cases.json")
        for case in cases:
            with self.subTest(case=case["case"]):
                manifest = copy.deepcopy(base)
                operation = case["operation"]
                if operation == "remove_item":
                    manifest["items"] = [item for item in manifest["items"] if item["name"] != case["name"]]
                elif operation == "duplicate_item":
                    item = next(item for item in manifest["items"] if item["name"] == case["name"])
                    manifest["items"].append(copy.deepcopy(item))
                else:
                    cursor: Any = manifest
                    for component in case["path"][:-1]:
                        cursor = cursor[component]
                    cursor[case["path"][-1]] = case["value"]
                with self.assertRaises(ContractFailure):
                    validate_artifact_manifest(manifest)

    def test_validator_detects_file_size_and_hash_changes_after_atomic_publish(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            writer = AtomicArtifactWriter(root / "tmp", root / "committed")
            writer.write_json("run_manifest.json", {"schema_version": "run-manifest.v1"})
            writer.write_json("preprocessing_summary.json", {"status": "ok"})
            writer.write_bytes("agent_partition_summary.csv", b"agent,count\n1,1\n", media_type="text/csv")
            writer.write_json("feature_schema.json", {"shape": [1, 64]})
            writer.write_json("anchor_summary.json", {"count": 1})
            writer.write_bytes("metrics.csv", b"agent,rmse\n1,0.0\n", media_type="text/csv")
            for agent in range(1, 4):
                writer.write_bytes(f"results_agent_{agent}.csv", f"index,value\n1,{agent}.0\n".encode(), media_type="text/csv")
            writer.write_bytes("alarms.csv", b"index,level\n", media_type="text/csv")
            writer.write_json("diagnostics.json", {"status": "ok"})
            manifest = writer.build_manifest(run_id="run_1", run_mode="REFERENCE", snapshot_sha256="a" * 64)
            writer.commit()
            self.assertEqual(len(validate_artifact_manifest(manifest, root / "committed")), 11)
            wrong_size = copy.deepcopy(manifest)
            item = next(item for item in wrong_size["items"] if item["name"] == "results_agent_1.csv")
            item["size_bytes"] += 1
            with self.assertRaises(ContractFailure):
                validate_artifact_manifest(wrong_size, root / "committed")
            result_path = root / "committed" / "results_agent_1.csv"
            result_path.write_bytes(b"x" * result_path.stat().st_size)
            with self.assertRaises(ContractFailure):
                validate_artifact_manifest(manifest, root / "committed")

    def test_validator_rejects_a_non_regular_committed_result_path(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            writer = _complete_artifact_writer(root)
            manifest = writer.build_manifest(run_id="run_1", run_mode="REFERENCE", snapshot_sha256="a" * 64)
            writer.commit()
            result_path = root / "committed" / "results_agent_1.csv"
            result_path.unlink()
            result_path.mkdir()
            with self.assertRaisesRegex(ContractFailure, "not a regular committed file"):
                validate_artifact_manifest(manifest, root / "committed")

    @unittest.skipIf(os.name == "nt", "symlink creation requires a Windows privilege not assumed by the host suite")
    def test_validator_rejects_a_symlinked_committed_result_path(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            writer = _complete_artifact_writer(root)
            manifest = writer.build_manifest(run_id="run_1", run_mode="REFERENCE", snapshot_sha256="a" * 64)
            writer.commit()
            result_path = root / "committed" / "results_agent_1.csv"
            result_path.unlink()
            result_path.symlink_to(root / "committed" / "results_agent_2.csv")
            with self.assertRaisesRegex(ContractFailure, "not a regular committed file"):
                validate_artifact_manifest(manifest, root / "committed")

    def test_writer_refuses_incomplete_required_artifact_set(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            writer = AtomicArtifactWriter(root / "tmp", root / "committed")
            writer.write_json("run_manifest.json", {"schema_version": "run-manifest.v1"})
            with self.assertRaises(WorkerFailure):
                writer.build_manifest(run_id="run_1", run_mode="REFERENCE", snapshot_sha256="a" * 64)

    def test_schema_declares_closed_items_and_required_artifact_contains(self) -> None:
        schema = _read_json(CONTRACT_ROOT / "artifact.manifest.v1.schema.json")
        self.assertEqual(schema["$schema"], "https://json-schema.org/draft/2020-12/schema")
        self.assertFalse(schema["additionalProperties"])
        self.assertFalse(schema["$defs"]["item"]["additionalProperties"])
        self.assertEqual(len(schema["allOf"]), 11)


if __name__ == "__main__":
    unittest.main()
