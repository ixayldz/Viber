#!/usr/bin/env python3
"""Native smoke and portable archive of a frozen engineering binary bundle.

This is not a stable release gate. Build provenance is generated separately by
GitHub's attestation job; archive hashes alone do not authenticate a download.
"""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import shutil
import stat
import subprocess
import tarfile
import tempfile
import zipfile

MAX_FILES = 256
MAX_BYTES = 128 << 20
DIGEST = re.compile(r"[0-9a-f]{64}\Z")
REVISION = re.compile(r"[0-9a-f]{40}\Z")
TARGETS = {"windows/amd64", "linux/amd64", "darwin/arm64"}


def fail(message):
    raise ValueError(message)


def strict_json(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                fail("duplicate JSON key")
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=pairs)


def canonical(value):
    return (json.dumps(value, sort_keys=True, indent=2) + "\n").encode()


def link(path):
    try:
        info = path.lstat()
    except FileNotFoundError:
        return False
    return path.is_symlink() or bool(getattr(info, "st_file_attributes", 0) & getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400))


def read_regular(root, name, limit):
    if not isinstance(name, str) or not name or len(name) > 2048 or "\\" in name:
        fail("portable relative file path required")
    parts = PurePosixPath(name)
    if parts.is_absolute() or parts.as_posix() != name or any(p in ("", ".", "..") or p.endswith((" ", ".")) for p in name.split("/")) or ":" in name:
        fail("unsafe package path")
    path = root
    for part in parts.parts:
        path = path / part
        if link(path):
            fail("symlink package input denied")
    if path.resolve().is_relative_to(root.resolve()) is False or not path.is_file():
        fail("package file must remain in its root")
    info = path.stat()
    if info.st_nlink != 1:
        fail("hardlinked package input denied")
    if info.st_size > limit:
        fail("package file exceeds byte bound")
    raw = path.read_bytes()
    if len(raw) > limit:
        fail("package file grew beyond byte bound")
    return raw


def bundle_files(root, expected_revision):
    proof = strict_json(read_regular(root, "bundle.json", 128 << 10))
    if (type(proof.get("schema_version")) is not int or proof.get("schema_version") != 1 or proof.get("source_revision") != expected_revision
            or proof.get("release_ready") is not False
            or proof.get("signature_status") != "UNSIGNED_ENGINEERING_BUNDLE"
            or not DIGEST.fullmatch(proof.get("source_archive_sha256", ""))
            or not re.fullmatch(r"go version go1\.27\.2 [a-z0-9]+/[a-z0-9]+", proof.get("go_version", ""))):
        fail("frozen engineering bundle identity required")
    rows = proof.get("files")
    if not isinstance(rows, list) or not 1 <= len(rows) <= MAX_FILES:
        fail("bounded file inventory required")
    files = {}
    folded = set()
    total = 0
    for row in rows:
        name = row.get("path")
        raw = read_regular(root, name, 32 << 20)
        if name in files or name.casefold() in folded:
            fail("duplicate portable package path")
        if type(row.get("bytes")) is not int or len(raw) != row["bytes"] or hashlib.sha256(raw).hexdigest() != row.get("sha256"):
            fail("bundle bytes do not match immutable inventory")
        total += len(raw)
        if total > MAX_BYTES:
            fail("bundle exceeds aggregate byte bound")
        files[name] = raw
        folded.add(name.casefold())
    if "LICENSE" not in files or "sbom.cdx.json" not in files:
        fail("license and SBOM required")
    sbom = strict_json(files["sbom.cdx.json"])
    if sbom.get("bomFormat") != "CycloneDX" or sbom.get("specVersion") != "1.6" or sbom.get("metadata", {}).get("component", {}).get("version") != expected_revision:
        fail("SBOM must be bound to source revision")
    for component in sbom.get("components", []):
        properties = {p["name"]: p["value"] for p in component.get("properties", [])}
        licenses = properties.get("viber:license_files", "").split(",")
        if not licenses or any(not name.startswith("licenses/") or name not in files for name in licenses):
            fail("dependency license attribution missing")
    targets = proof.get("targets")
    if not isinstance(targets, list) or not 1 <= len(targets) <= len(TARGETS):
        fail("bounded supported targets required")
    seen = set()
    for target in targets:
        name = target.get("target")
        if name not in TARGETS or name in seen or target.get("reproducible") is not True or target.get("native_acceptance") != "NOT_RUN":
            fail("invalid target inventory")
        expected_name = "viber-" + name.replace("/", "-") + (".exe" if name.startswith("windows/") else "")
        if target.get("binary") != expected_name or expected_name not in files or hashlib.sha256(files[expected_name]).hexdigest() != target.get("sha256"):
            fail("target binary identity mismatch")
        seen.add(name)
    return proof, files


def native_target():
    system = {"Windows": "windows", "Linux": "linux", "Darwin": "darwin"}.get(platform.system())
    arch = {"AMD64": "amd64", "x86_64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(platform.machine())
    result = f"{system}/{arch}"
    if result not in TARGETS:
        fail("unsupported native package host")
    return result


def git_bytes(source, revision, path):
    result = subprocess.run(["git", "-C", str(source), "show", f"{revision}:{path}"], capture_output=True, timeout=30)
    if result.returncode or len(result.stdout) > 1 << 20:
        fail("committed package asset unavailable")
    return result.stdout


def invoke(binary, args, expected=0):
    result = subprocess.run([str(binary), *args], capture_output=True, timeout=90)
    if result.returncode != expected or len(result.stdout) > 1 << 20 or len(result.stderr) > 64 << 10:
        code = "UNKNOWN"
        if len(result.stdout) <= 1 << 20:
            try:
                detail = strict_json(result.stdout).get("error", {})
                if isinstance(detail, dict) and isinstance(detail.get("code"), str) and re.fullmatch(r"[A-Z0-9_]{1,64}", detail["code"]):
                    code = detail["code"]
            except (ValueError, AttributeError):
                pass
        fail(f"native {args[0]} failed: expected exit={expected} observed exit={result.returncode} code={code}; raw output withheld")
    return result.stdout


def remove_private_tree(directory, parent, prefix):
    owned = directory.resolve()
    if link(directory) or owned.parent != parent.resolve() or not directory.name.startswith(prefix):
        fail("temporary cleanup scope changed")

    def readonly(function, path, error):
        child = Path(path)
        if link(child) or not child.resolve().is_relative_to(owned) or not isinstance(error, PermissionError):
            raise error
        # Windows rejects unlink of immutable candidate files. Only clear the
        # readonly bit of this test-owned child; never modify a source ACL.
        mode = child.stat().st_mode
        os.chmod(child, stat.S_IMODE(mode) | stat.S_IWUSR)
        function(path)

    shutil.rmtree(directory, onerror=lambda function, path, info: readonly(function, path, info[1]))


def smoke(binary, folder, expected_target):
    source = folder / "examples/offline/source/hello.txt"
    before = source.read_bytes()
    version = invoke(binary, ["version"]).decode().strip()
    doctor = strict_json(invoke(binary, ["doctor", "--json"]))
    if doctor.get("platform") != expected_target or doctor.get("version") != version or doctor.get("release_ready") is not False:
        fail("native version/platform/capability mismatch")
    temp = Path(tempfile.mkdtemp(prefix=".native-smoke-", dir=folder))
    try:
        store = temp / "store"
        result = strict_json(invoke(binary, ["run", "Update hello.txt to the fixture greeting", "--offline", "--fixture", str(folder / "examples/offline/greeting.json"), "--root", str(source.parent), "--store", str(store), "--task", "greeting", "--allow-unverified", "--json"], expected=2))
        state = result.get("state", {})
        expected = {"execution_state": "TERMINATED", "terminal_outcome": "FINISHED", "quality_verdict": "UNVERIFIED", "fulfillment_status": "SATISFIED"}
        if any(state.get(k) != v for k, v in expected.items()):
            fail("offline sample manufactured quality or failed delivery")
        diff = strict_json(invoke(binary, ["diff", "greeting", "--store", str(store), "--json"]))
        if len(diff) != 1 or diff[0].get("path") != "hello.txt" or diff[0].get("before_digest") != hashlib.sha256(before).hexdigest() or diff[0].get("after_digest") != hashlib.sha256(b"hello, Viber\n").hexdigest() or source.read_bytes() != before:
            fail("native sample did not preserve source or exact candidate")
        return {"status": "PASS", "platform": expected_target, "cli_version": version, "run_exit": 2, "quality": "UNVERIFIED", "source_preserved": True, "scope": "VERSION_DOCTOR_OFFLINE_GREETING_DIFF_ONLY"}
    finally:
        # Recursive cleanup is limited to the exact freshly created child.
        remove_private_tree(temp, folder, ".native-smoke-")


def quickstart(revision, target):
    windows = target.startswith("windows/")
    command = ".\\viber.exe" if windows else "./viber"
    return f"""# Viber engineering snapshot

Source: {revision}; native target: {target}.
Stable release gates are CLOSED. This is not a production release.
Build provenance authenticates the build origin, not coding quality.

No Go compiler is needed to use this binary. Extract into a fresh versioned
directory you own; keep task stores outside this directory. Add that directory
to your own PATH if desired. No administrator install is required.

Before extraction/execution, verify the archive with GitHub CLI:
gh attestation verify ARCHIVE -R ixayldz/Viber --signer-workflow ixayldz/Viber/.github/workflows/ci.yml --source-ref refs/heads/main --source-digest {revision}

From the extracted directory:
{command} version
{command} doctor --json
{command} run "Update hello.txt to the fixture greeting" --offline --fixture examples/offline/greeting.json --root examples/offline/source --store ../viber-private-demo-{revision[:12]} --task greeting --allow-unverified --json
Expected task exit: 2 (FINISHED / UNVERIFIED / SATISFIED), not VERIFIED.
{command} diff greeting --store ../viber-private-demo-{revision[:12]} --json
Use a fresh store path for each demo. Source hello.txt stays unchanged.

Update: verify/extract the new archive into a different versioned directory.
Pause/settle active work and stop its owner before changing which binary you
use. Keep the previous directory. Store migrations require explicit commands
and a validated backup; installation never migrates stores automatically.
Rollback: select the previous verified directory; never downgrade or replace
a store/authority. Incompatible stores remain fail-closed.
Uninstall: remove only this versioned program directory after stopping owners;
task stores, backups and credentials require separate explicit deletion.

Full usage and exact capability limits:
https://github.com/ixayldz/Viber/blob/{revision}/README.md
https://github.com/ixayldz/Viber/blob/{revision}/docs/CAPABILITIES.md
""".encode()


def verify_archive(archive, prefix, content, binary_name):
    expected = {prefix + "/" + name: raw for name, raw in content.items()}
    seen = set()
    if archive.suffix == ".zip":
        with zipfile.ZipFile(archive) as container:
            for entry in container.infolist():
                if entry.filename not in expected or entry.filename in seen or entry.file_size != len(expected[entry.filename]):
                    fail("archive inventory mismatch")
                mode = entry.external_attr >> 16
                expected_mode = 0o755 if entry.filename == prefix + "/" + binary_name else 0o644
                if not stat.S_ISREG(mode) or stat.S_IMODE(mode) != expected_mode or container.read(entry) != expected[entry.filename]:
                    fail("archive file type or byte mismatch")
                seen.add(entry.filename)
    else:
        with tarfile.open(archive, "r:gz") as container:
            for entry in container:
                if entry.name not in expected or entry.name in seen or not entry.isfile() or entry.size != len(expected[entry.name]):
                    fail("archive inventory or file type mismatch")
                stream = container.extractfile(entry)
                if stream is None or stream.read() != expected[entry.name] or entry.mode != (0o755 if entry.name == prefix + "/" + binary_name else 0o644):
                    fail("archive bytes or executable mode mismatch")
                seen.add(entry.name)
    if seen != set(expected):
        fail("archive omitted a required file")


def build(args):
    if not REVISION.fullmatch(args.revision):
        fail("exact source revision required")
    bundle, output, source = Path(args.bundle).resolve(), Path(args.output).absolute(), Path(args.source).resolve()
    if output.exists() or link(output.parent) or output.resolve().is_relative_to(bundle) or not output.resolve().is_relative_to(source):
        fail("fresh workspace output separate from bundle required")
    proof, files = bundle_files(bundle, args.revision)
    target = native_target()
    selected = next((t for t in proof["targets"] if t["target"] == target), None)
    if selected is None:
        fail("native target not present in bundle")
    output.mkdir(parents=True)
    stage = Path(tempfile.mkdtemp(prefix=".portable-stage-", dir=output))
    try:
        binary_name = "viber.exe" if target.startswith("windows/") else "viber"
        content = {name: raw for name, raw in files.items() if name == "LICENSE" or name == "sbom.cdx.json" or name.startswith("licenses/")}
        content[binary_name] = files[selected["binary"]]
        for asset in ["examples/offline/greeting.json", "examples/offline/source/hello.txt"]:
            content[asset] = git_bytes(source, args.revision, asset)
        content["README.md"] = quickstart(args.revision, target)
        for name, raw in content.items():
            path = stage / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(raw)
        binary = stage / binary_name
        binary.chmod(0o755)
        native = smoke(binary, stage, target)
        package = {"schema_version": 1, "channel": "engineering", "release_ready": False, "source_revision": args.revision, "source_archive_sha256": proof["source_archive_sha256"], "platform": target, "build_go_version": proof["go_version"], "native_smoke": native, "files": [{"path": name, "bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()} for name, raw in sorted(content.items())]}
        content["package.json"] = canonical(package)
        name = f"viber-{args.revision[:12]}-{target.replace('/', '-')}"
        archive = output / (name + (".zip" if target.startswith("windows/") else ".tar.gz"))
        if target.startswith("windows/"):
            with zipfile.ZipFile(archive, "x", compression=zipfile.ZIP_DEFLATED) as z:
                for path, raw in sorted(content.items()):
                    entry = zipfile.ZipInfo(name + "/" + path, date_time=(2026, 1, 1, 0, 0, 0))
                    entry.external_attr = (0o100755 if path == binary_name else 0o100644) << 16
                    z.writestr(entry, raw, compress_type=zipfile.ZIP_DEFLATED)
        else:
            with archive.open("xb") as destination, gzip.GzipFile(filename="", mode="wb", fileobj=destination, mtime=0) as compressed:
                with tarfile.open(fileobj=compressed, mode="w", format=tarfile.PAX_FORMAT) as tar:
                    for path, raw in sorted(content.items()):
                        entry = tarfile.TarInfo(name + "/" + path)
                        entry.size, entry.mode, entry.mtime = len(raw), (0o755 if path == binary_name else 0o644), 0
                        tar.addfile(entry, io.BytesIO(raw))
        verify_archive(archive, name, content, binary_name)
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        (output / "SHA256SUMS").write_text(f"{digest}  {archive.name}\n", encoding="utf-8")
        descriptor = {"schema_version": 1, "source_revision": args.revision, "platform": target, "archive": archive.name, "sha256": digest, "bytes": archive.stat().st_size, "release_ready": False, "native_smoke": native, "signature_status": "PROVENANCE_REQUIRED_NOT_YET_ATTESTED"}
    finally:
        remove_private_tree(stage, output, ".portable-stage-")
    # Cleanup must also succeed before publication. The CI signer attests the
    # archive separately; this marker never claims attestation success.
    (output / "distribution.json").write_bytes(canonical(descriptor))
    print(json.dumps(descriptor))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--source", default=".")
    parser.add_argument("--revision", required=True)
    try:
        build(parser.parse_args())
    except (ValueError, OSError, subprocess.TimeoutExpired, json.JSONDecodeError) as error:
        raise SystemExit("FAIL: " + str(error))
