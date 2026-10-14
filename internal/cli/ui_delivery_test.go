package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/owner"
)

func TestUIAndCLIExportThroughExistingOwnerPreserveSource(t *testing.T) {
	ctx := context.Background()
	source, directory := t.TempDir(), t.TempDir()
	before, after := []byte("original\n"), []byte("generated application\n")
	if err := os.WriteFile(filepath.Join(source, "app.txt"), before, 0600); err != nil {
		t.Fatal(err)
	}
	session, err := agent.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	args, _ := json.Marshal(struct {
		ReadSet []c.ReadCondition `json:"read_set"`
		Changes []c.Change        `json:"changes"`
	}{[]c.ReadCondition{{Path: "app.txt", Kind: "FILE", Digest: c.HashBytes(before)}}, []c.Change{{Path: "app.txt", BeforeDigest: c.HashBytes(before), After: after}}})
	fixture, _ := json.Marshal(agent.Fixture{SchemaVersion: 1, Turns: []agent.Turn{
		{Calls: []model.Call{{ID: "generate", Name: "candidate_propose", Arguments: args}}, UsageKnown: true},
		{Text: "Candidate produced", UsageKnown: true},
	}})
	if _, err = session.Create(ctx, agent.StartOptions{Root: source, TaskID: "ui-project", Prompt: []byte("Produce the requested candidate"), Budget: agent.DefaultBudget(), Autonomy: "auto", AllowUnverified: true, Fixture: fixture}); err != nil {
		t.Fatal(err)
	}
	if _, err = session.Run(ctx, "ui-project"); err != nil {
		t.Fatal(err)
	}
	host, err := owner.Open(ctx, directory, session)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	u := uiSession{"ui-project", directory}
	cliOutput := filepath.Join(t.TempDir(), "cli export")
	var out, errs bytes.Buffer
	if code := Execute([]string{"export", "ui-project", "--store", directory, "--output", cliOutput, "--json"}, &out, &errs); code != 0 {
		t.Fatal("CLI attempted a second owner instead of routing export:", code, errs.String())
	}
	uiOutput := filepath.Join(t.TempDir(), "ui export")
	text, quit, err := u.command(ctx, "/export "+strconv.Quote(uiOutput))
	if err != nil || quit || !strings.Contains(text, "Exported 1 changes") {
		t.Fatal("slash export did not deliver through the active owner:", text, quit, err)
	}
	for _, output := range []string{cliOutput, uiOutput} {
		raw, err := os.ReadFile(filepath.Join(output, "after", "app.txt"))
		if err != nil || !bytes.Equal(raw, after) {
			t.Fatal("exported candidate differs from requested bytes:", err)
		}
		if _, err = os.Stat(filepath.Join(output, "manifest.json")); err != nil {
			t.Fatal("export lacks completion manifest:", err)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(source, "app.txt")); err != nil || !bytes.Equal(raw, before) {
		t.Fatal("delivery changed the original source:", err)
	}
	if _, _, err = u.command(ctx, "/export "+strconv.Quote(uiOutput)); err == nil {
		t.Fatal("export replaced an existing delivery directory")
	}
	if err = os.WriteFile(filepath.Join(source, "app.txt"), []byte("user edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	preview := filepath.Join(t.TempDir(), "preview")
	text, _, err = u.command(ctx, "/preview "+strconv.Quote(preview))
	if err != nil || !strings.Contains(text, "CONFLICT") {
		t.Fatal("preview failed to report the user's conflicting edit:", text, err)
	}
	if raw, err := os.ReadFile(filepath.Join(source, "app.txt")); err != nil || !bytes.Equal(raw, []byte("user edit\n")) {
		t.Fatal("preview overwrote the user's current edit:", err)
	}
}
