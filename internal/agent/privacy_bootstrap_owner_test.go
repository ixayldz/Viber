package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRestoredPrivacyBootstrapReopensThroughParentAlias(t *testing.T) {
	ctx := context.Background()
	session, _, _ := startFixture(t, []Turn{{Text: "done", UsageKnown: true}}, true)
	defer session.Close()
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err := session.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	alias := filepath.Join(t.TempDir(), "parent-alias")
	if err := os.Symlink(parent, alias); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	target := filepath.Join(parent, "restored")
	if _, err := RestoreBackup(ctx, backup, target); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenExisting(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	before, err := restored.Journal.PrivacyBootstrap(ctx)
	if err != nil || before == nil || before.RestoreBinding == "" {
		restored.Close()
		t.Fatal("restored owner lost bootstrap provenance", before, err)
	}
	if err = restored.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(ctx, filepath.Join(alias, "restored"))
	if err != nil {
		t.Fatal("same physical owner rejected through parent alias", err)
	}
	defer reopened.Close()
	after, err := reopened.Journal.PrivacyBootstrap(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("alias reopen changed the sealed restore binding", before, after, err)
	}
}
