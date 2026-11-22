import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import run as action


def encoded(tree):
    return json.dumps(tree).encode("utf-8")


def diagnostic(position="main.go:5:3", message="nil flow"):
    return {"posn": position, "message": message}


class ActionTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.temp = Path(temp.name).resolve()
        self.workspace = self.temp / "workspace"
        self.workspace.mkdir()
        subprocess.run(["git", "init", "-q", str(self.workspace)], check=True)
        self.module = self.workspace / "nested module"
        self.module.mkdir()
        (self.module / "go.mod").write_text("module example.com/project\n", encoding="utf-8")
        (self.module / "main.go").touch()
        self.env = {
            **os.environ,
            "GITHUB_WORKSPACE": str(self.workspace),
            "RUNNER_TEMP": str(self.temp),
            "GITHUB_OUTPUT": str(self.temp / "output"),
            "GITHUB_STEP_SUMMARY": str(self.temp / "summary"),
            "NILAWAY_WORKING_DIRECTORY": self.module.name,
            "NILAWAY_FAIL_ON_FINDINGS": "true",
            "NILAWAY_VERSION": "",
        }

    def test_module_directory(self):
        (self.workspace / "go.mod").touch()
        (self.module / "package").mkdir()
        (self.workspace / "outside").symlink_to(self.temp, target_is_directory=True)
        for directory, expected in [
            (".", self.workspace),
            (self.module.name, self.module),
            (f"{self.module.name}/package", None),  # Do not inherit a parent's go.mod.
            ("..", None),
            (str(self.module), None),
            ("missing", None),
            ("outside", None),
        ]:
            with self.subTest(directory=directory):
                if expected is None:
                    with self.assertRaisesRegex(action.CheckError, "Configuration"):
                        action.module_root(self.workspace, directory, self.env)
                else:
                    self.assertEqual(action.module_root(self.workspace, directory, self.env), expected)

    def test_symlink_go_mod_is_rejected(self):
        gomod = self.module / "go.mod"
        gomod.rename(self.temp / "go.mod")
        gomod.symlink_to(self.temp / "go.mod")
        with self.assertRaisesRegex(action.CheckError, "not a symlink"):
            action.module_root(self.workspace, self.module.name, self.env)

    def test_checkout_must_be_at_workspace_root(self):
        with self.assertRaisesRegex(action.CheckError, "at GITHUB_WORKSPACE"):
            action.module_root(self.temp, str(self.module.relative_to(self.temp)), self.env)
        subprocess.run(["git", "init", "-q", str(self.module)], check=True)
        with self.assertRaisesRegex(action.CheckError, "at GITHUB_WORKSPACE"):
            action.module_root(self.workspace, self.module.name, self.env)

    def test_effective_module(self):
        for replacement, message in [
            ({"GoMod": str(self.temp / "alternate.mod")}, "-modfile is not supported"),
            ({"Main": False}, "-modfile is not supported"),
            ({"Path": ""}, "expected a Go module path"),
            ({"Path": "example.com/project,"}, "expected a Go module path"),
        ]:
            with self.subTest(replacement=replacement):
                info = {"Path": "example.com/project", "Main": True, "GoMod": str(self.module / "go.mod")}
                with patch.object(action, "capture", return_value=encoded({**info, **replacement})):
                    with self.assertRaisesRegex(action.CheckError, message):
                        action.module_path(self.module, self.env)

    def test_capture_keeps_stderr_separate_and_bounds_output(self):
        def capture(script, **kwargs):
            return action.capture(
                [sys.executable, "-c", script], cwd=self.module, env=self.env, stage="Test", **kwargs,
            )

        self.assertEqual(capture("import sys; print('progress', file=sys.stderr); print('{}')"), b"{}\n")
        with self.assertRaisesRegex(action.CheckError, "output exceeds 2 bytes"):
            capture("print('{}')", limit=2)
        with self.assertRaisesRegex(action.CheckError, r"(?s)exit status 3.*dependency error"):
            capture("import sys; print('dependency error', file=sys.stderr); sys.exit(3)")
        with self.assertRaisesRegex(action.CheckError, "could not start"):
            action.capture(["/nonexistent/nilaway"], cwd=self.module, env=self.env, stage="Test")

    def test_run_policy_and_fixed_arguments(self):
        info = encoded({"Path": "example.com/project", "Main": True, "GoMod": str(self.module / "go.mod")})
        finding_tree = {"pkg": {"nilaway": [diagnostic()]}}
        cases = [
            ("clean", "true", "", {}, 0, "0", None),
            ("strict", "true", "", finding_tree, 1, "1", "::error title=NilAway,file="),
            ("warn", " FALSE ", "v0.1.0", finding_tree, 0, "1", "::warning title=NilAway,file="),
            ("invalid JSON", "false", "", b"\xff", 1, None, "Scan: expected a JSON object"),
            ("prerequisite error", "false", "", {"pkg": {"prereq": {"error": "bad"}}}, 1, None, "Scan: prereq:"),
            ("install error", "false", "v1.2.3", action.CheckError("Install: missing version"), 1, None, "Install:"),
            ("scan error", "false", "", action.CheckError("Scan: exit status 1"), 1, None, "Scan:"),
        ]
        for prefix in ("INTERNAL PANIC:", "INTERNAL ERROR(s):"):
            for mode in ("true", "false"):
                for ordinary in ([], [diagnostic()]):
                    tree = {"pkg": {"nilaway": [*ordinary, diagnostic(message=f"{prefix}\nfailed")]}}
                    cases.append((f"{prefix} {mode} mixed={bool(ordinary)}", mode, "", tree, 1, None, f"Scan: {prefix}"))
        for name, strict, version, result, status, count, log in cases:
            with self.subTest(name=name):
                out = Path(self.env["GITHUB_OUTPUT"])
                summary = Path(self.env["GITHUB_STEP_SUMMARY"])
                out.unlink(missing_ok=True)
                summary.unlink(missing_ok=True)
                env = {
                    **self.env, "NILAWAY_FAIL_ON_FINDINGS": strict, "NILAWAY_VERSION": version,
                    "GOOS": "linux", "GOARCH": "386", "GOFLAGS": "-tags=fixture",
                }
                calls = []

                def capture(args, *, stage, cwd, env):
                    calls.append((args, stage, cwd, env))
                    if args[0] == "git":
                        return os.fsencode(self.workspace) + b"\n"
                    if args[:2] == ["go", "list"]:
                        return info
                    if args[:2] == ["go", "env"]:
                        return b"linux\namd64\n"
                    if stage == "Install":
                        if name == "install error":
                            raise result
                        return b""
                    if isinstance(result, Exception):
                        raise result
                    return result if isinstance(result, bytes) else encoded(result)

                stdout = io.StringIO()
                with patch.object(action, "capture", side_effect=capture), contextlib.redirect_stdout(stdout):
                    self.assertEqual(action.run(env), status)
                if log is not None:
                    self.assertIn(log, stdout.getvalue())
                if count is None:
                    self.assertFalse(out.exists())
                    self.assertFalse(summary.exists())
                else:
                    self.assertEqual(out.read_text(), f"finding-count={count}\n")
                    self.assertIn(f"Findings: **{count}**", summary.read_text())

                args, _, cwd, install_env = calls[3]
                pin = version or action.VERSION_FILE.read_text().strip()
                self.assertEqual(args, ["go", "install", f"go.uber.org/nilaway/cmd/nilaway@{pin}"])
                self.assertEqual(install_env["GOBIN"], str(cwd / "bin"))
                self.assertEqual(install_env["GOFLAGS"], " ")
                self.assertEqual(install_env["GOARCH"], "amd64")
                if name == "install error":
                    self.assertEqual(len(calls), 4)
                else:
                    args, _, scan_cwd, scan_env = calls[4]
                    self.assertEqual(args, [
                        str(cwd / "bin/nilaway"), "-json", "-pretty-print=false",
                        "-include-pkgs=example.com/project", "./...",
                    ])
                    self.assertEqual(scan_cwd, self.module)
                    self.assertEqual(scan_env["GOARCH"], "386")
                    self.assertEqual(scan_env["GOFLAGS"], "-tags=fixture")
                    self.assertEqual(scan_env["GOWORK"], "off")
                    self.assertEqual(scan_env["GOPACKAGESDRIVER"], "off")

    def test_invalid_inputs_stop_before_subprocesses(self):
        for key, value in [
            ("NILAWAY_VERSION", "latest"),
            ("NILAWAY_VERSION", "main"),
            ("NILAWAY_VERSION", "v1.0.0\n::warning::unexpected"),
            ("NILAWAY_FAIL_ON_FINDINGS", "yes"),
        ]:
            with self.subTest(key=key, value=value):
                stdout = io.StringIO()
                with patch.object(action, "capture") as capture, contextlib.redirect_stdout(stdout):
                    self.assertEqual(action.run({**self.env, key: value}), 1)
                capture.assert_not_called()
                self.assertIn("Configuration:", stdout.getvalue())

    def test_json_shape_and_order(self):
        for payload in (b"", b"{oops", b"[]", b'{"pkg":[]}', b'{"pkg":{"nilaway":{}}}',
                        b'{"pkg":{"nilaway":[{"message":"missing posn"}]}}'):
            with self.subTest(payload=payload), self.assertRaisesRegex(action.CheckError, "Scan:"):
                action.findings_from(payload)
        first, second = diagnostic("a.go:1:1"), diagnostic("b.go:2:1")
        self.assertEqual(action.findings_from(encoded({
            "b": {"nilaway": [second]},
            "a": {"other": [diagnostic()], "nilaway": [first]},
        })), [("a.go:1:1", "nil flow"), ("b.go:2:1", "nil flow")])

    def test_annotation_location_and_escaping(self):
        file = self.module / "comma,colon:%.go"
        file.touch()
        external = self.temp / "external.go"
        external.touch()
        (self.module / "linked.go").symlink_to(external)
        for position in ("-", "main.go:0:4", "nonexistent.go:1:1", f"{external}:2:1", "linked.go:1:1"):
            with self.subTest(position=position):
                self.assertEqual(action.annotation_position(position, self.module, self.workspace), {})
        for location in (file.name, str(file)):
            with self.subTest(location=location):
                position = action.annotation_position(f"{location}:5:3", self.module, self.workspace)
                stdout = io.StringIO()
                with contextlib.redirect_stdout(stdout):
                    action.emit("warning", "NilAway", "100%\r\n::error::not a command", **position)
                self.assertEqual(
                    stdout.getvalue(),
                    "::warning title=NilAway,file=nested module/comma%2Ccolon%3A%25.go,line=5,col=3"
                    "::100%25%0D%0A::error::not a command\n",
                )

    def test_display_limits_do_not_change_gate_or_count(self):
        stdout = io.StringIO()
        count = action.MAX_ANNOTATIONS + 2
        with contextlib.redirect_stdout(stdout):
            status = action.report(
                [("-", "💡" * action.MAX_MESSAGE_BYTES)] * count,
                strict=True, module=self.module, workspace=self.workspace, env=self.env,
            )
        self.assertEqual(status, 1)
        self.assertEqual(len(stdout.getvalue().splitlines()), action.MAX_ANNOTATIONS)
        self.assertIn("…", stdout.getvalue())
        kept = stdout.getvalue().splitlines()[0].split("::")[-1]
        self.assertLessEqual(len(kept.encode("utf-8")), action.MAX_MESSAGE_BYTES)
        self.assertIn("2 additional findings", Path(self.env["GITHUB_STEP_SUMMARY"]).read_text())
        self.assertEqual(Path(self.env["GITHUB_OUTPUT"]).read_text(), f"finding-count={count}\n")


if __name__ == "__main__":
    unittest.main()
