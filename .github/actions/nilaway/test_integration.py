"""Run the action wrapper with the pinned NilAway CLI."""

import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ACTION = Path(__file__).resolve().parent
WORKSPACE = ACTION.parents[2]
FIXTURES = (ACTION / "testdata").relative_to(WORKSPACE)
MISSING_VERSION = "v0.0.0-nilaway-action-smoke-version-does-not-exist"
EXPERIMENTAL_FLAGS = {
    "experimental-anonymous-function": 14,
    "experimental-struct-init": 8,
    "experimental-struct-init-v2": 8,
}


class IntegrationTest(unittest.TestCase):
    def run_wrapper(self, fixture, *, fail_on_findings, version="", go_env=None, experimental=None):
        with tempfile.TemporaryDirectory() as temp:
            runner_temp = Path(temp).resolve()
            output = runner_temp / "output"
            env = {
                **os.environ,
                "GITHUB_WORKSPACE": str(WORKSPACE),
                "RUNNER_TEMP": str(runner_temp),
                "GITHUB_OUTPUT": str(output),
                "GITHUB_STEP_SUMMARY": str(runner_temp / "summary"),
                "NILAWAY_WORKING_DIRECTORY": str(FIXTURES / fixture),
                "NILAWAY_FAIL_ON_FINDINGS": fail_on_findings,
                "NILAWAY_VERSION": version,
            }
            for name in EXPERIMENTAL_FLAGS:
                key = "NILAWAY_" + name.upper().replace("-", "_")
                env[key] = "true" if name == experimental else "false"

            if go_env is not None:
                env.update(go_env)

            result = subprocess.run(
                [sys.executable, "-I", str(ACTION / "run.py")],
                cwd=WORKSPACE, env=env, capture_output=True, text=True, check=False,
            )
            return result, output.read_text(encoding="utf-8") if output.exists() else ""

    def test_nested_module_annotation_points_to_checkout(self):
        result, output = self.run_wrapper("cross-package", fail_on_findings="false")
        log = result.stdout + result.stderr
        self.assertEqual(result.returncode, 0, log)
        self.assertEqual(output, "finding-count=1\n", log)
        path = (FIXTURES / "cross-package/consumer/consumer.go").as_posix()
        self.assertTrue(
            result.stdout.startswith(f"::warning title=NilAway,file={path},line=5,col=28::"),
            log,
        )

    def test_experimental_checks_find_issues_only_when_enabled(self):
        result, output = self.run_wrapper("experimental", fail_on_findings="false")
        log = result.stdout + result.stderr
        self.assertEqual(result.returncode, 0, log)
        self.assertEqual(output, "finding-count=0\n", log)
        self.assertEqual(result.stdout, "", log)

        fixture = (FIXTURES / "experimental/experimental.go").as_posix()
        config_warning = "::warning title=Experimental NilAway checks enabled::"
        for flag, line in EXPERIMENTAL_FLAGS.items():
            with self.subTest(flag=flag):
                result, output = self.run_wrapper(
                    "experimental", fail_on_findings="false", experimental=flag,
                )
                log = result.stdout + result.stderr
                self.assertEqual(result.returncode, 0, log)
                self.assertEqual(output, "finding-count=1\n", log)
                self.assertEqual(result.stdout.count(config_warning), 1, log)
                self.assertTrue(result.stdout.splitlines()[0].endswith(f"%0A{flag}"), log)
                self.assertIn(f"::warning title=NilAway,file={fixture},line={line},", result.stdout, log)

    def test_broken_module_fails_during_scan_in_both_modes(self):
        for mode in ("true", "false"):
            with self.subTest(fail_on_findings=mode):
                result, output = self.run_wrapper("broken", fail_on_findings=mode)
                log = result.stdout + result.stderr
                self.assertNotEqual(result.returncode, 0, log)
                self.assertEqual(output, "", log)
                self.assertIn("::error title=NilAway action failed::[Scan] ", result.stdout)
                self.assertIn("broken.go:3:", result.stdout)

    def test_unavailable_version_fails_during_install(self):
        # An empty file proxy makes the version lookup miss without using the network.
        with tempfile.TemporaryDirectory() as proxy:
            result, output = self.run_wrapper(
                "clean", fail_on_findings="false", version=MISSING_VERSION,
                go_env={"GOPROXY": Path(proxy).resolve().as_uri(), "GONOPROXY": "none"},
            )

        log = result.stdout + result.stderr
        self.assertNotEqual(result.returncode, 0, log)
        self.assertEqual(output, "", log)
        self.assertIn("::error title=NilAway action failed::[Install] ", result.stdout)
        self.assertIn(MISSING_VERSION, result.stdout)


if __name__ == "__main__":
    unittest.main()
