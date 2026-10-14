package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/resource"
)

// OpenIndependentEvaluator creates a fresh separate owner under the original's
// external deletion authority. Its durable scope and owner lease precede raw
// candidate import. It never adopts or rebinds an existing store.
func (s *Session) OpenIndependentEvaluator(ctx context.Context, directory string) (*Session, error) {
	return s.openIndependentEvaluator(ctx, directory, nil)
}

// OpenIndependentEvaluatorPartition binds a freshly created report owner to
// its outer physical quota partition before the owner store starts writing.
func (s *Session) OpenIndependentEvaluatorPartition(ctx context.Context, directory string, allocation resource.Allocation) (*Session, error) {
	return s.openIndependentEvaluator(ctx, directory, &allocation)
}

func (s *Session) openIndependentEvaluator(ctx context.Context, directory string, partition *resource.Allocation) (*Session, error) {
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
	if partition == nil {
		return s.openQuotaBoundIndependentEvaluator(ctx, destination)
	}
	root, err := freshPrivate(destination)
	if err != nil {
		return nil, err
	}
	if err = root.Close(); err != nil {
		return nil, err
	}
	if err = partition.BindPartitionOwner(ctx, destination); err != nil {
		return nil, err
	}
	return openSession(ctx, destination, s.privacy)
}

func (s *Session) openQuotaBoundIndependentEvaluator(ctx context.Context, destination string) (*Session, error) {
	ledger := s.PhysicalQuota
	if ledger == nil || s.privacy == nil {
		return nil, c.Fail(c.StaleAuthority, "parent shared physical quota is required for a standalone evaluator")
	}
	pin, err := s.Journal.PhysicalQuotaBinding(ctx)
	if err != nil {
		return nil, err
	}
	if pin == nil || pin.AuthorityID != s.privacy.ID || pin.Directory != s.privacy.Directory || pin.PhysicalRoot != s.privacy.PhysicalRoot ||
		pin.AuthorityID != ledger.Authority.ID || pin.Directory != ledger.Authority.Directory || pin.PhysicalRoot != ledger.Authority.PhysicalRoot || pin.LedgerID != ledger.LedgerID {
		return nil, c.Fail(c.StaleAuthority, "parent shared physical quota pin is unavailable or changed")
	}
	if _, err = os.Lstat(destination); err == nil {
		return nil, c.Fail(c.PolicyDenied, "separate fresh evaluator owner required")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	upperBytes, upperEntries := resource.PartitionStartupEnvelope()
	binding := struct {
		SchemaVersion         int    `json:"schema_version"`
		ParentInstanceID      string `json:"parent_instance_id"`
		ParentDirectory       string `json:"parent_directory"`
		ParentPhysicalRoot    string `json:"parent_physical_root"`
		AuthorityID           string `json:"authority_id"`
		AuthorityDirectory    string `json:"authority_directory"`
		AuthorityPhysicalRoot string `json:"authority_physical_root"`
		LedgerID              string `json:"ledger_id"`
		Destination           string `json:"destination"`
		UpperBytes            uint64 `json:"upper_bytes"`
		UpperEntries          uint64 `json:"upper_entries"`
	}{1, s.instance.ID, s.directory, s.instance.PhysicalRoot, pin.AuthorityID, pin.Directory, pin.PhysicalRoot, pin.LedgerID, destination, upperBytes, upperEntries}
	requestDigest, err := c.Digest(binding)
	if err != nil {
		return nil, err
	}
	request := resource.Request{
		Writer: "evaluation", Family: "EVALUATOR_OWNER", RequestDigest: requestDigest, TargetRoot: destination,
		UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Payload,
	}
	match, err := ledger.ReserveOrMatch(ctx, request)
	if err != nil {
		return nil, err
	}
	if match.Scope != resource.ExactReservationScope || match.Status != "RESERVED" || match.UpperBytes != upperBytes ||
		match.UpperEntries != upperEntries || match.RequestDigest != requestDigest {
		return nil, c.Fail(c.StaleAuthority, "standalone evaluator requires its exact untouched startup reservation")
	}
	allocation := match.Allocation
	if err = allocation.PreparePartition(ctx, resource.IndependentOwnerPartition); err != nil {
		return nil, err
	}
	if err = allocation.Begin(ctx); err != nil {
		return nil, err
	}
	root, err := freshPrivate(destination)
	if err != nil {
		return nil, err
	}
	bindErr := allocation.BindPartitionRoot(ctx)
	if err = errors.Join(bindErr, root.Close()); err != nil {
		return nil, err
	}
	owner, err := openSession(ctx, destination, s.privacy)
	if err != nil {
		return nil, err
	}
	if err = owner.Close(); err != nil {
		return nil, err
	}
	if err = allocation.CommitPartition(ctx); err != nil {
		return nil, err
	}
	owner, err = OpenExisting(ctx, destination)
	if err != nil {
		return nil, err
	}
	if owner.PhysicalQuota == nil || owner.PhysicalQuota.Authority != ledger.Authority || owner.PhysicalQuota.LedgerID != ledger.LedgerID || !samePrivacyAuthority(owner.privacy, s.privacy) {
		return nil, errors.Join(c.Fail(c.StaleAuthority, "standalone evaluator reopened under a different shared quota binding"), owner.Close())
	}
	return owner, nil
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
	parentState, parentDoc, err := s.Load(ctx, source.TaskID)
	if err != nil {
		return err
	}
	if err = currentOperatorEffect(parentDoc, "export"); err != nil {
		return err
	}
	if parentState.TaskSeq != source.TaskSequence || parentState.DocumentDigest != source.DocumentDigest {
		return c.Fail(c.Conflict, "source changed before evaluation report registration")
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
	scope := privacyScope{InstanceID: evaluator.instance.ID, Directory: ownerPath, PhysicalRoot: evaluator.instance.PhysicalRoot}
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
