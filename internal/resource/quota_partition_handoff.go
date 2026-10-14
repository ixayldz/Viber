package resource

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	c "github.com/ixayldz/Viber/internal/contracts"
)

// PreparePartitionHandoff joins the stage move and owner partition intents in
// one durable admission record.  The allocation must still be RESERVED and
// its target is the stage path.  Keeping both records in one transaction is
// important: an interrupted restore must never have a partition without the
// corresponding move fence (or the reverse).
func (a Allocation) PreparePartitionHandoff(ctx context.Context, finalRoot string) error {
	if a.ledger == nil || !validID(a.ID) {
		return c.Fail(c.InvalidArgument, "physical quota allocation required")
	}
	stage, err := canonicalHandoffPath(a.targetRoot)
	if err != nil {
		return err
	}
	final, err := canonicalHandoffPath(finalRoot)
	if err != nil {
		return err
	}
	if stage == final || filepath.Dir(stage) != filepath.Dir(final) || filepath.Base(stage) == "." || filepath.Base(final) == "." {
		return c.Fail(c.UnsupportedCapability, "physical quota partition handoff requires distinct sibling paths")
	}
	if _, err = os.Lstat(stage); err == nil {
		return c.Fail(c.StaleAuthority, "physical quota partition handoff stage must not exist before preparation")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err = os.Lstat(final); err == nil {
		return c.Fail(c.Conflict, "physical quota partition handoff target already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, volume, parentPath, parentIdentity, _, stageExisted, _, err := targetVolume(stage)
	if err != nil {
		return err
	}
	finalRoot, finalVolume, finalParent, finalParentIdentity, _, finalExisted, _, err := targetVolume(final)
	if err != nil {
		return err
	}
	if finalRoot != final || stageExisted != 0 || finalExisted != 0 ||
		filepath.Clean(parentPath) != filepath.Clean(finalParent) || parentIdentity != finalParentIdentity || volume != finalVolume {
		return c.Fail(c.StaleAuthority, "physical quota partition handoff paths do not share one stable parent and volume")
	}

	return a.ledger.withLock(ctx, func() error {
		if err := ensureHandoffSchema(a.ledger.db); err != nil {
			return err
		}
		if err := ensureQuotaPartitionSchema(a.ledger.db); err != nil {
			return err
		}
		if err := validateHandoffRecordsOnOpen(a.ledger.db, a.ledger.root, a.ledger.Authority, a.ledger.LedgerID); err != nil {
			return err
		}
		if err := validateQuotaPartitionRecords(a.ledger.db, a.ledger.LedgerID); err != nil {
			return err
		}
		var partitionCount, handoffCount int
		if err := a.ledger.db.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM quota_partitions),(SELECT COUNT(*) FROM quota_handoffs)").Scan(&partitionCount, &handoffCount); err != nil {
			return err
		}
		if partitionCount >= maxQuotaPartitions || handoffCount >= maxHandoffRecords {
			return c.Fail(c.BudgetLimitReached, "physical quota partition handoff catalog is full")
		}
		allocation, err := loadHandoffAllocation(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if allocation.Status != "RESERVED" || filepath.Clean(allocation.TargetRoot) != stage || allocation.Volume != volume ||
			filepath.Clean(allocation.ParentPath) != filepath.Clean(parentPath) || allocation.ParentIdentity != parentIdentity ||
			allocation.TargetIdentity != parentIdentity || allocation.TargetExisted != 0 {
			return c.Fail(c.StaleAuthority, "physical quota partition handoff differs from its reserved stage witness")
		}
		if _, found, err := loadHandoffIntent(a.ledger.db, a.ID); err != nil {
			return err
		} else if found {
			return c.Fail(c.Conflict, "physical quota allocation already has a path handoff")
		}
		if _, found, err := loadQuotaPartition(a.ledger.db, a.ID); err != nil {
			return err
		} else if found {
			return c.Fail(c.Conflict, "physical quota allocation already has an owner partition")
		}
		var handoff handoffIntent
		handoff = handoffIntent{AllocationID: a.ID, SchemaVersion: quotaHandoffSchema, StageRoot: stage, FinalRoot: final,
			ParentPath: filepath.Clean(parentPath), ParentIdentity: parentIdentity, VolumeIdentity: volume, State: HandoffPrepared}
		handoff.OperationDigest, err = handoffOperationDigest(a.ledger, allocation, handoff)
		if err != nil {
			return err
		}
		partition := quotaPartition{AllocationID: a.ID, SchemaVersion: quotaPartitionSchema, Mode: IndependentOwnerPartition,
			RootPath: stage, OwnerPath: stage, State: "PREPARED"}
		partition.OperationDigest, err = c.Digest(partitionBinding{SchemaVersion: quotaPartitionSchema, LedgerID: a.ledger.LedgerID,
			AllocationID: a.ID, RequestDigest: allocation.RequestDigest, Mode: partition.Mode, RootPath: stage, OwnerPath: stage,
			ParentPath: filepath.Clean(parentPath), ParentIdentity: parentIdentity, VolumeIdentity: volume})
		if err != nil {
			return err
		}
		if err = admitMetadata(a.ledger.root, metadataAdmission); err != nil {
			return err
		}
		tx, err := a.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, `INSERT INTO quota_handoffs(allocation_id,schema_version,operation_digest,stage_root,final_root,parent_path,parent_identity,volume_identity,stage_identity,state)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, a.ID, quotaHandoffSchema, handoff.OperationDigest, stage, final,
			handoff.ParentPath, handoff.ParentIdentity, handoff.VolumeIdentity, "", string(HandoffPrepared)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO quota_partitions(allocation_id,schema_version,operation_digest,mode,root_path,owner_path,root_identity,owner_identity,volume_identity,state)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, a.ID, quotaPartitionSchema, partition.OperationDigest, string(partition.Mode), stage, stage, "", "", "", "PREPARED"); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// BindPartitionHandoffStage records the stage inode before any copied byte,
// then binds the same inode as an independent owner partition.  The two
// calls are intentionally retryable: a crash between them leaves the upper
// reservation charged and the next exact retry completes the missing bind.
func (a Allocation) BindPartitionHandoffStage(ctx context.Context) error {
	if err := a.BindHandoffStage(ctx); err != nil {
		return err
	}
	return a.BindPartitionRoot(ctx)
}

// CommitPartitionHandoff publishes a restore stage and settles its typed
// owner receipts in one ledger transaction.  Filesystem publication happens
// before the transaction while the original WRITING allocation remains
// charged; a crash at that boundary can only retry the exact bound inode.
func (a Allocation) CommitPartitionHandoff(ctx context.Context, gcSpecs ...GCPartitionSpec) (Allocation, error) {
	if a.ledger == nil || !validID(a.ID) {
		return Allocation{}, c.Fail(c.InvalidArgument, "physical quota allocation required")
	}
	var settled Allocation
	err := a.ledger.withLock(ctx, func() error {
		if err := ensureHandoffSchema(a.ledger.db); err != nil {
			return err
		}
		if err := ensureQuotaPartitionSchema(a.ledger.db); err != nil {
			return err
		}
		if err := validateHandoffRecordsOnOpen(a.ledger.db, a.ledger.root, a.ledger.Authority, a.ledger.LedgerID); err != nil {
			return err
		}
		if err := validateQuotaPartitionRecords(a.ledger.db, a.ledger.LedgerID); err != nil {
			return err
		}
		intent, found, err := loadHandoffIntent(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if !found {
			return c.Fail(c.StaleAuthority, "prepared physical quota partition handoff required")
		}
		partition, found, err := loadQuotaPartition(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if !found {
			return c.Fail(c.StaleAuthority, "prepared physical quota owner partition required")
		}
		allocation, err := loadHandoffAllocation(a.ledger.db, a.ID)
		if err != nil {
			return err
		}
		if intent.State == HandoffCommitted && partition.State == "COMMITTED" {
			if err = validateCommittedPartition(ctx, a.ledger, partition); err != nil {
				return err
			}
			settled = Allocation{ledger: a.ledger, ID: a.ID, targetRoot: intent.FinalRoot}
			return nil
		}
		if intent.State != HandoffStageBound || partition.State != "OWNER_BOUND" || allocation.Status != "WRITING" ||
			filepath.Clean(allocation.TargetRoot) != intent.StageRoot || filepath.Clean(partition.RootPath) != intent.StageRoot ||
			partition.OwnerPath != intent.StageRoot {
			return c.Fail(c.StaleAuthority, "bound physical quota partition handoff required")
		}
		if err = validateHandoffBinding(intent, allocation, a.ledger.LedgerID); err != nil {
			return err
		}
		if err = verifyHandoffParent(intent); err != nil {
			return err
		}
		// The publication rename is deliberately outside the settlement
		// transaction.  If the process dies after that rename, the durable
		// rows still describe the stage path while the exact bound inode is
		// already at the final path.  Validate the handoff witness first and
		// then verify whichever of those two paths currently owns the inode;
		// checking partition.RootPath unconditionally would turn that safe
		// retry into a false stale-authority failure.
		if err = validateBoundHandoffLocation(intent); err != nil {
			return err
		}
		stageInfo, stageErr := os.Lstat(intent.StageRoot)
		finalInfo, finalErr := os.Lstat(intent.FinalRoot)
		if stageErr != nil && !errors.Is(stageErr, os.ErrNotExist) || finalErr != nil && !errors.Is(finalErr, os.ErrNotExist) {
			return errors.Join(stageErr, finalErr)
		}
		boundRoot := intent.StageRoot
		if errors.Is(stageErr, os.ErrNotExist) && finalErr == nil {
			boundRoot = intent.FinalRoot
		}
		if stageInfo != nil && !stageInfo.IsDir() || finalInfo != nil && !finalInfo.IsDir() {
			return c.Fail(c.StaleAuthority, "physical quota partition handoff root is not a directory")
		}
		if err = verifyPartitionDirectory(boundRoot, partition.RootIdentity, partition.VolumeIdentity); err != nil {
			return err
		}
		if err = moveHandoffRoot(intent); err != nil {
			return err
		}

		// The stage inode is now at the final sibling.  Re-read the final path
		// through the same identity fence before measuring typed members.
		finalPartition := partition
		finalPartition.RootPath = intent.FinalRoot
		finalPartition.OwnerPath = intent.FinalRoot
		finalPartition.OperationDigest, err = c.Digest(partitionBinding{SchemaVersion: quotaPartitionSchema, LedgerID: a.ledger.LedgerID,
			AllocationID: a.ID, RequestDigest: allocation.RequestDigest, Mode: IndependentOwnerPartition,
			RootPath: intent.FinalRoot, OwnerPath: intent.FinalRoot, ParentPath: intent.ParentPath,
			ParentIdentity: intent.ParentIdentity, VolumeIdentity: intent.VolumeIdentity})
		if err != nil {
			return err
		}
		members, gcMembers, err := measurePartitionMembersWithGC(finalPartition, gcSpecs)
		if err != nil {
			return err
		}
		var measuredBytes, measuredEntries uint64
		for _, inventory := range members {
			measuredBytes, err = add(measuredBytes, inventory.ChargedBytes)
			if err == nil {
				measuredEntries, err = add(measuredEntries, inventory.Entries)
			}
			if err != nil {
				return err
			}
		}
		for _, inventory := range gcMembers {
			measuredBytes, err = add(measuredBytes, inventory.ChargedBytes)
			if err == nil {
				measuredEntries, err = add(measuredEntries, inventory.Entries)
			}
			if err != nil {
				return err
			}
		}
		if allocation.UpperBytes < 0 || allocation.UpperEntries < 0 || measuredBytes > uint64(allocation.UpperBytes) || measuredEntries > uint64(allocation.UpperEntries) {
			return c.Fail(c.BudgetLimitReached, "restored owner exceeds its admitted physical quota envelope")
		}
		if err = admitPartitionReceipts(a.ledger.root, members); err != nil {
			return err
		}
		tx, err := a.ledger.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		current, err := loadHandoffAllocationTx(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		currentIntent, found, err := loadHandoffIntentTx(ctx, tx, a.ID)
		if err != nil || !found {
			if err == nil {
				err = c.Fail(c.StaleAuthority, "physical quota handoff disappeared before settlement")
			}
			return err
		}
		currentPartition, found, err := loadQuotaPartitionTx(ctx, tx, a.ID)
		if err != nil || !found {
			if err == nil {
				err = c.Fail(c.StaleAuthority, "physical quota partition disappeared before settlement")
			}
			return err
		}
		if current.Status != "WRITING" || current.TargetRoot != intent.StageRoot || currentIntent.State != HandoffStageBound ||
			currentIntent.StageIdentity != intent.StageIdentity || currentPartition.State != "OWNER_BOUND" ||
			currentPartition.RootPath != intent.StageRoot || currentPartition.RootIdentity != partition.RootIdentity ||
			currentPartition.OwnerIdentity != partition.OwnerIdentity {
			return c.Fail(c.StaleAuthority, "physical quota partition handoff changed before settlement")
		}
		// Retarget the parent row before settlePartitionTx so its atomic
		// validation and the committed typed members use the final path.
		if _, err = tx.ExecContext(ctx, `UPDATE quota_allocations SET target_root=?,target_identity=?,target_existed=1 WHERE id=? AND status='WRITING' AND target_root=?`, intent.FinalRoot, intent.StageIdentity, a.ID, intent.StageRoot); err != nil {
			return err
		}
		if err = settlePartitionTx(ctx, tx, a.ledger, finalPartition, members); err != nil {
			return err
		}
		if err = settleGCPartitionTx(ctx, tx, a.ledger, finalPartition, gcSpecs, gcMembers); err != nil {
			return err
		}
		if err = checkPartitionCapacity(ctx, tx, a.ledger.Limits); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE quota_partitions SET operation_digest=?,root_path=?,owner_path=?,state='COMMITTED'
			WHERE allocation_id=? AND state='OWNER_BOUND' AND operation_digest=?`, finalPartition.OperationDigest, finalPartition.RootPath, finalPartition.OwnerPath, a.ID, partition.OperationDigest); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE quota_handoffs SET state='COMMITTED' WHERE allocation_id=? AND state='STAGE_BOUND' AND operation_digest=?`, a.ID, intent.OperationDigest); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		settled = Allocation{ledger: a.ledger, ID: a.ID, targetRoot: intent.FinalRoot}
		return nil
	})
	if err != nil {
		return Allocation{}, err
	}
	return settled, nil
}
