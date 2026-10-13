package agent

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// GC maintenance scanning and backup/restore support is bounded to the default
// 32 MiB control-byte reserve. Larger retained GC histories must be retired by
// a future protocol before backup, restore or package-compatibility scans can
// support them safely.
const gcMaintenanceMaxBytes int64 = 32 << 20

func gcMaintenancePath(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) != 4 || parts[0] != "maintenance" || parts[1] != "gc" || !c.ValidDigest(parts[2]) {
		return false
	}
	return parts[3] == "intent.json" || parts[3] == "complete.json" || strings.HasSuffix(parts[3], ".json") && c.ValidDigest(strings.TrimSuffix(parts[3], ".json"))
}

func validateGCRecord(name string, raw []byte) error {
	if !gcMaintenancePath(name) {
		return c.Fail(c.StoreIntegrityError, "invalid managed GC path")
	}
	parts := strings.Split(name, "/")
	var canonical []byte
	switch parts[3] {
	case "intent.json":
		var record GCCommand
		if c.DecodeStrict(raw, &record) != nil || record.CommandID == "" || len(record.CommandID) > 128 || c.HashBytes([]byte(record.CommandID)) != parts[2] || record.Plan.SchemaVersion != 1 || record.Plan.Digest != planDigest(record.Plan) || len(record.Plan.Objects) > 2048 || !c.ValidDigest(record.Plan.RootsDigest) {
			return c.Fail(c.StoreIntegrityError, "invalid GC intent")
		}
		previous := ""
		for _, object := range record.Plan.Objects {
			if !artifact.ImmutablePath(object.Path) || object.Path <= previous || !c.ValidDigest(object.Digest) || object.Size < 0 || object.Size > 64<<20 {
				return c.Fail(c.StoreIntegrityError, "invalid GC object binding")
			}
			previous = object.Path
		}
		canonical, _ = c.CanonicalV1(record)
	case "complete.json":
		var result GCResult
		if c.DecodeStrict(raw, &result) != nil || result.SchemaVersion != 1 || result.CommandID == "" || len(result.CommandID) > 128 || c.HashBytes([]byte(result.CommandID)) != parts[2] || !c.ValidDigest(result.PlanDigest) || len(result.Objects) > 2048 || result.Objects == nil {
			return c.Fail(c.StoreIntegrityError, "invalid GC completion record")
		}
		bytesRemoved := int64(0)
		previous := ""
		for _, receipt := range result.Objects {
			if !artifact.ImmutablePath(receipt.Object.Path) || receipt.Object.Path <= previous || !c.ValidDigest(receipt.Object.Digest) || receipt.Object.Size < 0 || receipt.Object.Size > 64<<20 {
				return c.Fail(c.StoreIntegrityError, "invalid GC completion object binding")
			}
			previous = receipt.Object.Path
			if receipt.Status == "UNLINKED_AND_DIRECTORY_SYNCED" {
				bytesRemoved += receipt.Object.Size
			} else if receipt.Status != "ALREADY_ABSENT_AFTER_INTENT" {
				return c.Fail(c.StoreIntegrityError, "invalid GC completion status")
			}
		}
		if result.BytesRemoved != bytesRemoved {
			return c.Fail(c.StoreIntegrityError, "invalid GC byte receipt")
		}
		canonical, _ = c.CanonicalV1(result)
	default:
		var receipt GCObjectReceipt
		if c.DecodeStrict(raw, &receipt) != nil || !artifact.ImmutablePath(receipt.Object.Path) || !c.ValidDigest(receipt.Object.Digest) || receipt.Object.Size < 0 || receipt.Object.Size > 64<<20 || c.HashBytes([]byte(receipt.Object.Path))+".json" != parts[3] || (receipt.Status != "UNLINKED_AND_DIRECTORY_SYNCED" && receipt.Status != "ALREADY_ABSENT_AFTER_INTENT") {
			return c.Fail(c.StoreIntegrityError, "invalid GC object receipt")
		}
		canonical, _ = c.CanonicalV1(receipt)
	}
	if canonical == nil || !bytes.Equal(raw, canonical) {
		return c.Fail(c.StoreIntegrityError, "noncanonical GC maintenance record")
	}
	return nil
}

type gcMaintenanceGroup struct {
	command  *GCCommand
	result   *GCResult
	receipts map[string]GCObjectReceipt
}

func readGCCommandRecords(root *os.Root, base string) (map[string][]byte, error) {
	entries, err := fs.ReadDir(root.FS(), filepath.ToSlash(base))
	if err != nil {
		return nil, err
	}
	if len(entries) > 2050 { // one intent, one completion, and at most 2048 object receipts
		return nil, c.Fail(c.BudgetLimitReached, "GC command record quota exceeded")
	}
	records := make(map[string][]byte, len(entries))
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, c.Fail(c.StoreIntegrityError, "unexpected GC command record directory")
		}
		name := filepath.ToSlash(filepath.Join(base, entry.Name()))
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		size := info.Size()
		if size < 0 || size > 2<<20 || size > gcMaintenanceMaxBytes-total {
			return nil, c.Fail(c.BudgetLimitReached, "GC command maintenance byte quota exceeded")
		}
		raw, err := fileguard.ReadRegular(root, filepath.FromSlash(name), size)
		if err != nil {
			return nil, err
		}
		if int64(len(raw)) > gcMaintenanceMaxBytes-total {
			return nil, c.Fail(c.BudgetLimitReached, "GC command maintenance byte quota exceeded")
		}
		total += int64(len(raw))
		records[name] = raw
	}
	return records, nil
}

func validateGCCompletionAgainstCommand(command GCCommand, result GCResult) error {
	if result.SchemaVersion != 1 || result.CommandID != command.CommandID || result.PlanDigest != command.Plan.Digest || result.Objects == nil || len(result.Objects) != len(command.Plan.Objects) {
		return c.Fail(c.StoreIntegrityError, "GC completion is not bound to its durable intent")
	}
	bytesRemoved := int64(0)
	for i, receipt := range result.Objects {
		if receipt.Object != command.Plan.Objects[i] || (receipt.Status != "UNLINKED_AND_DIRECTORY_SYNCED" && receipt.Status != "ALREADY_ABSENT_AFTER_INTENT") {
			return c.Fail(c.StoreIntegrityError, "GC completion object differs from its durable intent")
		}
		if receipt.Status == "UNLINKED_AND_DIRECTORY_SYNCED" {
			bytesRemoved += receipt.Object.Size
		}
	}
	if result.BytesRemoved != bytesRemoved {
		return c.Fail(c.StoreIntegrityError, "GC completion byte count differs from its durable intent")
	}
	return nil
}

// validateGCMaintenanceRecords binds each completion to the exact command and
// object list in its durable intent. Per-object receipts may be absent only
// after a matching completion has made them redundant; partial commands still
// require each existing receipt to match its intent and completion status.
func validateGCMaintenanceRecords(records map[string][]byte) error {
	_, err := gcReceiptCompactionUsedInRecords(records)
	return err
}

func gcReceiptCompactionUsedInRecords(records map[string][]byte) (bool, error) {
	var total int64
	for _, raw := range records {
		if int64(len(raw)) > gcMaintenanceMaxBytes-total {
			return false, c.Fail(c.BudgetLimitReached, "GC maintenance byte quota exceeded")
		}
		total += int64(len(raw))
	}
	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	sort.Strings(names)
	groups := map[string]*gcMaintenanceGroup{}
	for _, name := range names {
		raw := records[name]
		if err := validateGCRecord(name, raw); err != nil {
			return false, err
		}
		parts := strings.Split(name, "/")
		group := groups[parts[2]]
		if group == nil {
			group = &gcMaintenanceGroup{receipts: map[string]GCObjectReceipt{}}
			groups[parts[2]] = group
		}
		switch parts[3] {
		case "intent.json":
			var command GCCommand
			if err := c.DecodeStrict(raw, &command); err != nil {
				return false, c.Fail(c.StoreIntegrityError, "invalid GC intent")
			}
			group.command = &command
		case "complete.json":
			var result GCResult
			if err := c.DecodeStrict(raw, &result); err != nil {
				return false, c.Fail(c.StoreIntegrityError, "invalid GC completion record")
			}
			group.result = &result
		default:
			var receipt GCObjectReceipt
			if err := c.DecodeStrict(raw, &receipt); err != nil {
				return false, c.Fail(c.StoreIntegrityError, "invalid GC object receipt")
			}
			if _, exists := group.receipts[receipt.Object.Path]; exists {
				return false, c.Fail(c.StoreIntegrityError, "duplicate GC object receipt")
			}
			group.receipts[receipt.Object.Path] = receipt
		}
	}
	groupIDs := make([]string, 0, len(groups))
	for id := range groups {
		groupIDs = append(groupIDs, id)
	}
	sort.Strings(groupIDs)
	compacted := false
	for _, id := range groupIDs {
		group := groups[id]
		if group.command == nil {
			return false, c.Fail(c.StoreIntegrityError, "GC maintenance records lack their durable intent")
		}
		if group.result != nil {
			if err := validateGCCompletionAgainstCommand(*group.command, *group.result); err != nil {
				return false, err
			}
			if len(group.receipts) < len(group.command.Plan.Objects) {
				compacted = true
			}
		}
		objects := make(map[string]artifact.Object, len(group.command.Plan.Objects))
		for _, object := range group.command.Plan.Objects {
			objects[object.Path] = object
		}
		completed := make(map[string]GCObjectReceipt, len(group.command.Plan.Objects))
		if group.result != nil {
			for _, receipt := range group.result.Objects {
				completed[receipt.Object.Path] = receipt
			}
		}
		for path, receipt := range group.receipts {
			object, exists := objects[path]
			if !exists || receipt.Object != object {
				return false, c.Fail(c.StoreIntegrityError, "GC object receipt differs from its durable intent")
			}
			if group.result != nil && completed[path] != receipt {
				return false, c.Fail(c.StoreIntegrityError, "GC object receipt differs from its durable completion")
			}
		}
	}
	return compacted, nil
}

func (s *Session) visitGCMaintenance(ctx context.Context, visit func(string, []byte) error) error {
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	return visitGCMaintenanceRoot(ctx, root, visit)
}

// GCReceiptCompactionUsed reports whether a completed GC command has fewer
// per-object receipts than its intent. It is read-only and bounded by the
// default control-byte reserve; package-switch callers must hold the owning
// store lock while using the result to reject older runtimes.
func GCReceiptCompactionUsed(ctx context.Context, directory string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return false, err
	}
	defer root.Close()
	if _, err = root.Lstat("maintenance/gc"); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	records, err := readGCMaintenanceRecords(ctx, root)
	if err != nil {
		return false, err
	}
	return gcReceiptCompactionUsedInRecords(records)
}

func visitGCMaintenanceRoot(ctx context.Context, root *os.Root, visit func(string, []byte) error) error {
	if _, err := root.Lstat("maintenance/gc"); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	records, err := readGCMaintenanceRecords(ctx, root)
	if err != nil {
		return err
	}
	if err = validateGCMaintenanceRecords(records); err != nil {
		return err
	}
	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = visit(name, records[name]); err != nil {
			return err
		}
	}
	return nil
}

func readGCMaintenanceRecords(ctx context.Context, root *os.Root) (map[string][]byte, error) {
	count := 0
	var total int64
	records := map[string][]byte{}
	err := fs.WalkDir(root.FS(), "maintenance/gc", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 32768 {
			return c.Fail(c.BudgetLimitReached, "GC maintenance quota exceeded")
		}
		parts := strings.Split(name, "/")
		if entry.IsDir() {
			if name == "maintenance/gc" || len(parts) == 3 && c.ValidDigest(parts[2]) {
				return nil
			}
			return c.Fail(c.StoreIntegrityError, "unexpected GC maintenance directory")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		size := info.Size()
		if size < 0 || size > 2<<20 || size > gcMaintenanceMaxBytes-total {
			return c.Fail(c.BudgetLimitReached, "GC maintenance byte quota exceeded")
		}
		raw, err := fileguard.ReadRegular(root, filepath.FromSlash(name), size)
		if err != nil {
			return err
		}
		if int64(len(raw)) > gcMaintenanceMaxBytes-total {
			return c.Fail(c.BudgetLimitReached, "GC maintenance byte quota exceeded")
		}
		total += int64(len(raw))
		records[name] = raw
		return nil
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}
