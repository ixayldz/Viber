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
	"testing"
	"time"
)

func TestRetentionCLIAndUIUseBoundedOwnerCommandsAndExplicitConsent(t *testing.T) {
	ctx := context.Background()
	directory, source := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := agent.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fixture, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "unused", UsageKnown: true}}})
	if _, err = s.Create(ctx, agent.StartOptions{Root: source, Prompt: []byte("captured task"), TaskID: "task", Budget: agent.DefaultBudget(), Autonomy: "guided", AllowUnverified: true, Fixture: fixture}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Controls(ctx, "task", "cancel"); err != nil {
		t.Fatal(err)
	}
	host, err := owner.Open(ctx, directory, s)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	before, err := s.Journal.SnapshotInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	run := func(args ...string) int {
		out.Reset()
		errs.Reset()
		return Execute(append([]string{"retention"}, args...), &out, &errs)
	}
	if code := run("task", "--store", directory, "--json"); code != 0 {
		t.Fatal(code, out.String(), errs.String())
	}
	deadline := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	if code := run("task", "--store", directory, "--expires-at", deadline, "--revision", "0", "--command-id", "enable", "--json"); code == 0 {
		t.Fatal("CLI enabled implicit expiry")
	}
	if code := run("task", "--store", directory, "--expires-at", deadline, "--acknowledge", c.RetentionAcknowledgement, "--revision", "0", "--command-id", "enable", "--json"); code != 0 {
		t.Fatal(code, out.String(), errs.String())
	}
	var policy c.RetentionPolicy
	if c.DecodeStrict(out.Bytes(), &policy) != nil || policy.Revision != 1 {
		t.Fatal(out.String())
	}
	u := uiSession{task: "task", directory: directory}
	if text, detached, err := u.command(ctx, "/retention"); err != nil || detached || text == "" {
		t.Fatal(text, err)
	}
	if _, _, err = u.command(ctx, "/retention keep 0"); err == nil {
		t.Fatal("UI accepted stale retention revision")
	}
	if _, _, err = u.command(ctx, "/retention keep 1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = u.command(ctx, "/retention run-due"); err != nil {
		t.Fatal("UI did not route store scope", err)
	}
	for _, request := range []ipc.Request{
		{ID: "bad-task", TaskID: "task", Command: "retention-run", Payload: []byte("null")},
		{ID: "bad-payload", Command: "retention-run", Payload: []byte(`{"limit":999}`)},
		{ID: "wrong-id", TaskID: "task", Command: "retention-set", Payload: []byte(`{"command_id":"other","expected_revision":2,"mode":"KEEP"}`)},
	} {
		if _, err = host.Handle(ctx, request); err == nil {
			t.Fatal("owner accepted invalid retention scope", request)
		}
	}
	after, err := s.Journal.SnapshotInfo(ctx)
	if err != nil || after != before {
		t.Fatal("retention configuration changed task journal", err)
	}
	if err = host.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if code := run("task", "--store", directory, "--json"); code != 0 {
		t.Fatal("standalone status", code, out.String(), errs.String())
	}
	var status agent.RetentionStatus
	if c.DecodeStrict(out.Bytes(), &status) != nil || status.Policy == nil || status.Policy.Revision != 2 || status.Status != "KEEP" {
		t.Fatal(out.String())
	}
}

func TestRetentionMissingStoreAndInvalidFlagsHaveNoEffects(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "absent")
	for _, args := range [][]string{
		{"task", "--store", directory, "--json"},
		{"--store", directory, "--run-due", "--json"},
		{"task", "--store", directory, "--run-due"},
		{"task", "--store", directory, "--keep", "--expires-at", "2027-01-01T00:00:00Z"},
		{"task", "--store", directory, "--keep", "--revision", "0"},
		{"task", "--store", directory, "--command-id", "unused"},
	} {
		var out, errs bytes.Buffer
		if code := Execute(append([]string{"retention"}, args...), &out, &errs); code == 0 {
			t.Fatal("invalid command accepted", args)
		}
		if _, err := os.Lstat(directory); !os.IsNotExist(err) {
			t.Fatal("retention created absent store", err)
		}
	}
}
