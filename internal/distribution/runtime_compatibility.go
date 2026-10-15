package distribution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ixayldz/Viber/internal/agent"
	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/resource"
	"github.com/ixayldz/Viber/internal/store"
)

const (
	RuntimeCapabilityGCReceiptCompactionV1           = "gc_completed_receipt_compaction_v1"
	RuntimeCapabilityLLMReadWorkersV1                = "llm_read_workers_v1"
	RuntimeCapabilityPhysicalQuotaHandoffV1          = "physical_quota_handoff_v1"
	RuntimeCapabilityPrivacyMetadataV1               = "physical_quota_metadata_v1"
	RuntimeCapabilityNativeEnginePartitionV1         = "physical_quota_native_engine_partition_v1"
	RuntimeCapabilityPhysicalQuotaPartitionV1        = "physical_quota_owner_partition_v1"
	RuntimeCapabilityPhysicalQuotaIntentV1           = "physical_quota_retry_intent_v1"
	RuntimeCapabilityPhysicalQuotaRestorePartitionV1 = "physical_quota_restore_partition_v1"
	RuntimeCapabilityPRDTaskContractV1               = "prd_task_contract_v1"
	RuntimeCapabilityPrivacyBootstrapV1              = "privacy_bootstrap_reservation_v1"
	RuntimeCapabilityProjectMemoryConflictMetadataV1 = "project_memory_conflict_metadata_v1"
	RuntimeCapabilityProjectMemoryCopyV1             = "project_memory_copy_v1"
	RuntimeCapabilityProjectMemoryV1                 = "project_memory_v1"
	RuntimeCapabilityReadWorkersV1                   = "read_workers_v1"
	RuntimeCapabilitySkillMarkdownV1                 = "skill_markdown_v1"
	maxRuntimeCapabilityStates                       = 1 << 16
	maxRuntimeCapabilityDocuments                    = 1 << 16
	maxRuntimeCapabilityBytes                        = 512 << 20
)

var supportedRuntimeCapabilities = []string{
	RuntimeCapabilityGCReceiptCompactionV1,
	RuntimeCapabilityLLMReadWorkersV1,
	RuntimeCapabilityPhysicalQuotaHandoffV1,
	RuntimeCapabilityPrivacyMetadataV1,
	RuntimeCapabilityNativeEnginePartitionV1,
	RuntimeCapabilityPhysicalQuotaPartitionV1,
	RuntimeCapabilityPhysicalQuotaRestorePartitionV1,
	RuntimeCapabilityPhysicalQuotaIntentV1,
	RuntimeCapabilityPRDTaskContractV1,
	RuntimeCapabilityPrivacyBootstrapV1,
	RuntimeCapabilityProjectMemoryConflictMetadataV1,
	RuntimeCapabilityProjectMemoryCopyV1,
	RuntimeCapabilityProjectMemoryV1,
	RuntimeCapabilityReadWorkersV1,
	RuntimeCapabilitySkillMarkdownV1,
}

func validateRuntimeCapabilities(capabilities []string) error {
	allowed := make(map[string]bool, len(supportedRuntimeCapabilities))
	for _, capability := range supportedRuntimeCapabilities {
		allowed[capability] = true
	}
	previous := ""
	for _, capability := range capabilities {
		if !allowed[capability] || capability <= previous {
			return c.Fail(c.InvalidArgument, "runtime capabilities must be known, unique and sorted")
		}
		previous = capability
	}
	declared := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		declared[capability] = true
	}
	if (declared[RuntimeCapabilityProjectMemoryCopyV1] || declared[RuntimeCapabilityProjectMemoryConflictMetadataV1]) && !declared[RuntimeCapabilityProjectMemoryV1] {
		return c.Fail(c.InvalidArgument, "project-memory copy support requires project-memory support")
	}
	if declared[RuntimeCapabilityNativeEnginePartitionV1] && !declared[RuntimeCapabilityPhysicalQuotaPartitionV1] {
		return c.Fail(c.InvalidArgument, "native engine partition support requires owner partition support")
	}
	if declared[RuntimeCapabilityPhysicalQuotaRestorePartitionV1] && (!declared[RuntimeCapabilityPhysicalQuotaPartitionV1] || !declared[RuntimeCapabilityPhysicalQuotaHandoffV1]) {
		return c.Fail(c.InvalidArgument, "restore partitions require owner partition and handoff support")
	}
	return nil
}

func requireRuntimeCapabilities(manifest Manifest, required []string) error {
	if err := validateRuntimeCapabilities(manifest.RuntimeCapabilities); err != nil {
		return c.Fail(c.UnsupportedCapability, "package runtime capability declaration is invalid")
	}
	supported := make(map[string]bool, len(manifest.RuntimeCapabilities))
	for _, capability := range manifest.RuntimeCapabilities {
		supported[capability] = true
	}
	missing := []string{}
	for _, capability := range required {
		if !supported[capability] {
			missing = append(missing, capability)
		}
	}
	if len(missing) > 0 {
		return c.Fail(c.UnsupportedCapability, "selected package lacks runtime support required by registered stores: "+strings.Join(missing, ", "))
	}
	return nil
}

func verifiedBundleManifest(bundle Bundle) (Manifest, error) {
	var manifest Manifest
	raw, ok := bundle.Files["package.json"]
	if !ok || len(raw) == 0 || len(raw) > 1<<20 || !c.ValidDigest(bundle.ArchiveDigest) || c.HashBytes(raw) != bundle.ManifestDigest || c.DecodeStrict(raw, &manifest) != nil {
		return manifest, c.Fail(c.InvalidArgument, "bundle manifest bytes and verified identity are required")
	}
	providedDigest, providedErr := c.Digest(bundle.Manifest)
	parsedDigest, parsedErr := c.Digest(manifest)
	if providedErr != nil || parsedErr != nil || providedDigest != parsedDigest || validateRuntimeCapabilities(manifest.RuntimeCapabilities) != nil {
		return Manifest{}, c.Fail(c.InvalidArgument, "bundle runtime manifest differs from its verified package bytes")
	}
	return manifest, nil
}

// lockRegisteredStoreCapabilities opens every registered store under its
// exclusive owner lock and keeps those locks until the caller finishes changing
// the selected program. A racing writer in one of those stores either completes
// before this scan or prevents the lock from being acquired. Privacy-family
// copy intent is read from its authoritative journal under that journal's short
// registry lock; this does not claim an atomic handoff against an unregistered
// owner sharing the same authority.
func (m *Manager) lockRegisteredStoreCapabilities(ctx context.Context) ([]*store.Store, []string, error) {
	registered, err := m.RegisteredStores()
	if err != nil {
		return nil, nil, err
	}
	locked := make([]*store.Store, 0, len(registered))
	closeOnError := func(err error) ([]*store.Store, []string, error) {
		return nil, nil, errors.Join(err, closeStoreLocks(locked))
	}
	used := map[string]bool{}
	var documentBytes int64
	for _, known := range registered {
		if err = ctx.Err(); err != nil {
			return closeOnError(err)
		}
		if err = known.ValidateCurrent(); err != nil {
			return closeOnError(err)
		}
		statInfo, statErr := os.Lstat(known.Path)
		if statErr != nil || !statInfo.IsDir() || statInfo.Mode()&os.ModeSymlink != 0 {
			return closeOnError(c.Fail(c.StaleAuthority, "registered store is not a current real directory"))
		}
		owner, openErr := store.Open(ctx, known.Path)
		if openErr != nil {
			return closeOnError(openErr)
		}
		locked = append(locked, owner)
		if err = known.ValidateCurrent(); err != nil {
			return closeOnError(err)
		}
		if err = applyQuotaRuntimeCapabilities(ctx, owner, used); err != nil {
			return closeOnError(err)
		}
		compactionUsed, compactionErr := agent.GCReceiptCompactionUsed(ctx, known.Path)
		if compactionErr != nil {
			return closeOnError(compactionErr)
		}
		if compactionUsed {
			used[RuntimeCapabilityGCReceiptCompactionV1] = true
		}
		conflictMetadataUsed, metadataErr := owner.ProjectMemoryConflictMetadataUsed(ctx)
		if metadataErr != nil {
			return closeOnError(metadataErr)
		}
		if conflictMetadataUsed {
			used[RuntimeCapabilityProjectMemoryConflictMetadataV1] = true
			used[RuntimeCapabilityProjectMemoryV1] = true
		}
		copyUsed, copyErr := agent.ProjectMemoryCopyCapabilityUsed(ctx, owner)
		if copyErr != nil {
			return closeOnError(copyErr)
		}
		if copyUsed {
			used[RuntimeCapabilityProjectMemoryCopyV1] = true
			used[RuntimeCapabilityProjectMemoryV1] = true
		}
		snapshot, err := owner.SnapshotInfo(ctx)
		if err != nil {
			return closeOnError(err)
		}
		if snapshot.SchemaVersion != 2 || snapshot.ReducerVersion != c.ReducerVersion {
			return closeOnError(c.Fail(c.UnsupportedCapability, "registered store requires an explicit schema/reducer migration"))
		}
		states, replayErr := owner.Replay(ctx, "")
		if replayErr != nil {
			return closeOnError(replayErr)
		}
		if len(states) > maxRuntimeCapabilityStates {
			return closeOnError(c.Fail(c.BudgetLimitReached, "registered store exceeds the bounded runtime-capability state scan"))
		}
		for _, state := range states {
			if err = ctx.Err(); err != nil {
				return closeOnError(err)
			}
			if err = applyTaskRuntimeCapabilities(state, used); err != nil {
				return closeOnError(err)
			}
		}
		refs, refsErr := owner.DocumentReferences(ctx)
		if refsErr != nil {
			return closeOnError(refsErr)
		}
		if len(refs) > maxRuntimeCapabilityDocuments {
			return closeOnError(c.Fail(c.BudgetLimitReached, "registered store exceeds the bounded historical-document scan"))
		}
		found, copyFound, scannedBytes, scanErr := scanStoreProjectMemory(ctx, known.Path, states, refs, maxRuntimeCapabilityBytes-documentBytes, used)
		if scanErr != nil {
			return closeOnError(scanErr)
		}
		if found {
			used[RuntimeCapabilityProjectMemoryV1] = true
		}
		if copyFound {
			used[RuntimeCapabilityProjectMemoryCopyV1] = true
			used[RuntimeCapabilityProjectMemoryV1] = true
		}
		documentBytes += scannedBytes
	}
	required := make([]string, 0, len(used))
	for capability := range used {
		required = append(required, capability)
	}
	sort.Strings(required)
	return locked, required, nil
}

func applyQuotaRuntimeCapabilities(ctx context.Context, owner *store.Store, used map[string]bool) error {
	bootstrap, err := owner.PrivacyBootstrap(ctx)
	if err != nil {
		return err
	}
	if bootstrap != nil {
		used[RuntimeCapabilityPrivacyBootstrapV1] = true
	}
	pin, err := owner.PhysicalQuotaBinding(ctx)
	if err != nil || pin == nil {
		return err
	}
	ledger, err := resource.OpenPinnedExistingReadOnly(resource.Authority{ID: pin.AuthorityID, Directory: pin.Directory, PhysicalRoot: pin.PhysicalRoot}, pin.LedgerID)
	if err != nil {
		return err
	}
	usage, observeErr := ledger.FeatureUsage(ctx)
	partitionUsed, partitionErr := ledger.PartitionFeatureInUse(ctx)
	nativeEnginePartitionUsed, nativeEnginePartitionErr := ledger.NativeEnginePartitionFeatureInUse(ctx)
	closeErr := ledger.Close()
	if err = errors.Join(observeErr, partitionErr, nativeEnginePartitionErr, closeErr); err != nil {
		return err
	}
	if usage.ReservationIntents {
		used[RuntimeCapabilityPhysicalQuotaIntentV1] = true
	}
	if usage.PrivacyMetadata || usage.AllocatorSelfOverhead {
		used[RuntimeCapabilityPrivacyMetadataV1] = true
	}
	if usage.StageHandoffs {
		used[RuntimeCapabilityPhysicalQuotaHandoffV1] = true
	}
	if usage.RestorePartitions {
		used[RuntimeCapabilityPhysicalQuotaRestorePartitionV1] = true
	}
	if partitionUsed {
		used[RuntimeCapabilityPhysicalQuotaPartitionV1] = true
	}
	if nativeEnginePartitionUsed {
		used[RuntimeCapabilityNativeEnginePartitionV1] = true
		used[RuntimeCapabilityPhysicalQuotaPartitionV1] = true
	}
	return nil
}

func applyTaskRuntimeCapabilities(state c.TaskState, used map[string]bool) error {
	if state.ProjectMemoryRevision < 0 || state.ProjectMemoryCopyCount < 0 || state.ProjectMemoryCopyCount > state.ProjectMemoryRevision {
		return c.Fail(c.StoreIntegrityError, "registered task has invalid project-memory capability counters")
	}
	if state.ProjectMemoryRevision > 0 || state.ProjectMemoryCopyCount > 0 {
		used[RuntimeCapabilityProjectMemoryV1] = true
	}
	if state.ProjectMemoryCopyCount > 0 {
		// This task-state marker is durable across document purge. Historical
		// archive scans alone cannot retain copy capability after deletion.
		used[RuntimeCapabilityProjectMemoryCopyV1] = true
	}
	if len(state.ReadWorkerAdmissions) > 0 {
		used[RuntimeCapabilityReadWorkersV1] = true
	}
	for _, goal := range state.GoalQueue {
		if goal.Role == "" && goal.ReadWorkerLease == (c.ReadWorkerLease{}) {
			continue
		}
		if !c.ValidReadWorkerRole(goal.Role) || goal.ReadWorkerLease.Validate() != nil || goal.ReadWorkerLease.Role != goal.Role {
			return c.Fail(c.StoreIntegrityError, "registered task has invalid LLM read-worker capability binding")
		}
		used[RuntimeCapabilityLLMReadWorkersV1] = true
	}
	return nil
}

func closeStoreLocks(locked []*store.Store) error {
	var result error
	for i := len(locked) - 1; i >= 0; i-- {
		result = errors.Join(result, locked[i].Close())
	}
	return result
}

func scanStoreProjectMemory(ctx context.Context, directory string, states map[string]c.TaskState, refs []store.DocumentRef, byteLimit int64, featureMaps ...map[string]bool) (bool, bool, int64, error) {
	if err := ctx.Err(); err != nil {
		return false, false, 0, err
	}
	if byteLimit < 0 {
		return false, false, 0, c.Fail(c.BudgetLimitReached, "registered stores exceed the bounded historical-document scan")
	}
	if len(refs) == 0 {
		return false, false, 0, nil
	}
	inspect := make([]store.DocumentRef, 0, len(refs))
	allPending := true
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return false, false, 0, err
		}
		state, ok := states[ref.TaskID]
		if !ok {
			return false, false, 0, c.Fail(c.StoreIntegrityError, "historical document has no replayed task")
		}
		if state.Deletion != nil {
			switch state.Deletion.Status {
			case "PURGED":
				continue
			case "PENDING":
			default:
				return false, false, 0, c.Fail(c.StoreIntegrityError, "registered task has an invalid content deletion status")
			}
		} else {
			allPending = false
		}
		inspect = append(inspect, ref)
	}
	if len(inspect) == 0 {
		return false, false, 0, nil
	}
	storeRoot, err := os.OpenRoot(directory)
	if err != nil {
		if allPending {
			return true, false, 0, nil
		}
		return false, false, 0, err
	}
	defer storeRoot.Close()
	info, err := storeRoot.Lstat("artifacts")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		if allPending {
			return true, false, 0, nil
		}
		return false, false, 0, c.Fail(c.StoreIntegrityError, "registered store document archive is unavailable or replaced")
	}
	archiveRoot, err := storeRoot.OpenRoot("artifacts")
	if err != nil {
		if allPending {
			return true, false, 0, nil
		}
		return false, false, 0, err
	}
	defer archiveRoot.Close()
	if err = fileguard.Private(archiveRoot); err != nil {
		return false, false, 0, err
	}
	var scanned int64
	projectMemory := false
	projectMemoryCopy := false
	for _, ref := range inspect {
		if err := ctx.Err(); err != nil {
			return false, projectMemoryCopy, scanned, err
		}
		state, ok := states[ref.TaskID]
		if !ok {
			return false, projectMemoryCopy, scanned, c.Fail(c.StoreIntegrityError, "historical document has no replayed task")
		}
		pendingDeletion := false
		if state.Deletion != nil {
			if state.Deletion.Status == "PURGED" {
				continue
			}
			if state.Deletion.Status == "PENDING" {
				pendingDeletion = true
			} else {
				return false, projectMemoryCopy, scanned, c.Fail(c.StoreIntegrityError, "registered task has an invalid content deletion status")
			}
		}
		name, pathErr := artifact.ScopedBlobPath(ref.TaskID, ref.Digest)
		if pathErr != nil {
			return false, projectMemoryCopy, scanned, c.Fail(c.StoreIntegrityError, "historical document reference is invalid")
		}
		raw, readErr := fileguard.ReadRegular(archiveRoot, filepath.FromSlash(name), 8<<20)
		if readErr != nil && pendingDeletion && errors.Is(readErr, os.ErrNotExist) {
			// A pending deletion may already have removed some historical
			// documents. Their former feature use cannot be recovered from the
			// remaining bytes, so conservatively require project-memory support.
			return true, projectMemoryCopy, scanned, nil
		}
		if readErr != nil || c.HashBytes(raw) != ref.Digest {
			return false, projectMemoryCopy, scanned, c.Fail(c.StoreIntegrityError, "registered store historical document is missing or changed")
		}
		scanned += int64(len(raw))
		if scanned > byteLimit {
			return false, projectMemoryCopy, scanned, c.Fail(c.BudgetLimitReached, "registered stores exceed the bounded historical-document scan")
		}
		var document map[string]json.RawMessage
		if err = c.DecodeStrict(raw, &document); err != nil || document == nil {
			return false, projectMemoryCopy, scanned, c.Fail(c.StoreIntegrityError, "registered store document cannot be inspected for runtime references")
		}
		if coverage, present := document["prd_coverage"]; present && !bytes.Equal(bytes.TrimSpace(coverage), []byte("null")) {
			for _, features := range featureMaps {
				features[RuntimeCapabilityPRDTaskContractV1] = true
			}
		}
		if marker, present := document["skill_markdown_used"]; present {
			if !bytes.Equal(bytes.TrimSpace(marker), []byte("true")) && !bytes.Equal(bytes.TrimSpace(marker), []byte("false")) {
				return false, projectMemoryCopy, scanned, c.Fail(c.StoreIntegrityError, "imported skill runtime marker is invalid")
			}
			if bytes.Equal(bytes.TrimSpace(marker), []byte("true")) {
				for _, features := range featureMaps {
					features[RuntimeCapabilitySkillMarkdownV1] = true
				}
			}
		}
		if originRaw, present := document["goal_origin"]; present && !bytes.Equal(bytes.TrimSpace(originRaw), []byte("null")) {
			var origin struct {
				Role  string            `json:"role"`
				Lease c.ReadWorkerLease `json:"read_worker_lease"`
			}
			if json.Unmarshal(originRaw, &origin) != nil {
				return false, projectMemoryCopy, scanned, c.Fail(c.StoreIntegrityError, "scheduled child runtime marker is invalid")
			}
			if origin.Role != "" || origin.Lease != (c.ReadWorkerLease{}) {
				if !c.ValidReadWorkerRole(origin.Role) || origin.Lease.Validate() != nil || origin.Lease.Role != origin.Role {
					return false, projectMemoryCopy, scanned, c.Fail(c.StoreIntegrityError, "LLM read-worker runtime marker is invalid")
				}
				for _, features := range featureMaps {
					features[RuntimeCapabilityLLMReadWorkersV1] = true
				}
			}
		}
		refsRaw, exists := document["project_memory_refs"]
		if !exists {
			continue
		}
		trimmed := bytes.TrimSpace(refsRaw)
		if bytes.Equal(trimmed, []byte("null")) {
			return false, projectMemoryCopy, scanned, c.Fail(c.StoreIntegrityError, "project-memory document reference list is malformed")
		}
		var memoryRefs []json.RawMessage
		if err = json.Unmarshal(refsRaw, &memoryRefs); err != nil {
			return false, projectMemoryCopy, scanned, c.Fail(c.StoreIntegrityError, "project-memory document reference list is malformed")
		}
		if len(memoryRefs) > 0 {
			projectMemory = true
			for _, memoryRefRaw := range memoryRefs {
				var memoryRef map[string]json.RawMessage
				if err = json.Unmarshal(memoryRefRaw, &memoryRef); err != nil || memoryRef == nil {
					return false, projectMemoryCopy, scanned, c.Fail(c.StoreIntegrityError, "project-memory document reference is malformed")
				}
				typeRaw, hasType := memoryRef["type"]
				if !hasType {
					continue
				}
				var referenceType string
				if err = json.Unmarshal(typeRaw, &referenceType); err != nil || referenceType == "" {
					return false, projectMemoryCopy, scanned, c.Fail(c.StoreIntegrityError, "project-memory document reference type is malformed")
				}
				if referenceType != "PROJECT_MEMORY_COPY" {
					return false, projectMemoryCopy, scanned, c.Fail(c.UnsupportedCapability, "registered store uses an unknown project-memory reference type")
				}
				projectMemoryCopy = true
			}
		}
	}
	return projectMemory, projectMemoryCopy, scanned, nil
}
