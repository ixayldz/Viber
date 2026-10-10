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
        paths = [parent / "archive.zip", parent / "release.json"]
        for path in paths:
            path.write_bytes(("uploaded " + path.name).encode())
        draft = {"id": 41, "draft": True, "prerelease": True, "tag_name": "engineering-" + REV, "body": "exact reviewed notes", "assets": [{"id": i + 1, "name": path.name, "state": "uploaded", "size": path.stat().st_size, "digest": "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()} for i, path in enumerate(paths)]}
        release.validate_draft(draft, "engineering-" + REV, "exact reviewed notes", paths)
        for name, mutate in [
            ("wrong tag", lambda d: d.update(tag_name="foreign-tag")),
            ("published overwrite", lambda d: d.update(draft=False)),
            ("stable promotion", lambda d: d.update(prerelease=False)),
            ("foreign notes", lambda d: d.update(body="unreviewed notes")),
            ("bool release ID", lambda d: d.update(id=True)),
            ("partial upload", lambda d: d["assets"][0].update(state="new")),
            ("duplicate upload", lambda d: d["assets"].append(copy.deepcopy(d["assets"][0]))),
            ("asset path injection", lambda d: d["assets"][0].update(name="../outside")),
            ("duplicate asset ID", lambda d: d["assets"][1].update(id=1)),
            ("bool asset size", lambda d: d["assets"][0].update(size=True)),
            ("changed uploaded bytes", lambda d: d["assets"][0].update(digest="sha256:" + "0" * 64)),
            ("missing server digest", lambda d: d["assets"][0].pop("digest")),
        ]:
            changed = copy.deepcopy(draft)
            mutate(changed)
            try:
                release.validate_draft(changed, "engineering-" + REV, "exact reviewed notes", paths)
            except ValueError:
                count += 1
            else:
                raise AssertionError("invalid draft recovery admitted: " + name)
        # Tag lookup intentionally cannot see drafts. Publication must use the
        # create response's exact ID until PATCH succeeds, then use public tag.
        original = release.api, release.gh, release.accepted_run, release.api_body
        try:
            for failure in (None, "readback", "body", "asset ID", "asset digest", "release ID", "new CI", "tag"):
                calls = []
                current = copy.deepcopy(draft)
                if failure == "body": current["body"] = "changed after download"
                if failure == "asset ID": current["assets"][0]["id"] = 99
                if failure == "asset digest": current["assets"][0]["digest"] = "sha256:" + "0" * 64
                if failure == "release ID": current["id"] = 42
                public = dict(copy.deepcopy(draft), draft=False, html_url="https://github.com/ixayldz/Viber/releases/tag/engineering-" + REV)

                def fake_api(path, pages=False):
                    calls.append(("GET", path))
                    if path == f"repos/{release.REPO}/releases/41": return current
                    if path == f"repos/{release.REPO}/git/ref/tags/engineering-{REV}":
                        return {"object": {"sha": "b" * 40 if failure == "tag" else REV}}
                    if path == f"repos/{release.REPO}/releases/tags/engineering-{REV}" and any(c[0] == "PATCH" for c in calls): return public
                    raise AssertionError("premature/foreign release lookup: " + path)

                def fake_gh(*args):
                    assert args[0] == "api" and args[2:] == ("--header", "Accept: application/octet-stream")
                    asset_id = int(args[1].rsplit("/", 1)[1])
                    raw = paths[asset_id - 1].read_bytes()
                    return b"x" * len(raw) if failure == "readback" else raw

                def fake_body(path, method, body, output):
                    assert method == "PATCH" and path == f"repos/{release.REPO}/releases/41"
                    assert body == {"draft": False, "prerelease": True, "make_latest": "false"}
                    calls.append((method, path))
                    return public

                release.api, release.gh, release.api_body = fake_api, fake_gh, fake_body
                release.accepted_run = lambda revision: {"id": 8 if failure == "new CI" else 7}
                output = parent / ("finish-" + str(failure).replace(" ", "-"))
                output.mkdir()
                try:
                    release.finish_draft(copy.deepcopy(draft), "engineering-" + REV, draft["body"], paths, REV, {"id": 7}, output)
                except ValueError:
                    assert failure is not None and not any(c[0] == "PATCH" for c in calls), failure
                    count += 1
                else:
                    assert failure is None and calls[-1] == ("GET", f"repos/{release.REPO}/releases/tags/engineering-{REV}"), failure
                    count += 1
        finally:
            release.api, release.gh, release.accepted_run, release.api_body = original
        print(f"PASS: {count} engineering publication trust contracts; no native inference/signature/GitHub write acceptance")
    finally:
        release.portable.remove_private_tree(parent, cache, "release-contract-")


if __name__ == "__main__":
    main()
