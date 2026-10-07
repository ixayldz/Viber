package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

func TestChangesetExportPublishesManifestLastWithoutSourceMutation(t *testing.T) {
	session, options, source := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "untrusted summary", UsageKnown: true}}, true)
	defer session.Close()
	state, err := session.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "export")
	manifest, err := session.Export(context.Background(), options.TaskID, output)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.LiveWorkspaceWritten || manifest.State.Quality != c.Unverified || len(manifest.Changeset.Changes) != 1 || !manifest.Changeset.PatchComplete {
		t.Fatal("export exaggerated assurance", manifest)
	}
	for _, f := range manifest.Files {
		raw, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(f.Path)))
		if err != nil || c.HashBytes(raw) != f.Digest || int64(len(raw)) != f.Size {
			t.Fatal("export bytes not manifest bound", err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(output, "after", "a.txt"))
	if err != nil || string(raw) != "after\r\n" {
		t.Fatal("exact bytes lost", err)
	}
	live, _ := os.ReadFile(filepath.Join(source, "a.txt"))
	if string(live) != "before\r\n" {
		t.Fatal("source overwritten")
	}
	after, err := session.State(context.Background(), options.TaskID)
	if err != nil || after.TaskSeq != state.TaskSeq {
		t.Fatal("export changed task lifecycle", err)
	}
	for _, target := range []string{output, filepath.Join(source, "export"), filepath.Join(session.directory, "export")} {
		if _, err = session.Export(context.Background(), options.TaskID, target); err == nil {
			t.Fatal("unsafe output accepted", target)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := filepath.Join(t.TempDir(), "cancelled")
	if _, err = session.Export(ctx, options.TaskID, cancelled); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err = os.Stat(filepath.Join(cancelled, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("cancelled export was published")
	}
}
