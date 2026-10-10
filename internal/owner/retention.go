package owner

import (
	"context"
	"errors"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"time"
)

func (o *Owner) RetentionMaintenance(ctx context.Context) (agent.RetentionRun, error) {
	o.control.Lock()
	defer o.control.Unlock()
	o.mu.Lock()
	unavailable := o.closed || o.active != nil
	o.mu.Unlock()
	if unavailable {
		return agent.RetentionRun{}, c.Fail(c.Conflict, "retention maintenance waits for a quiescent owner")
	}
	result, err := o.Session.RunRetention(ctx)
	if err == nil || len(result.Items) > 0 {
		// Own the result before publishing status to concurrent callers.
		raw, _ := c.CanonicalV1(result)
		var owned agent.RetentionRun
		if decodeErr := c.DecodeStrict(raw, &owned); decodeErr != nil {
			return result, decodeErr
		}
		o.mu.Lock()
		o.retentionLast = &owned
		o.retentionError = ""
		o.mu.Unlock()
	}
	if err != nil {
		code := "RETENTION_ATTEMPT_FAILED"
		var typed *c.Error
		if errors.As(err, &typed) {
			code = string(typed.Code)
		}
		o.mu.Lock()
		o.retentionError = code
		o.mu.Unlock()
	}
	return result, err
}
func (o *Owner) retentionLoop(interval time.Duration) {
	defer close(o.retentionDone)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if o.ctx.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(o.ctx, 30*time.Second)
		_, _ = o.RetentionMaintenance(ctx)
		cancel()
		select {
		case <-o.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
