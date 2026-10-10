package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/store"
)

type privacyScope struct {
	InstanceID   string `json:"instance_id"`
	Directory    string `json:"directory"`
	PhysicalRoot string `json:"physical_root"`
}
type DeletionStore struct {
	Scope          privacyScope       `json:"scope"`
	TaskSequence   int64              `json:"task_sequence"`
	DocumentDigest string             `json:"document_digest"`
	Store          store.SnapshotInfo `json:"store"`
	Objects        []artifact.Object  `json:"objects"`
}

func validPrivacyScope(scope privacyScope) bool {
	if len(scope.InstanceID) != 32 || !filepath.IsAbs(scope.Directory) || len(scope.Directory) > 2048 || !c.ValidDigest(scope.PhysicalRoot) {
		return false
	}
	for _, r := range scope.InstanceID {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
func scopeName(scope privacyScope) string { digest, _ := c.Digest(scope); return digest + ".json" }
func publishPrivacyScope(root *os.Root, scope privacyScope) error {
	if !validPrivacyScope(scope) {
		return c.Fail(c.StoreIntegrityError, "invalid managed owner scope")
	}
	raw, err := c.CanonicalV1(scope)
	if err != nil {
		return err
	}
	path := filepath.Join("owners", scopeName(scope))
	existing, err := fileguard.ReadRegular(root, path, 4096)
	if err == nil {
		if string(existing) != string(raw) {
			return c.Fail(c.StoreIntegrityError, "managed owner scope changed")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err = admitPrivacyCatalog(root, path); err != nil {
		return err
	}
	if err = admitPrivacyDisk(root, int64(len(raw))*2, false); err != nil {
		return err
	}
	return fileguard.Publish(root, path, raw)
}
func readPrivacyScopes(root *os.Root) ([]privacyScope, string, error) {
	result := []privacyScope{}
	dir, err := root.Open("owners")
	if err != nil {
		return result, "", err
	}
	names, err := dir.Readdirnames(8193)
	if err == io.EOF {
		err = nil
	}
	err = errors.Join(err, dir.Close())
	if err != nil {
		return result, "", err
	}
	if len(names) > 8192 {
		return result, "", c.Fail(c.BudgetLimitReached, "managed owner inventory full")
	}
	sort.Strings(names)
	for _, name := range names {
		if validPrivacyLeaseFilename(name) {
			continue
		}
		if !strings.HasSuffix(name, ".json") || !c.ValidDigest(strings.TrimSuffix(name, ".json")) {
			return result, "", c.Fail(c.StoreIntegrityError, "foreign owner scope file")
		}
		raw, err := fileguard.ReadRegular(root, filepath.Join("owners", name), 4096)
		if err != nil {
			return result, "", err
		}
		var scope privacyScope
		if c.DecodeStrict(raw, &scope) != nil || !validPrivacyScope(scope) || scopeName(scope) != name {
			return result, "", c.Fail(c.StoreIntegrityError, "managed owner scope binding invalid")
		}
		result = append(result, scope)
	}
	digest, err := c.Digest(result)
	return result, digest, err
}
func openDeletionStore(ctx context.Context, scope privacyScope, authority PrivacyAuthority) (*store.Store, *artifact.Archive, error) {
	root, err := os.OpenRoot(scope.Directory)
	if err != nil {
		return nil, nil, err
	}
	physical, err := fileguard.DirectoryIdentity(root)
	root.Close()
	if err != nil {
		return nil, nil, err
	}
	if physical != scope.PhysicalRoot {
		return nil, nil, c.Fail(c.StaleBase, "managed restored store root was replaced")
	}
	journal, err := store.Open(ctx, scope.Directory)
	if err != nil {
		return nil, nil, err
	}
	raw, err := journal.PrivacyAuthority(ctx)
	expected, _ := c.CanonicalV1(authority)
	if err == nil && string(raw) != string(expected) {
		err = c.Fail(c.StaleAuthority, "managed restored store lost its privacy authority pin")
	}
	if err != nil {
		journal.Close()
		return nil, nil, err
	}
	archive, err := artifact.Open(filepath.Join(scope.Directory, "artifacts"))
	if err != nil {
		journal.Close()
		return nil, nil, err
	}
	return journal, archive, nil
}
func deletionStorePlan(ctx context.Context, scope privacyScope, authority PrivacyAuthority, task string, deleted map[string]DeletionCommand) (DeletionStore, bool, error) {
	result := DeletionStore{Scope: scope, Objects: []artifact.Object{}}
	journal, archive, err := openDeletionStore(ctx, scope, authority)
	if errors.Is(err, os.ErrNotExist) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	defer journal.Close()
	defer archive.Close()
	states, err := journal.Replay(ctx, "")
	if err != nil {
		return result, false, err
	}
	state, exists := states[task]
	if !exists {
		return result, false, nil
	}
	if state.Deletion != nil {
		return result, false, c.Fail(c.StoreIntegrityError, "restored scope has an unregistered task tombstone")
	}
	for _, other := range states {
		if other.Deletion != nil {
			continue
		}
		if _, blocked := deleted[other.TaskID]; blocked {
			continue
		}
		raw, err := archive.GetBytes(other.TaskID, other.DocumentDigest)
		if err != nil {
			return result, false, err
		}
		var doc Document
		if c.DecodeStrict(raw, &doc) != nil || doc.TaskID != other.TaskID || doc.Spec.TaskID != other.TaskID || doc.Spec.Version != other.SpecVersion || doc.Candidate.SnapshotDigest != other.CandidateDigest {
			return result, false, c.Fail(c.StoreIntegrityError, "restored scope task document mismatch")
		}
		if other.TaskID != task {
			if doc.AttemptOrigin != nil && doc.AttemptOrigin.ParentTask == task {
				return result, false, c.Fail(c.PolicyDenied, "retained descendant in a restored scope pins its parent")
			}
			continue
		}
		if state.Execution != c.Terminated || doc.Pending != nil || doc.UnknownEffect || doc.Budget.ReservedInput != 0 || doc.Budget.ReservedOutput != 0 {
			return result, false, c.Fail(c.UnknownOutcome, "restored task is active or has unreconciled effects")
		}
		if state.Tokens != nil {
			for _, r := range state.Tokens.Reservations {
				if r.Status != "SETTLED" {
					return result, false, c.Fail(c.UnknownOutcome, "restored model risk pins content")
				}
			}
		}
		if state.Resources != nil {
			for _, r := range state.Resources.Reservations {
				if r.Status != "SETTLED" {
					return result, false, c.Fail(c.UnknownOutcome, "restored native risk pins content")
				}
			}
		}
	}
	result.TaskSequence, result.DocumentDigest = state.TaskSeq, state.DocumentDigest
	result.Store, err = journal.SnapshotInfo(ctx)
	if err != nil {
		return result, false, err
	}
	result.Objects, err = archive.TaskInventory(ctx, task)
	return result, true, err
}
func purgeDeletionStore(ctx context.Context, bound DeletionStore, authority PrivacyAuthority, command DeletionCommand) (resultErr error) {
	journal, archive, err := openDeletionStore(ctx, bound.Scope, authority)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, archive.Close(), journal.Close()) }()
	states, err := journal.Replay(ctx, command.Plan.TaskID)
	if err != nil {
		return err
	}
	state := states[command.Plan.TaskID]
	plan := command.Plan
	tombstone := c.TaskDeletion{SchemaVersion: 1, PlanDigest: plan.Digest, OriginalDocument: bound.DocumentDigest, ObjectsDigest: plan.ObjectsDigest, Scope: plan.Scope, Status: "PENDING", Watermark: plan.Watermark}
	actor := bindRetentionTombstone(command, &tombstone)
	id := "privacy-scope-" + c.HashBytes([]byte(command.CommandID+":"+bound.Scope.PhysicalRoot))
	if state.Deletion == nil {
		// A retry before this scope's first event still requires its original cursor.
		info, err := journal.SnapshotInfo(ctx)
		if err != nil {
			return err
		}
		if info != bound.Store {
			return c.Fail(c.StaleBase, "restored scope journal changed after deletion intent")
		}
		state, err = journal.Execute(ctx, store.Command{ID: id, TaskID: plan.TaskID, Actor: actor, ExpectedTaskSeq: bound.TaskSequence, Type: "TaskContentDeleted", Payload: c.EventPayload{Deletion: &tombstone, Reason: plan.Digest}})
		if err != nil {
			return err
		}
	}
	expected := tombstone
	if state.Deletion != nil && state.Deletion.Status == "PURGED" {
		expected.Status = "PURGED"
	}
	if state.Deletion == nil || *state.Deletion != expected {
		return c.Fail(c.StoreIntegrityError, "restored scope deletion intent mismatch")
	}
	for _, object := range bound.Objects {
		if err = ctx.Err(); err != nil {
			return err
		}
		if _, err = archive.RemoveTaskContent(plan.TaskID, object); err != nil {
			return err
		}
	}
	remaining, err := archive.TaskInventory(ctx, plan.TaskID)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return c.Fail(c.Conflict, "unplanned restored scope content prevents purge")
	}
	if state.Deletion.Status != "PURGED" {
		tombstone.Status = "PURGED"
		_, err = journal.Execute(ctx, store.Command{ID: id + "-purged", TaskID: plan.TaskID, Actor: "kernel", ExpectedTaskSeq: state.TaskSeq, Type: "TaskDeletionPurged", Payload: c.EventPayload{Deletion: &tombstone, Reason: plan.Digest}})
	}
	return err
}
