package agent

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"os"
	"path/filepath"
	"testing"
)

func TestOperatorDeliveryPreviewPreservesUserBytesAndHistoricalTask(t *testing.T) {
	session, options, source := startFixture(t, []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}, true)
	defer session.Close()
	state, err := session.Run(context.Background(), options.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "user.txt"), []byte("independent user file"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "preview")
	result, err := session.DeliveryPreview(context.Background(), options.TaskID, output)
	if err != nil || result.Preview.Status != "READY" || result.Preview.Verification != "UNVERIFIED" || result.Preview.LiveWorkspaceWritten {
		t.Fatal("unsafe delivery preview", result, err)
	}
	for _, file := range result.Files {
		raw, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(file.Path)))
		if err != nil || c.HashBytes(raw) != file.Digest || int64(len(raw)) != file.Size {
			t.Fatal("unbound preview bytes", err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(output, "merged", "a.txt"))
	if string(raw) != "after\r\n" {
		t.Fatal("candidate missing")
	}
	raw, _ = os.ReadFile(filepath.Join(output, "merged", "user.txt"))
	if string(raw) != "independent user file" {
		t.Fatal("user file not retained")
	}
	raw, _ = os.ReadFile(filepath.Join(source, "a.txt"))
	if string(raw) != "before\r\n" {
		t.Fatal("preview mutated source")
	}
	latest, err := session.State(context.Background(), options.TaskID)
	if err != nil || latest.TaskSeq != state.TaskSeq || latest.Quality != state.Quality || latest.DocumentDigest != state.DocumentDigest {
		t.Fatal("preview changed historical authority", err)
	}
	for _, unsafe := range []string{output, filepath.Join(source, "preview"), filepath.Join(session.directory, "preview")} {
		if _, err = session.DeliveryPreview(context.Background(), options.TaskID, unsafe); err == nil {
			t.Fatal("unsafe preview export path admitted")
		}
	}
	if err = os.WriteFile(filepath.Join(source, "a.txt"), []byte("user edit\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	conflictRoot := filepath.Join(t.TempDir(), "conflict")
	conflict, err := session.DeliveryPreview(context.Background(), options.TaskID, conflictRoot)
	if err != nil || conflict.Preview.Status != "CONFLICT" || conflict.Changeset != nil {
		t.Fatal("conflict not explicit", err)
	}
	if _, err = os.Stat(filepath.Join(conflictRoot, "merged")); !os.IsNotExist(err) {
		t.Fatal("partial merged export after conflict")
	}
	if _, err = os.Stat(filepath.Join(conflictRoot, "manifest.json")); err != nil {
		t.Fatal("complete conflict review missing")
	}
}
