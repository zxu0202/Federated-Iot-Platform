from __future__ import annotations

import json
import os
import stat
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from federated_iot_worker import preprocessing
from federated_iot_worker.preprocessing import AlgorithmCore


class PreprocessingTests(unittest.TestCase):
    def test_preflight_matches_the_frozen_small_fixture(self) -> None:
        fixture_directory = Path(__file__).parent / "fixtures"
        expected = json.loads((fixture_directory / "preprocessing_golden.v1.json").read_text(encoding="utf-8"))
        core = AlgorithmCore()
        dataset = core.preprocess_csv(fixture_directory / "preprocessing_fixture.csv")
        summary = core.preflight_summary(dataset).to_dict()

        self.assertEqual(summary["counts"], expected["expected_counts"])
        self.assertEqual(summary["filter_path"], expected["expected_filter_path"])
        self.assertEqual(len(dataset.running_rows), 35)
        self.assertTrue(dataset.running_rows[19].is_spike_sample)
        self.assertEqual(len(summary["summary_sha256"]), 64)

    def test_preflight_summary_is_atomically_written(self) -> None:
        fixture = Path(__file__).parent / "fixtures" / "preprocessing_fixture.csv"
        with tempfile.TemporaryDirectory() as temporary_directory:
            destination = Path(temporary_directory) / "preflight_summary.json"
            core = AlgorithmCore()
            summary = core.preflight_summary(core.preprocess_csv(fixture))
            core.write_preflight_summary(summary, destination)
            persisted = json.loads(destination.read_text(encoding="utf-8"))
            self.assertEqual(persisted["summary_sha256"], summary.summary_sha256)

    @unittest.skipUnless(os.name == "posix", "POSIX permission bits are enforced by the Linux Worker container.")
    def test_preflight_attempt_and_summary_modes_ignore_the_process_umask(self) -> None:
        fixture = Path(__file__).parent / "fixtures" / "preprocessing_fixture.csv"
        core = AlgorithmCore()
        summary = core.preflight_summary(core.preprocess_csv(fixture))
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            for umask in (0o022, 0o000):
                destination = root / f"attempt_{umask:o}" / "preflight_summary.json"
                original_umask = os.umask(umask)
                try:
                    core.write_preflight_summary(summary, destination)
                finally:
                    os.umask(original_umask)
                self.assertEqual(stat.S_IMODE(destination.parent.stat().st_mode), 0o770)
                self.assertEqual(stat.S_IMODE(destination.stat().st_mode), 0o640)
                self.assertEqual(json.loads(destination.read_text(encoding="utf-8"))["summary_sha256"], summary.summary_sha256)
                self.assertEqual(list(destination.parent.glob("*.writing.*")), [])

    def test_preflight_atomic_write_cleans_only_its_owned_temporary_file(self) -> None:
        fixture = Path(__file__).parent / "fixtures" / "preprocessing_fixture.csv"
        core = AlgorithmCore()
        summary = core.preflight_summary(core.preprocess_csv(fixture))
        with tempfile.TemporaryDirectory() as temporary_directory:
            destination = Path(temporary_directory) / "attempt" / "preflight_summary.json"
            with patch.object(preprocessing.json, "dump", side_effect=OSError("injected write failure")):
                with self.assertRaises(OSError):
                    core.write_preflight_summary(summary, destination)
            self.assertEqual(list(destination.parent.glob("*.writing.*")), [])
            self.assertFalse(destination.exists())

            foreign = destination.with_name(f"{destination.name}.writing.foreign")
            foreign.write_text("foreign owner", encoding="utf-8")
            with patch.object(preprocessing, "_reserve_temp_path", return_value=foreign):
                with self.assertRaises(FileExistsError):
                    core.write_preflight_summary(summary, destination)
            self.assertEqual(foreign.read_text(encoding="utf-8"), "foreign owner")

            observed_temp_modes: list[int] = []

            def fail_rename(temp_path: Path, _destination: Path) -> None:
                if os.name == "posix":
                    observed_temp_modes.append(stat.S_IMODE(Path(temp_path).stat().st_mode))
                raise OSError("injected rename failure")

            with patch.object(preprocessing.os, "replace", side_effect=fail_rename):
                with self.assertRaises(OSError):
                    core.write_preflight_summary(summary, destination)
            if os.name == "posix":
                self.assertEqual(observed_temp_modes, [0o640])
            self.assertEqual(list(destination.parent.glob("*.writing.*")), [foreign])


if __name__ == "__main__":
    unittest.main()
