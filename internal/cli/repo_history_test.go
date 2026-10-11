package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRepoHistoryRequiresExplicitLocalHistoryConsentBeforeCapture(t *testing.T) {
	var out, errout bytes.Buffer
	exit := runRepoHistory([]string{"--cutoff", "2026-01-01T00:00:00Z", "--json"}, &out, &errout)
	if exit != 4 || !strings.Contains(out.String(), "POLICY_DENIED") {
		t.Fatalf("history consent gate failed: exit=%d out=%s err=%s", exit, out.String(), errout.String())
	}
}
