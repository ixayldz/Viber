package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
	"github.com/ixayldz/Viber/internal/owner"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLISupervisorChild(t *testing.T) {
	if os.Getenv("VIBER_TEST_SUPERVISOR_CHILD") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(Execute(os.Args[i+1:], os.Stdout, os.Stderr))
		}
	}
	os.Exit(99)
}
func TestCLIBackgroundRunAttachAndGracefulStop(t *testing.T) {
	t.Setenv("VIBER_TEST_SUPERVISOR_CHILD", "1")
	original := supervisorPrefix
	supervisorPrefix = []string{"-test.run=^TestCLISupervisorChild$", "--"}
	defer func() { supervisorPrefix = original }()
	source := t.TempDir()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "done", UsageKnown: true, InputTokens: 1, OutputTokens: 1}}})
	fixture := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(fixture, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	defer func() {
		var cleanupOut, cleanupErr bytes.Buffer
		Execute([]string{"pause", "task", "--store", directory, "--json"}, &cleanupOut, &cleanupErr)
		Execute([]string{"owner-stop", "--store", directory, "--json"}, &cleanupOut, &cleanupErr)
	}()
	code := Execute([]string{"run", "Review source", "--detach", "--offline", "--fixture", fixture, "--root", source, "--store", directory, "--task", "task", "--json"}, &out, &errs)
	if code != 0 {
		t.Fatal(code, out.String(), errs.String())
	}
	var admission owner.View
	if err := c.DecodeStrict(out.Bytes(), &admission); err != nil || !admission.Detached || admission.InvocationID == "" {
		t.Fatal(admission, err)
	}
	info, err := ipc.Discover(directory)
	if err != nil || info.PID == os.Getpid() {
		t.Fatal("no independent process", info, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	cursor := int64(0)
	var final owner.AttachmentPage
	for {
		final, err = readAttachment(context.Background(), directory, "task", owner.Page{After: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range final.History.Records {
			if record.Event.TaskSeq != cursor+1 {
				t.Fatal("cursor gap", cursor, record.Event.TaskSeq)
			}
			cursor = record.Event.TaskSeq
		}
		if !final.Active && !final.History.HasMore {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background task did not settle")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.State.Execution != c.WaitingUser || cursor != final.State.TaskSeq || final.State.Quality != c.Unverified {
		t.Fatal(final)
	}
	out.Reset()
	errs.Reset()
	if code = Execute([]string{"detach", "task", "--store", directory, "--command-id", admission.InvocationID, "--json"}, &out, &errs); code != 0 {
		t.Fatal("completion dedup lost", out.String(), errs.String())
	}
	out.Reset()
	errs.Reset()
	if code = Execute([]string{"attach", "task", "--store", directory, "--after", "0", "--limit", "2", "--follow", "--json"}, &out, &errs); code != 0 {
		t.Fatal(code, out.String(), errs.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		t.Fatal("attachment did not emit bounded JSONL pages")
	}
	out.Reset()
	errs.Reset()
	if code = Execute([]string{"owner-stop", "--store", directory, "--json"}, &out, &errs); code != 0 {
		t.Fatal(code, out.String(), errs.String())
	}
	for {
		_, err = ipc.Discover(directory)
		if err == ipc.ErrNoOwner {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owner did not remove endpoint")
		}
		time.Sleep(20 * time.Millisecond)
	}
	restored, err := agent.OpenExisting(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	state, doc, err := restored.Load(context.Background(), "task")
	restored.Close()
	if err != nil || doc.UnknownEffect || state.Execution != c.WaitingUser || len(state.Tokens.Reservations) != 1 {
		t.Fatal(state, err)
	}
	current, _ := os.ReadFile(filepath.Join(source, "a.txt"))
	if string(current) != "source" {
		t.Fatal("background command changed source")
	}
}
