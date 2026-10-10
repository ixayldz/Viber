package owner

import (
	"context"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
	"github.com/ixayldz/Viber/internal/store"
	"time"
)

func (o *Owner) Run(ctx context.Context, task, id string) (View, error) {
	return o.run(ctx, task, id, false)
}
func (o *Owner) StartDetached(ctx context.Context, task, id string) (View, error) {
	return o.run(ctx, task, id, true)
}

type SupervisorStatus struct {
	Retention      *agent.RetentionRun `json:"retention_maintenance,omitempty"`
	RetentionError string              `json:"retention_maintenance_error,omitempty"`
	SchemaVersion  int                 `json:"schema_version"`
	Owner          ipc.Info            `json:"owner"`
	ActiveTask     string              `json:"active_task,omitempty"`
	InvocationID   string              `json:"invocation_id,omitempty"`
	Closing        bool                `json:"closing"`
}

func (o *Owner) SupervisorStatus() SupervisorStatus {
	o.mu.Lock()
	defer o.mu.Unlock()
	status := SupervisorStatus{SchemaVersion: 1, Owner: o.Server.Info, Closing: o.closed, Retention: o.retentionLast, RetentionError: o.retentionError}
	if o.retentionLast != nil {
		raw, err := c.CanonicalV1(o.retentionLast)
		var owned agent.RetentionRun
		if err == nil {
			err = c.DecodeStrict(raw, &owned)
		}
		if err == nil {
			status.Retention = &owned
		} else {
			status.Retention = nil
			status.RetentionError = "RETENTION_STATUS_INTEGRITY"
		}
	}
	if o.active != nil {
		status.ActiveTask = o.active.task
		status.InvocationID = o.active.id
	}
	return status
}

type AttachmentPage struct {
	SchemaVersion   int               `json:"schema_version"`
	Owner           *ipc.Info         `json:"owner,omitempty"`
	History         store.HistoryPage `json:"history"`
	State           c.TaskState       `json:"current_state"`
	Active          bool              `json:"active"`
	EarliestTaskSeq int64             `json:"earliest_available_task_seq"`
	ResyncRequired  bool              `json:"resync_required"`
	EphemeralReplay string            `json:"ephemeral_stream_replay"`
}

func (o *Owner) AttachPage(ctx context.Context, task string, query Page) (AttachmentPage, error) {
	result := AttachmentPage{SchemaVersion: 1, Owner: &o.Server.Info, EarliestTaskSeq: 1, EphemeralReplay: "NOT_RETAINED"}
	page, err := o.Session.Journal.History(ctx, task, query.After, query.Limit)
	if err != nil {
		return result, err
	}
	for i, record := range page.Records {
		if record.Event.TaskSeq != query.After+int64(i)+1 {
			return result, c.Fail(c.StoreIntegrityError, "retained attachment sequence is not contiguous")
		}
	}
	result.History = page
	result.State, err = o.Session.State(ctx, task)
	if err != nil {
		return result, err
	}
	o.mu.Lock()
	result.Active = o.active != nil && o.active.task == task
	o.mu.Unlock()
	return result, nil
}
func (o *Owner) StopSupervisor(ctx context.Context) (SupervisorStatus, error) {
	o.control.Lock()
	defer o.control.Unlock()
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return SupervisorStatus{}, c.Fail(c.StoreOwned, "owner is closing")
	}
	if o.active != nil {
		o.mu.Unlock()
		return SupervisorStatus{}, c.Fail(c.Conflict, "pause or cancel the active task before stopping its owner")
	}
	status := SupervisorStatus{SchemaVersion: 1, Owner: o.Server.Info, Closing: true}
	o.closed = true
	o.mu.Unlock()
	// Shutdown is quiescent and never kills a task. The acknowledgment may be
	// lost with the transport; a caller reconciles disappearance of this owner ID.
	go func() {
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			o.cancel()
		case <-o.ctx.Done():
		}
	}()
	return status, nil
}
