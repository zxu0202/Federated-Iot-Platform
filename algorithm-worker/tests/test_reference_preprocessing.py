from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from federated_iot_worker.preprocessing import AlgorithmCore


class FrozenPreprocessingRegressionTests(unittest.TestCase):
    def test_local_fixture_counts_and_filter_metadata_are_frozen(self) -> None:
        fixture_directory = ROOT / "tests" / "fixtures"
        expected = json.loads((fixture_directory / "preprocessing_golden.v1.json").read_text(encoding="utf-8"))
        dataset = AlgorithmCore().preprocess_csv(fixture_directory / "preprocessing_fixture.csv")
        self.assertEqual(
            {
                "raw_rows": len(dataset.running_rows) + len(dataset.invalid_rows) + dataset.stop_count + dataset.suspicious_count,
                "invalid_numeric_rows": len(dataset.invalid_rows),
                "stop_rows": dataset.stop_count,
                "suspicious_rows": dataset.suspicious_count,
                "running_rows": len(dataset.running_rows),
                "spike_rows": dataset.spike_count,
            },
            expected["expected_counts"],
        )
        self.assertEqual(
            {
                "median": dataset.config.median_filter_path,
                "smoothing": dataset.config.smoothing_path,
            },
            expected["expected_filter_path"],
        )


if __name__ == "__main__":
    unittest.main()
