"""Fail closed when Go JSON evidence is truncated, failed, or skips required acceptance."""
import argparse
import json
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("evidence", type=Path)
parser.add_argument("--docker", action="store_true")
parser.add_argument("--rootless", action="store_true")
parser.add_argument("--disk-pressure", action="store_true")
parser.add_argument("--migration", action="store_true")
parser.add_argument("--vault", action="store_true")
parser.add_argument("--vault-failure", action="store_true")
parser.add_argument("--privacy", action="store_true")
parser.add_argument("--retention", action="store_true")
parser.add_argument("--retrieval", action="store_true")
parser.add_argument("--delivery", action="store_true")
args = parser.parse_args()

# Explicit package identity is part of mandatory acceptance. This static
# manifest is reviewed with the validator; test-name collisions cannot replace
# a skipped native test with an unrelated package PASS.
EXPECTED_TEST_PACKAGES = {
    "TestSupervisorShutdownCannotCompleteWhileStoreLockRemainsHeld": "github.com/ixayldz/Viber/internal/cli",
    "TestRetentionConsentRejectsImplicitScopeActorClockAndNoncanonicalDeadline": "github.com/ixayldz/Viber/internal/contracts",
    "TestRetentionExpiryRequiresDistinctActorElapsedDeadlineAndNoUnknownRisk": "github.com/ixayldz/Viber/internal/kernel",
    "TestRetentionActualExpiryPurgesManagedBackupWithoutChangingRiskLedger": "github.com/ixayldz/Viber/internal/agent",
    "TestRetentionRevisionConsentDedupAndEarlyForgeryPreserveContent": "github.com/ixayldz/Viber/internal/agent",
    "TestRetentionActualCrashRecoveryUsesImmutableExpiryIntent": "github.com/ixayldz/Viber/internal/agent",
    "TestRetentionBoundedPassDoesNotStarveEligibleTasksBehindActiveTasks": "github.com/ixayldz/Viber/internal/agent",
    "TestRetentionUnknownRiskAndRetainedDescendantRemainPinned": "github.com/ixayldz/Viber/internal/agent",
    "TestRetentionReservesPendingDeletionReceiptNamespacesAndRejectsRehashedConsent": "github.com/ixayldz/Viber/internal/agent",
    "TestRetentionPendingExpiryKeepsConsentAndPurgesRegisteredRestoredOwner": "github.com/ixayldz/Viber/internal/agent",
    "TestOwnerActualRetentionTimerPurgesAfterDeadlineAndStatusHasNoMutableAlias": "github.com/ixayldz/Viber/internal/owner",
    "TestRetentionCLIAndUIUseBoundedOwnerCommandsAndExplicitConsent": "github.com/ixayldz/Viber/internal/cli",
    "TestRetentionMissingStoreAndInvalidFlagsHaveNoEffects": "github.com/ixayldz/Viber/internal/cli",
    "TestUnlinkedOpenMetadataIsAbsentWhileHardlinkRemainsDenied": "github.com/ixayldz/Viber/internal/fileguard",
    "TestPrivacyOperationRetirementBoundsNativeDescriptorsAcrossLargeInventory": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyOperationLeasesRetireOnCloseAndActualCrashWithoutChangingPins": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyOperationRetirementPreservesActiveChildAndForeignCatalog": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyOperationRetirementAtCatalogLimitRestoresOnlyTypedCapacity": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyOperationRetirementRejectsWrongAuthorityScopeAndHardlink": "github.com/ixayldz/Viber/internal/agent",
    "TestExistingLeaseAndRetirementNeverAdoptMissingOrReplacementIdentity": "github.com/ixayldz/Viber/internal/fileguard",
    "TestActualBoundedFilesystemPressurePreservesControlPublicationAndReopen": "github.com/ixayldz/Viber/internal/diskguard",
    "TestPrivacyCapacityCLIAndUIRouteStoreScopeWithoutCreatingTasks": "github.com/ixayldz/Viber/internal/cli",
    "TestPrivacyCapacityMissingStoreAndInvalidFlagsHaveNoEffects": "github.com/ixayldz/Viber/internal/cli",
    "TestPrivacyReserveRemediationReplenishesPhysicalBytesWithoutChangingAuthorityOrLedger": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyPhysicalMetadataBytePressureRejectsBeforeIntentPublication": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyFullJournalSaturationPreservesControlWatermarkOnTenReopens": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyPendingAllocationReopensAndBindingAtomicallyPromotesExactCAS": "github.com/ixayldz/Viber/internal/store",
    "TestPrivacyPendingAllocationCorruptionCannotPublishCurrentAuthority": "github.com/ixayldz/Viber/internal/store",
    "TestPrivacyInitializationCrashRecoveryReusesPinnedPhysicalAllocation": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyInitializationRetryRejectsForeignRootAndRecoversOnlyGenesisTemps": "github.com/ixayldz/Viber/internal/agent",
    "TestActualDockerAgentCheckRetainsEvidenceWithoutFalseVerification": "github.com/ixayldz/Viber/internal/agent",
    "TestActualDockerHostileProcessMatrix": "github.com/ixayldz/Viber/internal/runner",
    "TestActualDockerIndependentObserverCandidateFinal": "github.com/ixayldz/Viber/internal/agent",
    "TestActualDockerMergedCandidateReverificationRunsFreshRegisteredChecks": "github.com/ixayldz/Viber/internal/agent",
    "TestActualDockerNativeOrphanFencingPreventsLateCreateAndStart": "github.com/ixayldz/Viber/internal/runner",
    "TestActualDockerPersistentFenceInventoryAndReopen": "github.com/ixayldz/Viber/internal/runner",
    "TestActualIndependentEvaluationPrivacyPurgesNativeReceipts": "github.com/ixayldz/Viber/internal/evaluation",
    "TestActualIndependentHiddenFailureOverridesSelfVerifiedOnlyInEvaluation": "github.com/ixayldz/Viber/internal/evaluation",
    "TestActualNativeOwnerCrashRecoveryFencesOrphanAndKeepsVerificationUnknown": "github.com/ixayldz/Viber/internal/agent",
    "TestActualOSVaultRoundTripAndEnvironmentIsolation": "github.com/ixayldz/Viber/internal/auth",
    "TestActualObserverBaselineFailCandidatePassAndForgedPASS": "github.com/ixayldz/Viber/internal/agent",
    "TestApplicationMigrationValidatesBackupAndPreservesFullHistory": "github.com/ixayldz/Viber/internal/agent",
    "TestAvailableBytesRemainsBoundToRenamedHandleWhenOldPathIsAFile": "github.com/ixayldz/Viber/internal/fileguard",
    "TestBodyEvidencePrecedesPathAndMultipleIndependentSpansSurvive": "github.com/ixayldz/Viber/internal/retrieval",
    "TestCLIMigrationAndRestoreOfOldAndNewFormats": "github.com/ixayldz/Viber/internal/cli",
    "TestContextEffectivenessRelevanceAndExactSources": "github.com/ixayldz/Viber/internal/retrieval",
    "TestDeletionAuthorityLossAndConcurrentRestoredOwnerFailClosed": "github.com/ixayldz/Viber/internal/agent",
    "TestDeletionIntentRecoversAfterProcessRestartAndPartialUnlink": "github.com/ixayldz/Viber/internal/agent",
    "TestDeletionRejectsStalePlanAndChangedManagedCopyWithoutFalseReceipt": "github.com/ixayldz/Viber/internal/agent",
    "TestDeliveryReverificationCLIUsesOwnerAndDeduplicatesImmutableCreation": "github.com/ixayldz/Viber/internal/cli",
    "TestDeliveryReverificationMissingStoreAndTaskMismatchHaveNoEffects": "github.com/ixayldz/Viber/internal/cli",
    "TestDockerOfflineReadOnlyQuiescenceAndTimeout": "github.com/ixayldz/Viber/internal/runner",
    "TestEvaluationPrivacyChildFirstPurgesReportsBackupsAndRestoredOwners": "github.com/ixayldz/Viber/internal/agent",
    "TestEvaluationPrivacyForeignReportAndOwnerReplacementFailClosed": "github.com/ixayldz/Viber/internal/agent",
    "TestEvaluationPrivacyFreshOwnersHaveUniqueReservedTaskIdentities": "github.com/ixayldz/Viber/internal/agent",
    "TestEvaluationPrivacyUnpublishedProcessCrashPurgesImportedCandidate": "github.com/ixayldz/Viber/internal/agent",
    "TestIntentLogPreservesRootErrorWithinBudgetAndMandatoryContext": "github.com/ixayldz/Viber/internal/context",
    "TestMergedReverificationImportsFreshImmutableCandidateWithoutOldAuthority": "github.com/ixayldz/Viber/internal/agent",
    "TestMergedReverificationReconcilesCreatedTaskAfterRestart": "github.com/ixayldz/Viber/internal/agent",
    "TestMergedReverificationRejectsStaleTamperedAndUnregisteredPreviews": "github.com/ixayldz/Viber/internal/agent",
    "TestMigrationAbruptProcessRecovery": "github.com/ixayldz/Viber/internal/store",
    "TestMigrationCancelledCommitStaysReadOnlyAndCanReconcile": "github.com/ixayldz/Viber/internal/store",
    "TestMigrationPreservesJournalCheckpointDedupAndBindsLineage": "github.com/ixayldz/Viber/internal/store",
    "TestOperatorDeliveryPreviewPreservesUserBytesAndHistoricalTask": "github.com/ixayldz/Viber/internal/agent",
    "TestOwnerLazyPartitionsExposePartialCoverageAndBindCursorAndPolicy": "github.com/ixayldz/Viber/internal/agent",
    "TestOwnerSearchReusesIndexButRechecksPolicyAndBytes": "github.com/ixayldz/Viber/internal/agent",
    "TestPartitionByteBoundsAndSourceIdentityFailures": "github.com/ixayldz/Viber/internal/retrieval",
    "TestPartitionCapacityFallbackExplicitlyLabelsFullLiteralScope": "github.com/ixayldz/Viber/internal/agent",
    "TestPartitionPlanCoversAllSourcesDeterministicallyWithinBounds": "github.com/ixayldz/Viber/internal/retrieval",
    "TestPerFileSpanLimitExplicitlyReportsMissingCoverage": "github.com/ixayldz/Viber/internal/retrieval",
    "TestPrivacyAuthorityWriteOnceCASSurvivesReplayAndRejectsCorruption": "github.com/ixayldz/Viber/internal/store",
    "TestPrivacyCLIThroughOwnerPreviewDeleteDedupAndDeletedObservations": "github.com/ixayldz/Viber/internal/cli",
    "TestPrivacyCopyAddedAfterIntentRemainsPendingAndExactRetryRecovers": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyCopyAllocationRejectsForeignFileAndRootReplacement": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyDeletesExportPreviewSupportAndInterruptedAllocation": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyDeletesInterruptedRestoreBeforePublication": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyDuplicateCommandIDRejectedBeforeIrreversibleIntent": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyExternalPhysicalReserveReplacementStopsPublication": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyMetadataPressurePreservesDeletionClassAndBoundsTemporaryBytes": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyMissingStoreAndTaskCommandMismatchHaveNoEffects": "github.com/ixayldz/Viber/internal/cli",
    "TestPrivacyOwnerCatalogRejectsBeforeLeaseAndExistingOwnerCanReopenAndDelete": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyStagedAttemptExactRetryReconcilesWithoutDuplicateLineage": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyStagedAttemptStaleAndLateContentCannotProduceFalsePurge": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyUnpublishedAttemptCrashIsPurgedWithParentAndCannotResurrect": "github.com/ixayldz/Viber/internal/agent",
    "TestPrivacyWorkJournalSaturationStillPublishesRealDeletionIntent": "github.com/ixayldz/Viber/internal/agent",
    "TestRankedPartitionArgumentsRejectNullUnboundedAndOtherToolUse": "github.com/ixayldz/Viber/internal/agent",
    "TestRealRepositoryOwnerRetrievalRecallSpansAndWarmInvalidation": "github.com/ixayldz/Viber/internal/agent",
    "TestRootedReserveOpeningNeverAdoptsReplacementDirectory": "github.com/ixayldz/Viber/internal/diskguard",
    "TestTaskContentDeletionPurgesCASMaterializationsAndManagedBackups": "github.com/ixayldz/Viber/internal/agent",
    "TestTaskContentDeletionRefusesUnknownActiveAndRetainedDescendants": "github.com/ixayldz/Viber/internal/agent",
    "TestTaskDeletionCannotWaiveRiskOrManufactureQuality": "github.com/ixayldz/Viber/internal/kernel",
    "TestTaskDeletionPreservesHistoricalLedgerButRevokesContentSuccess": "github.com/ixayldz/Viber/internal/kernel",
    "TestVaultExecutableTrustIncludesEveryResolvedAncestor": "github.com/ixayldz/Viber/internal/auth",
    "TestVaultProcessBoundsTimeoutOverflowAndInheritedPipeDrain": "github.com/ixayldz/Viber/internal/auth",
    "TestVaultResponseFailureMatrixNeverAdoptsNoisyOrNoncanonicalKey": "github.com/ixayldz/Viber/internal/auth",
    "TestVaultRotationFailureKeepsExistingCiphertextAndAccountIdentity": "github.com/ixayldz/Viber/internal/auth"
}

# Parent PASS cannot waive a skipped native resource-bound scenario.
HOSTILE_SCENARIOS = {
    "TestActualDockerHostileProcessMatrix/descendants-ignore-term",
    "TestActualDockerHostileProcessMatrix/output-flood",
    "TestActualDockerHostileProcessMatrix/scratch-exhaustion",
    "TestActualDockerHostileProcessMatrix/pid-exhaustion",
    "TestActualDockerHostileProcessMatrix/memory-exhaustion",
}
EXPECTED_TEST_PACKAGES.update({name: "github.com/ixayldz/Viber/internal/runner" for name in HOSTILE_SCENARIOS})
RETENTION_ACCEPTANCE = {
    "TestRetentionConsentRejectsImplicitScopeActorClockAndNoncanonicalDeadline",
    "TestRetentionExpiryRequiresDistinctActorElapsedDeadlineAndNoUnknownRisk",
    "TestRetentionActualExpiryPurgesManagedBackupWithoutChangingRiskLedger",
    "TestRetentionRevisionConsentDedupAndEarlyForgeryPreserveContent",
    "TestRetentionActualCrashRecoveryUsesImmutableExpiryIntent",
    "TestRetentionBoundedPassDoesNotStarveEligibleTasksBehindActiveTasks",
    "TestRetentionUnknownRiskAndRetainedDescendantRemainPinned",
    "TestRetentionReservesPendingDeletionReceiptNamespacesAndRejectsRehashedConsent",
    "TestRetentionPendingExpiryKeepsConsentAndPurgesRegisteredRestoredOwner",
    "TestOwnerActualRetentionTimerPurgesAfterDeadlineAndStatusHasNoMutableAlias",
    "TestRetentionCLIAndUIUseBoundedOwnerCommandsAndExplicitConsent",
    "TestRetentionMissingStoreAndInvalidFlagsHaveNoEffects",
}

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
if args.retention or args.privacy:
    required |= RETENTION_ACCEPTANCE
if args.docker or args.rootless:
    required |= HOSTILE_SCENARIOS
if args.delivery:
    required |= {
        "TestMergedReverificationImportsFreshImmutableCandidateWithoutOldAuthority",
        "TestMergedReverificationRejectsStaleTamperedAndUnregisteredPreviews",
        "TestMergedReverificationReconcilesCreatedTaskAfterRestart",
        "TestDeliveryReverificationCLIUsesOwnerAndDeduplicatesImmutableCreation",
        "TestDeliveryReverificationMissingStoreAndTaskMismatchHaveNoEffects",
        "TestOperatorDeliveryPreviewPreservesUserBytesAndHistoricalTask",
    }
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
if args.disk_pressure:
    required.add("TestActualBoundedFilesystemPressurePreservesControlPublicationAndReopen")
if args.privacy:
    required |= {
        "TestSupervisorShutdownCannotCompleteWhileStoreLockRemainsHeld",
        "TestUnlinkedOpenMetadataIsAbsentWhileHardlinkRemainsDenied",
        "TestPrivacyOperationRetirementBoundsNativeDescriptorsAcrossLargeInventory",
        "TestPrivacyOperationLeasesRetireOnCloseAndActualCrashWithoutChangingPins",
        "TestPrivacyOperationRetirementPreservesActiveChildAndForeignCatalog",
        "TestPrivacyOperationRetirementAtCatalogLimitRestoresOnlyTypedCapacity",
        "TestPrivacyOperationRetirementRejectsWrongAuthorityScopeAndHardlink",
        "TestExistingLeaseAndRetirementNeverAdoptMissingOrReplacementIdentity",
        "TestPrivacyCapacityCLIAndUIRouteStoreScopeWithoutCreatingTasks",
        "TestPrivacyCapacityMissingStoreAndInvalidFlagsHaveNoEffects",
        "TestPrivacyReserveRemediationReplenishesPhysicalBytesWithoutChangingAuthorityOrLedger",
        "TestPrivacyPhysicalMetadataBytePressureRejectsBeforeIntentPublication",
        "TestPrivacyFullJournalSaturationPreservesControlWatermarkOnTenReopens",
        "TestPrivacyPendingAllocationReopensAndBindingAtomicallyPromotesExactCAS",
        "TestPrivacyPendingAllocationCorruptionCannotPublishCurrentAuthority",
        "TestPrivacyInitializationCrashRecoveryReusesPinnedPhysicalAllocation",
        "TestPrivacyInitializationRetryRejectsForeignRootAndRecoversOnlyGenesisTemps",
        "TestTaskContentDeletionPurgesCASMaterializationsAndManagedBackups",
        "TestPrivacyMetadataPressurePreservesDeletionClassAndBoundsTemporaryBytes",
        "TestPrivacyWorkJournalSaturationStillPublishesRealDeletionIntent",
        "TestPrivacyOwnerCatalogRejectsBeforeLeaseAndExistingOwnerCanReopenAndDelete",
        "TestPrivacyExternalPhysicalReserveReplacementStopsPublication",
        "TestRootedReserveOpeningNeverAdoptsReplacementDirectory",
        "TestAvailableBytesRemainsBoundToRenamedHandleWhenOldPathIsAFile",
        "TestEvaluationPrivacyChildFirstPurgesReportsBackupsAndRestoredOwners",
        "TestEvaluationPrivacyUnpublishedProcessCrashPurgesImportedCandidate",
        "TestEvaluationPrivacyFreshOwnersHaveUniqueReservedTaskIdentities",
        "TestEvaluationPrivacyForeignReportAndOwnerReplacementFailClosed",
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
if args.vault_failure:
    required |= {
        "TestVaultResponseFailureMatrixNeverAdoptsNoisyOrNoncanonicalKey",
        "TestVaultExecutableTrustIncludesEveryResolvedAncestor",
        "TestVaultProcessBoundsTimeoutOverflowAndInheritedPipeDrain",
        "TestVaultRotationFailureKeepsExistingCiphertextAndAccountIdentity",
    }
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
        "TestActualIndependentEvaluationPrivacyPurgesNativeReceipts",
        "TestActualDockerMergedCandidateReverificationRunsFreshRegisteredChecks",
    }
if args.rootless:
    required |= {
        "TestActualDockerHostileProcessMatrix",
        "TestActualDockerPersistentFenceInventoryAndReopen",
        "TestDockerOfflineReadOnlyQuiescenceAndTimeout",
        "TestActualDockerNativeOrphanFencingPreventsLateCreateAndStart",
    }
if args.migration:
    required |= {
        "TestMigrationAbruptProcessRecovery",
        "TestMigrationCancelledCommitStaysReadOnlyAndCanReconcile",
        "TestMigrationPreservesJournalCheckpointDedupAndBindsLineage",
        "TestApplicationMigrationValidatesBackupAndPreservesFullHistory",
        "TestCLIMigrationAndRestoreOfOldAndNewFormats",
    }
unmapped = required - EXPECTED_TEST_PACKAGES.keys()
if unmapped:
    raise SystemExit("FAIL: mandatory acceptance has no reviewed package identity: " + ", ".join(sorted(unmapped)))
expected = {(EXPECTED_TEST_PACKAGES[name], name) for name in required}
missing = sorted(package + ":" + name for package, name in expected - passed)
if missing:
    raise SystemExit("FAIL: required acceptance did not pass: " + ", ".join(missing))
print(json.dumps({"status": "PASS", "passed": len(passed), "skipped": len(skipped), "failed": len(failed), "required": sorted(required)}))
