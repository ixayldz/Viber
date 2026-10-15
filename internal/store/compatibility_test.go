package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type compatibilityDatabaseState struct {
	Generation int64
	Version    int
	Tables     []string
}

func readCompatibilityDatabaseState(t *testing.T, directory string) compatibilityDatabaseState {
	t.Helper()
	db, err := openStoreDatabaseReadOnly(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	state := compatibilityDatabaseState{}
	if err = db.QueryRow("PRAGMA user_version").Scan(&state.Version); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT value FROM meta WHERE key='generation'").Scan(&state.Generation); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		state.Tables = append(state.Tables, name)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(state.Tables)
	return state
}

func TestOpenForCompatibilityIsReadOnlyAndDoesNotCreateState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := open(t, root)
	if _, err := s.Execute(ctx, cmd("create", "t", "TaskCreated", 0, c.EventPayload{SpecVersion: 1})); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before := readCompatibilityDatabaseState(t, root)

	compat, err := OpenForCompatibility(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if compat.Generation() != before.Generation {
		t.Fatalf("compatibility open changed generation: got %d want %d", compat.Generation(), before.Generation)
	}
	if got, err := compat.Replay(ctx, "t"); err != nil || got["t"].TaskSeq != 1 {
		t.Fatalf("compatibility replay failed: state=%v err=%v", got, err)
	}
	var typed *c.Error
	if _, err = compat.Execute(ctx, cmd("blocked", "t", "StateTransitioned", 1, c.EventPayload{State: c.Scoping})); !errors.As(err, &typed) || typed.Code != c.UnsupportedCapability {
		t.Fatalf("compatibility execute was not refused as read-only: %v", err)
	}
	if err = compat.Writable(); !errors.As(err, &typed) || typed.Code != c.UnsupportedCapability {
		t.Fatalf("compatibility writable check was not refused: %v", err)
	}
	if _, err = Open(ctx, root); !errors.As(err, &typed) || typed.Code != c.StoreOwned {
		t.Fatalf("compatibility lease did not exclude writer: %v", err)
	}
	if err = compat.Close(); err != nil {
		t.Fatal(err)
	}
	after := readCompatibilityDatabaseState(t, root)
	if after.Generation != before.Generation || after.Version != before.Version || !equalStrings(after.Tables, before.Tables) {
		t.Fatalf("compatibility inspection mutated database: before=%+v after=%+v", before, after)
	}

	empty := t.TempDir()
	missing := filepath.Join(empty, "missing-store")
	if _, err = OpenForCompatibility(ctx, missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing store result=%v, want not-exist", err)
	}
	if _, err = os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing store was created: %v", err)
	}
	entries, err := os.ReadDir(empty)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("compatibility probe created files in parent: %v", entries)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
