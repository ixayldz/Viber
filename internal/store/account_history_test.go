package store

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"testing"
)

func TestAccountHistoryPagesExactReplayAndDetectsUnseenCorruption(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st := resourceTask(t, s, "a", testResourcePolicy())
	reserved, err := s.Execute(context.Background(), resourceReserve(st))
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.AccountHistory(context.Background(), "a", 0, 1)
	if err != nil || len(page.Records) != 1 || !page.HasMore || page.Records[0].State.TaskSeq != 1 {
		t.Fatal(page, err)
	}
	first := page.Records[0].State
	next, err := s.AccountHistory(context.Background(), "a", page.Next, 1)
	if err != nil || len(next.Records) != 1 || next.HasMore || next.Head != reserved.TaskSeq || next.Next != reserved.TaskSeq || next.Records[0].State.Resources.Reserved != reserved.Resources.Reserved {
		t.Fatal(next, err)
	}
	if first.Resources.Reserved != (c.ResourceVector{}) || len(first.Resources.Reservations) != 0 {
		t.Fatal("history account alias")
	}
	expected, err := s.ReplayAt(context.Background(), "a", next.Records[0].State.TaskSeq)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := c.Digest(expected)
	b, _ := c.Digest(next.Records[0].State)
	if a != b {
		t.Fatal("batched state differs from exact replay")
	}
	// Corruption after the requested first page must fail the entire operation.
	if _, err = s.db.Exec("UPDATE payloads SET body=? WHERE digest=(SELECT payload_digest FROM events ORDER BY store_seq DESC LIMIT 1)", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AccountHistory(context.Background(), "a", 0, 1); err == nil {
		t.Fatal("unseen tail corruption skipped")
	}
}
func TestAccountHistoryRetainsRawInputBoundaries(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st := resourceTask(t, s, "a", testResourcePolicy())
	st, err = s.Execute(context.Background(), Command{ID: "input", TaskID: "a", Actor: "user", ExpectedTaskSeq: st.TaskSeq, Type: "InputRecorded", Payload: c.EventPayload{InputDigest: c.HashBytes([]byte("raw")), InputBytes: 3, InputID: "input", Reason: c.HashBytes([]byte("input"))}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.AccountHistory(context.Background(), "a", 0, 64)
	if err != nil || len(page.Records) != 2 || page.Records[1].Event.Type != "InputRecorded" || !page.Records[1].State.InputBarrier || page.Records[1].State.PendingInputIDs[0] != "input" {
		t.Fatal(page, err)
	}
}
