// Package owner connects the peer-authenticated command channel to one active
// offline kernel. It does not hold a second store writer or expose host effects.
package owner

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ixayldz/Viber/internal/agent"
	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/delivery"
	"github.com/ixayldz/Viber/internal/ipc"
)

type View struct {
	Protection   *agent.ProtectionInfo `json:"check_protection,omitempty"`
	Context      *agent.ContextAudit   `json:"context,omitempty"`
	State        c.TaskState           `json:"state"`
	Budget       agent.Budget          `json:"budget"`
	Blocker      string                `json:"blocker"`
	Summary      string                `json:"untrusted_model_summary"`
	FinalReady   bool                  `json:"final_ready"`
	Candidate    artifact.Ref          `json:"candidate"`
	ReleaseReady bool                  `json:"release_ready"`
}
type Cursor struct {
	Sequence int64 `json:"task_seq"`
}
type Page struct {
	After int64 `json:"after"`
	Limit int   `json:"limit"`
}
type DiffChange struct {
	Path    string `json:"path"`
	Before  string `json:"before_digest"`
	After   string `json:"after_digest"`
	Deleted bool   `json:"deleted"`
}
type activeRun struct {
	task   string
	pause  atomic.Bool
	cancel context.CancelFunc
	done   chan struct{}
	state  c.TaskState
	err    error
}
type Owner struct {
	Session   *agent.Session
	Server    *ipc.Server
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	control   sync.Mutex
	active    *activeRun
	serveDone chan error
	closeOnce sync.Once
	closeErr  error
	closed    bool
}

func Open(ctx context.Context, directory string, session *agent.Session) (*Owner, error) {
	if session == nil {
		return nil, c.Fail(c.InvalidArgument, "owned kernel session required")
	}
	ownerCtx, cancel := context.WithCancel(ctx)
	owner := &Owner{Session: session, ctx: ownerCtx, cancel: cancel, serveDone: make(chan error, 1)}
	server, err := ipc.OpenServer(ownerCtx, directory, session.Journal.Generation(), owner.Handle)
	if err != nil {
		cancel()
		return nil, err
	}
	owner.Server = server
	go func() {
		err := server.Serve()
		owner.mu.Lock()
		if owner.active != nil {
			owner.active.pause.Store(true)
		}
		owner.mu.Unlock()
		cancel()
		owner.serveDone <- err
	}()
	return owner, nil
}
func (o *Owner) Done() <-chan struct{} { return o.ctx.Done() }
func (o *Owner) Close() error          { o.closeOnce.Do(func() { o.closeErr = o.close() }); return o.closeErr }
func (o *Owner) close() error {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	o.mu.Lock()
	if o.active != nil {
		o.active.pause.Store(true)
	}
	o.mu.Unlock()
	o.cancel()
	err := o.Server.Close()
	o.mu.Lock()
	active := o.active
	o.mu.Unlock()
	if active != nil {
		active.cancel()
		<-active.done
	}
	serveErr := <-o.serveDone
	return errors.Join(err, serveErr)
}
func (o *Owner) view(ctx context.Context, task string, sequence int64) (View, error) {
	state, doc, err := o.Session.Inspect(ctx, task, sequence)
	return View{Protection: agent.CheckProtection(doc), Context: doc.Context, State: state, Budget: doc.Budget, Blocker: doc.Blocker, Summary: doc.FinalSummary, FinalReady: doc.FinalReady, Candidate: doc.Candidate}, err
}
func (o *Owner) Run(ctx context.Context, task, id string) (View, error) {
	o.control.Lock()
	admitted := false
	defer func() {
		if !admitted {
			o.control.Unlock()
		}
	}()
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return View{}, c.Fail(c.StoreOwned, "owner is shutting down")
	}
	if recorded, found, err := o.Session.ControlReceipt(ctx, task, id, "resume"); found || err != nil {
		o.mu.Unlock()
		if err != nil {
			return View{}, err
		}
		return o.view(ctx, task, recorded.TaskSeq)
	}
	if o.active != nil {
		o.mu.Unlock()
		return View{}, c.Fail(c.Conflict, "owner already has an active task")
	}
	state, completed, err := o.Session.InvocationStart(ctx, task, id)
	if err != nil {
		o.mu.Unlock()
		return View{}, err
	}
	if completed {
		o.mu.Unlock()
		_, doc, err := o.Session.Inspect(ctx, task, state.TaskSeq)
		return View{Protection: agent.CheckProtection(doc), Context: doc.Context, State: state, Budget: doc.Budget, Blocker: doc.Blocker, Summary: doc.FinalSummary, FinalReady: doc.FinalReady, Candidate: doc.Candidate}, err
	}
	runCtx, cancel := context.WithCancel(o.ctx)
	active := &activeRun{task: task, cancel: cancel, done: make(chan struct{})}
	o.active = active
	o.mu.Unlock()
	o.control.Unlock()
	admitted = true
	stop := context.AfterFunc(ctx, func() { active.pause.Store(true); cancel() })
	defer stop()
	go func() {
		active.state, active.err = o.Session.RunControlled(runCtx, task, &active.pause)
		receiptCtx, receiptCancel := context.WithTimeout(context.Background(), 5*time.Second)
		recorded, receiptErr := o.Session.InvocationFinishWithError(receiptCtx, task, id, active.err)
		if receiptErr == nil {
			active.state = recorded
			_, _, active.err = o.Session.ControlReceipt(receiptCtx, task, id, "resume")
		} else {
			active.err = errors.Join(active.err, receiptErr)
		}
		receiptCancel()
		cancel()
		o.mu.Lock()
		o.active = nil
		close(active.done)
		o.mu.Unlock()
	}()
	<-active.done
	if active.err != nil {
		return View{}, active.err
	}
	return o.view(context.Background(), task, active.state.TaskSeq)
}
func (o *Owner) taskControl(ctx context.Context, task, action, id string) (View, error) {
	o.control.Lock()
	defer o.control.Unlock()
	if recorded, found, err := o.Session.ControlReceipt(ctx, task, id, action); found || err != nil {
		if err != nil {
			return View{}, err
		}
		return o.view(ctx, task, recorded.TaskSeq)
	}
	o.mu.Lock()
	active := o.active
	if active != nil && active.task != task {
		o.mu.Unlock()
		return View{}, c.Fail(c.Conflict, "another task owns the foreground slot")
	}
	if active != nil {
		active.pause.Store(action == "pause")
		active.cancel()
	}
	o.mu.Unlock()
	if active != nil {
		select {
		case <-active.done:
		case <-ctx.Done():
			return View{}, ctx.Err()
		}
		if active.err != nil {
			return View{}, active.err
		}
	}
	state, err := o.Session.ControlsWithID(ctx, task, action, id)
	if err != nil {
		return View{}, err
	}
	return o.view(ctx, task, state.TaskSeq)
}
func nullPayload(raw json.RawMessage) error {
	if string(raw) != "null" {
		return c.Fail(c.InvalidArgument, "command takes no payload")
	}
	return nil
}
func (o *Owner) Handle(ctx context.Context, request ipc.Request) (any, error) {
	if request.TaskID == "" {
		return nil, c.Fail(c.InvalidArgument, "task ID required")
	}
	switch request.Command {
	case "status":
		if err := nullPayload(request.Payload); err != nil {
			return nil, err
		}
		return o.view(ctx, request.TaskID, 0)
	case "resume":
		if err := nullPayload(request.Payload); err != nil {
			return nil, err
		}
		return o.Run(ctx, request.TaskID, request.ID)
	case "pause", "cancel":
		if err := nullPayload(request.Payload); err != nil {
			return nil, err
		}
		return o.taskControl(ctx, request.TaskID, request.Command, request.ID)
	case "inspect", "replay":
		var cursor Cursor
		if err := c.DecodeStrict(request.Payload, &cursor); err != nil {
			return nil, err
		}
		return o.view(ctx, request.TaskID, cursor.Sequence)
	case "events":
		var page Page
		if err := c.DecodeStrict(request.Payload, &page); err != nil {
			return nil, err
		}
		return o.Session.Journal.History(ctx, request.TaskID, page.After, page.Limit)
	case "diff":
		if err := nullPayload(request.Payload); err != nil {
			return nil, err
		}
		_, doc, err := o.Session.Load(ctx, request.TaskID)
		if err != nil {
			return nil, err
		}
		before, err := o.Session.Archive.Get(doc.Baseline)
		if err != nil {
			return nil, err
		}
		after, err := o.Session.Archive.Get(doc.Candidate)
		if err != nil {
			return nil, err
		}
		changeset, _, err := delivery.Build(before, after)
		if err != nil {
			return nil, err
		}
		result := []DiffChange{}
		for _, change := range changeset.Changes {
			result = append(result, DiffChange{Path: change.Path, Before: change.BeforeDigest, After: change.AfterDigest, Deleted: change.Kind == "DELETED"})
		}
		return result, nil
	case "steer":
		var input agent.SteeringInput
		if err := c.DecodeStrict(request.Payload, &input); err != nil {
			return nil, err
		}
		if input.CommandID != request.ID || input.TaskID != request.TaskID {
			return nil, c.Fail(c.InvalidArgument, "steering envelope mismatch")
		}
		return o.steer(ctx, input)
	case "requests", "respond", "revise":
		o.mu.Lock()
		defer o.mu.Unlock()
		busy := o.active != nil
		if busy {
			return nil, c.Fail(c.Conflict, "request response requires a quiescent task boundary")
		}
		if request.Command == "requests" {
			if err := nullPayload(request.Payload); err != nil {
				return nil, err
			}
			return o.Session.Requests(ctx, request.TaskID)
		}
		if request.Command == "revise" {
			var revision agent.ScopeRevision
			if err := c.DecodeStrict(request.Payload, &revision); err != nil {
				return nil, err
			}
			if revision.CommandID != request.ID || revision.TaskID != request.TaskID {
				return nil, c.Fail(c.InvalidArgument, "revision envelope mismatch")
			}
			return o.Session.Revise(ctx, revision)
		}
		var response agent.UserResponse
		if err := c.DecodeStrict(request.Payload, &response); err != nil {
			return nil, err
		}
		if response.CommandID != request.ID || response.TaskID != request.TaskID {
			return nil, c.Fail(c.InvalidArgument, "response command envelope mismatch")
		}
		return o.Session.Respond(ctx, response)
	default:
		return nil, c.Fail(c.UnsupportedCapability, "command is not in the owner RPC registry")
	}
}
