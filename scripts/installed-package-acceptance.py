#!/usr/bin/env python3
"""Exercise an exact-source attested package through the native version manager."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import shutil
import sys

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("portable", Path(__file__).with_name("portable-package.py"))
portable = importlib.util.module_from_spec(spec)
spec.loader.exec_module(portable)


def parse_protocol_downgrade_refusal(raw, current_revision):
    """Validate the durable-protocol rollback refusal and return missing caps."""
    payload = portable.strict_json(raw)
    error = payload.get("error")
    if not isinstance(error, dict) or error.get("code") != "UNSUPPORTED_CAPABILITY":
        portable.fail("protocol downgrade must fail with UNSUPPORTED_CAPABILITY")
    message = error.get("message")
    prefix = "selected package lacks runtime support required by registered stores: "
    if not isinstance(message, str) or not message.startswith(prefix):
        portable.fail("protocol downgrade refusal must expose its missing capability set")
    missing = [item.strip() for item in message[len(prefix):].split(",")]
    # The 90bc97 baseline has a physical-quota pin but no metadata catalog or
    # bootstrap reservation. The current child creates the catalog when it
    # first opens that store, so this exact transition must report precisely
    # the metadata capability and no inferred/extra capability.
    expected = ["physical_quota_metadata_v1"]
    if (missing != expected
            or any(not re.fullmatch(r"[a-z0-9]+(?:_[a-z0-9]+){1,15}", item) for item in missing)):
        portable.fail("protocol downgrade refusal has an invalid missing capability set")
    if not portable.REVISION.fullmatch(current_revision):
        portable.fail("current revision is required for protocol downgrade evidence")
    return missing


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
    registered = package + ["--store", str(stores)]
    # Selecting another binary never performs an implicit store migration. When
    # an older baseline is present, keep its child selected for this first diff:
    # invoking the current bootstrap directly would initialize the new bound
    # metadata protocol before the compatible selection round-trip is measured.
    baseline_run = ["package", "run", "--prefix", str(prefix), "--", "diff", "greeting",
                    "--store", str(stores), "--json"]
    diff_args = baseline_run if baseline else ["diff", "greeting", "--store", str(stores), "--json"]
    diff = portable.strict_json(portable.invoke(bootstrap, diff_args, environment=environment))
    portable.invoke(bootstrap, registered)
    if baseline:
        # The baseline/current selection transition is measured before the
        # current child opens the store. This proves compatible selection
        # rollback without allowing a new runtime to manufacture a capability
        # receipt that did not exist in the baseline store.
        old = portable.strict_json(portable.invoke(bootstrap, ["package", "rollback", "--prefix", str(prefix),
                                   "--revision", initial_revision, "--json"]))
        if old.get("active", {}).get("revision") != initial_revision:
            portable.fail("cross-version rollback selection mismatch")
        restored = portable.strict_json(portable.invoke(bootstrap, baseline_run, environment=environment))
        if restored != diff:
            portable.fail("baseline selection rollback changed existing task projection")
        portable.invoke(bootstrap, registered)
        selected = portable.strict_json(portable.invoke(bootstrap, ["package", "status", "--prefix", str(prefix), "--json"]))
        if selected.get("active", {}).get("revision") != revision:
            portable.fail("cross-version upgrade selection mismatch")
        current_run = portable.strict_json(portable.invoke(bootstrap, ["package", "run", "--prefix", str(prefix), "--",
                    "diff", "greeting", "--store", str(stores), "--json"], environment=environment))
        if current_run != diff or source.read_bytes() != before:
            portable.fail("new binary changed the existing task projection or source")
        before_refusal = portable.strict_json(portable.invoke(bootstrap, ["package", "status", "--prefix", str(prefix), "--json"]))
        refusal = portable.strict_json(portable.invoke(bootstrap, ["package", "rollback", "--prefix", str(prefix),
                    "--revision", initial_revision, "--json"], expected=4))
        missing_capabilities = parse_protocol_downgrade_refusal(portable.canonical(refusal), revision)
        after_refusal = portable.strict_json(portable.invoke(bootstrap, ["package", "status", "--prefix", str(prefix), "--json"]))
        if after_refusal != before_refusal or after_refusal.get("active", {}).get("revision") != revision:
            portable.fail("rejected protocol downgrade changed the active selection")
        active_before = before_refusal.get("active")
        if not isinstance(active_before, dict):
            portable.fail("protocol downgrade status omitted the active selection")
        manifest_digest = active_before.get("manifest_digest")
        archive_digest = active_before.get("archive_digest")
        if (not isinstance(manifest_digest, str) or not portable.DIGEST.fullmatch(manifest_digest)
                or not isinstance(archive_digest, str) or not portable.DIGEST.fullmatch(archive_digest)):
            portable.fail("protocol downgrade status omitted exact active selection identities")
        after_rejection = portable.strict_json(portable.invoke(bootstrap, ["package", "run", "--prefix", str(prefix), "--",
                    "diff", "greeting", "--store", str(stores), "--json"], environment=environment))
        if after_rejection != current_run or source.read_bytes() != before:
            portable.fail("new binary projection/source changed after rejected downgrade")
    else:
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
                                  "rollback_refused_after_protocol_activation": True,
                                  "rollback_refusal_code": "UNSUPPORTED_CAPABILITY",
                                  "missing_capabilities": missing_capabilities,
                                  "active_revision_after_refusal": revision,
                                  "active_selection_preserved_after_refusal": True,
                                  "active_manifest_digest_after_refusal": manifest_digest,
                                  "active_archive_digest_after_refusal": archive_digest,
                                  "new_binary_projection_preserved": True,
                                  "scope": "COMPATIBLE_SELECTION_THEN_DURABLE_PROTOCOL_DOWNGRADE_REFUSAL"}
        proof["scope"] = "EXACT_SOURCE_INSTALL_REINSTALL_CROSS_VERSION_ROLLBACK_UNINSTALL; DURABLE_PROTOCOL_DOWNGRADE_REFUSAL; NO_SIGNING_CLAIM"
    (output / "installation.json").write_bytes(portable.canonical(proof))
    print(json.dumps(proof))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for field in ("bootstrap", "archive", "revision", "output"):
        parser.add_argument("--" + field, required=True)
    parser.add_argument("--baseline-archive")
    parser.add_argument("--baseline-revision")
    main(parser.parse_args())
