#!/usr/bin/env python3
"""Exercise an exact-source attested package through the native version manager."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import sys

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("portable", Path(__file__).with_name("portable-package.py"))
portable = importlib.util.module_from_spec(spec)
spec.loader.exec_module(portable)


def main(args):
    revision = args.revision
    if not portable.REVISION.fullmatch(revision):
        portable.fail("exact source revision required")
    bootstrap = Path(args.bootstrap).resolve(strict=True)
    archive = Path(args.archive).resolve(strict=True)
    baseline = None
    if bool(args.baseline_archive) != bool(args.baseline_revision):
        portable.fail("baseline archive and exact revision required together")
    if args.baseline_archive:
        if not portable.REVISION.fullmatch(args.baseline_revision) or args.baseline_revision == revision:
            portable.fail("distinct exact baseline revision required")
        baseline = Path(args.baseline_archive).resolve(strict=True)
    output = Path(args.output).absolute()
    if output.exists() or portable.link(output.parent):
        portable.fail("fresh acceptance directory required")
    gh = shutil.which("gh")
    if not gh:
        portable.fail("GitHub CLI is required for actual provenance verification")
    output.mkdir()
    prefix = output / "program"
    stores = output / "task-store"
    package = ["package", "install", "--prefix", str(prefix), "--archive", str(archive),
               "--source-sha", revision, "--gh", str(Path(gh).resolve()), "--json"]
    initial_revision = args.baseline_revision if baseline else revision
    initial_package = (["package", "install", "--prefix", str(prefix), "--archive", str(baseline),
                        "--source-sha", initial_revision, "--gh", str(Path(gh).resolve()), "--json"]
                       if baseline else package)
    installed = portable.strict_json(portable.invoke(bootstrap, initial_package))
    if installed.get("active", {}).get("revision") != initial_revision or installed.get("release_ready") is not False:
        portable.fail("installer source/capability mismatch")
    binary_root = prefix / "versions" / initial_revision
    source = binary_root / "examples/offline/source/hello.txt"
    before = source.read_bytes()
    # No compiler/PATH dependency is used by the installed child. Provenance was
    # actually verified above with the caller-selected absolute GitHub CLI.
    environment = dict(portable.os.environ, PATH="")
    run = ["package", "run", "--prefix", str(prefix), "--", "run", "Update hello.txt to the fixture greeting",
           "--offline", "--fixture", str(binary_root / "examples/offline/greeting.json"), "--root", str(source.parent),
           "--store", str(stores), "--task", "greeting", "--allow-unverified", "--json"]
    result = portable.strict_json(portable.invoke(bootstrap, run, expected=2, environment=environment))
    if result.get("state", {}).get("quality_verdict") != "UNVERIFIED" or source.read_bytes() != before:
        portable.fail("installed task quality/source preservation mismatch")
    diff = portable.strict_json(portable.invoke(bootstrap, ["diff", "greeting", "--store", str(stores), "--json"], environment=environment))
    # Selecting another binary never performs an implicit store migration.
    registered = package + ["--store", str(stores)]
    portable.invoke(bootstrap, registered)
    if baseline:
        upgraded = portable.strict_json(portable.invoke(bootstrap, ["package", "run", "--prefix", str(prefix), "--",
                    "diff", "greeting", "--store", str(stores), "--json"], environment=environment))
        if upgraded != diff:
            portable.fail("cross-version upgrade changed existing task projection")
        old = portable.strict_json(portable.invoke(bootstrap, ["package", "rollback", "--prefix", str(prefix),
                                   "--revision", initial_revision, "--json"]))
        if old.get("active", {}).get("revision") != initial_revision:
            portable.fail("cross-version rollback selection mismatch")
        restored = portable.strict_json(portable.invoke(bootstrap, ["package", "run", "--prefix", str(prefix), "--",
                    "diff", "greeting", "--store", str(stores), "--json"], environment=environment))
        if restored != diff:
            portable.fail("cross-version rollback changed existing task projection")
        portable.invoke(bootstrap, registered)
    portable.invoke(bootstrap, package)
    rollback = portable.strict_json(portable.invoke(bootstrap, ["package", "rollback", "--prefix", str(prefix), "--revision", revision, "--json"]))
    if rollback.get("active", {}).get("revision") != revision:
        portable.fail("installed rollback selection mismatch")
    uninstalled = portable.strict_json(portable.invoke(bootstrap, ["package", "uninstall", "--prefix", str(prefix), "--json"]))
    portable.invoke(bootstrap, ["package", "uninstall", "--prefix", str(prefix), "--json"])
    if uninstalled.get("installed") or uninstalled.get("binary") or binary_root.exists():
        portable.fail("owned program inventory survived uninstall")
    if (prefix / "versions" / revision).exists():
        portable.fail("upgraded program inventory survived uninstall")
    observed = portable.strict_json(portable.invoke(bootstrap, ["diff", "greeting", "--store", str(stores), "--json"], environment=environment))
    if observed != diff:
        portable.fail("program uninstall changed external task store")
    proof = {"schema_version": 1, "source_revision": revision, "platform": portable.native_target(),
             "status": "PASS", "archive_sha256": hashlib.sha256(archive.read_bytes()).hexdigest(),
             "actual_oidc_verification": True, "installed_first_task": "OFFLINE_FIXTURE_UNVERIFIED_EXIT_2",
             "external_store_preserved": True, "owned_uninstall": True,
             "scope": "EXACT_SOURCE_INSTALL_REINSTALL_SAME_REVISION_ROLLBACK_UNINSTALL; NO_CROSS_VERSION_UPGRADE_OR_SIGNING_CLAIM",
             "release_ready": False}
    if baseline:
        proof["cross_version"] = {"baseline_revision": initial_revision,
                                  "baseline_archive_sha256": hashlib.sha256(baseline.read_bytes()).hexdigest(),
                                  "actual_oidc_verification": True, "status": "PASS",
                                  "existing_task_projection_preserved": True,
                                  "upgrade_rollback_upgrade": True,
                                  "scope": "COMPATIBLE_FORMAT_NO_IMPLICIT_STORE_MIGRATION"}
        proof["scope"] = "EXACT_SOURCE_INSTALL_REINSTALL_CROSS_VERSION_ROLLBACK_UNINSTALL; NO_SIGNING_CLAIM"
    (output / "installation.json").write_bytes(portable.canonical(proof))
    print(json.dumps(proof))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for field in ("bootstrap", "archive", "revision", "output"):
        parser.add_argument("--" + field, required=True)
    parser.add_argument("--baseline-archive")
    parser.add_argument("--baseline-revision")
    main(parser.parse_args())
