package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/resource"
	"github.com/ixayldz/Viber/internal/store"
)

const maxSupportReportBytes = 2 << 20

func supportExportEnvelope(report SupportReport) ([]byte, uint64, uint64, error) {
	raw, err := c.CanonicalV1(report)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(raw) > maxSupportReportBytes {
		return nil, 0, 0, c.Fail(c.BudgetLimitReached, "support report exceeds bounded numeric profile")
	}
	estimateReport := report
	if report.PrivacyMetadata != nil {
		status := *report.PrivacyMetadata
		status.AvailableBytes = uint64(1<<63 - 1)
		status.WorkBlockers = append([]string(nil), report.PrivacyMetadata.WorkBlockers...)
		status.NextActions = append([]string(nil), report.PrivacyMetadata.NextActions...)
		estimateReport.PrivacyMetadata = &status
	}
	estimateRaw, err := c.CanonicalV1(estimateReport)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(estimateRaw) > maxSupportReportBytes {
		return nil, 0, 0, c.Fail(c.BudgetLimitReached, "support export estimate exceeds bounded numeric profile")
	}
	upperBytes, upperEntries, err := exportPhysicalEstimate(nil, estimateRaw)
	return raw, upperBytes, upperEntries, err
}

type supportExportBinding struct {
	SchemaVersion            int                `json:"schema_version"`
	Store                    store.SnapshotInfo `json:"store"`
	RuntimeInstanceID        string             `json:"runtime_instance_id"`
	RuntimePhysicalRoot      string             `json:"runtime_physical_root"`
	PrivacyAuthorityDigest   string             `json:"privacy_authority_digest,omitempty"`
	RegistrySequence         int64              `json:"registry_sequence"`
	RegistryTail             string             `json:"registry_tail,omitempty"`
	DeletionWatermark        int64              `json:"deletion_watermark"`
	RegistryCheckpointDigest string             `json:"registry_checkpoint_digest,omitempty"`
	NumericReportDigest      string             `json:"numeric_report_digest"`
}

func supportReportDigest(report SupportReport) (string, error) {
	copy := report
	if report.PrivacyMetadata != nil {
		status := *report.PrivacyMetadata
		status.AvailableBytes = 0
		status.WorkBlockers = append([]string(nil), report.PrivacyMetadata.WorkBlockers...)
		status.NextActions = append([]string(nil), report.PrivacyMetadata.NextActions...)
		copy.PrivacyMetadata = &status
	}
	raw, err := c.CanonicalV1(copy)
	if err != nil {
		return "", err
	}
	if len(raw) > maxSupportReportBytes {
		return "", c.Fail(c.BudgetLimitReached, "support report exceeds bounded numeric profile")
	}
	return c.HashBytes(raw), nil
}

func buildSupportExportBinding(ctx context.Context, s *Session, report SupportReport) (supportExportBinding, []byte, error) {
	binding := supportExportBinding{SchemaVersion: 1, RuntimeInstanceID: s.instance.ID, RuntimePhysicalRoot: s.instance.PhysicalRoot}
	info, err := s.Journal.SnapshotInfo(ctx)
	if err != nil {
		return binding, nil, err
	}
	if info.SchemaVersion != report.StoreSchema || info.ReducerVersion != report.ReducerVersion || info.StoreSeq != report.JournalSeq {
		return binding, nil, c.Fail(c.StaleBase, "support journal changed before export reservation")
	}
	binding.Store = info
	if s.privacy != nil {
		binding.PrivacyAuthorityDigest, err = c.Digest(*s.privacy)
		if err != nil {
			return binding, nil, err
		}
		root, openErr := openPrivacyRoot(*s.privacy)
		if openErr != nil {
			return binding, nil, openErr
		}
		lock, lockErr := privacyLock(ctx, root)
		if lockErr != nil {
			root.Close()
			return binding, nil, lockErr
		}
		view, viewErr := readPrivacy(root)
		bytesUsed, bytesErr := privacyMetadataBytes(root)
		anchor, checkpointDigest, checkpointErr := currentPrivacyCheckpointAnchor(root)
		closeErr := errors.Join(lock.Close(), root.Close())
		if err = errors.Join(viewErr, bytesErr, checkpointErr, closeErr); err != nil {
			return binding, nil, err
		}
		if report.PrivacyMetadata == nil || report.PrivacyMetadata.Sequence != view.Sequence || report.PrivacyMetadata.Records != view.RecordCount || report.PrivacyMetadata.JournalBytes != bytesUsed {
			return binding, nil, c.Fail(c.StaleBase, "privacy registry changed before support export reservation")
		}
		binding.RegistrySequence, binding.RegistryTail, binding.DeletionWatermark = view.Sequence, view.Tail, view.Watermark
		if anchor != nil {
			binding.RegistryCheckpointDigest = checkpointDigest
		}
	} else if report.PrivacyMetadata != nil {
		return binding, nil, c.Fail(c.StaleAuthority, "support privacy status has no bound authority")
	}
	binding.NumericReportDigest, err = supportReportDigest(report)
	if err != nil {
		return binding, nil, err
	}
	raw, err := c.CanonicalV1(binding)
	return binding, raw, err
}

func decodeSupportExportBinding(raw []byte) (supportExportBinding, error) {
	var binding supportExportBinding
	if len(raw) == 0 || len(raw) > 32<<10 || c.DecodeStrict(raw, &binding) != nil || binding.SchemaVersion != 1 || binding.RuntimeInstanceID == "" || !c.ValidDigest(binding.RuntimePhysicalRoot) || !c.ValidDigest(binding.NumericReportDigest) || binding.Store.SchemaVersion < 1 || binding.Store.ReducerVersion < 1 || binding.Store.StoreSeq < 0 || binding.RegistrySequence < 0 || binding.DeletionWatermark < 0 || binding.RegistrySequence == 0 && binding.RegistryTail != "" || binding.RegistrySequence > 0 && !c.ValidDigest(binding.RegistryTail) || binding.PrivacyAuthorityDigest != "" && !c.ValidDigest(binding.PrivacyAuthorityDigest) || binding.RegistryCheckpointDigest != "" && !c.ValidDigest(binding.RegistryCheckpointDigest) {
		return binding, c.Fail(c.StoreIntegrityError, "saved support export binding is invalid")
	}
	canonical, err := c.CanonicalV1(binding)
	if err != nil || !bytes.Equal(canonical, raw) {
		return binding, c.Fail(c.StoreIntegrityError, "saved support export binding is noncanonical")
	}
	return binding, nil
}

// supportRetryBinding accepts either the exact pre-write registry snapshot or
// one contiguous suffix that this same support export registered immediately
// after creating its root. Any checkpoint, watermark change, or other registry
// append leaves the retry stale.
func supportRetryBinding(ctx context.Context, s *Session, target string, report SupportReport, current supportExportBinding, savedRaw []byte) ([]byte, bool, error) {
	saved, err := decodeSupportExportBinding(savedRaw)
	if err != nil {
		return nil, false, err
	}
	currentRaw, err := c.CanonicalV1(current)
	if err != nil {
		return nil, false, err
	}
	if bytes.Equal(currentRaw, savedRaw) {
		return append([]byte(nil), savedRaw...), false, nil
	}
	if current.Store != saved.Store || current.RuntimeInstanceID != saved.RuntimeInstanceID || current.RuntimePhysicalRoot != saved.RuntimePhysicalRoot || current.PrivacyAuthorityDigest != saved.PrivacyAuthorityDigest || current.DeletionWatermark != saved.DeletionWatermark || current.RegistryCheckpointDigest != saved.RegistryCheckpointDigest || current.RegistrySequence != saved.RegistrySequence+1 || s.privacy == nil {
		return nil, false, c.Fail(c.StaleAuthority, "support retry binding changed beyond its own registered output")
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return nil, false, err
	}
	lock, err := privacyLock(ctx, root)
	if err != nil {
		root.Close()
		return nil, false, err
	}
	view, viewErr := readPrivacy(root)
	checkpointAnchor, checkpointDigest, checkpointErr := currentPrivacyCheckpointAnchor(root)
	if err = errors.Join(viewErr, checkpointErr); err != nil {
		_ = lock.Close()
		_ = root.Close()
		return nil, false, err
	}
	if view.Sequence != current.RegistrySequence || view.Tail != current.RegistryTail || view.Watermark != saved.DeletionWatermark || (checkpointAnchor == nil && saved.RegistryCheckpointDigest != "") || (checkpointAnchor != nil && checkpointDigest != saved.RegistryCheckpointDigest) {
		_ = lock.Close()
		_ = root.Close()
		return nil, false, c.Fail(c.StaleAuthority, "support retry privacy lineage changed")
	}
	sequence := view.Sequence
	name := filepath.Join("records", fmt.Sprintf("%010d.json", sequence))
	recordRaw, err := fileguard.ReadRegular(root, name, 16<<20)
	closeErr := errors.Join(lock.Close(), root.Close())
	if err = errors.Join(err, closeErr); err != nil {
		return nil, false, err
	}
	var record privacyRecord
	if c.DecodeStrict(recordRaw, &record) != nil || record.SchemaVersion != 1 || record.Sequence != sequence || record.Previous != saved.RegistryTail || record.Type != "MANAGED_COPY" || record.Copy == nil || record.Copy.Kind != "SUPPORT" || filepath.Clean(record.Copy.Directory) != filepath.Clean(target) || len(record.Copy.Files) != 0 {
		return nil, false, c.Fail(c.StaleAuthority, "support retry registry suffix is not its exact output registration")
	}
	canonicalRecord, err := c.CanonicalV1(record)
	if err != nil || !bytes.Equal(canonicalRecord, recordRaw) || current.RegistryTail != c.HashBytes(recordRaw) {
		return nil, false, c.Fail(c.StoreIntegrityError, "support retry registry suffix is noncanonical")
	}
	outputRoot, err := os.OpenRoot(target)
	if err != nil {
		return nil, false, err
	}
	physical, idErr := fileguard.DirectoryIdentity(outputRoot)
	closeErr = outputRoot.Close()
	if err = errors.Join(idErr, closeErr); err != nil {
		return nil, false, err
	}
	tasks, err := supportTasks(ctx, s)
	if err != nil {
		return nil, false, err
	}
	if record.Copy.PhysicalRoot != physical || !sameManagedTasks(record.Copy.Tasks, tasks) {
		return nil, false, c.Fail(c.StaleAuthority, "support retry output registration differs from the saved task inventory")
	}
	if report.PrivacyMetadata == nil || report.PrivacyMetadata.Sequence != view.Sequence || report.PrivacyMetadata.Records != view.RecordCount || report.PrivacyMetadata.Records < 1 || report.PrivacyMetadata.JournalBytes < int64(len(recordRaw)) {
		return nil, false, c.Fail(c.StaleAuthority, "support retry report does not match its registered output suffix")
	}
	adjusted := report
	status := *report.PrivacyMetadata
	status.Sequence--
	status.Records--
	status.JournalBytes -= int64(len(recordRaw))
	status.WorkBlockers = append([]string(nil), report.PrivacyMetadata.WorkBlockers...)
	status.NextActions = append([]string(nil), report.PrivacyMetadata.NextActions...)
	adjusted.PrivacyMetadata = &status
	digest, err := supportReportDigest(adjusted)
	if err != nil {
		return nil, false, err
	}
	if digest != saved.NumericReportDigest {
		return nil, false, c.Fail(c.StaleAuthority, "support retry numeric report changed")
	}
	return append([]byte(nil), savedRaw...), true, nil
}

func supportTasks(ctx context.Context, s *Session) ([]string, error) {
	states, err := s.Journal.Replay(ctx, "")
	if err != nil {
		return nil, err
	}
	tasks := make([]string, 0, len(states))
	for task, state := range states {
		if state.Deletion == nil {
			tasks = append(tasks, task)
		}
	}
	sort.Strings(tasks)
	return tasks, nil
}

func verifySupportManagedCopy(ctx context.Context, s *Session, target, physicalRoot string, tasks []string) error {
	if s.privacy == nil {
		return c.Fail(c.StaleAuthority, "support managed-copy authority is unavailable")
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return err
	}
	view, readErr := readPrivacy(root)
	closeErr := lock.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		return err
	}
	count := 0
	for _, copy := range view.Copies {
		if filepath.Clean(copy.Directory) != filepath.Clean(target) {
			continue
		}
		count++
		if copy.Kind != "SUPPORT" || copy.PhysicalRoot != physicalRoot || len(copy.Files) != 0 || !sameManagedTasks(copy.Tasks, tasks) {
			return c.Fail(c.StaleAuthority, "support output differs from its managed-copy registration")
		}
	}
	if count != 1 {
		return c.Fail(c.StaleAuthority, "support output has no unique managed-copy registration")
	}
	return nil
}

func readSupportPayload(raw []byte) (SupportReport, error) {
	var report SupportReport
	if len(raw) == 0 || len(raw) > maxSupportReportBytes || c.DecodeStrict(raw, &report) != nil {
		return report, c.Fail(c.StoreIntegrityError, "support export payload is invalid")
	}
	canonical, err := c.CanonicalV1(report)
	if err != nil || !bytes.Equal(canonical, raw) {
		return report, c.Fail(c.StoreIntegrityError, "support export payload is noncanonical")
	}
	return report, nil
}

func validateRetainedSupport(ctx context.Context, match resource.ReservationMatch, root *os.Root, bindingRaw []byte) (SupportReport, error) {
	var report SupportReport
	if match.Status != "RETAINED" || len(bindingRaw) == 0 {
		return report, c.Fail(c.StaleAuthority, "support output has no retained intent")
	}
	raw, err := fileguard.ReadRegular(root, "support.json", maxSupportReportBytes)
	if err != nil {
		return report, err
	}
	if c.HashBytes(raw) != match.RequestDigest {
		return report, c.Fail(c.StaleAuthority, "retained support report differs from its original intent")
	}
	report, err = readSupportPayload(raw)
	if err != nil {
		return report, err
	}
	binding, err := decodeSupportExportBinding(bindingRaw)
	if err != nil {
		return report, err
	}
	digest, err := supportReportDigest(report)
	if err != nil || digest != binding.NumericReportDigest {
		return report, c.Fail(c.StaleAuthority, "retained support report differs from its stable binding")
	}
	inventory, err := resource.Measure(root, true)
	if err != nil {
		return report, err
	}
	if err = match.Allocation.ValidateRetained(ctx, inventory); err != nil {
		return report, err
	}
	return report, nil
}
