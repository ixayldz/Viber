#!/usr/bin/env python3
"""Adversarial inventory contracts. Fixtures are not native/signature acceptance."""
import argparse
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import io
import sys
import tarfile
import tempfile
import zipfile

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("portable", ROOT / "scripts/portable-package.py")
portable = importlib.util.module_from_spec(spec)
spec.loader.exec_module(portable)
REV = "a" * 40


def fixture(root):
    files = {"LICENSE": b"fixture license; not a release", "sbom.cdx.json": json.dumps({"bomFormat": "CycloneDX", "specVersion": "1.6", "metadata": {"component": {"version": REV}}, "components": []}).encode(), "viber-windows-amd64.exe": b"fixture bytes; not an executable"}
    for name, raw in files.items():
        (root / name).write_bytes(raw)
    proof = {"schema_version": 1, "source_revision": REV, "source_archive_sha256": "b" * 64, "go_version": "go version go1.27.2 windows/amd64", "release_ready": False, "signature_status": "UNSIGNED_ENGINEERING_BUNDLE", "files": [{"path": name, "bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()} for name, raw in files.items()], "targets": [{"target": "windows/amd64", "binary": "viber-windows-amd64.exe", "sha256": hashlib.sha256(files["viber-windows-amd64.exe"]).hexdigest(), "reproducible": True, "native_acceptance": "NOT_RUN"}]}
    (root / "bundle.json").write_bytes(portable.canonical(proof))
    return proof


def main():
    cache = ROOT / ".cache"
    cache.mkdir(exist_ok=True)
    parent = Path(tempfile.mkdtemp(prefix="portable-contract-", dir=cache))
    count = 0
    try:
        root = parent / "bundle"
        root.mkdir()
        original = fixture(root)
        portable.bundle_files(root, REV)
        for name, mutate in [
            ("foreign revision", lambda p: p.update(source_revision="c" * 40)),
            ("fake stable readiness", lambda p: p.update(release_ready=True)),
            ("fake signature status", lambda p: p.update(signature_status="SIGNED")),
            ("bool schema", lambda p: p.update(schema_version=True)),
            ("wrong compiler", lambda p: p.update(go_version="go version go1.27.3 windows/amd64")),
            ("missing license", lambda p: p["files"].pop(0)),
            ("forged native status", lambda p: p["targets"][0].update(native_acceptance="PASS")),
            ("foreign binary identity", lambda p: p["targets"][0].update(binary="other.exe")),
            ("duplicate target", lambda p: p["targets"].append(copy.deepcopy(p["targets"][0]))),
            ("path traversal", lambda p: p["files"][0].update(path="../outside-license")),
            ("Windows stream alias", lambda p: p["files"][0].update(path="LICENSE:secret")),
            ("noncanonical path alias", lambda p: p["files"][0].update(path="./LICENSE")),
            ("stale byte digest", lambda p: p["files"][0].update(sha256="0" * 64)),
            ("unbounded inventory", lambda p: p.update(files=p["files"] * 100)),
        ]:
            proof = copy.deepcopy(original)
            mutate(proof)
            (root / "bundle.json").write_bytes(portable.canonical(proof))
            output = parent / ("rejected-" + str(count))
            try:
                portable.build(argparse.Namespace(bundle=str(root), source=str(ROOT), output=str(output), revision=REV))
            except ValueError:
                if output.exists():
                    raise AssertionError("invalid bundle allocated output: " + name)
            else:
                raise AssertionError("invalid bundle accepted: " + name)
            count += 1
        (root / "bundle.json").write_bytes(portable.canonical(original))
        alias = parent / "outside-license"
        os.link(root / "LICENSE", alias)
        try:
            portable.bundle_files(root, REV)
        except ValueError:
            count += 1
        else:
            raise AssertionError("actual hardlink alias accepted")
        alias.unlink()
        raw = portable.canonical(original).decode().replace('"schema_version": 1', '"schema_version": 1, "schema_version": 1')
        (root / "bundle.json").write_text(raw)
        try:
            portable.bundle_files(root, REV)
        except ValueError:
            count += 1
        else:
            raise AssertionError("duplicate JSON identity accepted")
        # Archive readback rejects extra, aliased, duplicated and malformed
        # entries before an archive can receive a distribution marker.
        content = {"viber": b"fixture binary", "LICENSE": b"fixture license"}
        for name, archive_kind, rows in [
            ("unexpected ZIP member", "zip", [("viber", b"fixture binary", 0o100755), ("../outside", b"x", 0o100644)]),
            ("duplicate ZIP member", "zip", [("viber", b"fixture binary", 0o100755), ("viber", b"fixture binary", 0o100755)]),
            ("ZIP symlink", "zip", [("viber", b"fixture binary", 0o120755)]),
            ("ZIP missing executable bit", "zip", [("viber", b"fixture binary", 0o100644)]),
            ("ZIP changed bytes", "zip", [("viber", b"changed bytes", 0o100755)]),
            ("tar missing executable bit", "tar", [("viber", b"fixture binary", 0o644)]),
            ("tar changed bytes", "tar", [("viber", b"changed bytes", 0o755)]),
            ("tar omitted license", "tar", [("viber", b"fixture binary", 0o755)]),
        ]:
            archive = parent / (str(count) + (".zip" if archive_kind == "zip" else ".tar.gz"))
            if archive_kind == "zip":
                with zipfile.ZipFile(archive, "x") as output:
                    for path, raw, mode in rows:
                        entry = zipfile.ZipInfo("snapshot/" + path)
                        entry.external_attr = mode << 16
                        output.writestr(entry, raw)
            else:
                with tarfile.open(archive, "w:gz") as output:
                    for path, raw, mode in rows:
                        entry = tarfile.TarInfo("snapshot/" + path)
                        entry.size, entry.mode = len(raw), mode
                        output.addfile(entry, io.BytesIO(raw))
            try:
                portable.verify_archive(archive, "snapshot", content, "viber")
            except ValueError:
                count += 1
            else:
                raise AssertionError("invalid archive accepted: " + name)
        print(f"PASS: {count} adversarial portable inventory contracts; fixtures are not native execution or signature acceptance")
    finally:
        portable.remove_private_tree(parent, cache, "portable-contract-")


if __name__ == "__main__":
    main()
