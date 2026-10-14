#!/usr/bin/env python3
"""Drive a small, private Viber task through a real terminal interface.

This is an acceptance smoke, not a terminal conformance matrix. It requires a
caller-supplied Viber executable and full source revision, uses only temporary
state, and emits proof metadata without publishing the terminal transcript.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import platform
import re
import select
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import time
from pathlib import Path

sys.dont_write_bytecode = True

MAX_CAPTURE = 4 << 20
MAX_CLI_OUTPUT = 2 << 20
OSC_MARKER = b"VIBER_OSC_SENTINEL_9f3c"
INITIAL_SIZE = (80, 18)
RESIZED_SIZE = (180, 36)


class SmokeFailure(Exception):
    def __init__(self, stage: str, *, preserve_private_root: bool = False):
        super().__init__(stage)
        self.stage = stage
        self.preserve_private_root = preserve_private_root


def _platform_name() -> str:
    machine = platform.machine().lower()
    if machine in ("amd64", "x86_64", "x64"):
        machine = "amd64"
    elif machine in ("arm64", "aarch64"):
        machine = "arm64"
    return f"{sys.platform}/{machine}"


def _environment(private_home: Path) -> dict[str, str]:
    env = os.environ.copy()
    for name in ("HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"):
        target = private_home / name.lower()
        target.mkdir(parents=True, exist_ok=True)
        env[name] = str(target)
    env["TERM"] = "xterm-256color"
    env["NO_COLOR"] = "1"
    return env


def _run_cli(binary: Path, args: list[str], *, cwd: Path, env: dict[str, str], timeout: float = 60.0) -> tuple[int, bytes]:
    process = subprocess.Popen(
        [str(binary), *args], cwd=str(cwd), env=env,
        stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        process.kill()
        process.communicate()
        raise SmokeFailure("cli_timeout")
    if len(stdout) + len(stderr) > MAX_CLI_OUTPUT:
        raise SmokeFailure("cli_output_bound")
    return process.returncode, stdout


def _json_output(code: int, raw: bytes, allowed_codes: tuple[int, ...], stage: str):
    if code not in allowed_codes:
        raise SmokeFailure(stage)
    try:
        return json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        raise SmokeFailure(stage)


def _write_fixture(path: Path) -> None:
    hello = b"hello\n"
    after = b"hello, Viber\n"
    digest = hashlib.sha256(hello).hexdigest()
    summary = "Offline greeting smoke complete."
    fixture = {
        "schema_version": 1,
        "turns": [
            {
                "tool_calls": [{"id": "read-greeting", "name": "fs_read", "arguments": {"path": "hello.txt"}}],
                "usage_known": True, "input_tokens": 128, "output_tokens": 24,
            },
            {
                "tool_calls": [{
                    "id": "update-greeting", "name": "candidate_propose",
                    "arguments": {"read_set": [{"path": "hello.txt", "kind": "FILE", "digest": digest}],
                                  "changes": [{"path": "hello.txt", "before_digest": digest,
                                               "after_bytes": base64.b64encode(after).decode("ascii"), "delete": False}]},
                }],
                "usage_known": True, "input_tokens": 192, "output_tokens": 64,
            },
            {"text": summary, "usage_known": True, "input_tokens": 128, "output_tokens": 32},
        ],
    }
    path.write_text(json.dumps(fixture, ensure_ascii=True, separators=(",", ":")), encoding="utf-8")


def _wait_owner(binary: Path, store: Path, cwd: Path, env: dict[str, str], owner: subprocess.Popen, timeout: float = 15.0) -> None:
    endpoint = store / "owner.json"
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if owner.poll() is not None:
            raise SmokeFailure("owner_start")
        try:
            info = json.loads(endpoint.read_text(encoding="utf-8"))
            if info.get("owner_pid") == owner.pid:
                code, output = _run_cli(binary, ["owner-status", "--store", str(store), "--json"], cwd=cwd, env=env, timeout=4)
                if code == 0:
                    _json_output(code, output, (0,), "owner_status")
                    return
        except (OSError, json.JSONDecodeError):
            pass
        time.sleep(0.05)
    raise SmokeFailure("owner_ready_timeout")


class PosixPTY:
    backend = "pty"

    def __init__(self, argv: list[str], cwd: Path, env: dict[str, str]):
        import fcntl
        import pty
        import termios

        self.fcntl = fcntl
        self.termios = termios
        self.pid, self.master = pty.fork()
        self.capture = bytearray()
        self.closed = False
        self.exit_code = None
        if self.pid == 0:
            try:
                os.chdir(cwd)
                self._set_size(*INITIAL_SIZE)
                os.execve(argv[0], argv, env)
            except BaseException:
                os._exit(127)

    def _set_size(self, width: int, height: int) -> None:
        self.fcntl.ioctl(self.master if self.pid != 0 else 0, self.termios.TIOCSWINSZ,
                         struct.pack("HHHH", height, width, 0, 0))

    def resize(self, width: int, height: int) -> None:
        self._set_size(width, height)
        try:
            os.kill(self.pid, signal.SIGWINCH)
        except ProcessLookupError:
            pass

    def write(self, value: bytes) -> None:
        view = memoryview(value)
        while view:
            written = os.write(self.master, view)
            view = view[written:]

    def _read_once(self, wait: float) -> bytes:
        ready, _, _ = select.select([self.master], [], [], wait)
        if not ready:
            return b""
        try:
            return os.read(self.master, 65536)
        except OSError as exc:
            if getattr(exc, "errno", None) in (5, 6):
                return b""
            raise

    def wait_for(self, marker: bytes, timeout: float, stage: str) -> None:
        # A previous read can contain several frames. Cached rendering may
        # produce no new bytes, so consume an already observed marker first.
        if marker in self.capture:
            return
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            chunk = self._read_once(0.05)
            if chunk:
                self.capture.extend(chunk)
                if len(self.capture) > MAX_CAPTURE:
                    raise SmokeFailure("terminal_capture_bound")
                if marker in self.capture:
                    return
            if self._exit_code() is not None:
                raise SmokeFailure(stage)
        raise SmokeFailure(stage)

    def drain(self, duration: float = 0.15) -> None:
        deadline = time.monotonic() + duration
        while time.monotonic() < deadline:
            chunk = self._read_once(0.03)
            if chunk:
                self.capture.extend(chunk)
                if len(self.capture) > MAX_CAPTURE:
                    raise SmokeFailure("terminal_capture_bound")

    def clear_capture(self) -> None:
        self.capture.clear()

    def _exit_code(self):
        if self.pid < 0:
            return self.exit_code
        try:
            pid, status = os.waitpid(self.pid, os.WNOHANG)
        except ChildProcessError:
            self.pid = -1
            raise SmokeFailure("ui_exit_status")
        if pid == 0:
            return None
        self.exit_code = os.waitstatus_to_exitcode(status)
        self.pid = -1
        return self.exit_code

    def wait_exit(self, timeout: float, stage: str) -> int:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            code = self._exit_code()
            if code is not None:
                self.drain(0.05)
                return code
            chunk = self._read_once(0.05)
            if chunk:
                self.capture.extend(chunk)
                if len(self.capture) > MAX_CAPTURE:
                    raise SmokeFailure("terminal_capture_bound")
        raise SmokeFailure(stage)

    def terminate(self) -> None:
        if self.pid > 0 and self._exit_code() is None:
            try:
                os.kill(self.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                self.wait_exit(2.0, "ui_terminate")
            except SmokeFailure:
                try:
                    os.kill(self.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                try:
                    os.waitpid(self.pid, 0)
                except ChildProcessError:
                    pass
                self.pid = -1

    def close(self) -> None:
        if not self.closed:
            self.closed = True
            try:
                os.close(self.master)
            except OSError:
                pass


if os.name == "nt":
    from native_tui_conpty import ConPTYFailure, WindowsConPTY

def _terminal(binary: Path, task: str, store: Path, cwd: Path, env: dict[str, str]):
    argv = [str(binary), "ui", task, "--store", str(store)]
    if os.name == "nt":
        try:
            return WindowsConPTY(argv, cwd, env)
        except ConPTYFailure as failure:
            raise SmokeFailure(
                failure.stage,
                preserve_private_root=failure.cleanup_complete is False,
            ) from failure
    return PosixPTY(argv, cwd, env)


def _task_status(binary: Path, task: str, store: Path, cwd: Path, env: dict[str, str]) -> dict:
    code, output = _run_cli(binary, ["status", task, "--store", str(store), "--json"], cwd=cwd, env=env)
    result = _json_output(code, output, (0,), "task_cursor")
    state = result.get("state") if isinstance(result, dict) else None
    if not isinstance(state, dict) or state.get("task_id") != task:
        raise SmokeFailure("task_cursor")
    return result


def _cursor_from_status(result: dict, task: str) -> dict:
    state = result.get("state")
    if not isinstance(state, dict) or state.get("task_id") != task:
        raise SmokeFailure("task_cursor")
    required = ("task_seq", "journal_seq", "kernel_generation", "document_digest", "candidate_digest")
    if any(key not in state for key in required):
        raise SmokeFailure("task_cursor")
    return {"task_id": task, **{key: state[key] for key in required}}


def _stop_owner(binary: Path, store: Path, cwd: Path, env: dict[str, str], owner: subprocess.Popen) -> bool:
    try:
        code, output = _run_cli(binary, ["owner-stop", "--store", str(store), "--json"], cwd=cwd, env=env, timeout=15)
        if code != 0:
            owner.terminate()
            owner.wait(timeout=5)
            return False
        _json_output(code, output, (0,), "owner_stop")
        owner.wait(timeout=12)
        return owner.returncode == 0
    except (SmokeFailure, subprocess.TimeoutExpired, OSError):
        if owner.poll() is None:
            owner.terminate()
            try:
                owner.wait(timeout=5)
            except subprocess.TimeoutExpired:
                owner.kill()
                owner.wait(timeout=5)
        return False


def _journey(binary: Path, revision: str) -> dict:
    temp_root = Path(tempfile.gettempdir()).resolve(strict=True)
    private = Path(tempfile.mkdtemp(prefix="viber-native-tui-smoke-"))
    owner = None
    owner_stopped = False
    preserve_private_root = False
    cwd = private
    env = None
    capture_bytes = 0
    try:
        private = private.resolve(strict=True)
        if private.parent != temp_root or not private.name.startswith("viber-native-tui-smoke-") or private.is_symlink():
            raise SmokeFailure("private_temp_root")
        try:
            private.chmod(0o700)
        except OSError:
            pass
        source = private / "source"
        store = private / "store"
        home = private / "home"
        source.mkdir()
        store.mkdir()
        hello = source / "hello.txt"
        hello.write_bytes(b"hello\n")
        control_bytes = b"Terminal control fixture: \x1b]52;c;" + OSC_MARKER + b"\x07\n"
        control_file = source / "control.txt"
        control_file.write_bytes(control_bytes)
        config_file = private / "empty-user-config.json"
        config_file.write_text('{"schema_version":1,"preferences":{},"restrictions":{}}\n', encoding="utf-8")
        fixture = private / "greeting-fixture.json"
        _write_fixture(fixture)
        env = _environment(home)
        task = "tui-smoke-" + os.urandom(6).hex()
        cwd = private

        code, output = _run_cli(binary, ["run", "Create a small offline greeting change", "--root", str(source),
                                         "--store", str(store), "--task", task, "--user-config", str(config_file),
                                         "--offline", "--fixture", str(fixture), "--autonomy", "auto",
                                         "--allow-unverified", "--json"],
                                cwd=cwd, env=env, timeout=90)
        task_result = _json_output(code, output, (2,), "task_create")
        initial_state = task_result.get("state") if isinstance(task_result, dict) else None
        if (not isinstance(initial_state, dict) or initial_state.get("task_id") != task
                or initial_state.get("quality_verdict") != "UNVERIFIED"):
            raise SmokeFailure("task_create")
        source_bytes_after_task = hello.read_bytes()
        if source_bytes_after_task != b"hello\n":
            raise SmokeFailure("source_changed_during_task")

        memory_id = os.urandom(16).hex()
        code, output = _run_cli(binary, ["project-memory", "record", "--store", str(store), "--task", task,
                                         "--command-id", memory_id, "--kind", "FACT", "--key", "greeting",
                                         "--text", "Offline greeting note", "--json"], cwd=cwd, env=env)
        _json_output(code, output, (0,), "memory_seed")
        initial_status = _task_status(binary, task, store, cwd, env)
        initial_cursor = _cursor_from_status(initial_status, task)

        owner = subprocess.Popen([str(binary), "serve", "--store", str(store), "--json"], cwd=str(cwd), env=env,
                                 stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        ui = None
        stage = "terminal_start"
        try:
            _wait_owner(binary, store, cwd, env, owner)
            ui = _terminal(binary, task, store, cwd, env)
            try:
                ui.wait_for(b"Type / for commands", 8, "terminal_initial_frame")
            except SmokeFailure:
                if b"Viber connected." in ui.capture:
                    raise SmokeFailure("terminal_not_interactive")
                if not ui.capture:
                    raise SmokeFailure("terminal_no_output")
                raise SmokeFailure("terminal_initial_frame")

            stage = "command_filter"
            ui.write(b"/mem")
            ui.wait_for(b"Commands | Up/Down select | Tab/Enter fill", 3, stage)
            ui.wait_for(b"/memory", 3, stage)

            stage = "tab_completion"
            ui.write(b"\t")
            ui.wait_for(b"Complete the command, then press Enter to run.", 3, stage)

            stage = "memory_search"
            ui.write(b"\r")
            ui.wait_for(b"Project memory: 1 entries.", 12, stage)
            ui.drain()
            captured_before_help = len(ui.capture)

            stage = "terminal_escape_safety"
            ui.clear_capture()
            ui.write(b"/read control.txt\r")
            safe_osc = b"\\x1b]52;c;" + OSC_MARKER + b"\\a"
            raw_osc = b"\x1b]52;c;" + OSC_MARKER + b"\x07"
            ui.wait_for(safe_osc, 8, stage)
            ui.drain()
            if raw_osc in ui.capture:
                raise SmokeFailure(stage)
            captured_escape_output = len(ui.capture)

            stage = "help"
            ui.clear_capture()
            ui.write(b"/help\r")
            ui.wait_for("Commands — type / to browse; Up/Down selects, Tab completes, Esc closes.".encode("utf-8"), 8, stage)
            pre_resize = bytes(ui.capture)
            # This command lies below the initial short viewport and becomes
            # visible after the resize. Probe the stable command name rather
            # than help prose that changes with memory features.
            resize_text = b"/context"
            if resize_text in pre_resize:
                raise SmokeFailure("resize_probe_precondition")

            stage = "terminal_resize"
            ui.resize(*RESIZED_SIZE)
            ui.wait_for(resize_text, 5, stage)
            ui.drain()

            stage = "detach"
            ui.write(b"/exit\r")
            if ui.wait_exit(10, stage) != 0:
                raise SmokeFailure(stage)
            capture_bytes = captured_before_help + captured_escape_output + len(ui.capture)
            ui.close()
            if getattr(ui, "cleanup_complete", None) is False:
                preserve_private_root = True
                raise SmokeFailure("terminal_cleanup_unknown", preserve_private_root=True)
            ui = None
        finally:
            if ui is not None:
                try:
                    try:
                        ui.terminate()
                    except Exception:
                        pass
                finally:
                    try:
                        ui.close()
                    except Exception:
                        pass
                    if getattr(ui, "cleanup_complete", None) is False:
                        preserve_private_root = True

        if not _stop_owner(binary, store, cwd, env, owner):
            raise SmokeFailure("owner_stop")
        owner_stopped = True
        final_status = _task_status(binary, task, store, cwd, env)
        cursor = _cursor_from_status(final_status, task)
        unchanged_keys = ("task_seq", "journal_seq", "execution_state", "quality_verdict", "fulfillment_status", "document_digest", "candidate_digest")
        if any(initial_status["state"].get(key) != final_status["state"].get(key) for key in unchanged_keys):
            raise SmokeFailure("terminal_task_mutation")
        if initial_status.get("budget") != final_status.get("budget"):
            raise SmokeFailure("terminal_budget_mutation")
        source_bytes_after = hello.read_bytes()
        if source_bytes_after != source_bytes_after_task or source_bytes_after != b"hello\n":
            raise SmokeFailure("source_changed_during_ui")
        if control_file.read_bytes() != control_bytes:
            raise SmokeFailure("source_changed_during_ui")
        return {
            "schema_version": 1,
            "status": "PASS",
            "platform": _platform_name(),
            "source_revision": revision,
            "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
            "terminal_backend": "conpty" if os.name == "nt" else "pty",
            "initial_task_cursor": initial_cursor,
            "task_cursor": cursor,
            "terminal_read_only": {
                "task_seq_unchanged": True,
                "journal_seq_unchanged": True,
                "execution_state_unchanged": True,
                "quality_unchanged": True,
                "budget_unchanged": True,
                "source_bytes_unchanged": True,
            },
            "journey": {
                "offline_task_created": True,
                "project_memory_seeded": True,
                "memory_filter_palette": True,
                "tab_completion": True,
                "memory_search": True,
                "help": True,
                "resize_probe": {"supported": True, "from": list(INITIAL_SIZE), "to": list(RESIZED_SIZE), "reflow_observed": True},
                "exit_detach": True,
                "owner_stop": True,
            },
            "terminal_output": {
                "bounded": True,
                "captured_bytes": capture_bytes,
                "escaped_control_seen": True,
                "injected_osc_absent": True,
                "transcript_included": False,
            },
            "source_sha256": hashlib.sha256(source_bytes_after).hexdigest(),
            "control_file_sha256": hashlib.sha256(control_bytes).hexdigest(),
        }
    except SmokeFailure as failure:
        preserve_private_root = preserve_private_root or failure.preserve_private_root
        raise
    finally:
        # Stop the exact tracked owner even after an intermediate failure. Do
        # not remove its store while it could still be writing.
        if owner is not None and owner.poll() is None and env is not None:
            owner_stopped = _stop_owner(binary, private / "store", cwd, env, owner)
        if not preserve_private_root and (owner is None or owner.poll() is not None or owner_stopped):
            try:
                cleanup_target = private.resolve(strict=True)
                is_junction = getattr(private, "is_junction", lambda: False)()
                if (cleanup_target.parent == temp_root and cleanup_target.name.startswith("viber-native-tui-smoke-")
                        and not private.is_symlink() and not is_junction):
                    shutil.rmtree(cleanup_target, ignore_errors=True)
            except OSError:
                pass


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, help="absolute path to an already-built Viber executable")
    parser.add_argument("--revision", required=True, help="full 40-hex source revision represented by the caller's checkout")
    args = parser.parse_args()
    revision = args.revision.lower()
    if not re.fullmatch(r"[0-9a-f]{40}", revision):
        result = {"schema_version": 1, "status": "FAIL", "platform": _platform_name(), "failure_stage": "revision_argument"}
        print(json.dumps(result, sort_keys=True, separators=(",", ":")))
        return 2
    try:
        binary = Path(args.binary).expanduser().resolve(strict=True)
    except OSError:
        binary = None
    if binary is None or not binary.is_absolute() or not binary.is_file():
        result = {"schema_version": 1, "status": "FAIL", "platform": _platform_name(), "source_revision": revision, "failure_stage": "binary_argument"}
        print(json.dumps(result, sort_keys=True, separators=(",", ":")))
        return 2
    try:
        result = _journey(binary, revision)
    except SmokeFailure as failure:
        result = {"schema_version": 1, "status": "FAIL", "platform": _platform_name(),
                  "source_revision": revision, "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                  "failure_stage": failure.stage}
        print(json.dumps(result, sort_keys=True, separators=(",", ":")))
        return 1
    except Exception as failure:
        # Error details may include temporary paths or process output; publish
        # only a stable category, never the exception text.
        stage = getattr(failure, "stage", "host_io")
        if not isinstance(stage, str) or not re.fullmatch(r"[a-z][a-z0-9_]{0,63}", stage):
            stage = "host_io"
        result = {"schema_version": 1, "status": "FAIL", "platform": _platform_name(),
                  "source_revision": revision, "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                  "failure_stage": stage}
        print(json.dumps(result, sort_keys=True, separators=(",", ":")))
        return 1
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
