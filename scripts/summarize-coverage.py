"""Summarize Go coverprofile data as machine-readable statement coverage."""

import argparse
import hashlib
import json
import re
from pathlib import Path


SCHEMA_VERSION = 1
LIMITATIONS = [
    "This measures Go statement execution only; it is not branch coverage.",
    "It does not establish behavior correctness, acceptance conformance, or test adequacy.",
]
BLOCK_RE = re.compile(r"^(.+):(\d+)\.(\d+),(\d+)\.(\d+)\s+(\d+)\s+(\d+)$")


def _read_json_stream(path):
    text = path.read_text(encoding="utf-8")
    decoder = json.JSONDecoder()
    position = 0
    values = []
    while position < len(text):
        while position < len(text) and text[position].isspace():
            position += 1
        if position == len(text):
            break
        value, position = decoder.raw_decode(text, position)
        if not isinstance(value, dict):
            raise ValueError("package inventory must contain JSON objects")
        values.append(value)
    return values


def _path_key(value):
    normalized = str(value).replace("\\", "/").rstrip("/")
    if len(normalized) > 1 and normalized[1] == ":":
        normalized = normalized.lower()
    return normalized


def _package_for_file(source_file, packages):
    source = _path_key(source_file)
    matches = []
    for package in packages:
        import_path = package["import_path"]
        if source.startswith(import_path + "/"):
            matches.append((len(import_path), package))
            continue
        directory = package["directory"]
        if directory and source.startswith(directory + "/"):
            matches.append((len(directory), package))
    if not matches:
        return None
    matches.sort(key=lambda item: item[0], reverse=True)
    if len(matches) > 1 and matches[0][0] == matches[1][0]:
        raise ValueError("coverage source path has ambiguous package ownership")
    return matches[0][1]


def summarize_test_run(test_path, package_test_files=None):
    package_test_files = package_test_files or {}
    expected_packages = set(package_test_files)
    package_started = set()
    package_terminal = {}
    package_terminal_conflicts = set()
    package_skip_without_tests = set()
    package_unexpected_skips = set()
    test_pass_count = 0
    test_fail_count = 0
    test_skip_count = 0
    event_count = 0
    failure_labels = []
    failure_label_limit = 256
    try:
        with Path(test_path).open(encoding="utf-8") as stream:
            for line_number, line in enumerate(stream, start=1):
                if not line.strip():
                    continue
                event_count += 1
                try:
                    event = json.loads(line)
                except json.JSONDecodeError:
                    return _unknown_test_run("malformed_jsonl", event_count)
                if not isinstance(event, dict) or not isinstance(event.get("Action"), str):
                    return _unknown_test_run("invalid_event", event_count)
                action = event["Action"]
                package = event.get("Package")
                test = event.get("Test")
                if not isinstance(package, str):
                    package = ""
                if not isinstance(test, str):
                    test = ""
                if action == "start" and package and not test:
                    package_started.add(package)
                if test and action in {"pass", "fail", "skip"}:
                    if action == "pass":
                        test_pass_count += 1
                    elif action == "skip":
                        test_skip_count += 1
                    else:
                        test_fail_count += 1
                        if len(failure_labels) < failure_label_limit:
                            failure_labels.append(f"test:{package}:{test}")
                elif package and action in {"pass", "fail", "skip"}:
                    prior = package_terminal.get(package)
                    if prior and prior != action:
                        package_terminal_conflicts.add(package)
                    package_terminal[package] = action
                    if action == "skip":
                        if package in package_test_files and package_test_files[package] == 0:
                            package_skip_without_tests.add(package)
                        else:
                            package_unexpected_skips.add(package)
                    if action == "fail" and len(failure_labels) < failure_label_limit:
                        failure_labels.append(f"package:{package}")
    except OSError:
        return _unknown_test_run("missing_or_unreadable_jsonl", event_count)

    if not event_count:
        return _unknown_test_run("empty_jsonl", 0)
    package_pass_count = sum(action == "pass" for action in package_terminal.values())
    package_fail_count = sum(action == "fail" for action in package_terminal.values())
    package_skip_count = sum(action == "skip" for action in package_terminal.values())
    missing_terminal = sorted(package_started - package_terminal.keys())
    terminal_without_start = sorted(
        package_terminal.keys() - package_started - package_skip_without_tests
    )
    missing_package_events = sorted(expected_packages - package_terminal.keys())
    unexpected_package_events = sorted(package_terminal.keys() - expected_packages)
    failed = package_fail_count > 0 or test_fail_count > 0 or bool(package_terminal_conflicts)
    all_package_events_passed = (
        bool(package_terminal)
        and package_pass_count + len(package_skip_without_tests) == len(package_terminal)
        and package_fail_count == 0
        and package_skip_count == len(package_skip_without_tests)
        and not package_unexpected_skips
        and not missing_terminal
        and not terminal_without_start
        and not missing_package_events
        and not unexpected_package_events
        and not package_terminal_conflicts
        and test_fail_count == 0
        and test_pass_count > 0
    )
    if failed:
        conclusion = "fail"
    elif all_package_events_passed:
        conclusion = "pass"
    else:
        conclusion = "incomplete"
    return {
        "conclusion": conclusion,
        "evidence_status": "valid",
        "jsonl_event_count": event_count,
        "started_package_count": len(package_started),
        "package_event_count": len(package_terminal),
        "expected_package_count": len(expected_packages),
        "package_pass_count": package_pass_count,
        "package_failure_count": package_fail_count,
        "package_skip_count": package_skip_count,
        "package_skip_without_test_sources": sorted(package_skip_without_tests),
        "package_unexpected_skips": sorted(package_unexpected_skips),
        "package_missing_terminal": missing_terminal,
        "package_events_missing_from_jsonl": missing_package_events,
        "unexpected_package_events": unexpected_package_events,
        "package_terminal_without_start": terminal_without_start,
        "package_terminal_conflicts": sorted(package_terminal_conflicts),
        "test_pass_count": test_pass_count,
        "test_failure_count": test_fail_count,
        "test_skip_count": test_skip_count,
        "test_terminal_event_count": test_pass_count + test_fail_count + test_skip_count,
        "failure_labels": failure_labels,
        "failure_label_truncated": (package_fail_count + test_fail_count) > len(failure_labels),
    }


def _unknown_test_run(reason, event_count):
    return {
        "conclusion": "unknown",
        "evidence_status": reason,
        "jsonl_event_count": event_count,
        "started_package_count": 0,
        "package_event_count": 0,
        "expected_package_count": 0,
        "package_pass_count": 0,
        "package_failure_count": 0,
        "package_skip_count": 0,
        "package_skip_without_test_sources": [],
        "package_unexpected_skips": [],
        "package_missing_terminal": [],
        "package_events_missing_from_jsonl": [],
        "unexpected_package_events": [],
        "package_terminal_without_start": [],
        "package_terminal_conflicts": [],
        "test_pass_count": 0,
        "test_failure_count": 0,
        "test_skip_count": 0,
        "test_terminal_event_count": 0,
        "failure_labels": [],
        "failure_label_truncated": False,
    }


def summarize(profile_path, packages_path, test_path, source_revision, go_version=None):
    profile_path = Path(profile_path)
    packages_path = Path(packages_path)
    if not re.fullmatch(r"(?:[0-9a-f]{40}|[0-9a-f]{64})", source_revision):
        raise ValueError("source revision must be a lowercase 40- or 64-character hex digest")
    package_objects = _read_json_stream(packages_path)
    packages = []
    seen_imports = set()
    for raw in package_objects:
        import_path = raw.get("ImportPath")
        if not isinstance(import_path, str) or not import_path:
            raise ValueError("package inventory has an invalid ImportPath")
        if import_path in seen_imports:
            raise ValueError("package inventory contains duplicate ImportPath")
        seen_imports.add(import_path)
        go_files = raw.get("GoFiles", [])
        cgo_files = raw.get("CgoFiles", [])
        test_go_files = raw.get("TestGoFiles", [])
        external_test_files = raw.get("XTestGoFiles", [])
        if (
            not isinstance(go_files, list)
            or not isinstance(cgo_files, list)
            or not isinstance(test_go_files, list)
            or not isinstance(external_test_files, list)
        ):
            raise ValueError("package inventory has invalid source-file lists")
        test_source_file_count = len(test_go_files) + len(external_test_files)
        packages.append(
            {
                "import_path": import_path,
                "directory": _path_key(raw.get("Dir", "")),
                "source_file_count": len(go_files) + len(cgo_files),
                "test_source_file_count": test_source_file_count,
                "statements": 0,
                "covered_statements": 0,
                "files": set(),
            }
        )
    if not packages:
        raise ValueError("package inventory is empty")
    test_run = summarize_test_run(
        test_path,
        {package["import_path"]: package["test_source_file_count"] for package in packages},
    )

    lines = profile_path.read_text(encoding="utf-8").splitlines()
    if not lines or not lines[0].startswith("mode: "):
        raise ValueError("coverage profile has no valid mode header")
    mode = lines[0][6:].strip()
    if mode not in {"set", "count", "atomic"}:
        raise ValueError("coverage profile has an unsupported mode")

    package_by_import = {package["import_path"]: package for package in packages}
    total_statements = 0
    covered_statements = 0
    block_keys = set()
    unmapped = {}
    for line_number, line in enumerate(lines[1:], start=2):
        if not line.strip():
            continue
        match = BLOCK_RE.fullmatch(line)
        if not match:
            raise ValueError(f"invalid coverage block at profile line {line_number}")
        source_file, start_line, start_column, end_line, end_column, statement_text, count_text = match.groups()
        statements = int(statement_text)
        count = int(count_text)
        if (
            statements < 0
            or count < 0
            or int(start_line) <= 0
            or int(start_column) <= 0
            or int(end_line) < int(start_line)
            or int(end_column) <= 0
        ):
            raise ValueError(f"invalid coverage counts at profile line {line_number}")
        block_key = (source_file, start_line, start_column, end_line, end_column, statements)
        if block_key in block_keys:
            raise ValueError("coverage profile contains duplicate blocks")
        block_keys.add(block_key)
        total_statements += statements
        if count > 0:
            covered_statements += statements

        package = _package_for_file(source_file, packages)
        if package is None:
            item = unmapped.setdefault(source_file, {"statements": 0, "covered_statements": 0})
            item["statements"] += statements
            if count > 0:
                item["covered_statements"] += statements
            continue
        package["statements"] += statements
        if count > 0:
            package["covered_statements"] += statements
        package["files"].add(source_file)

    package_records = []
    unmeasured = []
    for package in sorted(packages, key=lambda item: item["import_path"]):
        statement_count = package["statements"]
        if statement_count:
            status = "measured"
            percent = round(package["covered_statements"] * 100 / statement_count, 6)
        elif package["files"]:
            status = "no_countable_statements"
            percent = None
        elif package["source_file_count"] == 0:
            status = "no_active_go_sources"
            percent = None
        else:
            status = "not_measured"
            percent = None
            unmeasured.append(package["import_path"])
        package_records.append(
            {
                "import_path": package["import_path"],
                "status": status,
                "go_source_file_count": package["source_file_count"],
                "test_source_file_count": package["test_source_file_count"],
                "profile_file_count": len(package["files"]),
                "statement_count": statement_count,
                "covered_statement_count": package["covered_statements"],
                "statement_coverage_percent": percent,
            }
        )

    partial = bool(unmapped or unmeasured)
    if test_run["conclusion"] == "fail":
        report_status = "tests_failed"
    elif test_run["conclusion"] != "pass":
        report_status = "test_run_incomplete"
    elif partial:
        report_status = "partial"
    else:
        report_status = "available"
    return {
        "schema_version": SCHEMA_VERSION,
        "status": report_status,
        "coverage_status": "partial" if partial else "available",
        "measurement": "go_statement_coverage",
        "coverage_mode": mode,
        "threshold_enforced": False,
        "successful_test_run": test_run["conclusion"] == "pass",
        "source_revision": source_revision,
        "test_run_conclusion": test_run["conclusion"],
        "test_run": test_run,
        "go_version": go_version,
        "profile_sha256": hashlib.sha256(profile_path.read_bytes()).hexdigest(),
        "statement_count": total_statements,
        "covered_statement_count": covered_statements,
        "statement_coverage_percent": (
            round(covered_statements * 100 / total_statements, 6)
            if total_statements
            else None
        ),
        "package_count": len(package_records),
        "measured_package_count": sum(item["status"] == "measured" for item in package_records),
        "unmeasured_packages": unmeasured,
        "packages": package_records,
        "unmapped_files": [
            {
                "source_file": source_file,
                "statement_count": value["statements"],
                "covered_statement_count": value["covered_statements"],
            }
            for source_file, value in sorted(unmapped.items())
        ],
        "limitations": LIMITATIONS,
    }


def unavailable(reason, source_revision, test_path, go_version=None):
    if not re.fullmatch(r"(?:[0-9a-f]{40}|[0-9a-f]{64})", source_revision):
        raise ValueError("source revision must be a lowercase 40- or 64-character hex digest")
    test_run = summarize_test_run(test_path)
    return {
        "schema_version": SCHEMA_VERSION,
        "status": "unavailable",
        "coverage_status": "unavailable",
        "measurement": "go_statement_coverage",
        "coverage_mode": None,
        "threshold_enforced": False,
        "successful_test_run": test_run["conclusion"] == "pass",
        "source_revision": source_revision,
        "test_run_conclusion": test_run["conclusion"],
        "test_run": test_run,
        "go_version": go_version,
        "reason": reason,
        "statement_count": 0,
        "covered_statement_count": 0,
        "statement_coverage_percent": None,
        "package_count": 0,
        "measured_package_count": 0,
        "unmeasured_packages": [],
        "packages": [],
        "unmapped_files": [],
        "limitations": LIMITATIONS,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", type=Path)
    parser.add_argument("--packages", type=Path)
    parser.add_argument("--tests", type=Path, required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--go-version")
    parser.add_argument("--unavailable-reason")
    args = parser.parse_args()

    if args.unavailable_reason:
        if args.profile or args.packages:
            parser.error("--unavailable-reason cannot be combined with profile inputs")
        try:
            report = unavailable(args.unavailable_reason, args.revision, args.tests, args.go_version)
        except ValueError as error:
            parser.error(str(error))
    else:
        if not args.profile or not args.packages:
            parser.error("--profile and --packages are required unless coverage is unavailable")
        try:
            report = summarize(args.profile, args.packages, args.tests, args.revision, args.go_version)
        except (OSError, ValueError, json.JSONDecodeError) as error:
            parser.error(str(error))

    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
