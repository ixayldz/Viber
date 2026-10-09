package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/owner"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUILinesQueueSteeringSourceAndSafeSupportThroughOwner(t *testing.T) {
	ctx := context.Background()
	source, directory := t.TempDir(), t.TempDir()
	secret := "CANARY_SOURCE_TOKEN_127"
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := agent.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fixture, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "done", UsageKnown: true}}})
	if _, err = s.Create(ctx, agent.StartOptions{Root: source, TaskID: "canary-task", Prompt: []byte("CANARY_USER_EMAIL@example.test"), Budget: agent.DefaultBudget(), Autonomy: "guided", Fixture: fixture}); err != nil {
		t.Fatal(err)
	}
	h, err := owner.Open(ctx, directory, s)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	u := uiSession{"canary-task", directory}
	if _, _, err = u.command(ctx, "/queue add Review @a.txt"); err != nil {
		t.Fatal(err)
	}
	before, doc, err := s.Load(ctx, "canary-task")
	if err != nil || len(before.PromptQueue) != 1 || before.InputBarrier {
		t.Fatal(before, err)
	}
	prompts, err := s.PromptQueue(ctx, "canary-task")
	if err != nil || !strings.Contains(prompts[0].Text, doc.Candidate.SnapshotDigest) {
		t.Fatal("mention is not source-bound", prompts, err)
	}
	originalInput := uiInput
	uiInput = strings.NewReader("/help\n/queue\n/read a.txt\n/queue activate " + prompts[0].Ref.ID + "\n/status\n/quit\n")
	defer func() { uiInput = originalInput }()
	var out, errs bytes.Buffer
	if code := Execute([]string{"ui", "canary-task", "--store", directory, "--line"}, &out, &errs); code != 0 || errs.Len() != 0 {
		t.Fatal(code, out.String(), errs.String())
	}
	if !strings.Contains(out.String(), "UI detached") || !strings.Contains(out.String(), secret) {
		t.Fatal("UI command wiring missing", out.String())
	}
	after, _, err := s.Load(ctx, "canary-task")
	if err != nil || after.Execution != c.Paused || !after.InputBarrier || len(after.PromptQueue) != 0 {
		t.Fatal(after, err)
	}
	raw, err := s.PromptQueue(ctx, "canary-task")
	if err != nil || len(raw) != 0 {
		t.Fatal(raw, err)
	}
	out.Reset()
	errs.Reset()
	output := filepath.Join(t.TempDir(), "support")
	if code := Execute([]string{"support", "--store", directory, "--output", output, "--json"}, &out, &errs); code != 0 {
		t.Fatal(code, out.String(), errs.String())
	}
	bundle, err := os.ReadFile(filepath.Join(output, "support.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{secret, "CANARY_USER_EMAIL", "a.txt", source, directory, "canary-task", doc.Candidate.SnapshotDigest, prompts[0].Ref.Digest} {
		if bytes.Contains(bundle, []byte(leak)) {
			t.Fatalf("support leaked %q", leak)
		}
	}
}
