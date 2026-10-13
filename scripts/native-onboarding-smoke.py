#!/usr/bin/env python3
"""Exercise the offline onboarding wizard and its automatic terminal handoff.

This bounded acceptance journey uses a fresh private user-data root. It makes
no authenticated or model-backed request and publishes no terminal transcript.
"""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import os
import platform
import re
import shutil
import stat as statmod
import subprocess
import sys
import tempfile
import time
from pathlib import Path

sys.dont_write_bytecode = True

MAX_PHASE_SECONDS = 10.0
TASK_ID_RE = re.compile(r"task-[0-9a-f]{32}\Z")
PRIVACY_DIR_RE = re.compile(r"\.viber-privacy-[0-9a-f]{32}\Z")
TASK_CAPTURE_RE = re.compile(rb"Task ID:\s*(task-[0-9a-f]{32})")
GO_REVISION_RE = re.compile(r"[0-9a-f]{40}\Z")


class JourneyFailure(Exception):
    def __init__(self, stage: str):
        super().__init__(stage)
        self.stage = stage


def _load_terminal_helpers():
    source = Path(__file__).with_name("native-tui-smoke.py").resolve(strict=True)
    script_dir = str(source.parent)
    if script_dir not in sys.path:
        sys.path.insert(0, script_dir)
    spec = importlib.util.spec_from_file_location("viber_native_tui_smoke", source)
    if spec is None or spec.loader is None:
        raise JourneyFailure("terminal_helpers")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _platform_name() -> str:
    machine = platform.machine().lower()
    if machine in ("amd64", "x86_64", "x64"):
        machine = "amd64"
    elif machine in ("arm64", "aarch64"):
        machine = "arm64"
    return f"{sys.platform}/{machine}"


def _binary_build_metadata(binary: Path, declared_revision: str) -> dict:
    go = shutil.which("go")
    if not go:
        return {
            "metadata_assessment": "UNAVAILABLE",
            "assessment_basis": "go_version_-m_embedded_metadata_not_attestation",
            "reason": "go_tool_unavailable",
        }
    try:
        completed = subprocess.run(
            [go, "version", "-m", str(binary)],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            timeout=MAX_PHASE_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        return {
            "metadata_assessment": "UNAVAILABLE",
            "assessment_basis": "go_version_-m_embedded_metadata_not_attestation",
            "reason": "go_build_metadata_unavailable",
        }
    if completed.returncode != 0 or len(completed.stdout) > 64 << 10:
        return {
            "metadata_assessment": "UNAVAILABLE",
            "assessment_basis": "go_version_-m_embedded_metadata_not_attestation",
            "reason": "go_build_metadata_unavailable",
        }
    output = completed.stdout.decode("utf-8", "replace")
    vcs_revision = None
    vcs_modified = None
    go_version = None
    for line in output.splitlines():
        if go_version is None:
            match = re.search(r"\bgo[0-9]+\.[0-9]+\.[0-9]+(?:rc[0-9]+|beta[0-9]+)?", line)
            if match:
                go_version = match.group(0)
        if line.startswith("\tbuild\tvcs.revision="):
            value = line.split("=", 1)[1]
            if GO_REVISION_RE.fullmatch(value):
                vcs_revision = value
        elif line.startswith("\tbuild\tvcs.modified="):
            value = line.split("=", 1)[1]
            if value in ("true", "false"):
                vcs_modified = value == "true"
    result = {
        "go_version": go_version,
        "vcs_revision": vcs_revision,
        "vcs_modified": vcs_modified,
        "assessment_basis": "go_version_-m_embedded_metadata_not_attestation",
    }
    if vcs_revision is None or vcs_modified is None:
        result.update({"metadata_assessment": "INCOMPLETE", "reason": "incomplete_go_build_metadata"})
    elif vcs_modified:
        result.update({"metadata_assessment": "DIRTY_FLAG", "reason": "embedded_vcs_modified_true"})
    elif vcs_revision != declared_revision:
        result.update({"metadata_assessment": "REVISION_MISMATCH", "reason": "embedded_revision_differs_from_argument"})
    else:
        result.update({"metadata_assessment": "CLEAN_REVISION_MATCH", "reason": "embedded_revision_matches_and_modified_flag_false"})
    return result


def _user_data_root(env: dict[str, str]) -> Path:
    if os.name == "nt":
        base = env.get("APPDATA")
        if not base:
            raise JourneyFailure("private_config_root")
        return Path(base) / "viber"
    if sys.platform == "darwin":
        home = env.get("HOME")
        if not home:
            raise JourneyFailure("private_config_root")
        return Path(home) / "Library" / "Application Support" / "viber"
    config = env.get("XDG_CONFIG_HOME")
    if not config:
        raise JourneyFailure("private_config_root")
    return Path(config) / "viber"


def _is_junction(path: Path) -> bool:
    return bool(getattr(path, "is_junction", lambda: False)())


def _assert_private_path(path: Path, private_root: Path, stage: str, *, directory: bool) -> Path:
    try:
        private_real = private_root.resolve(strict=True)
        current = private_root
        relative = path.relative_to(private_root)
        for part in relative.parts:
            current = current / part
            if current.is_symlink() or _is_junction(current):
                raise JourneyFailure(stage)
        resolved = path.resolve(strict=True)
        resolved.relative_to(private_real)
        if directory and not resolved.is_dir():
            raise JourneyFailure(stage)
        if not directory and not resolved.is_file():
            raise JourneyFailure(stage)
        return resolved
    except JourneyFailure:
        raise
    except (OSError, ValueError):
        raise JourneyFailure(stage)


def _owned_task_paths(private_root: Path, env: dict[str, str], task_id: str):
    if not TASK_ID_RE.fullmatch(task_id):
        raise JourneyFailure("task_id")
    user_data = _user_data_root(env)
    user_data = _assert_private_path(user_data, private_root, "private_config_root", directory=True)
    tasks_root = _assert_private_path(user_data / "tasks", private_root, "task_inventory", directory=True)
    task_entries = []
    privacy_entries = []
    try:
        for entry in tasks_root.iterdir():
            if TASK_ID_RE.fullmatch(entry.name):
                task_entries.append(entry)
            elif PRIVACY_DIR_RE.fullmatch(entry.name):
                privacy_entries.append(entry)
            else:
                raise JourneyFailure("task_inventory")
            if len(task_entries) > 1 or len(privacy_entries) > 1 or len(task_entries) + len(privacy_entries) > 2:
                raise JourneyFailure("task_inventory")
    except OSError:
        raise JourneyFailure("task_inventory")
    if len(task_entries) != 1 or task_entries[0].name != task_id:
        raise JourneyFailure("task_inventory")
    if privacy_entries:
        _assert_private_path(privacy_entries[0], private_root, "task_inventory", directory=True)
    store = _assert_private_path(tasks_root / task_id, private_root, "private_store", directory=True)
    source = _assert_private_path(user_data / "demo-source" / task_id, private_root,
                                  "private_source", directory=True)
    sample = _assert_private_path(source / "hello.txt", private_root, "private_source", directory=False)
    return user_data, store, source, sample


def _discover_task_store(private_root: Path, env: dict[str, str]) -> tuple[Path | None, bool]:
    """Find at most the one store this fresh onboarding root could own.

    The boolean is true only when the private config/tasks path was readable
    and proved empty (or absent), so cleanup can distinguish absence from an
    unknown or inaccessible owner store.
    """
    user_data = _user_data_root(env)
    try:
        user_data.lstat()
    except FileNotFoundError:
        return None, True
    except OSError:
        return None, False
    try:
        user_data = _assert_private_path(user_data, private_root, "private_config_root", directory=True)
    except JourneyFailure:
        return None, False
    tasks_root = user_data / "tasks"
    try:
        tasks_root.lstat()
    except FileNotFoundError:
        return None, True
    except OSError:
        return None, False
    try:
        tasks_root = _assert_private_path(tasks_root, private_root, "task_inventory", directory=True)
        task_entries = []
        privacy_entries = []
        for entry in tasks_root.iterdir():
            if TASK_ID_RE.fullmatch(entry.name):
                task_entries.append(entry)
            elif PRIVACY_DIR_RE.fullmatch(entry.name):
                privacy_entries.append(entry)
            else:
                return None, False
            if len(task_entries) > 1 or len(privacy_entries) > 1 or len(task_entries) + len(privacy_entries) > 2:
                return None, False
    except (JourneyFailure, OSError):
        return None, False
    if not task_entries:
        if privacy_entries:
            return None, False
        return None, True
    if privacy_entries:
        try:
            _assert_private_path(privacy_entries[0], private_root, "task_inventory", directory=True)
        except JourneyFailure:
            return None, False
    try:
        store = _assert_private_path(tasks_root / task_entries[0].name, private_root, "private_store", directory=True)
    except JourneyFailure:
        return None, False
    return store, False


def _read_sample(sample: Path) -> bytes:
    try:
        info = sample.stat(follow_symlinks=False)
        if not statmod.S_ISREG(info.st_mode) or info.st_size != 5:
            raise JourneyFailure("source_changed")
        with sample.open("rb") as stream:
            content = stream.read(6)
    except JourneyFailure:
        raise
    except OSError:
        raise JourneyFailure("private_source")
    if content != b"hello":
        raise JourneyFailure("source_changed")
    return content


def _task_status(helpers, binary: Path, task_id: str, store: Path, cwd: Path, env: dict[str, str]) -> dict:
    code, output = helpers._run_cli(
        binary, ["status", task_id, "--store", str(store), "--json"],
        cwd=cwd, env=env, timeout=MAX_PHASE_SECONDS,
    )
    try:
        result = helpers._json_output(code, output, (0,), "task_status")
    except helpers.SmokeFailure as failure:
        raise JourneyFailure(failure.stage)
    state = result.get("state") if isinstance(result, dict) else None
    if not isinstance(state, dict) or state.get("task_id") != task_id:
        raise JourneyFailure("task_status")
    if state.get("quality_verdict") != "UNVERIFIED":
        raise JourneyFailure("unverified_required")
    return result


def _stop_owner(helpers, binary: Path, store: Path, cwd: Path, env: dict[str, str]) -> bool:
    try:
        code, output = helpers._run_cli(
            binary, ["owner-stop", "--store", str(store), "--json"],
            cwd=cwd, env=env, timeout=MAX_PHASE_SECONDS,
        )
        result = helpers._json_output(code, output, (0,), "owner_stop")
        owner = result.get("owner") if isinstance(result, dict) else None
        if not (isinstance(result, dict) and result.get("closing") is True
                and isinstance(owner, dict) and isinstance(owner.get("owner_id"), str)
                and bool(owner["owner_id"]) and isinstance(owner.get("owner_pid"), int)
                and owner["owner_pid"] > 0):
            return False
        try:
            (store / "owner.json").lstat()
            return False
        except FileNotFoundError:
            return True
        except OSError:
            return False
    except (JourneyFailure, helpers.SmokeFailure, OSError):
        return False


def _journey(binary: Path, revision: str) -> dict:
    helpers = _load_terminal_helpers()
    temp_root = Path(tempfile.gettempdir()).resolve(strict=True)
    private = Path(tempfile.mkdtemp(prefix="viber-native-onboarding-"))
    terminal = None
    env = None
    task_id = None
    store = None
    confirmed_demo = False
    store_absent_proven = False
    owner_stopped = False
    private_root_valid = False
    preserve_partial_tree = False
    source_bytes = b"hello"
    try:
        private = private.resolve(strict=True)
        if (private.parent != temp_root or not private.name.startswith("viber-native-onboarding-")
                or private.is_symlink() or _is_junction(private)):
            raise JourneyFailure("private_temp_root")
        private_root_valid = True
        try:
            private.chmod(0o700)
        except OSError:
            pass
        home = private / "home"
        env = helpers._environment(home)
        # Mode 3 is explicitly offline; remove ambient model credentials so a
        # future accidental branch change still cannot authenticate here.
        for name in tuple(env):
            upper = name.upper()
            if any(marker in upper for marker in ("_API_KEY", "_ACCESS_TOKEN", "_TOKEN", "_SECRET", "_PASSWORD", "_CREDENTIAL", "_AUTH")):
                env.pop(name, None)

        if os.name == "nt":
            try:
                terminal = helpers.WindowsConPTY([str(binary)], private, env)
            except helpers.ConPTYFailure as failure:
                preserve_partial_tree = failure.cleanup_complete is False
                raise JourneyFailure(failure.stage) from failure
        else:
            terminal = helpers.PosixPTY([str(binary)], private, env)

        terminal.wait_for(b"Choose 1, 2, or 3", MAX_PHASE_SECONDS, "welcome_choice")
        terminal.write(b"3\r")
        terminal.wait_for(b"Type YES to run the fixed demo:", MAX_PHASE_SECONDS, "offline_confirmation")
        # Once confirmation is submitted, the app may create a task and start
        # its background owner before this harness observes the receipt.
        confirmed_demo = True
        terminal.write(b"YES\r")
        terminal.wait_for(b"Task ID:", MAX_PHASE_SECONDS, "offline_task_created")
        terminal.wait_for(b"Opening the Viber task interface", MAX_PHASE_SECONDS, "auto_ui_handoff")
        terminal.wait_for(b"Connected.", MAX_PHASE_SECONDS, "auto_ui_connected")
        match = TASK_CAPTURE_RE.search(terminal.capture)
        if match is None:
            raise JourneyFailure("task_id")
        task_id = match.group(1).decode("ascii")
        user_data, store, source, sample = _owned_task_paths(private, env, task_id)
        if _read_sample(sample) != source_bytes:
            raise JourneyFailure("source_changed")

        initial = _task_status(helpers, binary, task_id, store, private, env)
        initial_state = initial["state"]
        terminal.write(b"/quit\r")
        if terminal.wait_exit(MAX_PHASE_SECONDS, "onboarding_exit") != 2:
            raise JourneyFailure("offline_exit_code")
        captured_bytes = len(terminal.capture)
        terminal.close()
        terminal = None

        owner_stopped = _stop_owner(helpers, binary, store, private, env)
        if not owner_stopped:
            raise JourneyFailure("owner_stop")
        final = _task_status(helpers, binary, task_id, store, private, env)
        final_state = final["state"]
        unchanged = (
            "task_seq", "journal_seq", "execution_state", "quality_verdict",
            "fulfillment_status", "document_digest", "candidate_digest",
        )
        if any(initial_state.get(key) != final_state.get(key) for key in unchanged):
            raise JourneyFailure("ui_mutated_task")
        if _read_sample(sample) != source_bytes:
            raise JourneyFailure("source_changed")

        return {
            "schema_version": 1,
            "status": "PASS",
            "source_revision": revision,
            "platform": _platform_name(),
            "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
            "terminal_backend": "conpty" if os.name == "nt" else "pty",
            "journey": {
                "bare_launch": True,
                "offline_demo_selected": True,
                "explicit_confirmation": True,
                "auto_ui_handoff": True,
                "ui_detached": True,
                "offline_exit_code_2": True,
                "owner_stop": True,
                "source_unchanged": True,
                "quality_verdict": "UNVERIFIED",
                "ui_state_unchanged": True,
            },
            "terminal_output": {
                "bounded": True,
                "captured_bytes": captured_bytes,
                "transcript_included": False,
            },
        }
    finally:
        if terminal is not None:
            try:
                terminal.terminate()
            except Exception:
                pass
            try:
                terminal.close()
            except Exception:
                pass
            preserve_partial_tree = preserve_partial_tree or getattr(terminal, "cleanup_complete", None) is False
        if env is not None and private_root_valid and confirmed_demo and store is None:
            store, store_absent_proven = _discover_task_store(private, env)
            if store is not None:
                task_id = store.name
        if env is not None and private_root_valid and store is not None and not owner_stopped:
            owner_stopped = _stop_owner(helpers, binary, store, private, env)
        can_delete = not preserve_partial_tree and (not confirmed_demo or store_absent_proven or owner_stopped)
        if private_root_valid and can_delete:
            try:
                target = private.resolve(strict=True)
                if (target.parent == temp_root and target.name.startswith("viber-native-onboarding-")
                        and not private.is_symlink() and not _is_junction(private)):
                    shutil.rmtree(target, ignore_errors=True)
            except OSError:
                pass


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, help="already-built Viber executable")
    parser.add_argument("--revision", required=True, help="full 40-hex checkout revision")
    args = parser.parse_args()
    revision = args.revision.lower()
    if not re.fullmatch(r"[0-9a-f]{40}", revision):
        result = {"schema_version": 1, "status": "FAIL", "platform": _platform_name(),
                  "failure_stage": "revision_argument"}
        print(json.dumps(result, sort_keys=True, separators=(",", ":")))
        return 2
    try:
        binary = Path(args.binary).expanduser().resolve(strict=True)
        if not binary.is_file():
            raise OSError("not a file")
    except OSError:
        result = {"schema_version": 1, "status": "FAIL", "platform": _platform_name(),
                  "source_revision": revision, "failure_stage": "binary_argument"}
        print(json.dumps(result, sort_keys=True, separators=(",", ":")))
        return 2
    binary_build = _binary_build_metadata(binary, revision)
    try:
        result = _journey(binary, revision)
        result["binary_build"] = binary_build
    except JourneyFailure as failure:
        result = {"schema_version": 1, "status": "FAIL", "platform": _platform_name(),
                  "source_revision": revision, "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                  "binary_build": binary_build, "failure_stage": failure.stage}
        print(json.dumps(result, sort_keys=True, separators=(",", ":")))
        return 1
    except Exception as failure:
        stage = getattr(failure, "stage", "host_io")
        if not isinstance(stage, str) or not re.fullmatch(r"[a-z][a-z0-9_]{0,63}", stage):
            stage = "host_io"
        result = {"schema_version": 1, "status": "FAIL", "platform": _platform_name(),
                  "source_revision": revision, "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                  "binary_build": binary_build, "failure_stage": stage}
        print(json.dumps(result, sort_keys=True, separators=(",", ":")))
        return 1
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
