package agent

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"path/filepath"
	"sort"
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
	SchemaVersion              int              `json:"schema_version"`
	Profile                    string           `json:"profile"`
	StoreSchema                int              `json:"store_schema"`
	ReducerVersion             int              `json:"reducer_version"`
	JournalSeq                 int64            `json:"journal_seq"`
	Tasks                      []SupportTask    `json:"tasks"`
	TokenCoverage              string           `json:"token_coverage"`
	TokenCharged               c.TokenLimits    `json:"global_token_charge"`
	TokenReserved              c.TokenLimits    `json:"global_token_reservation"`
	ResourceCoverage           string           `json:"resource_coverage"`
	ResourceCharged            c.ResourceVector `json:"global_resource_charge"`
	ResourceReserved           c.ResourceVector `json:"global_resource_reservation"`
	Telemetry                  bool             `json:"telemetry"`
	TrainingExport             bool             `json:"training_export"`
	RawPayloadsIncluded        bool             `json:"raw_payloads_included"`
	CredentialMetadataIncluded bool             `json:"credential_metadata_included"`
	ReleaseReady               bool             `json:"release_ready"`
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
	raw, err := c.CanonicalV1(report)
	if err != nil {
		return report, err
	}
	if err = s.Journal.DiskAdmission(int64(len(raw))+1<<20, false); err != nil {
		return report, err
	}
	root, err := freshPrivate(target)
	if err != nil {
		return report, err
	}
	defer root.Close()
	states, err := s.Journal.Replay(ctx, "")
	if err != nil {
		return report, err
	}
	tasks := []string{}
	for task, state := range states {
		if state.Deletion == nil {
			tasks = append(tasks, task)
		}
	}
	if err = s.allocateManagedCopy(ctx, root, "SUPPORT", tasks); err != nil {
		return report, err
	}
	if err = fileguard.Publish(root, "support.json", raw); err != nil {
		return report, err
	}
	parent, err := root.OpenRoot(".")
	if err != nil {
		return report, err
	}
	defer parent.Close()
	return report, fileguard.SyncParents(parent, filepath.Dir("support.json"))
}
