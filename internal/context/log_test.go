package context

import (
	"bytes"
	stdcontext "context"
	"encoding/json"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"testing"
)

func noisyLog() []byte {
	return append([]byte("compile: undefined: MissingSymbol\r\nroot dependency unavailable\r\n"), bytes.Repeat([]byte("progress item completed\n"), 20000)...)
}
func TestIntentLogPreservesRootErrorWithinBudgetAndMandatoryContext(t *testing.T) {
	raw := noisyLog()
	selected, err := SelectLog(stdcontext.Background(), raw, "BUILD", 512, true)
	if err != nil {
		t.Fatal(err)
	}
	if selected.SourceDigest != c.HashBytes(raw) || selected.SelectedBytes > 512 || selected.OmittedBytes+selected.SelectedBytes != len(raw) || !selected.SourceTruncated {
		t.Fatal("false provenance/coverage")
	}
	if bytes.Contains(raw[len(raw)-512:], []byte("MissingSymbol")) {
		t.Fatal("invalid comparison fixture")
	}
	found := false
	for _, span := range selected.Spans {
		if !bytes.Equal(span.Bytes, raw[span.Start:span.End]) {
			t.Fatal("invented span")
		}
		found = found || bytes.Contains(span.Bytes, []byte("MissingSymbol"))
	}
	if !found {
		t.Fatal("early build root error lost")
	}
	packedRaw, _ := json.Marshal(selected)
	required := []Block{{ID: "kernel", Text: "policy generation 4; never deploy; keep user edits"}, {ID: "constraints_pack", Text: "all required constraints"}}
	blocks, manifest, err := Pack(required, []Block{{ID: "log_hint", Text: string(packedRaw)}}, Limits{Context: 4096, Output: 512, SafetyMargin: 128}, bytesEstimator)
	if err != nil || len(blocks) != 3 || blocks[0] != required[0] || blocks[1] != required[1] {
		t.Fatal("required context damaged", err)
	}
	t.Logf("synthetic early-error recall=1/1; source_bytes=%d selected_bytes=%d serialized_bytes=%d packed_byte_estimate=%d; not provider token measurement", len(raw), selected.SelectedBytes, len(packedRaw), manifest.InputTokens)
}
func TestLogIntentCancellationBinaryAndScanBounds(t *testing.T) {
	raw := []byte("ok\n--- FAIL: Broken\n\xff\x00\r\nwarning: old API\n")
	for _, intent := range []string{"TEST", "ERROR", "BUILD", "DIAGNOSTIC"} {
		result, err := SelectLog(stdcontext.Background(), raw, intent, 20, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, span := range result.Spans {
			if !bytes.Equal(span.Bytes, raw[span.Start:span.End]) {
				t.Fatal("lossy bytes")
			}
		}
		if result.SelectedBytes > 20 {
			t.Fatal("budget overflow")
		}
	}
	ctx, cancel := stdcontext.WithCancel(stdcontext.Background())
	cancel()
	if _, err := SelectLog(ctx, raw, "TEST", 1, false); !errors.Is(err, stdcontext.Canceled) {
		t.Fatal(err)
	}
	if _, err := SelectLog(stdcontext.Background(), raw, "PASS", 20, false); err == nil {
		t.Fatal("invalid intent accepted")
	}
	result, err := SelectLog(stdcontext.Background(), bytes.Repeat([]byte("x\n"), 65537), "ERROR", 20, false)
	if err != nil || !result.ScanTruncated {
		t.Fatal("scan bound not reported", err)
	}
}
func BenchmarkContextLogPacking(b *testing.B) {
	raw := noisyLog()
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	for i := 0; i < b.N; i++ {
		result, err := SelectLog(stdcontext.Background(), raw, "BUILD", 512, false)
		if err != nil {
			b.Fatal(err)
		}
		encoded, _ := json.Marshal(result)
		b.ReportMetric(float64(result.SelectedBytes), "selected-bytes")
		b.ReportMetric(float64(len(encoded)), "serialized-bytes")
	}
}
