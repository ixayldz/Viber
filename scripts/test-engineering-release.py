#!/usr/bin/env python3
"""Publication trust-boundary contracts; no GitHub writes/native execution."""
import copy
import hashlib
import importlib.util
import io
from pathlib import Path
import sys
import tarfile
import tempfile

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("release", ROOT / "scripts/engineering-release.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)
REV = "a" * 40


def main():
    run = {"head_sha": REV, "head_branch": "main", "event": "push", "path": ".github/workflows/ci.yml", "status": "completed", "conclusion": "success", "repository": {"full_name": release.REPO}, "head_repository": {"full_name": release.REPO}, "pull_requests": []}
    jobs = [{"name": name, "status": "completed", "conclusion": "success"} for name in sorted(release.JOBS)]
    release.validate_run(run, jobs, REV)
    count = 0
    for name, mutate in [
        ("foreign SHA", lambda r, j: r.update(head_sha="b" * 40)),
        ("PR execution", lambda r, j: r.update(event="pull_request")),
        ("fork execution", lambda r, j: r.update(head_repository={"full_name": "foreign/Viber"})),
        ("other branch", lambda r, j: r.update(head_branch="feature")),
        ("other workflow", lambda r, j: r.update(path=".github/workflows/fake.yml")),
        ("incomplete run", lambda r, j: r.update(status="in_progress")),
        ("red run", lambda r, j: r.update(conclusion="failure")),
        ("PR lineage", lambda r, j: r.update(pull_requests=[{"number": 1}])),
        ("missing native job", lambda r, j: j.pop()),
        ("duplicate instead of native job", lambda r, j: j.__setitem__(0, copy.deepcopy(j[1]))),
        ("skipped native job", lambda r, j: j[0].update(conclusion="skipped")),
        ("failed native job", lambda r, j: j[0].update(conclusion="failure")),
        ("unfinished native job", lambda r, j: j[0].update(status="in_progress")),
    ]:
        row, changed = copy.deepcopy(run), copy.deepcopy(jobs)
        mutate(row, changed)
        try:
            release.validate_run(row, changed, REV)
        except ValueError:
            count += 1
        else:
            raise AssertionError("publication admitted: " + name)
    cache = ROOT / ".cache"
    cache.mkdir(exist_ok=True)
    parent = Path(tempfile.mkdtemp(prefix="release-contract-", dir=cache))
    try:
        content = {name: ("fixture " + name).encode() for name in ("viber", "LICENSE", "sbom.cdx.json", "README.md", "examples/offline/greeting.json", "examples/offline/source/hello.txt")}
        native = {"status": "PASS", "platform": "linux/amd64", "run_exit": 2, "quality": "UNVERIFIED", "source_preserved": True, "scope": "VERSION_DOCTOR_OFFLINE_GREETING_DIFF_ONLY"}
        package = {"schema_version": 1, "channel": "engineering", "release_ready": False, "source_revision": REV, "source_archive_sha256": "b" * 64, "platform": "linux/amd64", "build_go_version": "go version go1.27.2 linux/amd64", "native_smoke": native, "files": [{"path": name, "bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()} for name, raw in content.items()]}
        prefix = f"viber-{REV[:12]}-linux-amd64"
        archive = parent / (prefix + ".tar.gz")

        def write(proof):
            files = dict(content, **{"package.json": release.portable.canonical(proof)})
            with tarfile.open(archive, "w:gz") as output:
                for name, raw in files.items():
                    entry = tarfile.TarInfo(prefix + "/" + name)
                    entry.size, entry.mode = len(raw), 0o755 if name == "viber" else 0o644
                    output.addfile(entry, io.BytesIO(raw))

        write(package)
        release.archive_identity(archive, REV, "linux/amd64")
        for name, mutate in [
            ("stable claim", lambda p: p.update(release_ready=True)),
            ("bool schema", lambda p: p.update(schema_version=True)),
            ("foreign source", lambda p: p.update(source_revision="c" * 40)),
            ("foreign native platform", lambda p: p["native_smoke"].update(platform="darwin/arm64")),
            ("fake VERIFIED", lambda p: p["native_smoke"].update(quality="VERIFIED")),
            ("wrong exit", lambda p: p["native_smoke"].update(run_exit=0)),
            ("bool exit", lambda p: p["native_smoke"].update(run_exit=True)),
            ("source changed", lambda p: p["native_smoke"].update(source_preserved=False)),
            ("unmeasured scope", lambda p: p["native_smoke"].update(scope="FULL_INSTALL_UPDATE_ROLLBACK")),
            ("wrong compiler", lambda p: p.update(build_go_version="go version go1.27.3 linux/amd64")),
            ("forged byte inventory", lambda p: p["files"][0].update(sha256="0" * 64)),
            ("missing file inventory", lambda p: p["files"].pop()),
        ]:
            proof = copy.deepcopy(package)
            mutate(proof)
            write(proof)
            try:
                release.archive_identity(archive, REV, "linux/amd64")
            except ValueError:
                count += 1
            else:
                raise AssertionError("invalid publication archive admitted: " + name)
        print(f"PASS: {count} engineering publication trust contracts; no native inference/signature/GitHub write acceptance")
    finally:
        release.portable.remove_private_tree(parent, cache, "release-contract-")


if __name__ == "__main__":
    main()
