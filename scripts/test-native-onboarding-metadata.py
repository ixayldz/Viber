#!/usr/bin/env python3
"""Check that optional Go build metadata is reported conservatively."""

from __future__ import annotations

import importlib.util
import subprocess
import sys
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

sys.dont_write_bytecode = True
SCRIPT = Path(__file__).with_name("native-onboarding-smoke.py").resolve(strict=True)
SPEC = importlib.util.spec_from_file_location("native_onboarding_smoke", SCRIPT)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("cannot load native onboarding smoke script")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)

CONPTY_SCRIPT = Path(__file__).with_name("native_tui_conpty.py").resolve(strict=True)
CONPTY_SPEC = importlib.util.spec_from_file_location("native_tui_conpty_test", CONPTY_SCRIPT)
if CONPTY_SPEC is None or CONPTY_SPEC.loader is None:
    raise RuntimeError("cannot load ConPTY controller")
CONPTY = importlib.util.module_from_spec(CONPTY_SPEC)
CONPTY_SPEC.loader.exec_module(CONPTY)

REVISION = "0123456789abcdef0123456789abcdef01234567"
BINARY = Path("unused-viber-binary")


def check(name: str, expected: str, *, go_available: bool = True, output: bytes = b"") -> None:
    with patch.object(MODULE.shutil, "which", return_value="go" if go_available else None):
        with patch.object(
            MODULE.subprocess,
            "run",
            return_value=subprocess.CompletedProcess(["go"], 0, stdout=output, stderr=b""),
        ) as run:
            result = MODULE._binary_build_metadata(BINARY, REVISION)
    if result.get("metadata_assessment") != expected:
        raise AssertionError(f"{name}: expected {expected}, got {result!r}")
    if go_available:
        run.assert_called_once()
    else:
        run.assert_not_called()


class FakePipe:
    def __init__(self, events: list[str], name: str, lines: list[bytes] | None = None):
        self.events = events
        self.name = name
        self.lines = list(lines or [])

    def close(self) -> None:
        self.events.append(f"{self.name}_close")

    def readline(self, _maximum: int) -> bytes:
        self.events.append("stdout_read")
        return self.lines.pop(0) if self.lines else b""


class FakeProcess:
    def __init__(self, events: list[str], *, wait_times_out: bool = False):
        self.events = events
        self.stdin = FakePipe(events, "stdin")
        self.stdout = FakePipe(events, "stdout")
        self.returncode: int | None = None
        self.wait_times_out = wait_times_out
        self.wait_count = 0
        self.kill_count = 0

    def wait(self, timeout: float) -> int:
        self.events.append(f"wait_{self.wait_count}")
        self.wait_count += 1
        if self.wait_times_out and self.wait_count == 1:
            raise subprocess.TimeoutExpired("owned-powershell-helper", timeout)
        self.returncode = -9 if self.kill_count else 0
        return self.returncode

    def poll(self) -> int | None:
        return self.returncode

    def kill(self) -> None:
        self.events.append("kill_owned_helper")
        self.kill_count += 1


class FakeStartupInfo:
    def __init__(self):
        self.dwFlags = 0
        self.wShowWindow = 0


def check_conpty_startup_cleanup() -> None:
    def run_case(*, wait_times_out: bool, thread_start_fails: bool = False) -> tuple[list[str], FakeProcess, object]:
        events: list[str] = []
        process = FakeProcess(events, wait_times_out=wait_times_out)

        class BrokenThread:
            ident = None

            def __init__(self, **_: object):
                pass

            def start(self) -> None:
                events.append("reader_start")
                raise RuntimeError("injected thread startup failure")

        patches = [
            patch.object(CONPTY, "os", SimpleNamespace(name="nt", environ={})),
            patch.object(CONPTY.shutil, "which", return_value="powershell.exe"),
            patch.object(CONPTY.subprocess, "STARTUPINFO", FakeStartupInfo, create=True),
            patch.object(CONPTY.subprocess, "STARTF_USESHOWWINDOW", 1, create=True),
            patch.object(CONPTY.subprocess, "SW_HIDE", 0, create=True),
            patch.object(CONPTY.subprocess, "Popen", side_effect=lambda *args, **kwargs: (events.append("popen") or process)),
            patch.object(
                CONPTY.WindowsConPTY,
                "_next_line",
                side_effect=CONPTY.ConPTYFailure("injected_ready_failure"),
            ),
        ]
        if thread_start_fails:
            patches.append(patch.object(CONPTY, "threading", SimpleNamespace(Thread=BrokenThread)))
        entered = []
        try:
            for item in patches:
                entered.append(item)
                item.start()
            try:
                CONPTY.WindowsConPTY(["viber.exe"], Path.cwd(), {})
            except CONPTY.ConPTYFailure as failure:
                return events, process, failure
            raise AssertionError("ConPTY startup regression did not fail")
        finally:
            for item in reversed(entered):
                item.stop()

    graceful_events, graceful_process, graceful_failure = run_case(wait_times_out=False)
    if graceful_failure.cleanup_complete is not True or graceful_process.kill_count != 0:
        raise AssertionError("graceful EOF did not report managed ConPTY cleanup")
    if not (graceful_events.index("stdin_close") < graceful_events.index("wait_0")):
        raise AssertionError(f"helper wait preceded EOF: {graceful_events!r}")

    forced_events, forced_process, forced_failure = run_case(wait_times_out=True)
    if forced_failure.cleanup_complete is not False or forced_process.kill_count != 1:
        raise AssertionError("forced helper kill was incorrectly reported as child cleanup proof")
    if not (
        forced_events.index("stdin_close") < forced_events.index("wait_0")
        < forced_events.index("kill_owned_helper") < forced_events.index("wait_1")
    ):
        raise AssertionError(f"startup cleanup order was unsafe: {forced_events!r}")

    thread_events, thread_process, thread_failure = run_case(wait_times_out=False, thread_start_fails=True)
    if thread_failure.cleanup_complete is not True or thread_process.returncode != 0:
        raise AssertionError("reader-thread startup failure did not clean up the helper")
    if not (thread_events.index("reader_start") < thread_events.index("stdin_close") < thread_events.index("wait_0")):
        raise AssertionError(f"reader startup failure skipped bounded helper cleanup: {thread_events!r}")

    holder = object.__new__(CONPTY.WindowsConPTY)
    holder.lines = __import__("queue").Queue(maxsize=1)
    holder.reader_error = None
    if not holder._publish_reader_line(b"one") or holder._publish_reader_line(b"two"):
        raise AssertionError("full ConPTY protocol queue was not bounded")
    if holder.reader_error != b"ERROR:protocol_queue_full":
        raise AssertionError("full ConPTY protocol queue did not produce a bounded failure")


def main() -> int:
    check("missing go tool", "UNAVAILABLE", go_available=False)
    check("missing build metadata", "INCOMPLETE", output=b"viber: go1.27.2\n")
    check(
        "unknown modified value",
        "INCOMPLETE",
        output=(
            "viber: go1.27.2\n\tbuild\tvcs.revision=" + REVISION + "\n"
            "\tbuild\tvcs.modified=unknown\n"
        ).encode(),
    )
    check(
        "dirty build remains descriptive",
        "DIRTY_FLAG",
        output=(
            "viber: go1.27.2\n\tbuild\tvcs.revision=" + REVISION + "\n"
            "\tbuild\tvcs.modified=true\n"
        ).encode(),
    )
    check(
        "matching clean metadata is not called attestation",
        "CLEAN_REVISION_MATCH",
        output=(
            "viber: go1.27.2\n\tbuild\tvcs.revision=" + REVISION + "\n"
            "\tbuild\tvcs.modified=false\n"
        ).encode(),
    )
    check_conpty_startup_cleanup()
    print("native onboarding build metadata and ConPTY checks: PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
