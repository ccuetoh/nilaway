"""Install and run NilAway, then report the standalone checker's JSON to GitHub."""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile


VERSION_FILE = Path(__file__).with_name("nilaway-version.txt")
EXACT_VERSION = re.compile(
    r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"
    r"(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+incompatible)?"
)
POSITION = re.compile(r"(.+):([1-9][0-9]*):([1-9][0-9]*)")
INTERNAL_DIAGNOSTICS = ("INTERNAL PANIC:", "INTERNAL ERROR(s):")
MAX_JSON_BYTES = 32 * 1024 * 1024
MAX_MESSAGE_BYTES = 2048
MAX_ANNOTATIONS = 10


class CheckError(Exception):
    """A configuration, installation, or scan failure, not a nil-flow finding."""


def bounded_message(text):
    data = text.encode("utf-8", errors="replace")
    if len(data) <= MAX_MESSAGE_BYTES:
        return data.decode("utf-8")
    suffix = "…"
    return data[:MAX_MESSAGE_BYTES - len(suffix.encode("utf-8"))].decode(
        "utf-8", errors="ignore",
    ) + suffix


def escape(value, *, property_value=False):
    value = value.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")
    return value.replace(":", "%3A").replace(",", "%2C") if property_value else value


def emit(level, title, message, **position):
    attrs = {"title": title, **position}
    properties = ",".join(f"{k}={escape(v, property_value=True)}" for k, v in attrs.items())
    print(f"::{level} {properties}::{escape(bounded_message(message))}")


def capture(args, *, stage, cwd, env, limit=MAX_JSON_BYTES):
    # Keep logs and JSON separate. Never relay child output as workflow commands.
    with (
        tempfile.TemporaryFile(dir=env["RUNNER_TEMP"]) as out,
        tempfile.TemporaryFile(dir=env["RUNNER_TEMP"]) as err,
    ):
        try:
            result = subprocess.run(args, cwd=cwd, env=env, stdout=out, stderr=err, check=False)
        except OSError as exc:
            raise CheckError(f"{stage}: could not start {args[0]}: {exc}") from exc
        if result.returncode:
            err.seek(0)
            detail = err.read(MAX_MESSAGE_BYTES).decode("utf-8", errors="replace").strip()
            raise CheckError(
                f"{stage}: exit status {result.returncode}" + (f"\n{detail}" if detail else "")
            )
        out.seek(0)
        data = out.read(limit + 1)
        if len(data) > limit:
            raise CheckError(f"{stage}: output exceeds {limit} bytes; narrow scope or use a modular driver")
        return data


def decode_object(data, stage):
    try:
        result = json.loads(data.decode("utf-8"))
    except (UnicodeError, ValueError, RecursionError) as exc:
        raise CheckError(f"{stage}: expected a JSON object: {exc}") from exc
    if not isinstance(result, dict):
        raise CheckError(f"{stage}: expected a JSON object")
    return result


def module_root(workspace, directory, env):
    path = Path(directory)
    module = (workspace / path).resolve()
    if path.is_absolute() or not module.is_relative_to(workspace):
        raise CheckError("Configuration: working-directory must be within the checkout")
    gomod = module / "go.mod"
    if not gomod.is_file() or gomod.is_symlink():
        raise CheckError("Configuration: working-directory must contain a regular go.mod (not a symlink)")
    try:
        toplevel = capture(
            ["git", "rev-parse", "--show-toplevel"], stage="Configuration", cwd=module, env=env,
        )
    except CheckError as exc:
        raise CheckError("Configuration: check out the consumer repository at GITHUB_WORKSPACE") from exc
    if Path(os.fsdecode(toplevel).removesuffix("\n")).resolve() != workspace:
        raise CheckError("Configuration: check out the consumer repository at GITHUB_WORKSPACE")
    return module


def module_path(module, env):
    # Use the same Go environment as the scan to detect flags such as -modfile.
    result = decode_object(capture(
        ["go", "list", "-m", "-json"], stage="Configuration", cwd=module, env=env,
    ), "Configuration")
    if result.get("Main") is not True or result.get("GoMod") != str(module / "go.mod"):
        raise CheckError("Configuration: Go must use the selected module's go.mod (-modfile is not supported)")
    name = result.get("Path")
    if not isinstance(name, str) or not name.strip() or "," in name:
        raise CheckError("Configuration: expected a Go module path usable as a NilAway package prefix")
    return name


def install(version, directory, env):
    # GOFLAGS=' ' overrides even flags persisted by `go env -w`, without affecting the scan.
    host = capture(
        ["go", "env", "GOHOSTOS", "GOHOSTARCH"], stage="Install", cwd=directory,
        env={**env, "GOFLAGS": " "},
    ).split()
    if len(host) != 2:
        raise CheckError("Install: could not determine the native Go OS and architecture")
    os_name, arch = map(os.fsdecode, host)
    bindir = directory / "bin"
    capture(
        ["go", "install", f"go.uber.org/nilaway/cmd/nilaway@{version}"],
        stage="Install", cwd=directory,
        env={**env, "GOFLAGS": " ", "GOBIN": str(bindir), "GOOS": os_name, "GOARCH": arch},
    )
    return bindir / "nilaway"


def findings_from(data):
    findings = []
    for analyzers in decode_object(data, "Scan").values():
        if not isinstance(analyzers, dict):
            raise CheckError("Scan: invalid JSON package result")
        for name, diagnostics in analyzers.items():
            if isinstance(diagnostics, dict) and isinstance(diagnostics.get("error"), str):
                raise CheckError(f"Scan: {name}: {diagnostics['error']}")
            if not isinstance(diagnostics, list):
                raise CheckError(f"Scan: invalid JSON result for {name}")
            for diagnostic in diagnostics:
                if not isinstance(diagnostic, dict) or not all(
                    isinstance(diagnostic.get(key), str) for key in ("posn", "message")
                ):
                    raise CheckError(f"Scan: invalid JSON diagnostic for {name}")
                if name == "nilaway":
                    message = diagnostic["message"]
                    if message.startswith(INTERNAL_DIAGNOSTICS):
                        raise CheckError(f"Scan: {message}")
                    findings.append((diagnostic["posn"], message))
    return sorted(findings)


def annotation_position(position, module, workspace):
    match = POSITION.fullmatch(position)
    if match:
        filename, line, column = match.groups()
        try:
            path = (module / filename).resolve(strict=True)
            if path.is_relative_to(workspace) and path.is_file():
                return {"file": path.relative_to(workspace).as_posix(), "line": line, "col": column}
        except (OSError, ValueError, RuntimeError):
            # Some diagnostics have no usable source position. Keep them as fileless annotations.
            pass
    return {}


def report(findings, *, strict, module, workspace, env):
    count = len(findings)
    for position, message in findings[:MAX_ANNOTATIONS]:
        emit("error" if strict else "warning", "NilAway", message,
             **annotation_position(position, module, workspace))

    with open(env["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as summary:
        print(f"## NilAway\n\nFindings: **{count}**", file=summary)
        if count > MAX_ANNOTATIONS:
            print(f"\n{count - MAX_ANNOTATIONS} additional findings were not annotated.", file=summary)
    with open(env["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
        print(f"finding-count={count}", file=output)
    return int(strict and count > 0)


def run(env):
    try:
        strict = env.get("NILAWAY_FAIL_ON_FINDINGS", "true").strip().lower()
        if strict not in ("true", "false"):
            raise CheckError("Configuration: fail-on-findings must be true or false")
        version = env.get("NILAWAY_VERSION", "") or VERSION_FILE.read_text(encoding="utf-8").strip()
        if not EXACT_VERSION.fullmatch(version):
            raise CheckError("Configuration: nilaway-version must be an exact Go module version")
        workspace = Path(env["GITHUB_WORKSPACE"]).resolve(strict=True)
        module = module_root(workspace, env.get("NILAWAY_WORKING_DIRECTORY", "."), env)
        # Match ordinary standalone Go module loading, not a workspace or external package driver.
        go_env = {**env, "GO111MODULE": "on", "GOWORK": "off", "GOPACKAGESDRIVER": "off"}
        prefix = module_path(module, go_env)
        with tempfile.TemporaryDirectory(dir=env["RUNNER_TEMP"], prefix="nilaway-") as temp:
            binary = install(version, Path(temp), go_env)
            data = capture(
                [str(binary), "-json", "-pretty-print=false", f"-include-pkgs={prefix}", "./..."],
                stage="Scan", cwd=module, env=go_env,
            )
            findings = findings_from(data)
        return report(findings, strict=strict == "true", module=module, workspace=workspace, env=env)
    except (CheckError, OSError, RuntimeError, KeyError) as exc:
        emit("error", "NilAway action failed", str(exc))
        return 1


if __name__ == "__main__":
    raise SystemExit(run(os.environ))
