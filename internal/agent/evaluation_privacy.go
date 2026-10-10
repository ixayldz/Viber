package agent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// OpenIndependentEvaluator creates a fresh separate owner under the original's
// external deletion authority. Its durable scope and owner lease precede raw
// candidate import. It never adopts or rebinds an existing store.
func (s *Session) OpenIndependentEvaluator(ctx context.Context, directory string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePrivacy(ctx); err != nil {
		return nil, err
	}
	destination, err := fileguard.ResolveProspective(directory)
	if err != nil {
		return nil, err
	}
	if !fileguard.Disjoint(destination, s.directory) || !fileguard.Disjoint(destination, s.privacy.Directory) {
		return nil, c.Fail(c.PolicyDenied, "separate fresh evaluator owner required")
	}
	root, err := freshPrivate(destination)
	if err != nil {
		return nil, err
	}
	if err = root.Close(); err != nil {
		return nil, err
	}
	return openSession(ctx, destination, s.privacy)
}

// The instance namespace prevents one evaluator's deletion from revoking
// another evaluator belonging to the same authority.
func (s *Session) EvaluationTaskID() string { return "eval-" + s.instance.ID }

// RegisterEvaluationReport allocates the outer report before its first write.
// The nested owner is separately inventoried and retains its numeric journal.
func (s *Session) RegisterEvaluationReport(ctx context.Context, evaluator *Session, root *os.Root, source EvaluationSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if evaluator == nil || evaluator == s || s.privacy == nil || evaluator.privacy == nil || *s.privacy != *evaluator.privacy {
		return c.Fail(c.PolicyDenied, "registered separate evaluator required")
	}
	_, doc, err := evaluator.Load(ctx, evaluator.EvaluationTaskID())
	if err != nil {
		return err
	}
	if doc.EvaluationOrigin == nil || doc.EvaluationOrigin.ParentTask != source.TaskID || doc.EvaluationOrigin.ParentDocument != source.DocumentDigest || doc.EvaluationOrigin.ParentSequence != source.TaskSequence {
		return c.Fail(c.StaleAuthority, "report source differs from evaluator lineage")
	}
	directory, err := fileguard.ResolveProspective(root.Name())
	if err != nil {
		return err
	}
	ownerPath, err := filepath.EvalSymlinks(evaluator.directory)
	if err == nil {
		ownerPath, err = filepath.Abs(ownerPath)
	}
	if err != nil {
		return err
	}
	if ownerPath != filepath.Join(directory, "owner") || !fileguard.Disjoint(directory, s.directory) || !fileguard.Disjoint(directory, s.privacy.Directory) {
		return c.Fail(c.PolicyDenied, "report must enclose only its registered evaluator owner")
	}
	physical, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return err
	}
	tasks := []string{source.TaskID, doc.TaskID}
	sort.Strings(tasks)
	scope := privacyScope{evaluator.instance.ID, ownerPath, evaluator.instance.PhysicalRoot}
	return s.registerManagedCopy(ctx, ManagedCopy{Kind: "EVALUATION_REPORT", Directory: directory, PhysicalRoot: physical, Tasks: tasks, Files: []BackupFile{}, ExcludedStore: &scope})
}

func (s *Session) registerEvaluationLineage(ctx context.Context, bound privacyAttempt) error {
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return err
	}
	defer lock.Close()
	view, err := readPrivacy(root)
	if err != nil {
		return err
	}
	for _, task := range []string{bound.ParentTask, bound.ChildTask} {
		if _, deleted := view.Deletions[task]; deleted {
			return c.Fail(c.PolicyDenied, "deleted source cannot allocate an evaluator")
		}
	}
	for _, prior := range view.Attempts {
		if prior.ChildTask == bound.ChildTask {
			if prior != bound {
				return c.Fail(c.CommandIDConflict, "evaluator identity already allocated")
			}
			return nil
		}
	}
	scopes, _, err := readPrivacyScopes(root)
	if err != nil {
		return err
	}
	found := false
	for _, scope := range scopes {
		if scope == bound.Scope {
			found = true
		}
	}
	if !found {
		return c.Fail(c.StaleAuthority, "evaluator owner has no durable physical scope")
	}
	return appendPrivacy(root, view, privacyRecord{Type: "ATTEMPT_LINEAGE", Attempt: &bound})
}

func samePrivacyAuthority(a, b *PrivacyAuthority) bool { return a != nil && b != nil && *a == *b }

func (s *Session) validateEvaluationLineage(ctx context.Context, bound privacyAttempt) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if s.privacy == nil {
		return c.Fail(c.StaleAuthority, "evaluator lost its external deletion authority")
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
	defer lock.Close()
	view, err := readPrivacy(root)
	if err != nil {
		return err
	}
	for _, prior := range view.Attempts {
		if prior == bound {
			return nil
		}
	}
	return c.Fail(c.StoreIntegrityError, "evaluator lost its prepublication lineage")
}
