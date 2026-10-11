package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/diskguard"
	"github.com/ixayldz/Viber/internal/fileguard"
)

const privacyRecordLimit = 2048
const privacyWorkRecordLimit = 1536
const privacyMetadataLimit int64 = 64 << 20
const privacyWorkMetadataLimit int64 = 32 << 20
const privacyCatalogLimit = 8192
const privacyWorkCatalogLimit = 7680

// Catalog admission happens before even creating the zero-byte lease file.
// An ordinary failed allocation must not leave an over-quota catalog that
// prevents the already registered owner from reopening to delete content.
func admitPrivacyCatalog(root *os.Root, names ...string) (int, error) {
	dir, err := root.Open("owners")
	if err != nil {
		return 0, err
	}
	entries, err := dir.Readdirnames(privacyCatalogLimit + 1)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	err = errors.Join(err, dir.Close())
	if err != nil {
		return 0, err
	}
	if len(entries) > privacyCatalogLimit {
		return 0, c.Fail(c.BudgetLimitReached, "privacy owner catalog full")
	}
	existing := map[string]bool{}
	for _, name := range entries {
		if strings.HasSuffix(name, ".json") && c.ValidDigest(strings.TrimSuffix(name, ".json")) || validPrivacyLeaseFilename(name) {
			existing[name] = true
		} else {
			return 0, c.Fail(c.StoreIntegrityError, "foreign privacy catalog entry")
		}
	}
	additional := 0
	for _, name := range names {
		base := filepath.Base(name)
		if name != filepath.Join("owners", base) {
			return 0, c.Fail(c.StoreIntegrityError, "owner admission requires a catalog member")
		}
		if !existing[base] {
			additional++
			existing[base] = true
		}
	}
	if additional > 0 && len(entries)+additional > privacyWorkCatalogLimit {
		return len(entries), c.Fail(c.BudgetLimitReached, "ordinary owner catalog capacity exhausted; existing scopes retained")
	}
	return len(entries), nil
}

// Ordinary allocation/lineage records cannot use deletion's journal slots or
// byte capacity. This finite reserve is separate from physical emergency space;
// neither reserve promises an unbounded number of future deletion commands.
func privacyMetadataAdmission(records int64, retained, temporary int64, control bool) error {
	recordLimit := int64(privacyWorkRecordLimit)
	byteLimit := privacyWorkMetadataLimit
	if control {
		recordLimit = privacyRecordLimit
		byteLimit = privacyMetadataLimit
	}
	if records < 0 || retained < 0 || temporary < 0 || temporary > 16<<20 || records >= recordLimit || retained > byteLimit || temporary > byteLimit-retained {
		return c.Fail(c.BudgetLimitReached, "privacy metadata class capacity exhausted; no record published")
	}
	return nil
}

func privacyMetadataBytes(root *os.Root) (int64, error) {
	dir, err := root.Open("records")
	if err != nil {
		return 0, err
	}
	entries, err := dir.Readdir(privacyRecordLimit + 1)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	err = errors.Join(err, dir.Close())
	if err != nil {
		return 0, err
	}
	if len(entries) > privacyRecordLimit {
		return 0, c.Fail(c.BudgetLimitReached, "privacy metadata record quota exceeded")
	}
	total := int64(0)
	for _, entry := range entries {
		if !entry.Mode().IsRegular() || entry.Size() < 1 || entry.Size() > privacyMetadataLimit-total {
			return 0, c.Fail(c.StoreIntegrityError, "invalid privacy metadata capacity inventory")
		}
		total += entry.Size()
	}
	checkpointBytes, err := privacyCheckpointBytes(root)
	if err != nil {
		return 0, err
	}
	if checkpointBytes > privacyMetadataLimit-total {
		return 0, c.Fail(c.BudgetLimitReached, "privacy checkpoint allocation exceeds metadata bound")
	}
	total += checkpointBytes
	return total, nil
}

func privacyCheckpointBytes(root *os.Root) (int64, error) {
	info, err := root.Lstat(privacyCheckpointDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, c.Fail(c.StoreIntegrityError, "privacy checkpoint root is not a directory")
	}
	directory, err := root.Open(privacyCheckpointDirectory)
	if err != nil {
		return 0, err
	}
	names, err := directory.Readdirnames(8)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	err = errors.Join(err, directory.Close())
	if err != nil {
		return 0, err
	}
	if len(names) > 7 {
		return 0, c.Fail(c.BudgetLimitReached, "privacy checkpoint root inventory exceeds its bound")
	}
	allowed := map[string]bool{"current.json": true, "current.next": true, "intent.json": true, "data": true, "anchors": true, "retired": true}
	var total int64
	for _, name := range names {
		if !allowed[name] {
			return 0, c.Fail(c.StoreIntegrityError, "foreign privacy checkpoint member")
		}
		if name == "data" || name == "anchors" || name == "retired" {
			child, openErr := root.Open(filepath.Join(privacyCheckpointDirectory, name))
			if openErr != nil {
				return 0, openErr
			}
			entries, readErr := child.Readdir(int(privacyCheckpointAnchorLimit + 1))
			if errors.Is(readErr, io.EOF) {
				readErr = nil
			}
			readErr = errors.Join(readErr, child.Close())
			if readErr != nil {
				return 0, readErr
			}
			if int64(len(entries)) > privacyCheckpointAnchorLimit {
				return 0, c.Fail(c.BudgetLimitReached, "privacy checkpoint object quota exceeded")
			}
			for _, entry := range entries {
				base := strings.TrimSuffix(entry.Name(), ".json")
				if !strings.HasSuffix(entry.Name(), ".json") || !c.ValidDigest(base) || !entry.Mode().IsRegular() || entry.Size() < 1 || entry.Size() > privacyCheckpointFileLimit-total {
					return 0, c.Fail(c.StoreIntegrityError, "invalid privacy checkpoint object inventory")
				}
				if _, readErr = fileguard.ReadRegular(root, filepath.Join(privacyCheckpointDirectory, name, entry.Name()), privacyCheckpointFileLimit); readErr != nil {
					return 0, readErr
				}
				total += entry.Size()
			}
			continue
		}
		limit := int64(1 << 20)
		if name == "current.json" || name == "current.next" {
			limit = 4096
		}
		raw, readErr := fileguard.ReadRegular(root, filepath.Join(privacyCheckpointDirectory, name), limit)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil || int64(len(raw)) > privacyMetadataLimit-total {
			return 0, errors.Join(readErr, c.Fail(c.BudgetLimitReached, "privacy checkpoint metadata exceeds its bound"))
		}
		total += int64(len(raw))
	}
	return total, nil
}

func admitPrivacyDisk(root *os.Root, bytes int64, control bool) error {
	reserve, err := diskguard.OpenRoot(root)
	if err != nil {
		return err
	}
	return errors.Join(reserve.Admit(bytes, control), reserve.Close())
}

type PrivacyMetadataStatus struct {
	ObservationOnly      bool     `json:"observation_only"`
	WorkProbeBytes       int64    `json:"work_probe_bytes"`
	AvailableBytes       uint64   `json:"available_bytes"`
	LowWaterBytes        uint64   `json:"low_water_bytes"`
	WorkBlockers         []string `json:"work_blockers"`
	NextActions          []string `json:"supported_next_actions"`
	CatalogEntries       int      `json:"owner_catalog_entries"`
	CatalogLimit         int      `json:"owner_catalog_limit"`
	WorkCatalogLimit     int      `json:"work_owner_catalog_limit"`
	SchemaVersion        int      `json:"schema_version"`
	Records              int64    `json:"journal_records"`
	Sequence             int64    `json:"journal_sequence"`
	RecordLimit          int64    `json:"record_limit"`
	WorkRecordLimit      int64    `json:"work_record_limit"`
	JournalBytes         int64    `json:"journal_bytes"`
	ByteLimit            int64    `json:"byte_limit"`
	WorkByteLimit        int64    `json:"work_byte_limit"`
	PhysicalReserveBytes int64    `json:"physical_reserve_bytes"`
	TargetReserveBytes   int64    `json:"target_reserve_bytes"`
	WorkReady            bool     `json:"work_ready"`
}

// Status is an instant numeric capacity observation, not future admission or
// permission to discard UNKNOWN effects. Missing legacy reserve is unready.
func (s *Session) PrivacyMetadataCapacity(ctx context.Context) (*PrivacyMetadataStatus, error) {
	if s.privacy == nil {
		return nil, nil
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	view, err := readPrivacy(root)
	if err != nil {
		return nil, err
	}
	bytes, err := privacyMetadataBytes(root)
	if err != nil {
		return nil, err
	}
	result := &PrivacyMetadataStatus{SchemaVersion: 1, Records: view.RecordCount, Sequence: view.Sequence, RecordLimit: privacyRecordLimit, WorkRecordLimit: privacyWorkRecordLimit, JournalBytes: bytes, ByteLimit: privacyMetadataLimit, WorkByteLimit: privacyWorkMetadataLimit, TargetReserveBytes: diskguard.ReserveBytes, LowWaterBytes: diskguard.LowWaterBytes, WorkProbeBytes: 4096, ObservationOnly: true}
	result.CatalogEntries, err = admitPrivacyCatalog(root)
	if err != nil {
		return nil, err
	}
	result.CatalogLimit, result.WorkCatalogLimit = privacyCatalogLimit, privacyWorkCatalogLimit
	if _, err = root.Lstat("control.reserve"); errors.Is(err, os.ErrNotExist) {
		classifyPrivacyCapacity(result, false)
		return result, nil
	} else if err != nil {
		return nil, err
	}
	reserve, err := diskguard.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	status, readErr := reserve.Status()
	err = errors.Join(readErr, reserve.Close())
	if err != nil {
		return nil, err
	}
	result.PhysicalReserveBytes = status.RetainedReserveBytes
	result.AvailableBytes = status.AvailableBytes
	classifyPrivacyCapacity(result, true)
	return result, nil
}

// Fixed enum strings keep operator guidance within the support allowlist. A
// full journal/catalog has no safe pruning primitive yet: reserve replenishment
// must never suggest that those limits or UNKNOWN effects have been cleared.
func classifyPrivacyCapacity(result *PrivacyMetadataStatus, reservePresent bool) {
	result.WorkBlockers = []string{}
	result.NextActions = []string{"REFRESH_CAPACITY", "RETIRE_INACTIVE_OPERATION_LEASES"}
	if result.Records >= result.WorkRecordLimit {
		result.WorkBlockers = append(result.WorkBlockers, "WORK_JOURNAL_FULL_CHECKPOINT_REQUIRED")
	}
	if result.JournalBytes > result.WorkByteLimit-result.WorkProbeBytes {
		result.WorkBlockers = append(result.WorkBlockers, "WORK_METADATA_FULL_CHECKPOINT_REQUIRED")
	}
	if result.CatalogEntries >= result.WorkCatalogLimit {
		result.WorkBlockers = append(result.WorkBlockers, "OWNER_CATALOG_FULL_RETIREMENT_REQUIRED")
	}
	if !reservePresent {
		result.WorkBlockers = append(result.WorkBlockers, "CONTROL_RESERVE_MISSING")
	} else if result.PhysicalReserveBytes < result.TargetReserveBytes {
		result.WorkBlockers = append(result.WorkBlockers, "CONTROL_RESERVE_DEPLETED")
	}
	if !reservePresent || result.PhysicalReserveBytes < result.TargetReserveBytes {
		result.NextActions = append(result.NextActions, "REPLENISH_CONTROL_RESERVE")
	}
	// Missing reserve deliberately has no synthetic free-space measurement.
	if reservePresent && result.AvailableBytes < result.LowWaterBytes+uint64(result.WorkProbeBytes)+uint64(result.TargetReserveBytes-result.PhysicalReserveBytes) {
		result.WorkBlockers = append(result.WorkBlockers, "DISK_WORK_LOW_WATERMARK")
	}
	result.WorkReady = len(result.WorkBlockers) == 0
}

// ReplenishPrivacyControlReserve writes at most the existing bounded reserve,
// under the family registry lock, without retiring metadata, leases or fences.
// Disk admission verifies the low watermark and physically allocated readback.
func (s *Session) ReplenishPrivacyControlReserve(ctx context.Context) (*PrivacyMetadataStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.privacy == nil {
		return nil, c.Fail(c.UnsupportedCapability, "bound privacy authority required")
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return nil, err
	}
	if _, err = readPrivacy(root); err == nil {
		err = admitPrivacyDisk(root, 4096, false)
	}
	err = errors.Join(err, lock.Close())
	if err != nil {
		return nil, err
	}
	return s.PrivacyMetadataCapacity(ctx)
}
