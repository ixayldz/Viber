"""Bounded Windows ConPTY controller for native-tui-smoke.py."""

from __future__ import annotations

import base64
import os
import queue
import shutil
import subprocess
import threading
import time
from pathlib import Path

MAX_CAPTURE = 4 << 20
MAX_FRAME = 65536
MAX_PROTOCOL_LINE = 100000


class ConPTYFailure(Exception):
    def __init__(self, stage: str):
        super().__init__(stage)
        self.stage = stage


class WindowsConPTY:
    backend = "conpty"

    def __init__(self, argv: list[str], cwd: Path, env: dict[str, str]):
        if os.name != "nt":
            raise ConPTYFailure("conpty_platform")
        onboarding = len(argv) == 1
        existing_ui = len(argv) == 5 and argv[1] == "ui" and argv[3] == "--store"
        if not (onboarding or existing_ui):
            raise ConPTYFailure("conpty_arguments")
        helper = Path(__file__).with_name("native-tui-conpty.ps1").resolve(strict=True)
        powershell = shutil.which("powershell.exe") or shutil.which("pwsh.exe")
        if not powershell:
            system_root = os.environ.get("SystemRoot", r"C:\Windows")
            candidate = Path(system_root) / "System32" / "WindowsPowerShell" / "v1.0" / "powershell.exe"
            if candidate.is_file():
                powershell = str(candidate)
        if not powershell:
            raise ConPTYFailure("conpty_runtime")

        self.capture = bytearray()
        self.closed = False
        self.exit_code: int | None = None
        self.lines: queue.Queue[bytes | None] = queue.Queue(maxsize=64)
        startup = subprocess.STARTUPINFO()
        startup.dwFlags |= subprocess.STARTF_USESHOWWINDOW
        startup.wShowWindow = subprocess.SW_HIDE
        creation_flags = getattr(subprocess, "CREATE_NO_WINDOW", 0x08000000)
        try:
            helper_argv = [
                powershell,
                "-NoLogo",
                "-NoProfile",
                "-NonInteractive",
                "-ExecutionPolicy",
                "Bypass",
                "-File",
                str(helper),
                "-Binary",
                argv[0],
            ]
            if onboarding:
                helper_argv.append("-Onboarding")
            else:
                helper_argv.extend(["-Task", argv[2], "-Store", argv[4]])
            helper_argv.extend(["-WorkingDirectory", str(cwd)])
            self.process = subprocess.Popen(
                helper_argv,
                cwd=str(cwd),
                env=env,
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                bufsize=0,
                startupinfo=startup,
                creationflags=creation_flags,
            )
        except OSError:
            raise ConPTYFailure("conpty_helper_start")
        self.reader = threading.Thread(target=self._read_stdout, name="viber-conpty-reader", daemon=True)
        self.reader.start()
        first = self._next_line(10.0 if onboarding else 20.0, "conpty_helper_start")
        if first != b"READY":
            self._raise_protocol_error(first, "conpty_helper_start")

    def _read_stdout(self) -> None:
        try:
            assert self.process.stdout is not None
            while True:
                line = self.process.stdout.readline(MAX_PROTOCOL_LINE + 2)
                if not line:
                    break
                if len(line) > MAX_PROTOCOL_LINE or not line.endswith(b"\n"):
                    self.lines.put(b"ERROR:protocol_bound")
                    break
                self.lines.put(line[:-1].removesuffix(b"\r"))
        except Exception:
            self.lines.put(b"ERROR:protocol_read")
        finally:
            self.lines.put(None)

    def _next_line(self, timeout: float, stage: str) -> bytes:
        try:
            line = self.lines.get(timeout=max(0.0, timeout))
        except queue.Empty:
            raise ConPTYFailure(stage)
        if line is None:
            raise ConPTYFailure(stage)
        if line.startswith(b"ERROR:"):
            self._raise_protocol_error(line, stage)
        return line

    def _raise_protocol_error(self, line: bytes, fallback: str) -> None:
        stage = fallback
        if line.startswith(b"ERROR:"):
            value = line[6:].decode("ascii", "ignore")
            if value and all(ch.isascii() and (ch.isalnum() or ch == "_") for ch in value):
                stage = value
        raise ConPTYFailure(stage)

    def _send(self, command: bytes) -> None:
        if len(command) > MAX_PROTOCOL_LINE:
            raise ConPTYFailure("protocol_bound")
        if self.process.poll() is not None or self.process.stdin is None:
            raise ConPTYFailure("conpty_helper_exit")
        try:
            self.process.stdin.write(command + b"\n")
            self.process.stdin.flush()
        except OSError:
            raise ConPTYFailure("conpty_helper_pipe")

    def _append_data(self, line: bytes) -> None:
        if not line.startswith(b"DATA:"):
            raise ConPTYFailure("protocol_response")
        encoded = line[5:]
        if len(encoded) > ((MAX_FRAME + 2) // 3) * 4:
            raise ConPTYFailure("terminal_frame_bound")
        try:
            chunk = base64.b64decode(encoded, validate=True)
        except (ValueError, base64.binascii.Error):
            raise ConPTYFailure("protocol_data")
        if len(chunk) > MAX_FRAME or len(self.capture) + len(chunk) > MAX_CAPTURE:
            raise ConPTYFailure("terminal_capture_bound")
        self.capture.extend(chunk)

    def _poll(self, timeout: float = 2.0) -> None:
        self._send(b"POLL")
        while True:
            line = self._next_line(timeout, "terminal_read_timeout")
            if line == b"END":
                return
            if line.startswith(b"DATA:"):
                self._append_data(line)
                continue
            if line.startswith(b"EXIT:"):
                raise ConPTYFailure("protocol_poll_exit")
            if line.startswith(b"ERROR:"):
                self._raise_protocol_error(line, "protocol_poll_error")
            if line == b"RUNNING":
                raise ConPTYFailure("protocol_poll_running")
            if line == b"CLOSED":
                raise ConPTYFailure("protocol_poll_closed")
            raise ConPTYFailure("protocol_poll_other")

    def write(self, value: bytes) -> None:
        if len(value) > MAX_FRAME:
            raise ConPTYFailure("terminal_input_bound")
        self._send(b"WRITE:" + base64.b64encode(value))
        if self._next_line(3.0, "terminal_input") != b"OK":
            raise ConPTYFailure("terminal_input")

    def resize(self, width: int, height: int) -> None:
        self._send(f"RESIZE:{width}:{height}".encode("ascii"))
        if self._next_line(3.0, "terminal_resize") != b"OK":
            raise ConPTYFailure("terminal_resize")

    def wait_for(self, marker: bytes, timeout: float, stage: str) -> None:
        if marker in self.capture:
            return
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            self._poll(min(2.0, max(0.1, deadline - time.monotonic())))
            if marker in self.capture:
                return
            if self._wait_state(0.2) is not None:
                raise ConPTYFailure(stage)
            time.sleep(0.02)
        raise ConPTYFailure(stage)

    def _wait_state(self, timeout: float) -> int | None:
        self._send(b"WAIT")
        while True:
            line = self._next_line(timeout, "ui_exit_status")
            if line.startswith(b"DATA:"):
                self._append_data(line)
                continue
            if line == b"RUNNING":
                return None
            if line.startswith(b"EXIT:"):
                try:
                    self.exit_code = int(line[5:].decode("ascii"))
                except ValueError:
                    raise ConPTYFailure("ui_exit_status")
                return self.exit_code
            raise ConPTYFailure("protocol_response")

    def drain(self, duration: float = 0.15) -> None:
        deadline = time.monotonic() + duration
        while time.monotonic() < deadline:
            self._poll(2.0)
            time.sleep(0.02)

    def clear_capture(self) -> None:
        self.capture.clear()

    def wait_exit(self, timeout: float, stage: str) -> int:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            code = self._wait_state(min(2.0, max(0.1, deadline - time.monotonic())))
            if code is not None:
                self.drain(0.1)
                return code
            time.sleep(0.02)
        raise ConPTYFailure(stage)

    def terminate(self) -> None:
        if self.exit_code is not None or self.process.poll() is not None:
            return
        try:
            self._send(b"TERMINATE")
            while True:
                line = self._next_line(6.0, "ui_terminate")
                if line.startswith(b"DATA:"):
                    self._append_data(line)
                    continue
                if line.startswith(b"EXIT:"):
                    try:
                        self.exit_code = int(line[5:].decode("ascii"))
                    except ValueError:
                        raise ConPTYFailure("ui_terminate")
                    return
                raise ConPTYFailure("ui_terminate")
        except ConPTYFailure:
            if self.process.poll() is None:
                self.process.kill()
                try:
                    self.process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    pass

    def close(self) -> None:
        if self.closed:
            return
        self.closed = True
        if self.process.poll() is None:
            try:
                self._send(b"CLOSE")
                while True:
                    line = self._next_line(7.0, "conpty_close")
                    if line.startswith(b"DATA:"):
                        self._append_data(line)
                        continue
                    if line == b"CLOSED":
                        break
                    raise ConPTYFailure("conpty_close")
                self.process.wait(timeout=5)
            except (ConPTYFailure, subprocess.TimeoutExpired):
                if self.process.poll() is None:
                    self.process.kill()
                    try:
                        self.process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        pass
        if self.process.stdin is not None:
            try:
                self.process.stdin.close()
            except OSError:
                pass
        if self.process.stdout is not None:
            try:
                self.process.stdout.close()
            except OSError:
                pass
