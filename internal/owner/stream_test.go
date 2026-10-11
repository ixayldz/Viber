package owner

import (
	"context"
	"strings"
	"testing"
)

func TestProviderStreamBoundsSlowReaderAndRequiresResync(t *testing.T) {
	stream := newProviderStream("task", "invocation", 7)
	chunk := strings.Repeat("x", maxProviderDeltaRecord)
	for i := 0; i < maxProviderDeltas; i++ {
		stream.Publish(chunk)
	}
	stream.mu.Lock()
	if len(stream.deltas) > maxProviderDeltas || stream.bytes > maxProviderDeltaBytes {
		t.Fatalf("provider ring exceeded its bounds: records=%d bytes=%d", len(stream.deltas), stream.bytes)
	}
	earliest, current := stream.deltas[0].Sequence, stream.current
	stream.mu.Unlock()
	if earliest <= 1 {
		t.Fatal("slow reader did not force a retention gap", earliest, current)
	}
	page, err := stream.page(context.Background(), StreamPageQuery{After: 0, Limit: 16}, CurrentStateRef{TaskID: "task", TaskSeq: 7})
	if err != nil || !page.ResyncRequired || len(page.Deltas) != 0 || page.EarliestDeltaSeq != earliest || page.CurrentDeltaSeq != current {
		t.Fatal("retention gap was not explicit", page, err)
	}
	page, err = stream.page(context.Background(), StreamPageQuery{After: current, Limit: 16}, CurrentStateRef{TaskID: "task", TaskSeq: 7})
	if err != nil || page.ResyncRequired || len(page.Deltas) != 0 {
		t.Fatal("current cursor failed to resync", page, err)
	}
	stream.Publish("next")
	page, err = stream.page(context.Background(), StreamPageQuery{After: current, Limit: 16}, CurrentStateRef{TaskID: "task", TaskSeq: 7})
	if err != nil || page.Next != current+1 || len(page.Deltas) != 1 || page.Deltas[0].Text != "next" {
		t.Fatal("stream did not continue from the resynchronized cursor", page, err)
	}
}
