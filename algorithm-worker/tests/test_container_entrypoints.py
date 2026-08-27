from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import unittest
from hashlib import sha256
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))


def _config_contents() -> str:
    return """config_version: platform-config.v1
database:
  profile: postgres
limits:
  worker_pool_capacity: 1
"""


def _module_environment() -> dict[str, str]:
    environment = dict(os.environ)
    source_path = str(ROOT / "src")
    environment["PYTHONPATH"] = source_path + os.pathsep + environment.get("PYTHONPATH", "")
    return environment


class ContainerEntrypointTests(unittest.TestCase):
    def test_offline_linux_cp312_wheelhouse_matches_the_hash_locked_runtime_set(self) -> None:
        lock = ROOT / "requirements.lock"
        wheelhouse = ROOT / "wheelhouse"
        expected = {
            "numpy-2.1.3-cp312-cp312-manylinux_2_17_x86_64.manylinux2014_x86_64.whl": "2312b2aa89e1f43ecea6da6ea9a810d06aae08321609d8dc0d0eda6d946a541b",
            "psycopg-3.3.4-py3-none-any.whl": "b6bbc25ccf05c8fad3b061d9db2ef0909a555171b84b07f29458a447253d679a",
            "psycopg_binary-3.3.4-cp312-cp312-manylinux2014_x86_64.manylinux_2_17_x86_64.whl": "e7510c37550f91a187e3660a8cc50d4b760f8c3b8b2f89ebc5698cd2c7f2c85d",
            "typing_extensions-4.16.0-py3-none-any.whl": "481caa481374e813c1b176ada14e97f1f67a4539ce9cfeb3f350d78d6370c2e8",
        }
        self.assertEqual(
            lock.read_text(encoding="utf-8").count("--hash=sha256:"),
            len(expected),
        )
        self.assertTrue((wheelhouse / ".gitkeep").is_file())
        for name, expected_hash in expected.items():
            self.assertEqual(sha256((wheelhouse / name).read_bytes()).hexdigest(), expected_hash)
        self.assertEqual(
            (wheelhouse / "SHA256SUMS").read_text(encoding="utf-8").splitlines(),
            [f"{digest}  {name}" for name, digest in expected.items()],
        )

        # The host can be a different OS/Python than the frozen Linux CPython
        # 3.12 image. pip still verifies the exact supplied distribution files
        # offline; the Docker build performs the target-platform installation.
        environment = _module_environment()
        environment["PIP_DISABLE_PIP_VERSION_CHECK"] = "1"
        with tempfile.TemporaryDirectory() as download_directory:
            subprocess.run(
                [
                    sys.executable,
                    "-m",
                    "pip",
                    "download",
                    "--no-index",
                    f"--find-links={wheelhouse}",
                    "--require-hashes",
                    "--no-deps",
                    "--only-binary=:all:",
                    "--platform",
                    "manylinux2014_x86_64",
                    "--implementation",
                    "cp",
                    "--python-version",
                    "3.12",
                    "--abi",
                    "cp312",
                    "--dest",
                    download_directory,
                    "-r",
                    str(lock),
                ],
                check=True,
                timeout=15,
                env=environment,
            )
            self.assertEqual({item.name for item in Path(download_directory).iterdir()}, set(expected))

    def test_runner_module_entry_and_healthcheck_share_an_atomic_heartbeat(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            directory = Path(temporary_directory)
            config = directory / "platform.yaml"
            heartbeat = directory / "worker-heartbeat.json"
            config.write_text(_config_contents(), encoding="utf-8")
            command = [
                sys.executable,
                "-m",
                "federated_iot_worker.runner",
                "--config",
                str(config),
                "--heartbeat-file",
                str(heartbeat),
                "--heartbeat-interval-seconds",
                "0.05",
                "--check-config",
            ]
            runner = subprocess.run(
                command,
                check=True,
                capture_output=True,
                text=True,
                env=_module_environment(),
                timeout=10,
            )
            self.assertNotIn("RuntimeWarning", runner.stderr)
            self.assertTrue(heartbeat.is_file(), "runner module entry did not write a heartbeat")
            healthcheck = subprocess.run(
                [
                    sys.executable,
                    "-m",
                    "federated_iot_worker.healthcheck",
                    "--config",
                    str(config),
                    "--heartbeat-file",
                    str(heartbeat),
                    "--max-heartbeat-age-seconds",
                    "30",
                ],
                check=True,
                capture_output=True,
                text=True,
                env=_module_environment(),
                timeout=10,
            )
            self.assertEqual(json.loads(healthcheck.stdout)["status"], "ok")

    def test_heartbeat_cleanup_does_not_mask_the_stable_startup_failure(self) -> None:
        from federated_iot_worker.runner import RuntimeConfigurationError, write_heartbeat

        heartbeat = Path("/unwritable/worker-heartbeat.json")
        with (
            patch.object(Path, "mkdir"),
            patch.object(Path, "write_text", side_effect=PermissionError("write denied")),
            patch.object(Path, "unlink", side_effect=PermissionError("cleanup denied")),
        ):
            with self.assertRaisesRegex(RuntimeConfigurationError, "heartbeat cannot be written"):
                write_heartbeat(heartbeat)

    def test_healthcheck_rejects_stale_heartbeat(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            directory = Path(temporary_directory)
            config = directory / "platform.yaml"
            heartbeat = directory / "worker-heartbeat.json"
            config.write_text(_config_contents(), encoding="utf-8")
            heartbeat.write_text(
                json.dumps(
                    {
                        "schema_version": "worker.heartbeat.v1",
                        "pid": 1,
                        "updated_at": "2000-01-01T00:00:00.000+00:00",
                    }
                ),
                encoding="utf-8",
            )
            result = subprocess.run(
                [
                    sys.executable,
                    "-m",
                    "federated_iot_worker.healthcheck",
                    "--config",
                    str(config),
                    "--heartbeat-file",
                    str(heartbeat),
                    "--max-heartbeat-age-seconds",
                    "30",
                ],
                capture_output=True,
                text=True,
                env=_module_environment(),
                timeout=10,
            )
            self.assertEqual(result.returncode, 1)
            self.assertIn("stale", result.stderr)

    def test_healthcheck_normalizes_missing_and_invalid_configuration_failures(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            directory = Path(temporary_directory)
            heartbeat = directory / "worker-heartbeat.json"
            invalid_config = directory / "invalid-platform.yaml"
            invalid_config.write_text("config_version: platform-config.v1\n", encoding="utf-8")
            for name, config in {
                "missing": directory / "missing-platform.yaml",
                "invalid": invalid_config,
            }.items():
                with self.subTest(configuration=name):
                    result = subprocess.run(
                        [
                            sys.executable,
                            "-m",
                            "federated_iot_worker.healthcheck",
                            "--config",
                            str(config),
                            "--heartbeat-file",
                            str(heartbeat),
                            "--max-heartbeat-age-seconds",
                            "30",
                        ],
                        capture_output=True,
                        text=True,
                        env=_module_environment(),
                        timeout=10,
                    )
                    self.assertEqual(result.returncode, 1)
                    self.assertIn("worker healthcheck failed:", result.stderr)
                    self.assertNotIn("Traceback", result.stderr)
                    self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
