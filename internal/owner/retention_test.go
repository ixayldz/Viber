package owner

import (
	"context"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
	"testing"
	"time"
)

func TestOwnerActualRetentionTimerPurgesAfterDeadlineAndStatusHasNoMutableAlias(t *testing.T) {
	ctx := context.Background()
	old, directory, _ := ownerFixture(t, []agent.Turn{{Text: "unused", UsageKnown: true}}, true)
	if _, err := old.Session.Controls(ctx, "task", "cancel"); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	host, err := openWithRetentionInterval(ctx, directory, old.Session, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	options := agent.RetentionOptions{CommandID: "timer-consent", Mode: "EXPIRE", Deadline: time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339Nano), Acknowledgement: c.RetentionAcknowledgement}
	raw, _ := c.CanonicalV1(options)
	if _, err = host.Handle(ctx, ipc.Request{ID: options.CommandID, TaskID: "task", Command: "retention-set", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	bound := time.Now().Add(10 * time.Second)
	for time.Now().Before(bound) {
		status, err := old.Session.RetentionStatus(ctx, "task")
		if err != nil {
			t.Fatal(err)
		}
		if status.Status == "PURGED" {
			view := host.SupervisorStatus()
			if view.Retention == nil {
				t.Fatal("timer result not visible")
			}
			view.Retention.Limit = 999
			if host.SupervisorStatus().Retention.Limit != 16 {
				t.Fatal("status mutated scheduler")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("actual owner timer did not expire task", host.SupervisorStatus())
}
