"""Focused regression checks for scripts/summarize-coverage.py."""

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("summarize-coverage.py")
sys.dont_write_bytecode = True
SPEC = importlib.util.spec_from_file_location("coverage_summary", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class CoverageSummaryTests(unittest.TestCase):
    def test_summarizes_weighted_statements_by_package_and_disclaims_scope(self):
        with tempfile.TemporaryDirectory(dir=Path(__file__).resolve().parent.parent) as temporary:
            root = Path(temporary)
            profile = root / "coverage.out"
            inventory = root / "packages.json"
            tests = root / "tests.jsonl"
            profile.write_text(
                "mode: set\n"
                "example.test/mod/pkg/a.go:1.1,2.1 2 1\n"
                "example.test/mod/pkg/a.go:2.2,4.1 3 0\n"
                "example.test/mod/other/b.go:1.1,2.1 4 1\n"
                "example.test/mod/empty/empty.go:1.1,1.1 0 0\n",
                encoding="utf-8",
            )
            inventory.write_text(
                json.dumps(
                    {
                        "ImportPath": "example.test/mod/pkg",
                        "Dir": "/workspace/pkg",
                        "GoFiles": ["a.go"],
                    }
                )
                + "\n"
                + json.dumps(
                    {
                        "ImportPath": "example.test/mod/other",
                        "Dir": "/workspace/other",
                        "GoFiles": ["b.go"],
                        "TestGoFiles": ["b_test.go"],
                    }
                )
                + "\n"
                + json.dumps(
                    {
                        "ImportPath": "example.test/mod/empty",
                        "Dir": "/workspace/empty",
                        "GoFiles": ["empty.go"],
                    }
                )
                + "\n"
                + json.dumps(
                    {
                        "ImportPath": "example.test/mod/unseen",
                        "Dir": "/workspace/unseen",
                        "GoFiles": ["unseen.go"],
                    }
                ),
                encoding="utf-8",
            )
            packages = [
                "example.test/mod/pkg",
                "example.test/mod/other",
                "example.test/mod/empty",
                "example.test/mod/unseen",
            ]
            events = []
            for package in packages:
                if package in {"example.test/mod/empty", "example.test/mod/unseen"}:
                    events.append({"Action": "skip", "Package": package})
                    continue
                events.append({"Action": "start", "Package": package})
                if package == "example.test/mod/pkg":
                    events.extend(
                        [
                            {"Action": "run", "Package": package, "Test": "TestSample"},
                            {"Action": "pass", "Package": package, "Test": "TestSample"},
                        ]
                    )
                events.append({"Action": "pass", "Package": package})
            tests.write_text(
                "".join(json.dumps(event) + "\n" for event in events),
                encoding="utf-8",
            )

            revision = "a" * 40
            report = MODULE.summarize(profile, inventory, tests, revision, "go1.27.2")

            self.assertEqual(report["status"], "partial")
            self.assertEqual(report["test_run_conclusion"], "pass")
            self.assertEqual(report["source_revision"], revision)
            self.assertTrue(report["successful_test_run"])
            self.assertEqual(report["test_run"]["package_skip_count"], 2)
            self.assertEqual(report["test_run"]["package_skip_without_test_sources"], [
                "example.test/mod/empty",
                "example.test/mod/unseen",
            ])
            self.assertEqual(report["statement_count"], 9)
            self.assertEqual(report["covered_statement_count"], 6)
            self.assertAlmostEqual(report["statement_coverage_percent"], 66.666667)
            by_path = {item["import_path"]: item for item in report["packages"]}
            self.assertEqual(by_path["example.test/mod/pkg"]["statement_count"], 5)
            self.assertEqual(by_path["example.test/mod/pkg"]["covered_statement_count"], 2)
            self.assertEqual(by_path["example.test/mod/empty"]["status"], "no_countable_statements")
            self.assertEqual(by_path["example.test/mod/unseen"]["status"], "not_measured")
            self.assertFalse(report["threshold_enforced"])
            self.assertTrue(any("branch coverage" in item for item in report["limitations"]))

    def test_unavailable_report_is_explicit_and_does_not_invent_zero_coverage(self):
        report = MODULE.unavailable(
            "test command failed before producing a profile", "b" * 40, "missing-tests.jsonl"
        )
        self.assertEqual(report["status"], "unavailable")
        self.assertEqual(report["test_run_conclusion"], "unknown")
        self.assertIsNone(report["statement_coverage_percent"])
        self.assertFalse(report["threshold_enforced"])

    def test_failed_package_events_never_look_like_a_successful_run(self):
        with tempfile.TemporaryDirectory(dir=Path(__file__).resolve().parent.parent) as temporary:
            root = Path(temporary)
            profile = root / "coverage.out"
            inventory = root / "packages.json"
            tests = root / "tests.jsonl"
            profile.write_text(
                "mode: set\nexample.test/mod/pkg/a.go:1.1,2.1 2 1\n",
                encoding="utf-8",
            )
            inventory.write_text(
                json.dumps(
                    {
                        "ImportPath": "example.test/mod/pkg",
                        "Dir": "/workspace/pkg",
                        "GoFiles": ["a.go"],
                    }
                ),
                encoding="utf-8",
            )
            tests.write_text(
                "".join(
                    json.dumps(event) + "\n"
                    for event in (
                        {"Action": "start", "Package": "example.test/mod/pkg"},
                        {"Action": "run", "Package": "example.test/mod/pkg", "Test": "TestDenied"},
                        {"Action": "fail", "Package": "example.test/mod/pkg", "Test": "TestDenied"},
                        {"Action": "fail", "Package": "example.test/mod/pkg"},
                    )
                ),
                encoding="utf-8",
            )

            report = MODULE.summarize(profile, inventory, tests, "c" * 40)

            self.assertEqual(report["coverage_status"], "available")
            self.assertEqual(report["status"], "tests_failed")
            self.assertEqual(report["test_run_conclusion"], "fail")
            self.assertFalse(report["successful_test_run"])
            self.assertEqual(report["test_run"]["package_failure_count"], 1)
            self.assertEqual(report["test_run"]["test_failure_count"], 1)
            self.assertEqual(report["test_run"]["failure_labels"], [
                "test:example.test/mod/pkg:TestDenied",
                "package:example.test/mod/pkg",
            ])

    def test_skipped_package_with_test_sources_is_not_a_successful_run(self):
        with tempfile.TemporaryDirectory(dir=Path(__file__).resolve().parent.parent) as temporary:
            tests = Path(temporary) / "tests.jsonl"
            tests.write_text(
                "".join(
                    json.dumps(event) + "\n"
                    for event in (
                        {"Action": "start", "Package": "example.test/mod/pkg"},
                        {"Action": "skip", "Package": "example.test/mod/pkg"},
                    )
                ),
                encoding="utf-8",
            )
            test_run = MODULE.summarize_test_run(tests, {"example.test/mod/pkg": 1})
            self.assertEqual(test_run["conclusion"], "incomplete")
            self.assertEqual(test_run["package_unexpected_skips"], ["example.test/mod/pkg"])


if __name__ == "__main__":
    unittest.main()
