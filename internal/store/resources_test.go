package store

import (
	"context"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"path/filepath"
	"sync"
	"testing"
)

func resourceTask(t *testing.T, s *Store, id string, policy c.ResourcePolicy) c.TaskState {
	t.Helper()
	st, err := s.Execute(context.Background(), Command{ID: id + "-create", TaskID: id, Actor: "kernel", Type: "TaskCreated", Payload: c.EventPayload{SpecVersion: 1, SnapshotDigest: c.HashBytes([]byte("snapshot")), DocumentDigest: c.HashBytes([]byte("document")), Tokens: &c.TokenMutation{Action: "INIT", Limits: c.DefaultTokenLimits()}, Resources: &c.ResourceMutation{Action: "INIT", Policy: &policy}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []c.ExecutionState{c.Scoping, c.Ready, c.Running} {
		st, err = s.Execute(context.Background(), Command{ID: id + "-" + string(phase), TaskID: id, Actor: "kernel", ExpectedTaskSeq: st.TaskSeq, Type: "StateTransitioned", Payload: c.EventPayload{State: phase}})
		if err != nil {
			t.Fatal(err)
		}
	}
	return st
}
func resourceReserve(st c.TaskState) Command {
	upper := c.TokenLimits{Input: 6000, Output: 512}
	r := c.ResourceReservation{ID: st.TaskID + "-op", Kind: "MODEL", Provider: "openai", Model: "offline-test", PriceVersion: "test-price-v1", TokenUpper: upper, RequestDigest: c.HashBytes([]byte("request")), ProfileDigest: c.HashBytes([]byte("profile")), SpecVersion: st.SpecVersion, PolicyEpoch: st.PolicyEpoch, Generation: st.KernelGeneration, Upper: c.ResourceVector{MoneyMicros: 6512, CPUMillis: 800, DiskBytes: 750000, WallMillis: 800, Children: 1}, Status: "RESERVED"}
	tr := c.TokenReservation{ID: r.ID, RequestDigest: r.RequestDigest, ProfileDigest: r.ProfileDigest, SpecVersion: r.SpecVersion, PolicyEpoch: r.PolicyEpoch, Generation: r.Generation, Upper: upper, Status: "RESERVED"}
	return Command{ID: st.TaskID + "-reserve", TaskID: st.TaskID, Actor: "kernel", ExpectedTaskSeq: st.TaskSeq, Type: "SessionRecorded", Payload: c.EventPayload{DocumentDigest: c.HashBytes([]byte("reserved")), Tokens: &c.TokenMutation{Action: "RESERVE", Reservation: tr}, Resources: &c.ResourceMutation{Action: "RESERVE", Reservation: r}}}
}
func testResourcePolicy() c.ResourcePolicy {
	p := c.DefaultResourcePolicy()
	p.Limits.MoneyMicros = 1 << 30
	p.Prices = []c.ModelPrice{{Provider: "openai", Model: "offline-test", Version: "test-price-v1", InputMicrosPerMillion: 1000000, OutputMicrosPerMillion: 1000000}}
	return p
}
func TestGlobalResourceVectorAdmissionIsAtomicAcrossEveryDimension(t *testing.T) {
	for _, dimension := range []string{"money", "cpu", "disk", "wall", "children"} {
		t.Run(dimension, func(t *testing.T) {
			p := testResourcePolicy()
			switch dimension {
			case "money":
				p.Limits.MoneyMicros = 10000
			case "cpu":
				p.Limits.CPUMillis = 1000
			case "disk":
				p.Limits.DiskBytes = 1 << 20
			case "wall":
				p.Limits.WallMillis = 1000
			case "children":
				p.Limits.Children = 1
			}
			s, err := Open(context.Background(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			a, b := resourceTask(t, s, "a", p), resourceTask(t, s, "b", p)
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for _, st := range []c.TaskState{a, b} {
				wg.Add(1)
				go func(st c.TaskState) {
					defer wg.Done()
					_, err := s.Execute(context.Background(), resourceReserve(st))
					results <- err
				}(st)
			}
			wg.Wait()
			close(results)
			admitted, denied := 0, 0
			for err := range results {
				if err == nil {
					admitted++
				} else {
					var typed *c.Error
					if !errors.As(err, &typed) || typed.Code != c.BudgetLimitReached {
						t.Fatal(err)
					}
					denied++
				}
			}
			view, err := s.ResourceLedger(context.Background())
			if err != nil || admitted != 1 || denied != 1 || view.Reserved.MoneyMicros != 6512 || view.Reserved.Children != 1 || view.Used != (c.ResourceVector{}) {
				t.Fatal(view, admitted, denied, err)
			}
			tokens, err := s.TokenLedger(context.Background())
			if err != nil || tokens.Reserved.Input != 6000 {
				t.Fatal("partial multi-ledger commit", tokens, err)
			}
		})
	}
}
func TestUnknownResourceReservationSurvivesCancelAndRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	st := resourceTask(t, s, "a", testResourcePolicy())
	st, err = s.Execute(context.Background(), resourceReserve(st))
	if err != nil {
		t.Fatal(err)
	}
	r := st.Resources.Reservations[0]
	tr := st.Tokens.Reservations[0]
	r.Status = "UNKNOWN"
	tr.Status = "UNKNOWN"
	command := Command{ID: "unknown", TaskID: st.TaskID, Actor: "kernel", ExpectedTaskSeq: st.TaskSeq, Type: "SessionRecorded", Payload: c.EventPayload{DocumentDigest: c.HashBytes([]byte("unknown")), Tokens: &c.TokenMutation{Action: "UNKNOWN", Reservation: tr}, Resources: &c.ResourceMutation{Action: "UNKNOWN", Reservation: r}}}
	st, err = s.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	st, err = s.Execute(context.Background(), Command{ID: "cancel", TaskID: st.TaskID, Actor: "kernel", ExpectedTaskSeq: st.TaskSeq, Type: "StateTransitioned", Payload: c.EventPayload{State: c.Terminated, Outcome: c.Cancelled}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	view, err := s.ResourceLedger(context.Background())
	if err != nil || view.Unknown != 1 || view.Reserved != r.Upper {
		t.Fatal(view, err)
	}
	r.Status = "SETTLED"
	r.Meter = "KERNEL_NO_DISPATCH"
	r.ReceiptDigest = c.HashBytes([]byte("fake"))
	command.ID = "release"
	command.ExpectedTaskSeq = st.TaskSeq
	command.Payload.Resources = &c.ResourceMutation{Action: "SETTLE", Reservation: r}
	command.Payload.Tokens = nil
	if _, err = s.Execute(context.Background(), command); err == nil {
		t.Fatal("unknown risk released without paired receipt")
	}
}

func TestObservedMonetaryOverageIsChargedBeforeFurtherAdmissionStops(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st := resourceTask(t, s, "overage", testResourcePolicy())
	st, err = s.Execute(context.Background(), resourceReserve(st))
	if err != nil {
		t.Fatal(err)
	}
	tr := st.Tokens.Reservations[0]
	rr := st.Resources.Reservations[0]
	tr.Status = "SETTLED"
	tr.Used = c.TokenLimits{Input: 1 << 40, Output: 1 << 40}
	tr.ResponseDigest = c.HashBytes([]byte("observed-overage"))
	tr.UsageSource = "PROVIDER_REPORTED"
	rr.Status = "SETTLED"
	rr.TokenUsed = tr.Used
	rr.Meter = "CONSERVATIVE_KERNEL_RECEIPT_V1"
	rr.ReceiptDigest = c.HashBytes([]byte("observed-resource-proof"))
	rr.Used = rr.Upper
	rr.Used.Children = 0
	rr.Used.WallMillis = 10
	rr.Used.MoneyMicros = 2 << 40
	st, err = s.Execute(context.Background(), Command{ID: "overage-settle", TaskID: st.TaskID, Actor: "kernel", ExpectedTaskSeq: st.TaskSeq, Type: "SessionRecorded", Payload: c.EventPayload{DocumentDigest: c.HashBytes([]byte("settled")), Tokens: &c.TokenMutation{Action: "SETTLE", Reservation: tr}, Resources: &c.ResourceMutation{Action: "SETTLE", Reservation: rr}}})
	if err != nil || st.Resources.Used.MoneyMicros != 2<<40 || st.Resources.Reserved != (c.ResourceVector{}) {
		t.Fatal("bounded observed overage lost", st.Resources, err)
	}
	command := resourceReserve(st)
	command.ID = "next"
	command.Payload.Tokens.Reservation.ID = "next-op"
	command.Payload.Resources.Reservation.ID = "next-op"
	if _, err = s.Execute(context.Background(), command); err == nil {
		t.Fatal("work continued beyond observed monetary cap")
	}
}
