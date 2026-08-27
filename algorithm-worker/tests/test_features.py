from __future__ import annotations

import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from federated_iot_worker.features import (
    build_transition_dataset,
    chronological_split,
    feature_names,
    partition_contiguous_indices,
    training_standardization,
)
from federated_iot_worker.preprocessing import RunningRow


def _rows(count: int) -> list[RunningRow]:
    result = []
    for index in range(count):
        iavg = 10.0 + index
        result.append(
            RunningRow(
                index + 1, index + 1, f"2026-07-01T00:00:{index:02d}", iavg - 1, iavg, iavg + 1, 0.0, 5.0 + index, 2.0 + index,
                iavg, iavg * 3, 0.2, False, False, "", iavg - 1, iavg, iavg + 1, 0.0, 5.0 + index, 2.0 + index,
                iavg, iavg * 3, 0.2,
            )
        )
    return result


class FeatureTests(unittest.TestCase):
    def test_feature_order_and_dimension_are_frozen(self) -> None:
        dataset = build_transition_dataset(_rows(15), n_lag=8)
        self.assertEqual(dataset.shape, (7, 64))
        self.assertEqual(feature_names(8)[0:4], ("current_sd", "current_zl", "sd_lag_1", "sd_lag_2"))
        self.assertEqual(feature_names(8)[-2:], ("current_sd_jump", "transition_score"))
        self.assertEqual(dataset.values[0][0:2], (10.0, 13.0))
        self.assertEqual(dataset.targets[0], 18.0)

    def test_partition_and_splits_match_reference_counts(self) -> None:
        partitions = partition_contiguous_indices(49618, 3, 1)
        self.assertEqual(tuple(len(partition) for partition in partitions), (16539, 16540, 16539))
        self.assertEqual(tuple(len(partition) for partition in partitions), (16539, 16540, 16539))
        first = chronological_split(16531)
        middle = chronological_split(16532)
        self.assertEqual((len(first.train), len(first.calibration), len(first.test)), (11571, 2479, 2481))
        self.assertEqual((len(middle.train), len(middle.calibration), len(middle.test)), (11572, 2479, 2481))

    def test_standardization_uses_training_sample_statistics(self) -> None:
        standardization = training_standardization(((1.0, 2.0), (3.0, 2.0), (5.0, 2.0)))
        self.assertEqual(standardization.means, (3.0, 2.0))
        self.assertEqual(standardization.sample_stds[1], 1.0)
        self.assertAlmostEqual(standardization.sample_stds[0], 2.0)


if __name__ == "__main__":
    unittest.main()
