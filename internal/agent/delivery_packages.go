package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/resource"
)

type deliveryArtifactManifest struct {
	SchemaVersion int          `json:"schema_version"`
	Kind          string       `json:"kind"`
	TaskID        string       `json:"task_id"`
	OperationID   string       `json:"operation_id"`
	ReportDigest  string       `json:"report_digest"`
	Files         []BackupFile `json:"files"`
	Digest        string       `json:"digest"`
}

type deliveryArtifact struct {
	Directory string
	Manifest  deliveryArtifactManifest
	Report    []byte
	Files     map[string][]byte
}

func sealDeliveryArtifactManifest(manifest *deliveryArtifactManifest) ([]byte, error) {
	manifest.Digest = ""
	digest, err := c.Digest(*manifest)
	if err != nil {
		return nil, err
	}
	manifest.Digest = digest
	return c.CanonicalV1(*manifest)
}

func validateDeliveryArtifactManifest(manifest deliveryArtifactManifest) error {
	if manifest.SchemaVersion != 1 || (manifest.Kind != "PLAN" && manifest.Kind != "FILE_RECEIPT" && manifest.Kind != "AGGREGATE_RECEIPT") || manifest.TaskID == "" || !validDeliveryOperationID(manifest.OperationID) || !c.ValidDigest(manifest.ReportDigest) || !c.ValidDigest(manifest.Digest) || len(manifest.Files) == 0 || len(manifest.Files) > backupMaxFiles {
		return c.Fail(c.StoreIntegrityError, "invalid managed delivery artifact manifest")
	}
	previous := ""
	var total int64
	for _, file := range manifest.Files {
		if file.Path <= previous || !safeCopyPath("EXPORT", file.Path) || !c.ValidDigest(file.Digest) || file.Size < 0 || file.Size > backupMaxBytes-total {
			return c.Fail(c.StoreIntegrityError, "invalid delivery artifact inventory")
		}
		previous = file.Path
		total += file.Size
	}
	expected := manifest.Digest
	manifest.Digest = ""
	digest, err := c.Digest(manifest)
	if err != nil || digest != expected {
		return c.Fail(c.StoreIntegrityError, "delivery artifact manifest digest mismatch")
	}
	return nil
}

func (s *Session) publishDeliveryArtifact(ctx context.Context, task, directory, kind, operationID string, report []byte, files map[string][]byte, disjointFrom string) (deliveryArtifact, error) {
	result := deliveryArtifact{Files: map[string][]byte{}}
	if s.PhysicalQuota == nil || task == "" || !validDeliveryOperationID(operationID) || kind == "" || len(report) == 0 || len(files)+1 > backupMaxFiles {
		return result, c.Fail(c.UnsupportedCapability, "managed delivery artifacts require a bound physical quota and bounded payload")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	target, err := fileguard.ResolveProspective(directory)
	if err != nil {
		return result, err
	}
	if !fileguard.Disjoint(target, s.directory) || !fileguard.Disjoint(target, s.privacy.Directory) || !fileguard.Disjoint(target, disjointFrom) {
		return result, c.Fail(c.PolicyDenied, "delivery package overlaps a protected owner or live workspace")
	}
	fileRecords := make([]BackupFile, 0, len(files)+1)
	var total, largest uint64
	entryCount := uint64(2)
	for name, raw := range files {
		if !safeCopyPath("EXPORT", name) || name == "manifest.json" || name == "report.json" {
			return result, c.Fail(c.PolicyDenied, "delivery package path is outside the registered export profile")
		}
		size := uint64(len(raw))
		if size > ^uint64(0)-total {
			return result, c.Fail(c.UnsupportedCapability, "delivery package size estimate overflow")
		}
		total += size
		if size > largest {
			largest = size
		}
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(name))); parent != "." && parent != "/"; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			entryCount++
		}
		fileRecords = append(fileRecords, BackupFile{Path: name, Digest: c.HashBytes(raw), Size: int64(len(raw))})
	}
	total += uint64(len(report))
	if uint64(len(report)) > largest {
		largest = uint64(len(report))
	}
	fileRecords = append(fileRecords, BackupFile{Path: "report.json", Digest: c.HashBytes(report), Size: int64(len(report))})
	sort.Slice(fileRecords, func(i, j int) bool { return fileRecords[i].Path < fileRecords[j].Path })
	manifest := deliveryArtifactManifest{SchemaVersion: 1, Kind: kind, TaskID: task, OperationID: operationID, ReportDigest: c.HashBytes(report), Files: fileRecords}
	manifestRaw, err := sealDeliveryArtifactManifest(&manifest)
	if err != nil {
		return result, err
	}
	manifestRecord := BackupFile{Path: "manifest.json", Digest: c.HashBytes(manifestRaw), Size: int64(len(manifestRaw))}
	managedFiles := append(append([]BackupFile{}, fileRecords...), manifestRecord)
	sort.Slice(managedFiles, func(i, j int) bool { return managedFiles[i].Path < managedFiles[j].Path })
	if uint64(len(manifestRaw)) > ^uint64(0)-total {
		return result, c.Fail(c.UnsupportedCapability, "delivery package size estimate overflow")
	}
	total += uint64(len(manifestRaw))
	if uint64(len(manifestRaw)) > largest {
		largest = uint64(len(manifestRaw))
	}
	margin := uint64(64 << 10)
	if largest > ^uint64(0)-total || total+largest > ^uint64(0)-margin || entryCount > 1<<20 {
		return result, c.Fail(c.UnsupportedCapability, "delivery package physical estimate overflow")
	}
	upperBytes := total + largest + margin
	upperEntries := entryCount + uint64(len(managedFiles))
	if upperEntries < uint64(len(managedFiles)) {
		return result, c.Fail(c.UnsupportedCapability, "delivery package entry estimate overflow")
	}
	requestDigest := c.HashBytes(manifestRaw)
	allocation, err := s.PhysicalQuota.Reserve(ctx, resource.Request{Writer: "agent", Family: "DELIVERY_OPERATION", RequestDigest: requestDigest, TargetRoot: target, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Payload})
	if err != nil {
		return result, err
	}
	if err = allocation.Begin(ctx); err != nil {
		return result, err
	}
	root, err := freshPrivate(target)
	if err != nil {
		return result, err
	}
	defer root.Close()
	if err = s.allocateManagedCopy(ctx, root, "EXPORT", []string{task}); err != nil {
		return result, err
	}
	if err = fileguard.Publish(root, "report.json", report); err != nil {
		return result, err
	}
	for name, raw := range files {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if err = fileguard.Publish(root, filepath.FromSlash(name), raw); err != nil {
			return result, err
		}
	}
	if err = fileguard.Publish(root, "manifest.json", manifestRaw); err != nil {
		return result, err
	}
	physical, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return result, err
	}
	if err = s.registerManagedCopy(ctx, ManagedCopy{Kind: "EXPORT", Directory: root.Name(), PhysicalRoot: physical, Files: managedFiles, Tasks: []string{task}}); err != nil {
		return result, err
	}
	inventory, err := resource.Measure(root, true)
	if err != nil {
		return result, err
	}
	if err = allocation.Commit(ctx, inventory); err != nil {
		return result, err
	}
	result.Directory = root.Name()
	result.Manifest = manifest
	result.Report = append([]byte{}, report...)
	for name, raw := range files {
		result.Files[name] = append([]byte{}, raw...)
	}
	return result, nil
}

func validDeliveryOperationID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func (s *Session) readDeliveryArtifacts(ctx context.Context, task, operationID, kind string) ([]deliveryArtifact, error) {
	if !validDeliveryOperationID(operationID) {
		return nil, c.Fail(c.InvalidArgument, "invalid delivery operation ID")
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return nil, err
	}
	lock, err := privacyLock(ctx, root)
	if err != nil {
		root.Close()
		return nil, err
	}
	view, err := readPrivacy(root)
	closeErr := errors.Join(lock.Close(), root.Close())
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	marker := ".delivery-" + operationID
	results := []deliveryArtifact{}
	for _, copy := range view.Copies {
		if copy.Kind != "EXPORT" || !sameManagedTasks(copy.Tasks, []string{task}) || !strings.Contains(filepath.Base(copy.Directory), marker) {
			continue
		}
		if err = checkManagedCopy(ctx, copy, false); err != nil {
			return nil, err
		}
		managed, openErr := os.OpenRoot(copy.Directory)
		if openErr != nil {
			return nil, openErr
		}
		manifestRaw, readErr := fileguard.ReadRegular(managed, "manifest.json", 1<<20)
		var manifest deliveryArtifactManifest
		if readErr == nil {
			readErr = c.DecodeStrict(manifestRaw, &manifest)
		}
		if readErr == nil {
			readErr = validateDeliveryArtifactManifest(manifest)
		}
		if readErr == nil && (manifest.TaskID != task || manifest.OperationID != operationID || c.HashBytes(manifestRaw) != copyFileDigest(copy.Files, "manifest.json")) {
			readErr = c.Fail(c.StoreIntegrityError, "registered delivery package does not match its task and operation")
		}
		if readErr != nil {
			managed.Close()
			return nil, readErr
		}
		if manifest.Kind != kind {
			if closeErr := managed.Close(); closeErr != nil {
				return nil, closeErr
			}
			continue
		}
		report, readErr := fileguard.ReadRegular(managed, "report.json", backupMaxBytes)
		if readErr == nil && c.HashBytes(report) != manifest.ReportDigest {
			readErr = c.Fail(c.StoreIntegrityError, "managed delivery report digest mismatch")
		}
		files := map[string][]byte{}
		for _, item := range manifest.Files {
			if item.Path == "report.json" {
				continue
			}
			if readErr != nil {
				break
			}
			files[item.Path], readErr = fileguard.ReadRegular(managed, item.Path, backupMaxBytes)
			if readErr == nil && (int64(len(files[item.Path])) != item.Size || c.HashBytes(files[item.Path]) != item.Digest) {
				readErr = c.Fail(c.StoreIntegrityError, "managed delivery file digest mismatch")
			}
		}
		closeErr := managed.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.Join(readErr, closeErr)
		}
		results = append(results, deliveryArtifact{Directory: copy.Directory, Manifest: manifest, Report: report, Files: files})
	}
	return results, nil
}

func copyFileDigest(files []BackupFile, path string) string {
	for _, file := range files {
		if file.Path == path {
			return file.Digest
		}
	}
	return ""
}

func deliveryArtifactDirectory(output, operationID, suffix string) (string, error) {
	if output == "" || !validDeliveryOperationID(operationID) || strings.ContainsAny(suffix, "\\/:\x00\r\n") {
		return "", c.Fail(c.InvalidArgument, "invalid delivery package location")
	}
	return fileguard.ResolveProspective(fmt.Sprintf("%s.delivery-%s.%s", output, operationID, suffix))
}
