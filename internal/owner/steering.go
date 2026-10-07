package owner

import (
	"context"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func (o *Owner) steer(ctx context.Context, input agent.SteeringInput) (View, error) {
	o.control.Lock()
	defer o.control.Unlock()
	if _, found, err := o.Session.SteeringReceipt(ctx, input); found || err != nil {
		if err != nil {
			return View{}, err
		}
		recorded, complete, err := o.Session.ControlReceipt(ctx, input.TaskID, input.CommandID+"-pause", "pause")
		if err != nil {
			return View{}, err
		}
		if !complete {
			return View{}, c.Fail(c.UnknownOutcome, "raw input is durable but its pause receipt is missing; inspect task before another command")
		}
		return o.view(ctx, input.TaskID, recorded.TaskSeq)
	}
	o.mu.Lock()
	if o.closed || o.active != nil && o.active.task != input.TaskID {
		o.mu.Unlock()
		return View{}, c.Fail(c.Conflict, "owner foreground slot unavailable")
	}
	active := o.active
	state, err := o.Session.RecordSteering(ctx, input)
	if err == nil && active != nil {
		active.pause.Store(true)
		active.cancel()
	}
	o.mu.Unlock()
	if err != nil {
		return View{}, err
	}
	if active != nil {
		select {
		case <-active.done:
		case <-ctx.Done():
			return View{}, ctx.Err()
		}
	}
	// The raw input is already durable. Quiescent pause cannot resolve or approve it.
	state, err = o.Session.ControlsWithID(ctx, input.TaskID, "pause", input.CommandID+"-pause")
	if err != nil {
		return View{}, err
	}
	return o.view(ctx, input.TaskID, state.TaskSeq)
}
