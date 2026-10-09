package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
)

// The allocation precedes the first inherited input write, so a process crash
// before TaskCreated cannot leave an unregistered copy of the parent content.
type privacyAttempt struct {
	Scope          privacyScope `json:"scope"`
	ParentTask     string       `json:"parent_task"`
	ParentSequence int64        `json:"parent_task_sequence"`
	ParentDocument string       `json:"parent_document"`
	ChildTask      string       `json:"child_task"`
	RequestDigest  string       `json:"request_digest"`
}

func (a privacyAttempt) validate() error {
	if !validPrivacyScope(a.Scope) || a.ParentTask == a.ChildTask || a.ParentSequence < 1 || !c.ValidDigest(a.ParentDocument) || !c.ValidDigest(a.RequestDigest) {
		return c.Fail(c.StoreIntegrityError, "invalid attempt allocation binding")
	}
	for _, task := range []string{a.ParentTask, a.ChildTask} {
		if _, err := artifact.ScopedBlobPath(task, c.HashBytes(nil)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) registerAttemptLineage(ctx context.Context, options AttemptOptions, parent c.TaskState, digest string) error {
	if err := s.ensurePrivacy(ctx); err != nil {
		return err
	}
	directory, err := filepath.EvalSymlinks(s.directory)
	if err == nil {
		directory, err = filepath.Abs(directory)
	}
	if err != nil {
		return err
	}
	bound := privacyAttempt{Scope: privacyScope{s.instance.ID, directory, s.instance.PhysicalRoot}, ParentTask: options.ParentTask, ParentSequence: parent.TaskSeq, ParentDocument: parent.DocumentDigest, ChildTask: options.NewTask, RequestDigest: digest}
	if err = bound.validate(); err != nil {
		return err
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
	for _, task := range []string{bound.ParentTask, bound.ChildTask} {
		if _, deleted := view.Deletions[task]; deleted {
			return c.Fail(c.PolicyDenied, "deleted content cannot allocate an attempt")
		}
	}
	for _, prior := range view.Attempts {
		if prior.Scope.PhysicalRoot == bound.Scope.PhysicalRoot && prior.ChildTask == bound.ChildTask {
			if prior.ParentTask != bound.ParentTask || prior.ParentSequence != bound.ParentSequence || prior.ParentDocument != bound.ParentDocument || prior.RequestDigest != bound.RequestDigest {
				return c.Fail(c.CommandIDConflict, "staged attempt identity belongs to another request")
			}
			return nil
		}
	}
	return appendPrivacy(root, view, privacyRecord{Type: "ATTEMPT_LINEAGE", Attempt: &bound})
}

func (s *Session) validateAttemptAllocation(ctx context.Context, task string, origin *AttemptOrigin) error {
	if s.privacy == nil {
		return nil
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
		if prior.Scope.PhysicalRoot != s.instance.PhysicalRoot || prior.ChildTask != task {
			continue
		}
		if origin == nil || prior.ParentTask != origin.ParentTask || prior.ParentSequence != origin.ParentSequence || prior.ParentDocument != origin.ParentDocument || prior.RequestDigest != origin.RequestDigest {
			return c.Fail(c.CommandIDConflict, "task ID is reserved by a durable attempt allocation")
		}
		return nil
	}
	if origin != nil {
		return c.Fail(c.PolicyDenied, "derived task requires durable allocation before content publication")
	}
	return nil
}

type DeletionAttempt struct {
	Allocation privacyAttempt    `json:"allocation"`
	Objects    []artifact.Object `json:"objects"`
}

func attemptKey(a privacyAttempt) string { return a.Scope.Directory + "\x00" + a.ChildTask }

func (s *Session) stagedAttemptPlans(ctx context.Context, task string, view privacyView) ([]DeletionAttempt, error) {
	result := []DeletionAttempt{}
	for _, allocation := range view.Attempts {
		if allocation.ParentTask != task {
			continue
		}
		if _, deleted := view.Deletions[allocation.ChildTask]; deleted {
			continue
		}
		var objects []artifact.Object
		if allocation.Scope.PhysicalRoot == s.instance.PhysicalRoot {
			states, err := s.Journal.Replay(ctx, "")
			if err != nil {
				return nil, err
			}
			if child, found := states[allocation.ChildTask]; found {
				if child.Deletion != nil {
					continue
				}
				return nil, c.Fail(c.PolicyDenied, "published derived task pins its parent; delete descendants first")
			}
			objects, err = s.Archive.TaskInventory(ctx, allocation.ChildTask)
			if err != nil {
				return nil, err
			}
		} else {
			journal, archive, err := openDeletionStore(ctx, allocation.Scope, *s.privacy)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			states, replayErr := journal.Replay(ctx, "")
			if replayErr == nil {
				if child, found := states[allocation.ChildTask]; found && child.Deletion == nil {
					replayErr = c.Fail(c.PolicyDenied, "published derived task in another owner pins its parent")
				} else if !found {
					objects, replayErr = archive.TaskInventory(ctx, allocation.ChildTask)
				}
			}
			err = errors.Join(replayErr, archive.Close(), journal.Close())
			if err != nil {
				return nil, err
			}
			if objects == nil {
				continue
			}
		}
		result = append(result, DeletionAttempt{Allocation: allocation, Objects: objects})
	}
	sort.Slice(result, func(i, j int) bool { return attemptKey(result[i].Allocation) < attemptKey(result[j].Allocation) })
	return result, nil
}

func (s *Session) purgeStagedAttempt(ctx context.Context, bound DeletionAttempt) (resultErr error) {
	journal, archive := s.Journal, s.Archive
	if bound.Allocation.Scope.PhysicalRoot != s.instance.PhysicalRoot {
		var err error
		journal, archive, err = openDeletionStore(ctx, bound.Allocation.Scope, *s.privacy)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, archive.Close(), journal.Close()) }()
	}
	states, err := journal.Replay(ctx, "")
	if err != nil {
		return err
	}
	if _, exists := states[bound.Allocation.ChildTask]; exists {
		return c.Fail(c.Conflict, "staged attempt was published after deletion authorization")
	}
	for _, object := range bound.Objects {
		if err = ctx.Err(); err != nil {
			return err
		}
		if _, err = archive.RemoveTaskContent(bound.Allocation.ChildTask, object); err != nil {
			return err
		}
	}
	remaining, err := archive.TaskInventory(ctx, bound.Allocation.ChildTask)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return c.Fail(c.Conflict, "new staged attempt content prevents PURGED receipt")
	}
	return nil
}
