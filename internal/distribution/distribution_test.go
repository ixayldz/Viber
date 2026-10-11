package distribution

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func fixtureArchive(t *testing.T, revision string, mutation func(map[string][]byte)) []byte {
	t.Helper()
	files := map[string][]byte{binaryName(): []byte("fixture program"), "LICENSE": []byte("MIT"), "README.md": []byte("engineering"), "sbom.cdx.json": []byte(`{"bomFormat":"CycloneDX"}`)}
	m := Manifest{SchemaVersion: 1, Channel: "engineering", Revision: revision, SourceDigest: strings.Repeat("a", 64), Platform: runtime.GOOS + "/" + runtime.GOARCH, GoVersion: "go1.27.2", StoreFormatMin: 2, StoreFormatMax: 2, ReducerVersion: c.ReducerVersion}
	names := []string{}
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		raw := files[name]
		m.Files = append(m.Files, File{Path: name, Bytes: int64(len(raw)), Digest: c.HashBytes(raw)})
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	files["package.json"] = raw
	if mutation != nil {
		mutation(files)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, raw := range files {
		header := &zip.FileHeader{Name: "viber-" + revision[:12] + "-" + strings.ReplaceAll(m.Platform, "/", "-") + "/" + name, Method: zip.Deflate}
		header.SetMode(0600)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
func nativePlatform(t *testing.T) string {
	t.Helper()
	platform := runtime.GOOS + "/" + runtime.GOARCH
	if platform != "windows/amd64" && platform != "linux/amd64" && platform != "darwin/arm64" {
		t.Skip("outside measured portable targets")
	}
	return platform
}

func TestPortableArchiveRejectsUninventoriedAliasedAndChangedBytes(t *testing.T) {
	platform := nativePlatform(t)
	revision := strings.Repeat("1", 40)
	for name, mutation := range map[string]func(map[string][]byte){"traversal": func(m map[string][]byte) { m["../escape"] = []byte("bad") }, "alias": func(m map[string][]byte) { m["readme.md"] = m["README.md"] }, "extra": func(m map[string][]byte) { m["surprise.dll"] = []byte("bad") }, "tamper": func(m map[string][]byte) { m[binaryName()] = []byte("changed") }} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseArchive(fixtureArchive(t, revision, mutation), "zip", revision, platform); err == nil {
				t.Fatal("hostile archive admitted")
			}
		})
	}
	if _, err := ParseArchive(fixtureArchive(t, revision, nil), "zip", revision, platform); err != nil {
		t.Fatal(err)
	}
}

func TestProgramSelectionInstallRollbackAndDeactivatePreserveExternalData(t *testing.T) {
	platform := nativePlatform(t)
	parent := t.TempDir()
	store := filepath.Join(parent, "task-store")
	if err := os.Mkdir(store, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(store, "user-data")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(parent, "program")
	manager, err := Open(prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	revisions := []string{strings.Repeat("1", 40), strings.Repeat("2", 40)}
	for _, revision := range revisions {
		bundle, err := ParseArchive(fixtureArchive(t, revision, nil), "zip", revision, platform)
		if err != nil {
			t.Fatal(err)
		}
		state, err := manager.Install(context.Background(), bundle)
		if err != nil {
			t.Fatal(err)
		}
		if state.Active.Revision != revision {
			t.Fatal("active version not selected")
		}
	}
	state, err := manager.Rollback(revisions[0])
	if err != nil || state.Active.Revision != revisions[0] {
		t.Fatalf("rollback: %v %+v", err, state)
	}
	state, err = manager.Deactivate()
	if err != nil || state.Binary != "" || state.Active.Action != "DEACTIVATE" {
		t.Fatalf("deactivate: %v %+v", err, state)
	}
	raw, err := os.ReadFile(sentinel)
	if err != nil || string(raw) != "preserve" {
		t.Fatal("external task data changed")
	}
	if _, err = manager.Rollback(strings.Repeat("3", 40)); err == nil {
		t.Fatal("uninstalled target selected")
	}
	state, err = manager.Uninstall()
	if err != nil || len(state.Installed) != 0 || state.Active.Action != "UNINSTALL" {
		t.Fatalf("uninstall: %v %+v", err, state)
	}
	state, err = manager.Uninstall()
	if err != nil || len(state.Installed) != 0 {
		t.Fatalf("idempotent uninstall: %v", err)
	}
	if raw, err = os.ReadFile(sentinel); err != nil || string(raw) != "preserve" {
		t.Fatal("uninstall changed task data")
	}
}

func TestProgramSelectionRejectsChangedBinaryAndIncompatibleRollback(t *testing.T) {
	platform := nativePlatform(t)
	revision := strings.Repeat("1", 40)
	bundle, err := ParseArchive(fixtureArchive(t, revision, nil), "zip", revision, platform)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := Open(filepath.Join(t.TempDir(), "program"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	legacy := bundle
	legacy.Manifest.StoreFormatMin = 0
	legacy.Manifest.StoreFormatMax = 0
	if _, err = manager.Install(context.Background(), legacy); err == nil {
		t.Fatal("missing format contract admitted")
	}
	if _, err = manager.Install(context.Background(), bundle); err != nil {
		t.Fatal(err)
	}
	if err = manager.root.WriteFile(filepath.Join("versions", revision, binaryName()), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Status(); err == nil {
		t.Fatal("changed binary admitted")
	}
}

func TestInterruptedProgramStageResumesOnlyExactMissingFiles(t *testing.T) {
	platform := nativePlatform(t)
	revision := strings.Repeat("1", 40)
	bundle, err := ParseArchive(fixtureArchive(t, revision, nil), "zip", revision, platform)
	if err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(t.TempDir(), "program")
	m, err := Open(prefix)
	if err != nil {
		t.Fatal(err)
	}
	version := filepath.Join("versions", revision)
	if err = m.root.Mkdir(version, 0700); err != nil {
		t.Fatal(err)
	}
	if err = m.root.WriteFile(filepath.Join(version, "LICENSE"), bundle.Files["LICENSE"], 0600); err != nil {
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
	state, err := m.Install(context.Background(), bundle)
	if err != nil || state.Active.Revision != revision {
		t.Fatalf("exact stage recovery: %+v %v", state, err)
	}
	if second, err := Open(prefix); err == nil {
		second.Close()
		t.Fatal("concurrent program updater admitted")
	}
}
