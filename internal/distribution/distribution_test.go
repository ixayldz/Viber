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

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/store"
)

func fixtureArchive(t *testing.T, revision string, mutation func(map[string][]byte)) []byte {
	return fixtureArchiveCapabilities(t, revision, nil, mutation)
}

func fixtureArchiveCapabilities(t *testing.T, revision string, capabilities []string, mutation func(map[string][]byte)) []byte {
	t.Helper()
	files := map[string][]byte{binaryName(): []byte("fixture program"), "LICENSE": []byte("MIT"), "README.md": []byte("engineering"), "sbom.cdx.json": []byte(`{"bomFormat":"CycloneDX"}`)}
	m := Manifest{SchemaVersion: 1, Channel: "engineering", Revision: revision, SourceDigest: strings.Repeat("a", 64), Platform: runtime.GOOS + "/" + runtime.GOARCH, GoVersion: "go1.27.2", StoreFormatMin: 2, StoreFormatMax: 2, ReducerVersion: c.ReducerVersion, RuntimeCapabilities: append([]string{}, capabilities...)}
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

func createRuntimeCapabilityStore(t *testing.T, directory string, projectMemory, readWorkers bool) {
	t.Helper()
	ctx := context.Background()
	archive, err := artifact.Open(filepath.Join(directory, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := archive.Close(); err != nil {
			t.Error(err)
		}
	}()
	owner, err := store.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	}()
	document := []byte(`{}`)
	if projectMemory {
		document = []byte(`{"project_memory_refs":[{"id":"memory-entry"}]}`)
	}
	documentDigest, err := archive.PutBytes("task-one", document)
	if err != nil {
		t.Fatal(err)
	}
	payload := c.EventPayload{SpecVersion: 1, DocumentDigest: documentDigest, SnapshotDigest: c.HashBytes([]byte("candidate"))}
	if readWorkers {
		budget := c.TaskWorkBudget{Limits: c.BudgetAllocation{MaxSteps: 16, MaxToolCalls: 64, MaxInputTokens: 512 << 10, MaxOutputTokens: 32 << 10, MaxActiveMillis: 120000}}
		payload.WorkBudget = &budget
	}
	state, err := owner.Execute(ctx, store.Command{ID: "create-task", TaskID: "task-one", Actor: "kernel", Type: "TaskCreated", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if !readWorkers {
		return
	}
	nextDocument := []byte(`{"updated":true}`)
	nextDigest, err := archive.PutBytes("task-one", nextDocument)
	if err != nil {
		t.Fatal(err)
	}
	admission := c.ReadWorkerAdmission{
		OperationID: strings.Repeat("a", 32), RequestDigest: c.HashBytes([]byte("request")),
		SourceDocumentDigest: state.DocumentDigest, CandidateDigest: state.CandidateDigest,
		PolicyDigest: c.HashBytes([]byte("policy")), SourceTaskSeq: state.TaskSeq,
		SpecVersion: state.SpecVersion, PolicyEpoch: state.PolicyEpoch, KernelGeneration: state.KernelGeneration,
		WorkerCount: 2, ChargedSteps: 2, ChargedToolCalls: 2, ChargedActiveMillis: 250,
		Status: c.ReadWorkerAdmissionCharged,
	}
	admission.AdmissionDigest, err = admission.Digest()
	if err != nil {
		t.Fatal(err)
	}
	updated := *state.WorkBudget
	updated.Steps += 2
	updated.ToolCalls += 2
	updated.ActiveMillis += 250
	if _, err = owner.Execute(ctx, store.Command{ID: "admit-workers", TaskID: "task-one", Actor: "kernel", ExpectedTaskSeq: state.TaskSeq, Type: "ReadWorkersAdmitted", Payload: c.EventPayload{DocumentDigest: nextDigest, WorkBudget: &updated, ReadWorkerAdmission: &admission}}); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCapabilityManifestCompatibilityIsStrict(t *testing.T) {
	platform := nativePlatform(t)
	legacyRevision := strings.Repeat("4", 40)
	legacy, err := ParseArchive(fixtureArchive(t, legacyRevision, nil), "zip", legacyRevision, platform)
	if err != nil || len(legacy.Manifest.RuntimeCapabilities) != 0 {
		t.Fatalf("legacy manifest must remain readable: %v", err)
	}
	newRevision := strings.Repeat("5", 40)
	capabilities := []string{RuntimeCapabilityProjectMemoryV1, RuntimeCapabilityReadWorkersV1}
	newer, err := ParseArchive(fixtureArchiveCapabilities(t, newRevision, capabilities, nil), "zip", newRevision, platform)
	if err != nil || len(newer.Manifest.RuntimeCapabilities) != 2 {
		t.Fatalf("supported runtime capability manifest rejected: %v", err)
	}
	var preCapabilityManifest struct {
		SchemaVersion  int             `json:"schema_version"`
		Channel        string          `json:"channel"`
		ReleaseReady   bool            `json:"release_ready"`
		Revision       string          `json:"source_revision"`
		SourceDigest   string          `json:"source_archive_sha256"`
		Platform       string          `json:"platform"`
		GoVersion      string          `json:"build_go_version"`
		NativeSmoke    json.RawMessage `json:"native_smoke"`
		StoreFormatMin int             `json:"store_format_min,omitempty"`
		StoreFormatMax int             `json:"store_format_max,omitempty"`
		ReducerVersion int             `json:"reducer_version,omitempty"`
		Files          []File          `json:"files"`
	}
	if err = c.DecodeStrict(newer.Files["package.json"], &preCapabilityManifest); err == nil {
		t.Fatal("old strict package reader silently accepted a manifest with unknown runtime semantics")
	}
	for name, invalid := range map[string][]string{
		"unknown":   {RuntimeCapabilityProjectMemoryV1, "future_feature_v9"},
		"duplicate": {RuntimeCapabilityProjectMemoryV1, RuntimeCapabilityProjectMemoryV1},
		"unsorted":  {RuntimeCapabilityReadWorkersV1, RuntimeCapabilityProjectMemoryV1},
	} {
		t.Run(name, func(t *testing.T) {
			revision := strings.Repeat("6", 40)
			if _, err := ParseArchive(fixtureArchiveCapabilities(t, revision, invalid, nil), "zip", revision, platform); err == nil {
				t.Fatal("invalid runtime capability declaration accepted")
			}
		})
	}
}

func TestInstallAndRollbackRespectProjectMemoryCompatibility(t *testing.T) {
	platform := nativePlatform(t)
	parent := t.TempDir()
	manager, err := Open(filepath.Join(parent, "program"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	legacyStore := filepath.Join(parent, "legacy-store")
	createRuntimeCapabilityStore(t, legacyStore, false, false)
	if err = manager.RegisterStore(legacyStore); err != nil {
		t.Fatal(err)
	}
	legacyRevision := strings.Repeat("7", 40)
	legacyBundle, err := ParseArchive(fixtureArchive(t, legacyRevision, nil), "zip", legacyRevision, platform)
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.Install(context.Background(), legacyBundle)
	if err != nil || state.Active.Revision != legacyRevision {
		t.Fatalf("legacy-only package should remain installable when unused: %+v %v", state, err)
	}

	memoryStore := filepath.Join(parent, "memory-store")
	createRuntimeCapabilityStore(t, memoryStore, true, false)
	if err = manager.RegisterStore(memoryStore); err != nil {
		t.Fatal(err)
	}
	unsupportedRevision := strings.Repeat("8", 40)
	unsupported, err := ParseArchive(fixtureArchive(t, unsupportedRevision, nil), "zip", unsupportedRevision, platform)
	if err != nil {
		t.Fatal(err)
	}
	forged := unsupported
	forged.Manifest.RuntimeCapabilities = []string{RuntimeCapabilityProjectMemoryV1}
	if _, err = manager.Install(context.Background(), forged); err == nil {
		t.Fatal("caller-supplied capability metadata overrode the verified package manifest")
	}
	if _, err = manager.Install(context.Background(), unsupported); err == nil {
		t.Fatal("package without project-memory support activated after a registered store used project memory")
	}
	projectOnlyRevision := strings.Repeat("9", 40)
	projectOnly, err := ParseArchive(fixtureArchiveCapabilities(t, projectOnlyRevision, []string{RuntimeCapabilityProjectMemoryV1}, nil), "zip", projectOnlyRevision, platform)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Install(context.Background(), projectOnly); err != nil {
		t.Fatalf("project-memory-capable package rejected: %v", err)
	}
	status, err := manager.Status()
	if err != nil || status.Active.Revision != projectOnlyRevision {
		t.Fatalf("compatible package not selected: %+v %v", status, err)
	}
	if _, err = manager.Rollback(legacyRevision); err == nil {
		t.Fatal("rollback to a package without project-memory support was allowed")
	}
	status, err = manager.Status()
	if err != nil || status.Active.Revision != projectOnlyRevision {
		t.Fatalf("failed rollback changed selection: %+v %v", status, err)
	}
}

func TestInstallRequiresReadWorkerRuntimeForAdmittedEvents(t *testing.T) {
	platform := nativePlatform(t)
	parent := t.TempDir()
	manager, err := Open(filepath.Join(parent, "program"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	workerStore := filepath.Join(parent, "worker-store")
	createRuntimeCapabilityStore(t, workerStore, false, true)
	if err = manager.RegisterStore(workerStore); err != nil {
		t.Fatal(err)
	}
	oldRevision := strings.Repeat("a", 40)
	projectMemoryOnly, err := ParseArchive(fixtureArchiveCapabilities(t, oldRevision, []string{RuntimeCapabilityProjectMemoryV1}, nil), "zip", oldRevision, platform)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Install(context.Background(), projectMemoryOnly); err == nil {
		t.Fatal("package without read-worker support activated in a store with read-worker admissions")
	}
	newRevision := strings.Repeat("b", 40)
	allCapabilities, err := ParseArchive(fixtureArchiveCapabilities(t, newRevision, []string{RuntimeCapabilityProjectMemoryV1, RuntimeCapabilityReadWorkersV1}, nil), "zip", newRevision, platform)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Install(context.Background(), allCapabilities); err != nil {
		t.Fatalf("package declaring read-worker support rejected: %v", err)
	}
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
