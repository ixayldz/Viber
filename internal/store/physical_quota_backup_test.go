package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

type physicalQuotaBackupFixture struct {
	rootPath     string
	root         *os.Root
	rootIdentity string
	databasePath string
	digest       string
	bytes        int64
	binding      *PhysicalQuotaBinding
}

func newPhysicalQuotaBackupFixture(t *testing.T, withBinding bool) physicalQuotaBackupFixture {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	ownerPath := filepath.Join(base, "owner")
	owner, err := Open(ctx, ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	var expected *PhysicalQuotaBinding
	if withBinding {
		authorityID := c.HashBytes([]byte("backup physical quota authority"))[:32]
		ledgerID := c.HashBytes([]byte("backup physical quota ledger"))[:32]
		physicalRoot := c.HashBytes([]byte("backup physical quota authority root"))
		authority := physicalQuotaAuthority{SchemaVersion: 1, ID: authorityID,
			Directory: filepath.Join(base, "authority"), PhysicalRoot: physicalRoot}
		authorityRaw, encodeErr := c.CanonicalV1(authority)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if err = owner.BindPrivacyAuthority(ctx, authorityRaw); err != nil {
			t.Fatal(err)
		}
		binding := PhysicalQuotaBinding{SchemaVersion: 1, AuthorityID: authorityID, Directory: authority.Directory,
			PhysicalRoot: physicalRoot, LedgerID: ledgerID}
		if err = owner.BindPhysicalQuota(ctx, binding); err != nil {
			t.Fatal(err)
		}
		expected = &binding
	}
	backupPath := filepath.Join(base, "backup")
	databaseDirectory := filepath.Join(backupPath, "database")
	if err = os.MkdirAll(databaseDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.SnapshotDatabase(ctx, databaseDirectory); err != nil {
		t.Fatal(err)
	}
	if err = owner.Close(); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	rootIdentity, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(databaseDirectory, "state.sqlite")
	info, err := os.Stat(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := fileguard.ReadRegular(root, "database/state.sqlite", physicalQuotaBackupDatabaseMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return physicalQuotaBackupFixture{rootPath: backupPath, root: root, rootIdentity: rootIdentity,
		databasePath: databasePath, digest: c.HashBytes(data), bytes: info.Size(), binding: expected}
}

func TestReadPhysicalQuotaBindingFromBackupIsReadOnlyAndBoundToSnapshot(t *testing.T) {
	fixture := newPhysicalQuotaBackupFixture(t, true)
	directoryBefore, err := os.ReadDir(filepath.Dir(fixture.databasePath))
	if err != nil {
		t.Fatal(err)
	}
	infoBefore, err := os.Stat(fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	dataBefore, err := os.ReadFile(fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadPhysicalQuotaBindingFromBackup(context.Background(), fixture.root, fixture.rootIdentity, fixture.digest, fixture.bytes)
	if err != nil || !reflect.DeepEqual(got, fixture.binding) {
		t.Fatal("valid backup quota pin was not read", got, err)
	}
	infoAfter, err := os.Stat(fixture.databasePath)
	if err != nil || !os.SameFile(infoBefore, infoAfter) || infoBefore.Size() != infoAfter.Size() || !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Fatal("read-only inspection changed the database file", err)
	}
	dataAfter, err := os.ReadFile(fixture.databasePath)
	if err != nil || c.HashBytes(dataAfter) != c.HashBytes(dataBefore) {
		t.Fatal("read-only inspection changed database bytes", err)
	}
	directoryAfter, err := os.ReadDir(filepath.Dir(fixture.databasePath))
	if err != nil || !reflect.DeepEqual(directoryNames(directoryBefore), directoryNames(directoryAfter)) {
		t.Fatal("read-only inspection created sidecars", directoryAfter, err)
	}
	for _, name := range []string{"owner.lock", "state.sqlite-wal", "state.sqlite-shm", "state.sqlite-journal"} {
		if _, err = fixture.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("read-only inspection created an owner or SQLite sidecar", name, err)
		}
	}
}

func TestReadPhysicalQuotaBindingFromBackupAllowsMissingLegacyPin(t *testing.T) {
	fixture := newPhysicalQuotaBackupFixture(t, false)
	got, err := ReadPhysicalQuotaBindingFromBackup(context.Background(), fixture.root, fixture.rootIdentity, fixture.digest, fixture.bytes)
	if err != nil || got != nil {
		t.Fatal("missing legacy pin was not reported as absent", got, err)
	}
}

func TestReadPhysicalQuotaBindingFromBackupRejectsAmbiguousMarker(t *testing.T) {
	fixture := newPhysicalQuotaBackupFixture(t, true)
	mutatePhysicalQuotaBackup(t, fixture.databasePath, func(db *sql.DB) error {
		_, err := db.Exec("INSERT INTO meta(key,value) VALUES(?,1)", "physical_quota_v1:"+c.HashBytes([]byte("second pin")))
		return err
	})
	refreshPhysicalQuotaBackupWitness(t, &fixture)
	if _, err := ReadPhysicalQuotaBindingFromBackup(context.Background(), fixture.root, fixture.rootIdentity, fixture.digest, fixture.bytes); err == nil {
		t.Fatal("ambiguous physical quota markers accepted")
	}
}

func TestReadPhysicalQuotaBindingFromBackupRejectsNonIntegerMarkerValue(t *testing.T) {
	fixture := newPhysicalQuotaBackupFixture(t, true)
	markerValue := bytes.Repeat([]byte("m"), 1<<20)
	mutatePhysicalQuotaBackup(t, fixture.databasePath, func(db *sql.DB) error {
		_, err := db.Exec("UPDATE meta SET value=? WHERE key LIKE 'physical_quota_v1:%'", markerValue)
		return err
	})
	refreshPhysicalQuotaBackupWitness(t, &fixture)
	if _, err := ReadPhysicalQuotaBindingFromBackup(context.Background(), fixture.root, fixture.rootIdentity, fixture.digest, fixture.bytes); err == nil {
		t.Fatal("non-integer physical quota marker value accepted")
	}
}

func TestReadPhysicalQuotaBindingFromBackupRejectsOversizedCASBeforeHydration(t *testing.T) {
	fixture := newPhysicalQuotaBackupFixture(t, true)
	oversized := strings.Repeat("x", 4097)
	rewritePhysicalQuotaBackupCAS(t, fixture.databasePath, []byte(oversized))
	refreshPhysicalQuotaBackupWitness(t, &fixture)
	if _, err := ReadPhysicalQuotaBindingFromBackup(context.Background(), fixture.root, fixture.rootIdentity, fixture.digest, fixture.bytes); err == nil {
		t.Fatal("oversized physical quota CAS accepted")
	}
}

func TestReadPhysicalQuotaBindingFromBackupRejectsNoncanonicalAndTamperedCAS(t *testing.T) {
	t.Run("noncanonical", func(t *testing.T) {
		fixture := newPhysicalQuotaBackupFixture(t, true)
		raw, err := json.Marshal(fixture.binding)
		if err != nil {
			t.Fatal(err)
		}
		rewritePhysicalQuotaBackupCAS(t, fixture.databasePath, raw)
		refreshPhysicalQuotaBackupWitness(t, &fixture)
		if _, err = ReadPhysicalQuotaBindingFromBackup(context.Background(), fixture.root, fixture.rootIdentity, fixture.digest, fixture.bytes); err == nil {
			t.Fatal("noncanonical physical quota CAS accepted")
		}
	})
	t.Run("tampered", func(t *testing.T) {
		fixture := newPhysicalQuotaBackupFixture(t, true)
		mutatePhysicalQuotaBackup(t, fixture.databasePath, func(db *sql.DB) error {
			_, err := db.Exec("UPDATE payloads SET body=? WHERE task_id='' AND digest=?", []byte("tampered"), c.HashBytes(mustPhysicalQuotaBinding(t, fixture.binding)))
			return err
		})
		refreshPhysicalQuotaBackupWitness(t, &fixture)
		if _, err := ReadPhysicalQuotaBindingFromBackup(context.Background(), fixture.root, fixture.rootIdentity, fixture.digest, fixture.bytes); err == nil {
			t.Fatal("tampered physical quota CAS accepted")
		}
	})
}

func TestReadPhysicalQuotaBindingFromBackupRejectsReplacedAndLinkedInputs(t *testing.T) {
	t.Run("replaced root", func(t *testing.T) {
		fixture := newPhysicalQuotaBackupFixture(t, true)
		replacement := newPhysicalQuotaBackupFixture(t, true)
		moved := fixture.rootPath + ".moved"
		if err := fixture.root.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(fixture.rootPath, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(fixture.rootPath, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(fixture.rootPath, "database"), 0700); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(replacement.databasePath)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(fixture.rootPath, "database", "state.sqlite"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		replacementRoot, err := os.OpenRoot(fixture.rootPath)
		if err != nil {
			t.Fatal(err)
		}
		defer replacementRoot.Close()
		if _, err = ReadPhysicalQuotaBindingFromBackup(context.Background(), replacementRoot, fixture.rootIdentity, fixture.digest, fixture.bytes); err == nil {
			t.Fatal("replacement backup root accepted")
		}
	})
	t.Run("replaced database", func(t *testing.T) {
		fixture := newPhysicalQuotaBackupFixture(t, true)
		replacement := newPhysicalQuotaBackupFixture(t, true)
		if err := os.Remove(fixture.databasePath); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(replacement.databasePath)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(fixture.databasePath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = ReadPhysicalQuotaBindingFromBackup(context.Background(), fixture.root, fixture.rootIdentity, fixture.digest, fixture.bytes); err == nil {
			t.Fatal("replacement database accepted")
		}
	})
	t.Run("linked database", func(t *testing.T) {
		fixture := newPhysicalQuotaBackupFixture(t, true)
		if err := os.Link(fixture.databasePath, filepath.Join(filepath.Dir(fixture.databasePath), "linked.sqlite")); err != nil {
			t.Fatal("hardlink fixture unavailable", err)
		}
		if _, err := ReadPhysicalQuotaBindingFromBackup(context.Background(), fixture.root, fixture.rootIdentity, fixture.digest, fixture.bytes); err == nil {
			t.Fatal("hard-linked database accepted")
		}
	})
}

func mutatePhysicalQuotaBackup(t *testing.T, path string, mutate func(*sql.DB) error) {
	t.Helper()
	db, err := openMutablePhysicalQuotaBackup(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = mutate(db); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

func rewritePhysicalQuotaBackupCAS(t *testing.T, path string, raw []byte) {
	t.Helper()
	db, err := openMutablePhysicalQuotaBackup(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var oldMarker string
	if err = db.QueryRow("SELECT key FROM meta WHERE key LIKE 'physical_quota_v1:%' LIMIT 1").Scan(&oldMarker); err != nil {
		t.Fatal(err)
	}
	oldDigest := strings.TrimPrefix(oldMarker, "physical_quota_v1:")
	newDigest := c.HashBytes(raw)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE payloads SET digest=?,body=? WHERE task_id='' AND digest=?", newDigest, raw, oldDigest); err == nil {
		_, err = tx.Exec("UPDATE meta SET key=? WHERE key=?", "physical_quota_v1:"+newDigest, oldMarker)
	}
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func openMutablePhysicalQuotaBackup(path string) (*sql.DB, error) {
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func refreshPhysicalQuotaBackupWitness(t *testing.T, fixture *physicalQuotaBackupFixture) {
	t.Helper()
	info, err := os.Stat(fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.bytes = info.Size()
	fixture.digest = c.HashBytes(raw)
}

func mustPhysicalQuotaBinding(t *testing.T, binding *PhysicalQuotaBinding) []byte {
	t.Helper()
	raw, err := c.CanonicalV1(binding)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func directoryNames(entries []os.DirEntry) []string {
	result := make([]string, len(entries))
	for i, entry := range entries {
		result[i] = entry.Name()
	}
	return result
}
