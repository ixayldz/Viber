package store

import (
	"context"
	"reflect"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestHistoricalReplayPagingAndFullTailIntegrity(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	first, err := s.Execute(ctx, cmd("create", "t", "TaskCreated", 0, c.EventPayload{SpecVersion: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(ctx, cmd("other", "u", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})); err != nil {
		t.Fatal(err)
	}
	second, err := s.Execute(ctx, cmd("scope", "t", "StateTransitioned", 1, c.EventPayload{State: c.Scoping}))
	if err != nil {
		t.Fatal(err)
	}
	for seq, want := range map[int64]c.TaskState{1: first, 2: second, 0: second} {
		got, err := s.ReplayAt(ctx, "t", seq)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("historical state mismatch", seq, err)
		}
	}
	for _, seq := range []int64{-1, 3} {
		if _, err = s.ReplayAt(ctx, "t", seq); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
	page, err := s.History(ctx, "t", 0, 1)
	if err != nil || page.Head != 2 || page.Next != 1 || !page.HasMore || len(page.Records) != 1 {
		t.Fatal(page, err)
	}
	page, err = s.History(ctx, "t", page.Next, 1)
	if err != nil || page.Next != 2 || page.HasMore || page.Records[0].Event.StoreSeq != 3 {
		t.Fatal("task/global cursors confused", page, err)
	}
	page, err = s.History(ctx, "t", 2, 1)
	if err != nil || len(page.Records) != 0 || page.HasMore {
		t.Fatal("end page", page, err)
	}
	if _, err = s.History(ctx, "t", 3, 1); err == nil {
		t.Fatal("future cursor accepted")
	}
	if _, err = s.db.Exec("UPDATE events SET event_hash='corrupt' WHERE store_seq=3"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReplayAt(ctx, "t", 1); err == nil {
		t.Fatal("historical inspection hid corrupt retained tail")
	}
}
func TestCommandDedupRejectsForgedHistoricalResult(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	command := cmd("create", "t", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})
	state, err := s.Execute(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	state.Quality = c.Verified
	raw, _ := c.CanonicalV1(state)
	if _, err = s.db.Exec("UPDATE commands SET result=? WHERE command_id='create'", raw); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(ctx, command); err == nil {
		t.Fatal("forged dedup receipt returned")
	}
	if _, _, _, _, err = s.CommandReceipt(ctx, "create"); err == nil {
		t.Fatal("forged application receipt returned")
	}
}
