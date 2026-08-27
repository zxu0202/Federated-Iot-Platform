from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from federated_iot_worker.simulation import LocalGPModel, _ReferenceTwister, _round_half_away_from_zero, local_gp_predict


class FrozenOracleTests(unittest.TestCase):
    """Validate deterministic numeric behavior without an external source dependency."""

    def test_numeric_kernel_checkpoint(self) -> None:
        checkpoint = json.loads((ROOT / "tests" / "fixtures" / "reference_checkpoint.v1.json").read_text(encoding="utf-8"))
        stream = _ReferenceTwister(checkpoint["random_stream"]["seed"])
        self.assertEqual(
            [stream.uniform() for _ in checkpoint["random_stream"]["uniform"]],
            checkpoint["random_stream"]["uniform"],
        )
        self.assertEqual(
            [_round_half_away_from_zero(value) for value in checkpoint["rounding"]["inputs"]],
            checkpoint["rounding"]["outputs"],
        )
        local_gp = checkpoint["local_gp"]
        model = LocalGPModel(
            tuple(tuple(row) for row in local_gp["training"]),
            tuple(local_gp["targets"]),
            local_gp["k_neighbors"],
            local_gp["length_scale"],
            local_gp["signal_scale"],
            local_gp["noise_scale"],
            local_gp["regularization"],
        )
        mean, std, fallbacks = local_gp_predict(model, tuple(tuple(row) for row in local_gp["queries"]))
        self.assertEqual(fallbacks, 0)
        tolerance = checkpoint["tolerance"]
        for actual, expected in zip(mean, local_gp["means"], strict=True):
            self.assertAlmostEqual(actual, expected, delta=tolerance)
        for actual, expected in zip(std, local_gp["stddevs"], strict=True):
            self.assertAlmostEqual(actual, expected, delta=tolerance)


if __name__ == "__main__":
    unittest.main()
