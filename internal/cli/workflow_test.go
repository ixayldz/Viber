package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/store"
)

func TestCLIRequestBackupRestoreResponseAndHistoricalReplay(t *testing.T) {
	source := t.TempDir()
	directory := filepath.Join(t.TempDir(), "session")
	inputs := t.TempDir()
	fixture := filepath.Join(inputs, "fixture.json")
	raw, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{{Text: "done", UsageKnown: true}}})
	if err := os.WriteFile(fixture, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	run := func(want int, args ...string) []byte {
		t.Helper()
		out.Reset()
		stderr.Reset()
		if exit := Execute(args, &out, &stderr); exit != want {
			t.Fatalf("%v: exit %d want %d: %s %s", args, exit, want, out.String(), stderr.String())
		}
		return append([]byte(nil), out.Bytes()...)
	}
	run(3, "run", "Review empty project", "--offline", "--fixture", fixture, "--root", source, "--store", directory, "--task", "workflow", "--json")
	raw = run(0, "requests", "workflow", "--store", directory, "--json")
	var requests []agent.UserRequest
	if err := json.Unmarshal(raw, &requests); err != nil || len(requests) != 1 {
		t.Fatal(err, string(raw))
	}
	response := agent.ResponseTemplate(requests[0], "response-once", "approve")
	responsePath := filepath.Join(inputs, "response.json")
	raw, _ = json.Marshal(response)
	if err := os.WriteFile(responsePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	run(0, "store-backup", "--store", directory, "--output", backup, "--json")
	restored := filepath.Join(t.TempDir(), "restored")
	run(0, "store-restore", "--backup", backup, "--store", restored, "--json")
	run(0, "respond", "workflow", "--store", restored, "--request", requests[0].ID, "--response-file", responsePath, "--json")
	run(0, "respond", "workflow", "--store", restored, "--request", requests[0].ID, "--response-file", responsePath, "--json")
	run(2, "resume", "workflow", "--store", restored, "--json")
	for _, command := range []string{"inspect", "replay"} {
		cursorFlag := "--at"
		if command == "replay" {
			cursorFlag = "--until"
		}
		raw = run(0, command, "workflow", "--store", restored, cursorFlag, "1", "--json")
		var result struct {
			State   c.TaskState `json:"state"`
			Effects bool        `json:"effects_executed"`
		}
		if err := json.Unmarshal(raw, &result); err != nil || result.State.Execution != c.Created || result.State.TaskSeq != 1 || result.Effects {
			t.Fatal("historical command ran or returned latest", err, string(raw))
		}
	}
	raw = run(0, "events", "workflow", "--store", restored, "--after", "0", "--limit", "2", "--json")
	var page store.HistoryPage
	if err := json.Unmarshal(raw, &page); err != nil || len(page.Records) != 2 || page.Next != 2 || !page.HasMore {
		t.Fatal("event paging", err, string(raw))
	}
	run(4, "store-restore", "--backup", backup, "--store", restored, "--json")
	run(4, "inspect", "workflow", "--store", restored, "--until", "1", "--json")
}

func TestControlCommandsDoNotInitializeMissingStore(t *testing.T) {
	for _, command := range []string{"status", "inspect", "replay", "events", "requests", "export", "delivery-preview", "store-backup"} {
		t.Run(command, func(t *testing.T) {
			empty := t.TempDir()
			var out, stderr bytes.Buffer
			args := []string{command, "absent", "--store", empty, "--json"}
			if command == "export" || command == "delivery-preview" {
				args = append(args, "--output", filepath.Join(t.TempDir(), "export"))
			}
			if command == "store-backup" {
				args = []string{command, "--store", empty, "--output", filepath.Join(t.TempDir(), "backup"), "--json"}
			}
			if exit := Execute(args, &out, &stderr); exit != 4 {
				t.Fatal(exit, out.String())
			}
			entries, err := os.ReadDir(empty)
			if err != nil || len(entries) != 0 {
				t.Fatal("read/control created state", command, err)
			}
		})
	}
}
