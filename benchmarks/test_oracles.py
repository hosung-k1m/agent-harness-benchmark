#!/usr/bin/env python3
"""Prove every bundled deterministic benchmark has a passing solution."""

import json
import pathlib
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
VERIFY = ROOT / "docker" / "verifier" / "verify.py"
BENCHMARKS = ROOT / "benchmarks"


class BenchmarkOracleTest(unittest.TestCase):
    def verify_workspace(self, task, workspace, temp, name):
        archive = temp / f"{name}.tar"
        with tarfile.open(archive, "w") as bundle:
            for item in workspace.rglob("*"):
                bundle.add(item, arcname=item.relative_to(workspace), recursive=False)

        result_path = temp / f"{name}.json"
        subprocess.run(
            [sys.executable, str(VERIFY), str(archive), str(task / "hidden"), str(result_path)],
            check=True,
        )
        return json.loads(result_path.read_text())

    def test_every_capsule_has_deterministic_fail_and_pass_results(self):
        tasks = sorted(BENCHMARKS.glob("*/tasks/*"))
        self.assertEqual(len(tasks), 20)

        failures = []
        for task in tasks:
            with self.subTest(task=task.relative_to(BENCHMARKS)):
                hidden = task / "hidden"
                oracle = hidden / "oracle"
                self.assertTrue((hidden / "verifier.json").is_file())
                self.assertTrue(oracle.is_dir())

                with tempfile.TemporaryDirectory(prefix="benchmark-oracle-") as directory:
                    temp = pathlib.Path(directory)
                    workspace = temp / "workspace"
                    shutil.copytree(task / "fixture", workspace)

                    failing_result = self.verify_workspace(task, workspace, temp, "fixture-result")
                    if failing_result.get("status") != "completed" or failing_result.get("verifier_passed") is not False:
                        failures.append((str(task.relative_to(BENCHMARKS)), "fixture must fail", failing_result))

                    shutil.copytree(oracle, workspace, dirs_exist_ok=True)
                    passing_result = self.verify_workspace(task, workspace, temp, "oracle-result")
                    if passing_result.get("status") != "completed" or passing_result.get("verifier_passed") is not True:
                        failures.append((str(task.relative_to(BENCHMARKS)), "oracle must pass", passing_result))

        self.assertEqual(failures, [])


if __name__ == "__main__":
    unittest.main()
