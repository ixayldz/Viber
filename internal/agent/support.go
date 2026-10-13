package agent

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/resource"
)

type SupportTask struct {
	ContentDeletion     string           `json:"content_deletion,omitempty"`
	Ordinal             int              `json:"ordinal"`
	Execution           c.ExecutionState `json:"execution_state"`
	Outcome             c.Outcome        `json:"outcome"`
	Quality             c.Quality        `json:"quality"`
	Fulfillment         c.Fulfillment    `json:"fulfillment"`
	TaskSeq             int64            `json:"task_seq"`
	InputBarrier        bool             `json:"input_barrier"`
	RequiredObligations int              `json:"open_required_obligations"`
	Provider            string           `json:"provider_class"`
	Budget              Budget           `json:"budget"`
	PendingKind         string           `json:"pending_kind,omitempty"`
	PendingStatus       string           `json:"pending_status,omitempty"`
	UnknownEffect       bool             `json:"unknown_effect"`
	BaselineFiles       int              `json:"baseline_files"`
	CandidateFiles      int              `json:"candidate_files"`
	Compactions         int              `json:"compactions"`
	Pins                int              `json:"pins"`
	Checks              int              `json:"retained_check_runs"`
}
type SupportReport struct {
	PrivacyMetadata            *PrivacyMetadataStatus `json:"privacy_metadata_capacity,omitempty"`
	SchemaVersion              int                    `json:"schema_version"`
	Profile                    string                 `json:"profile"`
	StoreSchema                int                    `json:"store_schema"`
	ReducerVersion             int                    `json:"reducer_version"`
	JournalSeq                 int64                  `json:"journal_seq"`
	Tasks                      []SupportTask          `json:"tasks"`
	TokenCoverage              string                 `json:"token_coverage"`
	TokenCharged               c.TokenLimits          `json:"global_token_charge"`
	TokenReserved              c.TokenLimits          `json:"global_token_reservation"`
	ResourceCoverage           string                 `json:"resource_coverage"`
	ResourceCharged            c.ResourceVector       `json:"global_resource_charge"`
	ResourceReserved           c.ResourceVector       `json:"global_resource_reservation"`
	Telemetry                  bool                   `json:"telemetry"`
	TrainingExport             bool                   `json:"training_export"`
	RawPayloadsIncluded        bool                   `json:"raw_payloads_included"`
	CredentialMetadataIncluded bool                   `json:"credential_metadata_included"`
	ReleaseReady               bool                   `json:"release_ready"`
}

// Support includes only an explicit typed allowlist. It does not depend on a
// heuristic secret scanner and never exports arbitrary model/operator strings,
// source names/hashes, task IDs, source roots, endpoints, credentials or logs.
func (s *Session) Support(ctx context.Context) (SupportReport, error) {
	report := SupportReport{SchemaVersion: 1, Profile: "NUMERIC_ALLOWLIST_SUPPORT_V1", Tasks: []SupportTask{}}
	info, err := s.Journal.SnapshotInfo(ctx)
	if err != nil {
		return report, err
	}
	report.StoreSchema, report.ReducerVersion, report.JournalSeq = info.SchemaVersion, info.ReducerVersion, info.StoreSeq
	states, err := s.Journal.Replay(ctx, "")
	if err != nil {
		return report, err
	}
	ids := make([]string, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > 1024 {
		return report, c.Fail(c.BudgetLimitReached, "support task count exceeds bounded profile")
	}
	for i, id := range ids {
		if state := states[id]; state.Deletion != nil {
			report.Tasks = append(report.Tasks, SupportTask{Ordinal: i + 1, Execution: state.Execution, Outcome: state.Outcome, Quality: state.Quality, Fulfillment: state.Fulfillment, TaskSeq: state.TaskSeq, RequiredObligations: state.OpenRequiredObligations, ContentDeletion: state.Deletion.Status, Provider: "REDACTED"})
			continue
		}
		state, doc, err := s.Load(ctx, id)
		if err != nil {
			return report, err
		}
		task := SupportTask{Ordinal: i + 1, Execution: state.Execution, Outcome: state.Outcome, Quality: state.Quality, Fulfillment: state.Fulfillment, TaskSeq: state.TaskSeq, InputBarrier: state.InputBarrier, RequiredObligations: state.OpenRequiredObligations, Budget: doc.Budget, Provider: "fixture", UnknownEffect: doc.UnknownEffect, Compactions: len(doc.Compactions), Pins: len(doc.Pins), Checks: len(doc.CheckRuns)}
		if doc.Runtime != nil {
			task.Provider = doc.Runtime.Provider
		}
		if doc.Pending != nil {
			task.PendingKind, task.PendingStatus = "OTHER", "OTHER"
			if doc.Pending.Kind == "MODEL" || doc.Pending.Kind == "NATIVE_TOOL" {
				task.PendingKind = doc.Pending.Kind
			}
			if doc.Pending.Status == "INTENT" || doc.Pending.Status == "RESERVED" || doc.Pending.Status == "UNKNOWN" {
				task.PendingStatus = doc.Pending.Status
			}
		}
		before, err := s.Archive.ReadManifest(doc.Baseline)
		if err != nil {
			return report, err
		}
		after, err := s.Archive.ReadManifest(doc.Candidate)
		if err != nil {
			return report, err
		}
		task.BaselineFiles, task.CandidateFiles = len(before.Snapshot.Entries), len(after.Snapshot.Entries)
		report.Tasks = append(report.Tasks, task)
	}
	tokens, err := s.Journal.TokenLedger(ctx)
	if err != nil {
		return report, err
	}
	report.TokenCoverage, report.TokenCharged, report.TokenReserved = tokens.Coverage, tokens.Used, tokens.Reserved
	resources, err := s.Journal.ResourceLedger(ctx)
	if err != nil {
		return report, err
	}
	report.ResourceCoverage, report.ResourceCharged, report.ResourceReserved = resources.Coverage, resources.Used, resources.Reserved
	report.PrivacyMetadata, err = s.PrivacyMetadataCapacity(ctx)
	if err != nil {
		return report, err
	}
	current, err := s.Journal.SnapshotInfo(ctx)
	if err != nil {
		return report, err
	}
	if current != info {
		return report, c.Fail(c.StaleBase, "support snapshot changed during collection; retry")
	}
	return report, nil
}
func (s *Session) ExportSupport(ctx context.Context, output string) (SupportReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	report, err := s.Support(ctx)
	if err != nil {
		return report, err
	}
	roots, err := s.backupClosure(ctx)
	if err != nil {
		return report, err
	}
	roots = append(roots, s.directory)
	if err = disjointRoots(output, roots); err != nil {
		return report, err
	}
	target, err := fileguard.ResolveProspective(output)
	if err != nil {
		return report, err
	}
	raw, upperBytes, upperEntries, err := supportExportEnvelope(report)
	if err != nil {
		return report, err
	}
	if err = s.Journal.DiskAdmission(int64(len(raw))+1<<20, false); err != nil {
		return report, err
	}
	var allocation resource.Allocation
	owned := false
	var bindingRaw []byte
	registeredCopy := false
	if s.PhysicalQuota != nil {
		currentBinding, currentBindingRaw, bindingErr := buildSupportExportBinding(ctx, s, report)
		if bindingErr != nil {
			return report, bindingErr
		}
		bindingRaw = currentBindingRaw
		prior, found, findErr := s.PhysicalQuota.FindReservationIntent(ctx, target, "agent", "SUPPORT")
		if findErr != nil {
			return report, findErr
		}
		if found {
			bindingRaw, registeredCopy, err = supportRetryBinding(ctx, s, target, report, currentBinding, prior.Binding)
			if err != nil {
				return report, err
			}
			// The stable binding was validated above, so retry with the exact
			// envelope admitted alongside its original payload. The generic
			// ledger matcher still enforces that envelope byte-for-byte.
			upperBytes, upperEntries = prior.Match.UpperBytes, prior.Match.UpperEntries
		}
	}
	if s.PhysicalQuota != nil {
		request := resource.Request{Writer: "agent", Family: "SUPPORT", RequestDigest: c.HashBytes(raw), TargetRoot: target, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Payload}
		match, savedPayload, matchErr := s.PhysicalQuota.ReserveOrMatchWithIntent(ctx, request, bindingRaw, raw)
		if matchErr != nil {
			return report, matchErr
		}
		switch match.Scope {
		case resource.AncestorReservationScope:
			// The trusted outer writer owns this receipt; do not settle it here.
		case resource.ExactReservationScope:
			allocation = match.Allocation
			owned = true
			switch match.Status {
			case "RESERVED":
				if err = allocation.Begin(ctx); err != nil {
					return report, err
				}
			case "WRITING":
				// Resume an interrupted writer under its original reservation.
			case "RETAINED":
				if !registeredCopy {
					return report, c.Fail(c.StaleAuthority, "retained support output has no verified self-registration")
				}
				retainedRoot, openErr := os.OpenRoot(target)
				if openErr != nil {
					return report, openErr
				}
				retainedReport, validateErr := validateRetainedSupport(ctx, match, retainedRoot, bindingRaw)
				closeErr := retainedRoot.Close()
				if err = errors.Join(validateErr, closeErr); err != nil {
					return report, err
				}
				return retainedReport, nil
			default:
				return report, c.Fail(c.StaleAuthority, "support output reservation is not writable")
			}
		default:
			return report, c.Fail(c.StoreIntegrityError, "support physical reservation scope is invalid")
		}
		if match.Scope == resource.ExactReservationScope {
			if len(savedPayload) == 0 {
				return report, c.Fail(c.StoreIntegrityError, "active support intent did not return its original payload")
			}
			raw = savedPayload
			report, err = readSupportPayload(raw)
			if err != nil {
				return report, err
			}
		}
	}
	var root *os.Root
	if registeredCopy {
		root, err = os.OpenRoot(target)
		if err == nil {
			err = fileguard.Private(root)
		}
	} else if _, statErr := os.Lstat(target); statErr == nil {
		err = c.Fail(c.StaleAuthority, "support retry found an output directory without its exact managed registration")
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		err = statErr
	} else {
		root, err = freshPrivate(target)
	}
	if err != nil {
		if root != nil {
			_ = root.Close()
		}
		return report, err
	}
	defer root.Close()
	tasks, err := supportTasks(ctx, s)
	if err != nil {
		return report, err
	}
	if registeredCopy {
		identity, identityErr := fileguard.DirectoryIdentity(root)
		if identityErr != nil {
			return report, identityErr
		}
		if err = verifySupportManagedCopy(ctx, s, target, identity, tasks); err != nil {
			return report, err
		}
	} else if err = s.allocateManagedCopy(ctx, root, "SUPPORT", tasks); err != nil {
		return report, err
	}
	if existing, readErr := fileguard.ReadRegular(root, "support.json", maxSupportReportBytes); readErr == nil {
		if !bytes.Equal(existing, raw) {
			return report, c.Fail(c.StaleAuthority, "interrupted support output contains a different report")
		}
	} else if errors.Is(readErr, os.ErrNotExist) {
		entries, listErr := fs.ReadDir(root.FS(), ".")
		if listErr != nil {
			return report, listErr
		}
		if len(entries) != 0 {
			return report, c.Fail(c.StaleAuthority, "interrupted support output contains an unrecognized partial file")
		}
		if err = fileguard.Publish(root, "support.json", raw); err != nil {
			return report, err
		}
	} else {
		return report, readErr
	}
	parent, err := root.OpenRoot(".")
	if err != nil {
		return report, err
	}
	defer parent.Close()
	if err = fileguard.SyncParents(parent, filepath.Dir("support.json")); err != nil {
		return report, err
	}
	if s.PhysicalQuota != nil && owned {
		inventory, measureErr := resource.Measure(root, true)
		if measureErr != nil {
			return report, measureErr
		}
		if inventory.Entries != 2 || len(inventory.Files) != 1 || inventory.Files[0].Path != "support.json" || inventory.Files[0].Digest != c.HashBytes(raw) {
			return report, c.Fail(c.StaleAuthority, "support output contains content outside its original intent")
		}
		if err = allocation.Commit(ctx, inventory); err != nil {
			return report, err
		}
	}
	return report, nil
}
