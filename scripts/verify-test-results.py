"""Fail closed when Go JSON evidence is truncated, failed, or skips required acceptance."""
import argparse
import json
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("evidence", type=Path)
parser.add_argument("--docker", action="store_true")
parser.add_argument("--migration", action="store_true")
parser.add_argument("--vault", action="store_true")
parser.add_argument("--privacy", action="store_true")
parser.add_argument("--retrieval", action="store_true")
args = parser.parse_args()
events = [json.loads(line) for line in args.evidence.read_text(encoding="utf-8-sig").splitlines() if line.strip()]
started = {(e["Package"], e["Test"]) for e in events if e.get("Action") == "run" and e.get("Test")}
passed = {(e["Package"], e["Test"]) for e in events if e.get("Action") == "pass" and e.get("Test")}
skipped = {(e["Package"], e["Test"]) for e in events if e.get("Action") == "skip" and e.get("Test")}
failed = [e for e in events if e.get("Action") == "fail"]
packages = {e["Package"] for e in events if e.get("Action") == "start"}
finished = {e["Package"] for e in events if e.get("Action") in {"pass", "skip"} and not e.get("Test")}
if failed or not passed or packages != finished or started != passed | skipped:
    raise SystemExit("FAIL: incomplete or failing Go test evidence")
required = set()
if args.retrieval:
    required |= {
        "TestContextEffectivenessRelevanceAndExactSources",
        "TestRealRepositoryOwnerRetrievalRecallSpansAndWarmInvalidation",
        "TestOwnerSearchReusesIndexButRechecksPolicyAndBytes",
        "TestBodyEvidencePrecedesPathAndMultipleIndependentSpansSurvive",
        "TestPerFileSpanLimitExplicitlyReportsMissingCoverage",
        "TestIntentLogPreservesRootErrorWithinBudgetAndMandatoryContext",
        "TestPartitionPlanCoversAllSourcesDeterministicallyWithinBounds",
        "TestPartitionByteBoundsAndSourceIdentityFailures",
        "TestOwnerLazyPartitionsExposePartialCoverageAndBindCursorAndPolicy",
        "TestRankedPartitionArgumentsRejectNullUnboundedAndOtherToolUse",
        "TestPartitionCapacityFallbackExplicitlyLabelsFullLiteralScope",
    }
if args.privacy:
    required |= {
        "TestTaskContentDeletionPurgesCASMaterializationsAndManagedBackups",
        "TestTaskContentDeletionRefusesUnknownActiveAndRetainedDescendants",
        "TestDeletionIntentRecoversAfterProcessRestartAndPartialUnlink",
        "TestDeletionRejectsStalePlanAndChangedManagedCopyWithoutFalseReceipt",
        "TestDeletionAuthorityLossAndConcurrentRestoredOwnerFailClosed",
        "TestPrivacyDeletesExportPreviewSupportAndInterruptedAllocation",
        "TestPrivacyDeletesInterruptedRestoreBeforePublication",
        "TestPrivacyCopyAddedAfterIntentRemainsPendingAndExactRetryRecovers",
        "TestPrivacyUnpublishedAttemptCrashIsPurgedWithParentAndCannotResurrect",
        "TestPrivacyStagedAttemptExactRetryReconcilesWithoutDuplicateLineage",
        "TestPrivacyStagedAttemptStaleAndLateContentCannotProduceFalsePurge",
        "TestPrivacyCopyAllocationRejectsForeignFileAndRootReplacement",
        "TestPrivacyDuplicateCommandIDRejectedBeforeIrreversibleIntent",
        "TestPrivacyCLIThroughOwnerPreviewDeleteDedupAndDeletedObservations",
        "TestPrivacyMissingStoreAndTaskCommandMismatchHaveNoEffects",
        "TestPrivacyAuthorityWriteOnceCASSurvivesReplayAndRejectsCorruption",
        "TestTaskDeletionPreservesHistoricalLedgerButRevokesContentSuccess",
        "TestTaskDeletionCannotWaiveRiskOrManufactureQuality",
    }
if args.vault:
    required.add("TestActualOSVaultRoundTripAndEnvironmentIsolation")
if args.docker:
    required |= {
		"TestActualDockerHostileProcessMatrix",
		"TestActualDockerPersistentFenceInventoryAndReopen",
        "TestDockerOfflineReadOnlyQuiescenceAndTimeout",
        "TestActualDockerNativeOrphanFencingPreventsLateCreateAndStart",
        "TestActualDockerAgentCheckRetainsEvidenceWithoutFalseVerification",
        "TestActualDockerIndependentObserverCandidateFinal",
        "TestActualObserverBaselineFailCandidatePassAndForgedPASS",
        "TestActualNativeOwnerCrashRecoveryFencesOrphanAndKeepsVerificationUnknown",
        "TestActualIndependentHiddenFailureOverridesSelfVerifiedOnlyInEvaluation",
    }
if args.migration:
    required |= {
        "TestMigrationAbruptProcessRecovery",
        "TestMigrationCancelledCommitStaysReadOnlyAndCanReconcile",
        "TestMigrationPreservesJournalCheckpointDedupAndBindsLineage",
        "TestApplicationMigrationValidatesBackupAndPreservesFullHistory",
        "TestCLIMigrationAndRestoreOfOldAndNewFormats",
    }
names = {name for _, name in passed}
missing = sorted(required - names)
if missing:
    raise SystemExit("FAIL: required acceptance did not pass: " + ", ".join(missing))
print(json.dumps({"status": "PASS", "passed": len(passed), "skipped": len(skipped), "failed": len(failed), "required": sorted(required)}))
