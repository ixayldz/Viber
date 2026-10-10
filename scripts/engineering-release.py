#!/usr/bin/env python3
"""Publish only explicit, exact-SHA engineering tags after native/provenance gates.

No downloaded binary is executed. A failed upload/readback leaves a draft,
never a partial public release. Existing releases are never overwritten.
"""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import zipfile

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("portable", Path(__file__).with_name("portable-package.py"))
portable = importlib.util.module_from_spec(spec)
spec.loader.exec_module(portable)
REPO = "ixayldz/Viber"
JOBS = {"rootless", "docker", "foundation", "attest-packages"} | {f"test ({os}-latest)" for os in ("ubuntu", "windows", "macos")} | {f"vault ({os}-latest)" for os in ("ubuntu", "macos")} | {f"packages ({os}-latest)" for os in ("ubuntu", "windows", "macos")}
PLATFORMS = {"windows-latest": "windows/amd64", "ubuntu-latest": "linux/amd64", "macos-latest": "darwin/arm64"}


def gh(*args):
    result = subprocess.run(["gh", *args], capture_output=True, timeout=180)
    if result.returncode or len(result.stdout) > 8 << 20:
        portable.fail("GitHub operation failed; raw response withheld; any draft is retained")
    return result.stdout


def api(path, pages=False):
    options = ["--paginate", "--slurp"] if pages else []
    return portable.strict_json(gh("api", path, *options))


def validate_run(run, jobs, revision):
    if (run.get("head_sha") != revision or run.get("head_branch") != "main" or run.get("event") != "push"
            or run.get("path") != ".github/workflows/ci.yml" or run.get("status") != "completed"
            or run.get("conclusion") != "success" or run.get("repository", {}).get("full_name") != REPO
            or run.get("head_repository", {}).get("full_name") != REPO or run.get("pull_requests")):
        portable.fail("exact native main-push CI acceptance required")
    if len(jobs) != len(JOBS) or {job.get("name") for job in jobs} != JOBS or any(job.get("status") != "completed" or job.get("conclusion") != "success" for job in jobs):
        portable.fail("all twelve native/provenance jobs must succeed; skipped jobs deny publication")


def accepted_run(revision):
    pages = api(f"repos/{REPO}/actions/runs?head_sha={revision}&event=push&per_page=100", pages=True)
    runs = [run for page in pages for run in page["workflow_runs"] if run.get("path") == ".github/workflows/ci.yml" and run.get("head_branch") == "main"]
    if not runs:
        portable.fail("no exact-SHA main native pipeline found")
    # Never choose an old successful run to hide a newer failure.
    run = max(runs, key=lambda row: row["id"])
    pages = api(f"repos/{REPO}/actions/runs/{run['id']}/jobs?filter=latest&per_page=100", pages=True)
    validate_run(run, [job for page in pages for job in page["jobs"]], revision)
    return run


def archive_identity(path, revision, platform):
    prefix = f"viber-{revision[:12]}-{platform.replace('/', '-')}"
    binary = "viber.exe" if platform.startswith("windows/") else "viber"
    expected_name = prefix + (".zip" if platform.startswith("windows/") else ".tar.gz")
    if path.name != expected_name:
        portable.fail("portable archive name does not match source/platform")
    content = {}
    def member(name, size, read):
        if not name.startswith(prefix + "/") or not 0 <= size <= 32 << 20 or len(content) >= portable.MAX_FILES:
            portable.fail("archive member outside bounded package inventory")
        relative = name[len(prefix) + 1:]
        if not relative or relative in content or relative.startswith("/") or any(part in ("", ".", "..") for part in relative.split("/")) or "\\" in relative or ":" in relative:
            portable.fail("archive path alias or duplicate")
        raw = read()
        if len(raw) != size or sum(map(len, content.values())) + size > portable.MAX_BYTES:
            portable.fail("archive byte bound mismatch")
        content[relative] = raw
    if path.suffix == ".zip":
        with zipfile.ZipFile(path) as container:
            for entry in container.infolist():
                member(entry.filename, entry.file_size, lambda e=entry: container.read(e))
    else:
        with tarfile.open(path, "r:gz") as container:
            for entry in container:
                if not entry.isfile():
                    portable.fail("nonregular archive member")
                member(entry.name, entry.size, lambda e=entry: container.extractfile(e).read())
    portable.verify_archive(path, prefix, content, binary)
    package = portable.strict_json(content.get("package.json", b"{}"))
    native = package.get("native_smoke", {})
    if (type(package.get("schema_version")) is not int or package.get("schema_version") != 1
            or package.get("channel") != "engineering" or package.get("release_ready") is not False
            or package.get("source_revision") != revision or package.get("platform") != platform
            or not portable.DIGEST.fullmatch(package.get("source_archive_sha256", ""))
            or package.get("build_go_version") != f"go version go1.27.2 {platform}"
            or native.get("status") != "PASS" or native.get("platform") != platform
            or type(native.get("run_exit")) is not int or native.get("run_exit") != 2
            or native.get("quality") != "UNVERIFIED" or native.get("source_preserved") is not True
            or native.get("scope") != "VERSION_DOCTOR_OFFLINE_GREETING_DIFF_ONLY"):
        portable.fail("attested archive must carry exact native engineering acceptance")
    rows = package.get("files", [])
    if not isinstance(rows, list) or len(rows) != len(content) - 1:
        portable.fail("archive package inventory incomplete")
    seen = set()
    for row in rows:
        name = row.get("path")
        raw = content.get(name)
        if name == "package.json" or name in seen or raw is None or type(row.get("bytes")) is not int or row["bytes"] != len(raw) or row.get("sha256") != hashlib.sha256(raw).hexdigest():
            portable.fail("archive package inventory bytes mismatch")
        seen.add(name)
    if not {binary, "LICENSE", "sbom.cdx.json", "README.md", "examples/offline/greeting.json", "examples/offline/source/hello.txt"}.issubset(seen):
        portable.fail("archive omits required user assets")
    return native


def provenance(path, revision):
    gh("attestation", "verify", str(path), "-R", REPO, "--signer-workflow", REPO + "/.github/workflows/ci.yml", "--source-ref", "refs/heads/main", "--source-digest", revision)


def publish(args):
    revision = args.revision
    if not portable.REVISION.fullmatch(revision) or args.tag != "engineering-" + revision:
        portable.fail("explicit full-SHA engineering tag required")
    tag = api(f"repos/{REPO}/git/ref/tags/{args.tag}")
    if tag.get("object", {}).get("type") != "commit" or tag["object"].get("sha") != revision:
        portable.fail("engineering tag must directly pin this commit")
    run = accepted_run(revision)
    output = Path(args.output).absolute()
    workspace = Path.cwd().resolve()
    if output.exists() or portable.link(output.parent) or not output.resolve().is_relative_to(workspace):
        portable.fail("fresh workspace publication directory required")
    pages = api(f"repos/{REPO}/actions/runs/{run['id']}/artifacts?per_page=100", pages=True)
    artifacts = [artifact for page in pages for artifact in page["artifacts"]]
    selected = []
    for os in PLATFORMS:
        name = f"portable-{os}-{revision}"
        matches = [a for a in artifacts if a.get("name") == name]
        if len(matches) != 1 or matches[0].get("expired") is not False or not 0 < matches[0].get("size_in_bytes", 0) <= 64 << 20:
            portable.fail("missing, expired, duplicate or oversized native package artifact")
        selected.append((os, name))
    output.mkdir(parents=True)
    assets = output / "assets"
    assets.mkdir()
    inventory = []
    for os, name in selected:
        folder = output / os
        gh("run", "download", str(run["id"]), "-R", REPO, "--name", name, "--dir", str(folder))
        descriptor = portable.strict_json(portable.read_regular(folder, "distribution.json", 64 << 10))
        archive_name = f"viber-{revision[:12]}-{PLATFORMS[os].replace('/', '-')}" + (".zip" if os == "windows-latest" else ".tar.gz")
        if set(p.name for p in folder.iterdir()) != {archive_name, "SHA256SUMS", "distribution.json"}:
            portable.fail("portable artifact contains unexpected members")
        raw = portable.read_regular(folder, archive_name, 64 << 20)
        digest = hashlib.sha256(raw).hexdigest()
        if (descriptor.get("source_revision") != revision or descriptor.get("platform") != PLATFORMS[os]
                or descriptor.get("archive") != archive_name or descriptor.get("sha256") != digest
                or type(descriptor.get("bytes")) is not int or descriptor["bytes"] != len(raw)
                or descriptor.get("release_ready") is not False):
            portable.fail("portable distribution byte/source identity mismatch")
        path = folder / archive_name
        provenance(path, revision)  # Authenticate before parsing archive members.
        native = archive_identity(path, revision, PLATFORMS[os])
        if descriptor.get("native_smoke") != native:
            portable.fail("distribution/native archive proof mismatch")
        (assets / archive_name).write_bytes(raw)
        inventory.append({"path": archive_name, "sha256": digest, "bytes": len(raw), "platform": PLATFORMS[os], "native_smoke": native})
    manifest = {"schema_version": 1, "channel": "engineering", "release_ready": False, "source_revision": revision, "tag": args.tag, "native_ci_run": run["id"], "provenance_verified": True, "platform_certificate_signing": "NOT_ACCEPTED", "assets": inventory}
    (assets / "release.json").write_bytes(portable.canonical(manifest))
    sums = "".join(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n" for path in sorted(assets.iterdir()))
    (assets / "SHA256SUMS").write_text(sums, encoding="utf-8")
    notes = output / "notes.md"
    notes.write_text(f"""Engineering snapshot — not a production/stable release. PRD A–D release gates remain CLOSED; doctor.release_ready=false.

Exact source: `{revision}`. All twelve [native CI/provenance jobs]({run['html_url']}) succeeded. Windows amd64, Linux amd64 and macOS arm64 packages contain the binary, MIT/dependency licenses, CycloneDX SBOM and offline first-task fixtures. No Go compiler or model API key is needed for the sample. The sample deliberately returns exit2/UNVERIFIED and preserves source files.

Verify the downloaded archive before extracting/executing:
```sh
gh attestation verify ARCHIVE -R {REPO} --signer-workflow {REPO}/.github/workflows/ci.yml --source-ref refs/heads/main --source-digest {revision}
```
Build provenance is not Windows Authenticode/macOS notarization or coding quality. Checksums alone do not authenticate a publisher. [Installation/update/rollback/uninstall](https://github.com/{REPO}/blob/{revision}/docs/INSTALL.md) · [Capabilities and open PRD acceptance](https://github.com/{REPO}/blob/{revision}/docs/CAPABILITIES.md).

Keep stores outside the program directory. Install/update by selecting a fresh versioned directory; no automatic store migration or downgrade. Safe live apply, metadata checkpoint/compaction, aggregate physical quotas, full verification/language tooling, supervisor/extension/worker/eval/pilot acceptance remain open. Provider-key acceptance is separate from these engineering gaps.
""", encoding="utf-8")
    paths = sorted(assets.iterdir())
    # Never use --clobber or edit/reuse an existing release. If interrupted after
    # this call, an operator can inspect the retained draft and its exact assets.
    gh("release", "create", args.tag, *map(str, paths), "-R", REPO, "--verify-tag", "--draft", "--prerelease", "--latest=false", "--title", "Viber engineering " + revision[:12], "--notes-file", str(notes))
    release = api(f"repos/{REPO}/releases/tags/{args.tag}")
    if release.get("draft") is not True or release.get("prerelease") is not True or release.get("tag_name") != args.tag or {asset.get("name") for asset in release.get("assets", [])} != {p.name for p in paths} or len(release.get("assets", [])) != len(paths):
        portable.fail("draft release asset inventory mismatch; draft retained")
    readback = output / "readback"
    gh("release", "download", args.tag, "-R", REPO, "--dir", str(readback))
    if set(p.name for p in readback.iterdir()) != {p.name for p in paths}:
        portable.fail("draft download inventory mismatch; draft retained")
    for path in paths:
        if portable.read_regular(readback, path.name, 64 << 20) != path.read_bytes():
            portable.fail("draft upload/readback byte mismatch; draft retained")
    # Re-read current CI status and tag immediately before the public effect.
    if accepted_run(revision)["id"] != run["id"] or api(f"repos/{REPO}/git/ref/tags/{args.tag}").get("object", {}).get("sha") != revision:
        portable.fail("CI or tag changed before publication; draft retained")
    gh("release", "edit", args.tag, "-R", REPO, "--verify-tag", "--draft=false", "--prerelease", "--latest=false")
    release = api(f"repos/{REPO}/releases/tags/{args.tag}")
    if release.get("draft") is not False or release.get("prerelease") is not True or release.get("tag_name") != args.tag:
        portable.fail("public engineering release readback failed")
    print(json.dumps({"tag": args.tag, "source_revision": revision, "url": release["html_url"], "release_ready": False, "native_ci_run": run["id"]}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--output", required=True)
    try:
        publish(parser.parse_args())
    except (ValueError, OSError, subprocess.TimeoutExpired) as error:
        raise SystemExit("FAIL: " + str(error))
