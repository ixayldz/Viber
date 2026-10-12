#!/usr/bin/env python3
"""Check that optional Go build metadata is reported conservatively."""

from __future__ import annotations

import importlib.util
import subprocess
import sys
from pathlib import Path
from unittest.mock import patch

sys.dont_write_bytecode = True
SCRIPT = Path(__file__).with_name("native-onboarding-smoke.py").resolve(strict=True)
SPEC = importlib.util.spec_from_file_location("native_onboarding_smoke", SCRIPT)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("cannot load native onboarding smoke script")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)

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
    print("native onboarding build metadata checks: PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
