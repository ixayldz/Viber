"""Black-box acceptance: foreign package PASS must not replace native evidence."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile

validator = Path(__file__).with_name("verify-test-results.py")
auth = "github.com/ixayldz/Viber/internal/auth"
foreign = "example.invalid/foreign/acceptance"
native = "TestActualOSVaultRoundTripAndEnvironmentIsolation"
diskguard = "github.com/ixayldz/Viber/internal/diskguard"
pressure = "TestActualBoundedFilesystemPressurePreservesControlPublicationAndReopen"
failures = [
    "TestVaultResponseFailureMatrixNeverAdoptsNoisyOrNoncanonicalKey",
    "TestVaultExecutableTrustIncludesEveryResolvedAncestor",
    "TestVaultProcessBoundsTimeoutOverflowAndInheritedPipeDrain",
    "TestVaultRotationFailureKeepsExistingCiphertextAndAccountIdentity",
]


def package_events(package, results, complete=True):
    events = [{"Action": "start", "Package": package}]
    for test, result in results:
        events.extend([
            {"Action": "run", "Package": package, "Test": test},
            {"Action": result, "Package": package, "Test": test},
        ])
    if complete:
        events.append({"Action": "pass", "Package": package})
    return events


samples = [
    ("actual bounded pressure", package_events(diskguard, [(pressure, "pass")]), ["--disk-pressure"], True),
    ("bounded pressure skipped", package_events(diskguard, [(pressure, "skip")]), ["--disk-pressure"], False),
    ("bounded pressure foreign identity", package_events(foreign, [(pressure, "pass")]), ["--disk-pressure"], False),
    ("native identity", package_events(auth, [(native, "pass")]), ["--vault"], True),
    ("foreign name collision", package_events(foreign, [(native, "pass")]), ["--vault"], False),
    ("native skipped and foreign passed", package_events(auth, [(native, "skip")]) + package_events(foreign, [(native, "pass")]), ["--vault"], False),
    ("subtest cannot replace parent", package_events(auth, [(native + "/forged", "pass")]), ["--vault"], False),
    ("failed native", package_events(auth, [(native, "fail")]), ["--vault"], False),
    ("incomplete package", package_events(auth, [(native, "pass")], complete=False), ["--vault"], False),
    ("all failure parents", package_events(auth, [(test, "pass") for test in failures]), ["--vault-failure"], True),
    ("failure parent skipped", package_events(auth, [(test, "skip" if index == 0 else "pass") for index, test in enumerate(failures)]), ["--vault-failure"], False),
    ("failure parents in foreign package", package_events(foreign, [(test, "pass") for test in failures]), ["--vault-failure"], False),
]

with tempfile.TemporaryDirectory(prefix="viber-evidence-validator-") as directory:
    evidence = Path(directory) / "fixture.jsonl"
    for name, events, flags, accepted in samples:
        evidence.write_text("".join(json.dumps(event) + "\n" for event in events), encoding="utf-8")
        result = subprocess.run([sys.executable, str(validator), str(evidence), *flags], capture_output=True, text=True, timeout=10)
        if (result.returncode == 0) != accepted:
            raise SystemExit("FAIL: evidence validator contract: " + name)
        if not accepted and "FAIL:" not in result.stderr:
            raise SystemExit("FAIL: fixture was rejected without the expected validation failure: " + name)

print("PASS: 12 evidence identity/failure/gap contracts; fixture evidence is not native acceptance")
