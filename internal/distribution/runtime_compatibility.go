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

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/store"
)

const (
	RuntimeCapabilityProjectMemoryV1 = "project_memory_v1"
	RuntimeCapabilityReadWorkersV1   = "read_workers_v1"
	maxRuntimeCapabilityStates       = 1 << 16
	maxRuntimeCapabilityDocuments    = 1 << 16
	maxRuntimeCapabilityBytes        = 512 << 20
)

var supportedRuntimeCapabilities = []string{
	RuntimeCapabilityProjectMemoryV1,
	RuntimeCapabilityReadWorkersV1,
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
// the selected program. A racing writer either completes before this scan or
// prevents the lock from being acquired.
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
			if len(state.ReadWorkerAdmissions) > 0 {
				used[RuntimeCapabilityReadWorkersV1] = true
			}
		}
		refs, refsErr := owner.DocumentReferences(ctx)
		if refsErr != nil {
			return closeOnError(refsErr)
		}
		if len(refs) > maxRuntimeCapabilityDocuments {
			return closeOnError(c.Fail(c.BudgetLimitReached, "registered store exceeds the bounded historical-document scan"))
		}
		found, scannedBytes, scanErr := scanStoreProjectMemory(known.Path, states, refs, maxRuntimeCapabilityBytes-documentBytes)
		if scanErr != nil {
			return closeOnError(scanErr)
		}
		if found {
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

func closeStoreLocks(locked []*store.Store) error {
	var result error
	for i := len(locked) - 1; i >= 0; i-- {
		result = errors.Join(result, locked[i].Close())
	}
	return result
}

func scanStoreProjectMemory(directory string, states map[string]c.TaskState, refs []store.DocumentRef, byteLimit int64) (bool, int64, error) {
	if byteLimit < 0 {
		return false, 0, c.Fail(c.BudgetLimitReached, "registered stores exceed the bounded historical-document scan")
	}
	if len(refs) == 0 {
		return false, 0, nil
	}
	inspect := make([]store.DocumentRef, 0, len(refs))
	allPending := true
	for _, ref := range refs {
		state, ok := states[ref.TaskID]
		if !ok {
			return false, 0, c.Fail(c.StoreIntegrityError, "historical document has no replayed task")
		}
		if state.Deletion != nil {
			switch state.Deletion.Status {
			case "PURGED":
				continue
			case "PENDING":
			default:
				return false, 0, c.Fail(c.StoreIntegrityError, "registered task has an invalid content deletion status")
			}
		} else {
			allPending = false
		}
		inspect = append(inspect, ref)
	}
	if len(inspect) == 0 {
		return false, 0, nil
	}
	storeRoot, err := os.OpenRoot(directory)
	if err != nil {
		if allPending {
			return true, 0, nil
		}
		return false, 0, err
	}
	defer storeRoot.Close()
	info, err := storeRoot.Lstat("artifacts")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		if allPending {
			return true, 0, nil
		}
		return false, 0, c.Fail(c.StoreIntegrityError, "registered store document archive is unavailable or replaced")
	}
	archiveRoot, err := storeRoot.OpenRoot("artifacts")
	if err != nil {
		if allPending {
			return true, 0, nil
		}
		return false, 0, err
	}
	defer archiveRoot.Close()
	if err = fileguard.Private(archiveRoot); err != nil {
		return false, 0, err
	}
	var scanned int64
	for _, ref := range inspect {
		state, ok := states[ref.TaskID]
		if !ok {
			return false, scanned, c.Fail(c.StoreIntegrityError, "historical document has no replayed task")
		}
		pendingDeletion := false
		if state.Deletion != nil {
			if state.Deletion.Status == "PURGED" {
				continue
			}
			if state.Deletion.Status == "PENDING" {
				pendingDeletion = true
			} else {
				return false, scanned, c.Fail(c.StoreIntegrityError, "registered task has an invalid content deletion status")
			}
		}
		name, pathErr := artifact.ScopedBlobPath(ref.TaskID, ref.Digest)
		if pathErr != nil {
			return false, scanned, c.Fail(c.StoreIntegrityError, "historical document reference is invalid")
		}
		raw, readErr := fileguard.ReadRegular(archiveRoot, filepath.FromSlash(name), 8<<20)
		if readErr != nil && pendingDeletion && errors.Is(readErr, os.ErrNotExist) {
			// A pending deletion may already have removed some historical
			// documents. Their former feature use cannot be recovered from the
			// remaining bytes, so conservatively require project-memory support.
			return true, scanned, nil
		}
		if readErr != nil || c.HashBytes(raw) != ref.Digest {
			return false, scanned, c.Fail(c.StoreIntegrityError, "registered store historical document is missing or changed")
		}
		scanned += int64(len(raw))
		if scanned > byteLimit {
			return false, scanned, c.Fail(c.BudgetLimitReached, "registered stores exceed the bounded historical-document scan")
		}
		var document map[string]json.RawMessage
		if err = c.DecodeStrict(raw, &document); err != nil || document == nil {
			return false, scanned, c.Fail(c.StoreIntegrityError, "registered store document cannot be inspected for runtime references")
		}
		refsRaw, exists := document["project_memory_refs"]
		if !exists {
			continue
		}
		trimmed := bytes.TrimSpace(refsRaw)
		if bytes.Equal(trimmed, []byte("null")) {
			return false, scanned, c.Fail(c.StoreIntegrityError, "project-memory document reference list is malformed")
		}
		var memoryRefs []json.RawMessage
		if err = json.Unmarshal(refsRaw, &memoryRefs); err != nil {
			return false, scanned, c.Fail(c.StoreIntegrityError, "project-memory document reference list is malformed")
		}
		if len(memoryRefs) > 0 {
			return true, scanned, nil
		}
	}
	return false, scanned, nil
}
