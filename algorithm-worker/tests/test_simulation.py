from __future__ import annotations

import math
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from federated_iot_worker import simulation
from federated_iot_worker.simulation import LocalGPModel, _apply_rate_limit, gpoe_aggregate, local_gp_predict


class SimulationPrimitiveTests(unittest.TestCase):
    def test_gpoe_uses_normalized_support_and_positive_variance_floor(self) -> None:
        mean, variance = gpoe_aggregate((10.0, 14.0), (1.0, 4.0), (2.0, 2.0), 1e-8)
        self.assertAlmostEqual(variance, 1.6)
        self.assertAlmostEqual(mean, 10.8)

    def test_pathological_local_kernel_uses_a_finite_fallback(self) -> None:
        model = LocalGPModel(
            ((0.0, 0.0), (0.0, 0.0), (0.0, 0.0)),
            (4.0, 5.0, 6.0),
            3,
            1.0,
            1.0,
            1e-9,
            1e-12,
        )
        means, stds, fallbacks = local_gp_predict(model, ((0.0, 0.0),))
        self.assertGreaterEqual(fallbacks, 1)
        self.assertTrue(math.isfinite(means[0]))
        self.assertGreater(stds[0], 0.0)

    def test_rate_limit_is_sequence_local(self) -> None:
        self.assertEqual(_apply_rate_limit((1.0, 10.0, -10.0), 2.5), (1.0, 3.5, 1.0))

    def test_required_diagnostic_top_n_is_internal_and_always_present(self) -> None:
        context = SimpleNamespace(
            agent=1,
            runtime=SimpleNamespace(local_fallbacks=0, local_support_scale=1.0),
            parameters={"interval": {"coverage_window": 10}},
        )
        rows = tuple(
            {
                "FusedPrediction": 0.0,
                "TrueAverageCurrentSmoothed": float(index),
                "FusedInsideInterval": True,
            }
            for index in range(60)
        )

        diagnostics = simulation._agent_diagnostics(context, rows)
        self.assertEqual(len(diagnostics["top_fused_error_indices"]), 50)
        self.assertEqual(diagnostics["top_fused_error_indices"][:3], [59, 58, 57])

    @unittest.skipUnless(simulation._numpy is not None, "requires the approved Linux CPython 3.12 NumPy runtime")
    def test_numpy_local_gp_matches_the_pure_python_oracle(self) -> None:
        model = LocalGPModel(
            ((0.0, 0.0), (1.0, 0.0), (0.0, 1.0), (1.0, 1.0)),
            (1.0, 2.0, 3.0, 4.0),
            3,
            1.5,
            1.0,
            0.1,
            0.01,
        )
        queries = ((0.2, 0.3), (0.8, 0.6))
        expected = simulation._local_gp_predict_python(model, queries)
        actual = simulation._local_gp_predict_numpy(model, queries)
        self.assertEqual(actual[2], expected[2])
        for actual_values, expected_values in zip(actual[:2], expected[:2]):
            for actual_value, expected_value in zip(actual_values, expected_values):
                self.assertAlmostEqual(actual_value, expected_value, delta=1e-12)

    @unittest.skipUnless(simulation._numpy is not None, "requires the approved Linux CPython 3.12 NumPy runtime")
    def test_numpy_global_gp_and_anchor_selection_match_the_pure_python_oracle(self) -> None:
        points = ((0.0, 0.0), (1.0, 0.0), (0.0, 1.0), (1.0, 1.0), (0.4, 0.7))
        expected_centers = simulation._simple_kmeans_python(points, 3, 5, 2027)
        actual_centers = simulation._simple_kmeans_numpy(points, 3, 5, 2027)
        for actual_row, expected_row in zip(actual_centers, expected_centers):
            for actual_value, expected_value in zip(actual_row, expected_row):
                self.assertAlmostEqual(actual_value, expected_value, delta=1e-12)
        self.assertEqual(
            simulation._farthest_point_subset_numpy(points, 3, 2028),
            simulation._farthest_point_subset_python(points, 3, 2028),
        )

        parameters = {
            "ell": 1.75,
            "minimum_regularization": 1e-4,
            "noise_ratio": 0.25,
            "cholesky_attempts": 5,
        }
        targets = (2.0, 2.4, 2.7, 3.2, 2.8)
        noise = (0.2, 0.25, 0.21, 0.29, 0.22)
        expected_model = simulation._train_surrogate_python(points, targets, noise, parameters, 1e-8)
        actual_model = simulation._train_surrogate_numpy(points, targets, noise, parameters, 1e-8)
        queries = ((0.2, 0.3), (0.9, 0.8))
        expected = simulation._surrogate_predict_python(
            expected_model, queries, cancellation=simulation.NeverCancelled(), agent=1
        )
        actual = simulation._surrogate_predict_numpy(
            actual_model, queries, cancellation=simulation.NeverCancelled(), agent=1
        )
        for actual_values, expected_values in zip(actual, expected):
            for actual_value, expected_value in zip(actual_values, expected_values):
                self.assertAlmostEqual(actual_value, expected_value, delta=1e-11)


if __name__ == "__main__":
    unittest.main()
