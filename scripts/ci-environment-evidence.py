#!/usr/bin/env python3
"""Write a small, descriptive inventory of the CI host used by an acceptance.

This records observed versions at capture time. It does not attest the runner,
claim that packages were pinned, or describe state outside the allowlisted
platform and package fields below.
"""

from __future__ import annotations

import argparse
import json
import os
import platform
import re
import shlex
import shutil
import subprocess
import sys
from pathlib import Path


PACKAGES = {
    "rootless": (
        "docker-ce", "docker-ce-cli", "docker-ce-rootless-extras", "containerd.io",
        "uidmap", "dbus-user-session", "slirp4netns", "systemd", "libseccomp2",
    ),
    "vault": (
        "dbus-x11", "dbus-daemon", "gnome-keyring", "libsecret-tools",
        "libsecret-1-0", "libglib2.0-0",
    ),
}


def _command_version(*argv: str) -> dict[str, object]:
    executable = shutil.which(argv[0])
    if not executable:
        return {"status": "unavailable"}
    try:
        result = subprocess.run(
            [executable, *argv[1:]], stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, timeout=5, check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        return {"status": "unavailable"}
    first_line = result.stdout.splitlines()[0].strip() if result.stdout.splitlines() else ""
    if result.returncode != 0 or not first_line:
        return {"status": "unavailable", "exit_code": result.returncode}
    return {"status": "observed", "value": first_line[:256]}


def _os_release() -> dict[str, str]:
    if sys.platform == "darwin":
        try:
            result = subprocess.run(
                ["sw_vers"], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                text=True, timeout=5, check=False,
            )
        except (OSError, subprocess.TimeoutExpired):
            return {}
        if result.returncode != 0:
            return {}
        values = {}
        for line in result.stdout.splitlines():
            key, separator, value = line.partition(":")
            if separator and key.strip() in {"ProductName", "ProductVersion", "BuildVersion"}:
                values[key.strip()] = value.strip()
        return values

    if sys.platform.startswith("linux"):
        values = {}
        try:
            lines = Path("/etc/os-release").read_text(encoding="utf-8").splitlines()
        except OSError:
            return values
        allowed = {"NAME", "ID", "VERSION_ID", "VERSION_CODENAME", "UBUNTU_CODENAME", "PRETTY_NAME"}
        for line in lines:
            key, separator, value = line.partition("=")
            if separator and key in allowed:
                try:
                    parsed = shlex.split(value, comments=False, posix=True)
                except ValueError:
                    continue
                if parsed:
                    values[key] = parsed[0]
        return values
    return {}


def _dpkg_packages(names: tuple[str, ...]) -> list[dict[str, str]]:
    observations = []
    executable = shutil.which("dpkg-query")
    for name in names:
        if not executable:
            observations.append({"name": name, "state": "package_manager_unavailable"})
            continue
        try:
            result = subprocess.run(
                [executable, "-W", f"--showformat=${{db:Status-Status}}\t${{Version}}", name],
                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, timeout=5, check=False,
            )
        except (OSError, subprocess.TimeoutExpired):
            observations.append({"name": name, "state": "query_unavailable"})
            continue
        status, separator, version = result.stdout.strip().partition("\t")
        if result.returncode == 0 and separator and status == "installed" and version:
            observations.append({"name": name, "state": "installed", "version": version})
        else:
            observations.append({"name": name, "state": "not_installed_or_unavailable"})
    return observations


def _macos_packages() -> list[dict[str, str]]:
    package_id = "com.apple.pkg.CLTools_Executables"
    executable = shutil.which("pkgutil")
    if not executable:
        return [{"name": package_id, "state": "package_manager_unavailable"}]
    try:
        result = subprocess.run(
            [executable, "--pkg-info", package_id], stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, text=True, timeout=5, check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        return [{"name": package_id, "state": "query_unavailable"}]
    if result.returncode != 0:
        return [{"name": package_id, "state": "not_installed_or_unavailable"}]
    version = next(
        (line.partition(":")[2].strip() for line in result.stdout.splitlines()
         if line.startswith("version:")),
        "",
    )
    if not version:
        return [{"name": package_id, "state": "version_unavailable"}]
    return [{"name": package_id, "state": "installed", "version": version}]


def _source_revision() -> str | None:
    revision = os.environ.get("GITHUB_SHA", "").lower()
    if re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", revision):
        return revision
    if os.environ.get("GITHUB_ACTIONS", "").lower() == "true":
        raise RuntimeError("GitHub Actions source revision is unavailable")
    return None


def collect(role: str, phase: str) -> dict[str, object]:
    system = platform.system()
    if system == "Linux":
        packages = _dpkg_packages(PACKAGES[role])
        package_manager = "dpkg-query"
    elif system == "Darwin" and role == "vault":
        packages = _macos_packages()
        package_manager = "pkgutil"
    else:
        packages = []
        package_manager = "unavailable_for_platform"

    runtimes = {
        "go": _command_version("go", "version"),
        "python": {"status": "observed", "value": sys.version.splitlines()[0][:256]},
        "bash": _command_version("bash", "--version"),
    }
    if system == "Linux":
        runtimes["systemd"] = _command_version("systemctl", "--version")
    if role == "rootless":
        runtimes["docker_client"] = _command_version("docker", "--version")
        runtimes["docker_daemon_binary"] = _command_version("dockerd", "--version")
        runtimes["containerd"] = _command_version("containerd", "--version")
    elif system == "Linux":
        runtimes["dbus_daemon"] = _command_version("dbus-daemon", "--version")
        runtimes["gnome_keyring_daemon"] = _command_version("gnome-keyring-daemon", "--version")
        runtimes["secret_tool"] = _command_version("secret-tool", "--version")

    runner = {}
    for field in ("RUNNER_OS", "RUNNER_ARCH", "ImageOS", "ImageVersion", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"):
        value = os.environ.get(field)
        if value and len(value) <= 128 and "\n" not in value:
            runner[field.lower()] = value

    return {
        "schema_version": 1,
        "evidence_kind": "CI_HOST_ENVIRONMENT_OBSERVATION",
        "claim_scope": "descriptive_versions_only_not_an_attestation_or_package_pin_claim",
        "source_revision": _source_revision(),
        "role": role,
        "capture_phase": phase,
        "runner": runner,
        "system": {
            "name": system,
            "kernel_release": platform.release(),
            "machine": platform.machine(),
            "os_release": _os_release(),
        },
        "runtimes": runtimes,
        "package_manager": package_manager,
        "packages": packages,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("role", choices=sorted(PACKAGES))
    parser.add_argument("--phase", required=True, choices=("before_setup", "after_packages"))
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    evidence = collect(args.role, args.phase)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(evidence, sort_keys=True, separators=(",", ":")) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
