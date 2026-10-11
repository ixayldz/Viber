package distribution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func TestOwnedUninstallResumesDurableInventoryAndRejectsExtraFiles(t *testing.T) {
	revision := strings.Repeat("4", 40)
	bundle, err := ParseArchive(fixtureArchive(t, revision, nil), "zip", revision, nativePlatform(t))
	if err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(t.TempDir(), "program")
	m, err := Open(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Install(context.Background(), bundle); err != nil {
		t.Fatal(err)
	}
	intent := retirement{SchemaVersion: 1, Revision: revision, ManifestDigest: bundle.ManifestDigest, Files: append([]File{}, bundle.Manifest.Files...)}
	manifest := bundle.Files["package.json"]
	intent.Files = append(intent.Files, File{Path: "package.json", Digest: bundle.ManifestDigest, Bytes: int64(len(manifest))})
	raw, _ := c.CanonicalV1(intent)
	if err = fileguard.Publish(m.root, filepath.Join("retirements", revision+".json"), raw); err != nil {
		t.Fatal(err)
	}
	state, _ := m.Status()
	if _, err = m.publish(state, Selection{Action: "UNINSTALL"}); err != nil {
		t.Fatal(err)
	}
	if err = m.root.Remove(filepath.Join("versions", revision, "README.md")); err != nil {
		t.Fatal(err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = Open(prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	// A changed namespace never expands the immutable retirement inventory.
	extra := filepath.Join(prefix, "versions", revision, "unowned.txt")
	if err = os.WriteFile(extra, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Uninstall(); err == nil {
		t.Fatal("unowned file was accepted")
	}
	if raw, err = os.ReadFile(extra); err != nil || string(raw) != "keep" {
		t.Fatal("unowned file changed")
	}
	if err = os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	state, err = m.Uninstall()
	if err != nil || len(state.Installed) != 0 {
		t.Fatalf("interrupted uninstall did not resume: %v %+v", err, state)
	}
}

func TestRegisteredStoreIdentitySurvivesReopenAndRejectsReplacement(t *testing.T) {
	parent := t.TempDir()
	store := filepath.Join(parent, "store")
	if err := os.Mkdir(store, 0700); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(parent, "program")
	m, err := Open(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.RegisterStore(store); err != nil {
		t.Fatal(err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = Open(prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	known, err := m.RegisteredStores()
	if err != nil || len(known) != 1 {
		t.Fatalf("store registration lost: %v", err)
	}
	if err = known[0].ValidateCurrent(); err != nil {
		t.Fatal(err)
	}
	// Renaming preserves the old directory identity; a fresh directory at the
	// old spelling must never inherit the store's updater quiescence authority.
	if err = os.Rename(store, store+"-old"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(store, 0700); err != nil {
		t.Fatal(err)
	}
	if known[0].ValidateCurrent() == nil || m.RegisterStore(store) == nil {
		t.Fatal("replacement store identity adopted")
	}
}
