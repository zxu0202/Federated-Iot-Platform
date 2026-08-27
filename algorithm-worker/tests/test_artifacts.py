from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from federated_iot_worker.artifacts import AtomicArtifactWriter


class ArtifactTests(unittest.TestCase):
    def test_commit_requires_manifest_and_uses_one_directory_rename(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            writer = AtomicArtifactWriter(root / "tmp", root / "committed")
            writer.write_json("run_manifest.json", {"schema_version": "run-manifest.v1"})
            writer.write_json("preprocessing_summary.json", {"status": "ok"})
            writer.write_bytes("agent_partition_summary.csv", b"agent,count\n1,1\n", media_type="text/csv")
            writer.write_json("feature_schema.json", {"shape": [1, 64]})
            writer.write_json("anchor_summary.json", {"count": 1})
            writer.write_bytes("metrics.csv", b"agent,rmse\n1,0.0\n", media_type="text/csv")
            writer.write_bytes("results_agent_1.csv", b"index,value\n1,1.0\n", media_type="text/csv")
            writer.write_bytes("results_agent_2.csv", b"index,value\n1,2.0\n", media_type="text/csv")
            writer.write_bytes("results_agent_3.csv", b"index,value\n1,3.0\n", media_type="text/csv")
            writer.write_bytes("alarms.csv", b"index,level\n", media_type="text/csv")
            writer.write_json("diagnostics.json", {"status": "ok"})
            manifest = writer.build_manifest(run_id="run_1", run_mode="REFERENCE", snapshot_sha256="a" * 64)
            self.assertTrue((root / "tmp" / "artifact_manifest.json").is_file())
            self.assertFalse((root / "committed").exists())
            items = writer.commit()
            self.assertFalse((root / "tmp").exists())
            self.assertTrue((root / "committed" / "artifact_manifest.json").is_file())
            self.assertNotIn("artifact_manifest.json", [item["name"] for item in manifest["items"]])
            self.assertEqual(
                [item.name for item in items],
                [
                    "agent_partition_summary.csv",
                    "alarms.csv",
                    "anchor_summary.json",
                    "artifact_manifest.json",
                    "diagnostics.json",
                    "feature_schema.json",
                    "metrics.csv",
                    "preprocessing_summary.json",
                    "results_agent_1.csv",
                    "results_agent_2.csv",
                    "results_agent_3.csv",
                    "run_manifest.json",
                ],
            )
            persisted = json.loads((root / "committed" / "artifact_manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(persisted["schema_version"], "artifact.manifest.v1")
            if os.name != "nt":
                self.assertEqual((root / "committed").stat().st_mode & 0o777, 0o770)
                self.assertEqual((root / "committed" / "artifact_manifest.json").stat().st_mode & 0o777, 0o640)


if __name__ == "__main__":
    unittest.main()
