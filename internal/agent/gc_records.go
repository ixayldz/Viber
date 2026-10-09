package agent

import (
	"context"
	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

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
	case "complete.json":
		var result GCResult
		if c.DecodeStrict(raw, &result) != nil || result.SchemaVersion != 1 || c.HashBytes([]byte(result.CommandID)) != parts[2] || !c.ValidDigest(result.PlanDigest) || len(result.Objects) > 2048 {
			return c.Fail(c.StoreIntegrityError, "invalid GC completion record")
		}
		bytes := int64(0)
		for _, receipt := range result.Objects {
			if receipt.Status == "UNLINKED_AND_DIRECTORY_SYNCED" {
				bytes += receipt.Object.Size
			} else if receipt.Status != "ALREADY_ABSENT_AFTER_INTENT" {
				return c.Fail(c.StoreIntegrityError, "invalid GC completion status")
			}
		}
		if result.BytesRemoved != bytes {
			return c.Fail(c.StoreIntegrityError, "invalid GC byte receipt")
		}
	default:
		var receipt GCObjectReceipt
		if c.DecodeStrict(raw, &receipt) != nil || !artifact.ImmutablePath(receipt.Object.Path) || !c.ValidDigest(receipt.Object.Digest) || receipt.Object.Size < 0 || receipt.Object.Size > 64<<20 || c.HashBytes([]byte(receipt.Object.Path))+".json" != parts[3] || (receipt.Status != "UNLINKED_AND_DIRECTORY_SYNCED" && receipt.Status != "ALREADY_ABSENT_AFTER_INTENT") {
			return c.Fail(c.StoreIntegrityError, "invalid GC object receipt")
		}
	}
	return nil
}
func (s *Session) visitGCMaintenance(ctx context.Context, visit func(string, []byte) error) error {
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err = root.Lstat("maintenance/gc"); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	count := 0
	return fs.WalkDir(root.FS(), "maintenance/gc", func(name string, entry fs.DirEntry, err error) error {
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
		raw, err := fileguard.ReadRegular(root, filepath.FromSlash(name), 2<<20)
		if err != nil {
			return err
		}
		if err = validateGCRecord(name, raw); err != nil {
			return err
		}
		return visit(name, raw)
	})
}
