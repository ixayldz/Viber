# Restore and evaluator owner quota handoff

Status: partial implementation. The evaluator report and its nested owner now use a shared nonoverlapping quota partition. Restore stage integration and standalone independent evaluator bootstrap remain open. Proposed sections below describe their required contracts; they are not acceptance results. Source-specific implementation and CI evidence are recorded in [VALIDATION](VALIDATION.md).

13 October follow-up: `internal/resource/quota_handoff.go` now implements the narrow sibling stage-to-target primitive: durable prepare before mkdir, root identity binding before content, no-replace rename/sync, same-row final settlement and exact crash-boundary retry. OpenPinned validates its bounded schema/records; ordinary Commit cannot bypass a pending move. Resource regressions and registered-store downgrade/rollback protocol gates pass locally. This does **not** implement owner bootstrap/partition or bind `restoreBackup`/`OpenIndependentEvaluator`; those integrations below remain design/open. Exact-source native acceptance is still required.

## Scope and authority

The managed-writer inventory identifies `restoreBackup` and `OpenIndependentEvaluator` as unbound writers. The required behavior is described in the [managed-writer inventory](MANAGED_WRITER_INVENTORY.md) and package 2 of the [keyless delivery plan](KEYLESS_DELIVERY_PLAN.md). This design implements the product requirements in [PRD §11](../prd.md), [§13.3](../prd.md), [§14](../prd.md), [§20](../prd.md), [§23](../prd.md), [§27](../prd.md), and [§28, scenario 34](../prd.md).

Relevant code today is in [`restoreBackup`](../internal/agent/backup.go), [`OpenIndependentEvaluator` and report registration](../internal/agent/evaluation_privacy.go), [`RunCandidate`](../internal/evaluation/native.go), and the [physical quota ledger](../internal/resource/quota.go).

## Existing API boundary

`Ledger.Reserve` binds one target path to a volume and parent/root identity and charges byte and entry upper bounds. `Allocation.Begin` moves that allocation to `WRITING`. `Allocation.Commit` accepts an inventory only when its `RootPath` equals the reserved target and its root identity matches the admission witness. A begun allocation remains charged after a crash. These are good admission and settlement primitives for writers that keep one root path for their whole lifetime.

They do not yet represent either lifecycle here:

- Restore writes a fresh `.restore-*` stage and then renames it to the final target. The stage and target have the same directory inode after rename, but `Commit` still requires the stage path. Committing before rename leaves a retained receipt at a path that no longer exists; committing after rename fails the path binding.
- An evaluator owner is long-lived and receives later SQLite and CAS writes. While an enclosing allocation is `WRITING`, `EnclosingReservation` intentionally lets nested writers reuse it and suppresses their own base/standby accounting. Committing that allocation as a full owner-tree receipt and later reopening the owner imports overlapping SQLite, control-reserve, and archive receipts. Leaving the enclosing allocation `WRITING` permanently would instead let future nested writes bypass their own admission.
- In `RunCandidate`, an `EVALUATION_REPORT` allocation encloses `owner/`. `RegisterEvaluationReport` records an `ExcludedStore` for privacy deletion inventory, but the physical `resource.Measure` of the report root still includes `owner/**`. The report's `WRITING` reservation therefore covers child bootstrap writes. A later reopen can import those child files again as separate retained allocations. The privacy exclusion is not a physical-quota exclusion.

No restore/evaluator caller should work around these limits by committing an unmeasured root-only receipt, releasing a `WRITING` allocation on error, keeping a broad reservation active indefinitely, or trusting a lexical path after rename.

## Audit evidence

The evaluator overlap is visible in the existing deterministic path: [`RunCandidate`](../internal/evaluation/native.go#L144) measures and commits the complete output root at [the final settlement](../internal/evaluation/native.go#L424), while [`RegisterEvaluationReport`](../internal/agent/evaluation_privacy.go#L46) stores `ExcludedStore` only in the privacy record. The existing privacy integration test then reopens `bundle/owner` with `OpenExisting` at [the reopen step](../internal/evaluation/native_privacy_test.go#L47). On that reopen, the archive's [`BindPhysicalQuota`](../internal/artifact/archive.go#L103) imports the existing archive root through `ImportRetained` at [this call](../internal/artifact/archive.go#L116); the report is already `RETAINED`, so it is no longer an enclosing `WRITING` reservation. Store startup similarly reattaches SQLite and control-reserve accounting. The integration test currently checks deletion behavior, not before/after ledger capacity, so it does not detect the overlap.

The restore code path still validates and stages files, then publishes with [`RenameNoReplace`](../internal/agent/backup.go#L788) and registers the restored scope. It has no corresponding `PhysicalQuota.Reserve`, `Begin`, or settlement call. This is the existing unbound-writer gap, not a regression introduced by the project-memory lineage changes in commit `18188ea`; commit `1765582` changed support export, delivery preview, and GC paths, not restore or evaluator code.

## Proposed ledger handoff

Use one durable parent allocation for pre-publication admission, then hand its measured bytes to a non-overlapping durable accounting layout in a single ledger transaction. The operation needs a typed plan and two durable phases:

1. `PrepareHandoff(ctx, plan)` persists the exact operation digest, authority/ledger ID, writer family, source root path and identity, final root path and parent identity when applicable, volume identity, upper bounds, and intended member layout. It is idempotent only for the same request and identities. A different request at any reserved path is rejected.
2. After writes and any filesystem rename are durable, `CommitHandoff(ctx, receipt)` remeasures the final tree and every member, verifies the recorded identities and exact partition, and atomically replaces the parent outstanding amount with non-overlapping retained member receipts and future standbys. No observer can see a gap between releasing parent charge and installing child charge.

An API shape for review is:

```go
type HandoffPlan struct {
    OperationDigest string
    StageRoot       string
    FinalRoot       string // equal to StageRoot for a non-moved evaluator owner
    Members         []HandoffMember
}

type HandoffMember struct {
    ID             string // stable authority-scoped identity
    Kind           string // OWNER_REMAINDER, MUTABLE_SQLITE, CONTROL_RESERVE, ARCHIVE
    Root           string
    UpperBytes     uint64
    UpperEntries   uint64
    StandbyBytes   uint64
    StandbyEntries uint64
}

func (a Allocation) PrepareHandoff(ctx context.Context, plan HandoffPlan) error
func (a Allocation) CommitHandoff(ctx context.Context, receipt HandoffReceipt) error
func (l *Ledger) ReconcileHandoff(ctx context.Context, operationDigest string) (HandoffStatus, error)
```

The names are provisional. The required semantics are binding; a different API is acceptable only if it proves the same facts.

`CommitHandoff` must prove that the member inventories are pairwise non-overlapping at the file and directory-entry level, that every measured file and directory is represented exactly once, and that their charged bytes and entries equal the final tree inventory. It must not merely sum overlapping `Inventory` values. Mutable SQLite and control-reserve members need their typed base and standby rows created or adopted in the same transaction. The archive member needs its retained seed registered in the same transaction. The residual owner member accounts for runtime metadata and any files not owned by one of those typed writers. Existing owner-open hooks must then attach to these exact receipts idempotently.

For an enclosing report allocation, the owner is a child member of that same allocation family. The report remainder and owner member must settle together. `ExcludedStore` and the quota handoff must bind the same child instance, path, root identity, and volume, so the owner bytes are charged once while remaining separately deletable. If a writer discovers an enclosing allocation, it must join the registered member slot; it may not create an overlapping independent `WRITING` row.

## Restore lifecycle

Restore has an immutable source manifest, so admission can be calculated before creating the stage:

- Sum every validated `BackupFile.Size`, include the largest concurrent `fileguard.Publish` temporary alongside the final files, and include bounded restore-generated files such as the runtime instance, store sidecars, and owner `control.reserve`.
- Count the root, every unique parent directory, every final file, and the maximum simultaneous temporary entry. Add explicit bounded metadata allowance for directory and filesystem allocation charges. Use checked arithmetic and reject overflow or any value above the writer profile.
- Keep logical bytes, physical allocation, directory charge, temporary upper, and configured upper as separate measurements. The manifest's logical byte total is not the final physical receipt.

The physical allocation is reserved and begun before the `.restore-*` root is created. The existing restore operation lease and external privacy allocation must also bind the same operation digest, backup digest, stage path, final target path, source identity, authority/current checkpoint, and deletion watermark before the first copied byte. If any authority cannot record that intent, the operation stops and the physical reservation remains charged.

The target and stage must be on the same measured volume. The stage and final target parent identity are recorded before writing. After validation, the sequence is: durable restore intent; no-replace stage-to-target rename; sync the containing directory; remeasure the root at the final path; verify the root inode is the admitted stage inode; then atomically commit the handoff. The final receipt is rooted at the final path and retains the exact root and member identities.

The final owner inventory must be split into a residual owner receipt, mutable SQLite/control-reserve accounting, and archive accounting before future normal owner writes begin. This is what prevents a restored tree's initial full-copy charge from overlapping future owner receipts. The existing privacy checkpoint rebase, registered restored scope, restore binding, watermark, and managed-copy lifecycle remain independently required; physical quota settlement does not grant restore authority.

## Evaluator owner lifecycle

`OpenIndependentEvaluator` must use the parent's authority-pinned `PhysicalQuota` ledger. A missing ledger is a hard refusal. Its request digest binds the parent instance and ledger, canonical destination, evaluator operation identity, and the frozen source/assignment when available. The allocation begins before the owner directory exists. The child store's initial database, owner lock, control reserve, runtime instance, archive root, and any inherited CAS content are charged by the same family budget.

For a standalone child owner, its bootstrap handoff settles the initial tree into the exact residual/SQLite/control-reserve/archive members. On reopen, the child store and archive attach to those saved member IDs; neither owner open nor archive binding may import the same bytes again. Future writes reserve only their measured component upper before the first mutation and update the member receipt without recharging the existing bytes.

When the child is nested under an evaluation report, the report allocation is the one parent admission. `OpenIndependentEvaluator` joins a predeclared owner member under it rather than making an overlapping allocation. Before report publication, one ledger transaction settles the report remainder and the child owner member. The report member excludes the owner subtree physically as well as the privacy registry's `ExcludedStore`; the child member is then durable across report inspection and owner reopen.

The parent and evaluator remain on the same authority/ledger ID. No child-local quota may be minted, reset, or adopted. A failed or unknown evaluator operation remains charged even when the report contains no successful verdict.

## Crash and identity invariants

| Crash point | Required durable view and recovery |
|---|---|
| After reservation, before `Begin` | The exact `RESERVED` upper remains charged. An exact retry may resume only the same operation ID/digest and target witness; a different intent is rejected. |
| After `Begin`, before mkdir or first file | The full upper remains outstanding. Missing output alone does not release it; reconciliation must establish the exact operation and absence under the pinned parent/volume before any explicit retirement path. |
| During copy/bootstrap | Partial output and upper remain charged. Reopen measures only the exact admitted root and member paths; foreign entries, replaced parents, links, volume changes, or incomplete receipts block handoff and preserve charge. |
| After restore rename, before ledger settlement | Durable transfer intent identifies both paths and expected inode. If only the exact final inode is present, reconciliation may finish settlement after remeasurement. If only the exact stage inode remains, it can retry the same no-replace publication. Both paths present, neither path present, a replacement inode, or changed volume remains UNKNOWN/charged. |
| During evaluator owner handoff | The parent stays outstanding until a single ledger transaction creates all non-overlapping member receipts and standbys. There is no interval where the parent is released and child members are absent. |
| After ledger settlement, before owner re-open | All initial bytes are represented by the residual and typed member receipts. A fresh open attaches those same IDs; capacity is unchanged by attachment. |
| After later owner mutation or crash | The writer's member upper remains outstanding until exact member refresh. A failed measurement cannot erase the previous retained receipt or free the attempted upper. |

All identity checks include the authority ID, ledger ID, physical root, volume identity, canonical path, directory/file inode identity, operation digest, and member kind. A path match alone is never proof. Logical free space or a successful rename is not a quota receipt.

## Required acceptance before calling these writers bound

The code and exact-SHA validation record must include:

- Exhausted payload bytes/entries deny restore and evaluator allocation before their first mkdir or temp file; the control reserve remains available for bounded cleanup/control publication.
- Actual restore upper-bound tests for maximum file, largest temporary, directory count, overflow, symlink/foreign entry, different-volume target, stale source backup, and target appearance after admission.
- Child-process death before/after reserve, begin, first write, database open, report registration, no-replace rename, directory sync, handoff transaction, and owner reopen. Every unresolved case remains outstanding or unknown, and no allocation is counted twice.
- Physical low-space/ENOSPC on the supported filesystem profile, delayed target creation, cancellation, allocator failure, replacement root/parent, altered member inventory, duplicate handoff, stale ledger pin, and exact retry.
- Restore final target opens with its exact pinned privacy and physical-quota authority, current checkpoint, and deletion watermark. A pre-rename stage is not an owner, and old backup metadata cannot resurrect a deleted task.
- Evaluator parent and child report/owner use one ledger ID. Capacity before and after child reopen is identical absent a write; the report receipt excludes exactly the owner's bytes while the owner receipt covers them exactly once. Child deletion purges only its owned scope and does not drop parent report/source charges.
- Evaluator lifecycle tests use deterministic fixture execution only. Real provider/model acceptance remains separate, and this change makes no quality or readiness claim.

This design satisfies only the physical admission/handoff part of package 2 when implemented and accepted. It does not close external privacy authority/checkpoint/migration accounting, native fence identity quota, rootless or power-loss profiles, or the remaining release gates in the PRD delivery plan.

Implementation follow-up: evaluator report output now prepares and binds a nonoverlapping owner partition before bootstrap writes, then atomically settles REPORT_REMAINDER, OWNER_STATIC, MUTABLE_SQLITE, CONTROL_RESERVE and ARCHIVE receipts. Native default-32MiB tests cover reopen, exact later task/artifact and GC writes, and denial of foreign bytes. The original upper envelope remains charged on uncertainty. Restore callsites and full metadata/fence/legacy lifetime remain open; this is not complete A10/A11 acceptance.

## Restore integration sequence after the 14 October review

This is the implementation plan for the remaining restore writer, not an acceptance result. Publish the corrected model/evaluator snapshot only after its exact-source CI and package gates pass; implement and validate this next slice independently.

1. Read the backup database's saved `PhysicalQuotaBinding` through a bounded, read-only helper. Do not use ordinary `store.Open`: its WAL, lock and generation changes mutate the backup. Validate the singleton marker, bounded canonical payload, digest and authority identity before hydration. Require the saved ledger ID in `OpenPinned`; a missing pin must not create or adopt a replacement ledger.
2. After complete manifest, GC-record, embedded-root and current privacy-authority preflight, calculate the checked byte/entry envelope before stage creation. Include copied logical files, largest temporary, SQLite/runtime/control bootstrap allowance and unique directories. Bind the request to the canonical source root identity, manifest digest, current deletion watermark, authority and intended final root. Hold the existing privacy operation lease throughout publication and registration.
3. Add one combined `PrepareHandoffPartition` operation using the existing handoff and partition tables in one transaction. It must preserve both protocols' bounded schema checks and reject mixed states. Begin the original allocation before mkdir, bind the empty stage inode in both intents, record `RESTORE_STAGE` privacy ownership before copied content, then initialize the independent-owner partition.
4. Represent restored `maintenance/gc/<command>` trees with exact separately retained command allocations in the same settlement transaction. The mutable GC namespace cannot be absorbed into OWNER_STATIC. Partition validation must recognize only proven GC command roots and preserve later exact completion retry; otherwise report and implement the required schema change rather than accepting arbitrary descendants.
5. Copy and revalidate bytes, initialize the restore privacy checkpoint, validate the unfinished owner under the enclosing allocation, close/checkpoint and sync it, and prepare the privacy target before moving. Combined `CommitHandoffPartition` owns no-replace rename, parent sync, final identity/partition measurement, and one transaction retargeting allocation and partition paths, settling all typed members and marking both intents committed.
6. Reopen the final owner and complete privacy registration only after settlement. A crash after rename retains the original WRITING upper; exact retry can recognize only the bound final inode. A crash after settlement can retry registration without new model execution or a second initial charge. Both/neither paths, changed identities, foreign content and uncertain partial copies remain charged and refused. Preserve the bound root through privacy purge until an exact quota-free protocol exists.
7. Advertise combined durable use through a distinct `physical_quota_partition_handoff_v1` capability detected by a bounded handoff/partition join. Installation and rollback must reject runtimes that understand the separate primitives but cannot replay their combined transaction.

Acceptance must cover exhaustion before mkdir; real interruption at binding, first file, rename and ledger commit; foreign/replaced paths; current watermark and deletion races; and a restored backup containing completed GC records. The final reopened owner must perform later task/artifact/GC writes and exact GC retry under unchanged ledger identity without overlapping initial charge. Mid-copy recovery is not currently implemented: either add per-file verified resume with exact SQLite-sidecar handling or retain the uncertain upper and reject adoption. Prefix-based cleanup or an empty-directory inference is insufficient.
