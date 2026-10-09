package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func TestMigrationCanonicalBackupAliasRetryCannotChangeBackup(t *testing.T) {
	s, _ := migrationFixture(t)
	defer s.Close()
	parent := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(parent, alias); err != nil {
		t.Skip("symlink capability unavailable:", err)
	}
	path := filepath.Join(alias, "backup")
	receipt, err := s.Migrate(context.Background(), "alias-migration", path)
	if err != nil {
		t.Fatal(err)
	}
	physical, err := fileguard.ResolveProspective(path)
	if err != nil || receipt.Intent.BackupPath != physical {
		t.Fatal("noncanonical durable backup identity", receipt, err)
	}
	for _, retry := range []string{path, filepath.Join(parent, "backup"), physical} {
		again, err := s.Migrate(context.Background(), "alias-migration", retry)
		if err != nil || again != receipt {
			t.Fatal("equivalent backup path retry changed receipt", again, err)
		}
	}
	_, err = s.Migrate(context.Background(), "alias-migration", filepath.Join(t.TempDir(), "other-backup"))
	failure, ok := err.(*c.Error)
	if !ok || failure.Code != c.CommandIDConflict {
		t.Fatal("different backup retry not refused", err)
	}
}
