package evaluation

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/resource"
)

// RegistrationMode is deliberately narrower than a product evaluation. The
// current native evaluator observes fixed local fixtures; it does not attest a
// model run, a hidden holdout, or an adoption decision.
const RegistrationMode = "FROZEN_ASSIGNMENTS; NATIVE_FIXTURE_OBSERVATION_ONLY; NO_MODEL_OR_HOLDOUT_ATTESTATION"

const (
	registrationMaxFiles = 10000
	registrationMaxFile  = int64(32 << 20)
	registrationMaxBytes = int64(256 << 20)
)

// AssetPlan is the semantic map from protocol digests to paths under the
// dataset root. File hashes and sizes are computed from the root at register
// time, rather than trusted from this input.
type AssetPlan struct {
	SchemaVersion       int            `json:"schema_version"`
	DatasetManifestPath string         `json:"dataset_manifest_path"`
	Bindings            []AssetBinding `json:"bindings"`
}

type AssetBinding struct {
	Role string `json:"role"`
	Task string `json:"task,omitempty"`
	Arm  string `json:"arm,omitempty"`
	Path string `json:"path"`
}

type AssetFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// DatasetManifest is stored inside the dataset root. DatasetDigest in the
// protocol is the exact-byte SHA-256 of this file. It lists every other file
// and directory, avoiding a self-referential digest while binding the complete
// dataset tree.
type DatasetManifest struct {
	SchemaVersion int         `json:"schema_version"`
	Files         []AssetFile `json:"files"`
	Directories   []string    `json:"directories"`
}

type AssetIndex struct {
	SchemaVersion       int            `json:"schema_version"`
	DatasetManifestPath string         `json:"dataset_manifest_path"`
	Files               []AssetFile    `json:"files"`
	Directories         []string       `json:"directories"`
	Bindings            []AssetBinding `json:"bindings"`
}

type RegisteredAssignment struct {
	ID            string `json:"assignment_id"`
	Arm           string `json:"arm"`
	Task          string `json:"task"`
	Repeat        int    `json:"repeat"`
	HarnessDigest string `json:"harness_digest"`
}

type RegistrationFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type RegistrationManifest struct {
	SchemaVersion      int                `json:"schema_version"`
	Mode               string             `json:"mode"`
	RegisteredAtUTC    string             `json:"registered_at_utc"`
	ProtocolDigest     string             `json:"protocol_digest"`
	ProtocolFileDigest string             `json:"source_protocol_file_digest"`
	AssetPlanDigest    string             `json:"source_asset_plan_file_digest"`
	DatasetDigest      string             `json:"dataset_digest"`
	AssetIndexDigest   string             `json:"asset_index_digest"`
	Files              []RegistrationFile `json:"files"`
	RegistrationDigest string             `json:"registration_digest"`
}

type Registration struct {
	Directory      string
	Protocol       Protocol
	ProtocolDigest string
	AssetIndex     AssetIndex
	Assignments    []RegisteredAssignment
	Manifest       RegistrationManifest
}

type AssignmentStart struct {
	SchemaVersion      int    `json:"schema_version"`
	RegistrationDigest string `json:"registration_digest"`
	ProtocolDigest     string `json:"protocol_digest"`
	AssignmentID       string `json:"assignment_id"`
	Arm                string `json:"arm"`
	Task               string `json:"task"`
	Repeat             int    `json:"repeat"`
	RecipeDigest       string `json:"recipe_digest"`
	RecipeAssetDigest  string `json:"recipe_asset_digest"`
	BuildDigest        string `json:"build_digest"`
	SourceDigest       string `json:"source_digest"`
	SourceTreeDigest   string `json:"source_tree_digest"`
	OwnerTask          string `json:"owner_task"`
	BundleDirectory    string `json:"bundle_directory"`
	StartedAtUTC       string `json:"started_at_utc"`
}

type AssignmentReceipt struct {
	SchemaVersion        int    `json:"schema_version"`
	RegistrationDigest   string `json:"registration_digest"`
	ProtocolDigest       string `json:"protocol_digest"`
	AssignmentID         string `json:"assignment_id"`
	StartDigest          string `json:"start_digest"`
	OwnerTask            string `json:"owner_task"`
	OwnerCreatedSequence int64  `json:"owner_created_sequence"`
	OwnerCreatedDocument string `json:"owner_created_document_digest"`
	OwnerTaskSequence    int64  `json:"owner_task_sequence"`
	OwnerDocument        string `json:"owner_document_digest"`
	ResultDigest         string `json:"native_result_digest"`
	QuotaAllocationID    string `json:"quota_allocation_id"`
	CompletedAtUTC       string `json:"completed_at_utc"`
}

type AssignmentStatus struct {
	Assignment RegisteredAssignment `json:"assignment"`
	State      string               `json:"state"`
	Start      *AssignmentStart     `json:"start,omitempty"`
	Receipt    *AssignmentReceipt   `json:"receipt,omitempty"`
}

type RegistrationStatus struct {
	Manifest    RegistrationManifest `json:"manifest"`
	Planned     int                  `json:"planned"`
	Started     int                  `json:"started"`
	Completed   int                  `json:"completed"`
	Interrupted int                  `json:"interrupted"`
	Pending     int                  `json:"pending"`
	Assignments []AssignmentStatus   `json:"assignments"`
}

func safeDatasetPath(value string) bool {
	if value == "" || len(value) > 1024 || strings.ContainsAny(value, "\\:\x00") || path.IsAbs(value) || path.Clean(value) != value || value == "." {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		switch base {
		case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
			return false
		}
	}
	return true
}

func bindingKey(binding AssetBinding) string {
	return binding.Role + "\x00" + binding.Task + "\x00" + binding.Arm
}

func expectedBindings(protocol Protocol) map[string]string {
	want := map[string]string{"evaluator\x00\x00": protocol.EvaluatorDigest}
	for _, task := range protocol.Tasks {
		for role, digest := range map[string]string{
			"repository": task.RepositoryDigest, "requirements": task.RequirementsDigest,
			"initial_snapshot": task.InitialSnapshot, "acceptance": task.AcceptanceDigest,
			"check_set": task.CheckSetDigest,
		} {
			want[role+"\x00"+task.ID+"\x00"] = digest
		}
		if task.ContinuationCheckpoint != "" {
			want["continuation_checkpoint\x00"+task.ID+"\x00"] = task.ContinuationCheckpoint
		}
	}
	for _, arm := range protocol.Arms {
		conditions := arm.Conditions
		for role, digest := range map[string]string{
			"harness": arm.HarnessDigest, "model_profile": conditions.ModelProfileDigest,
			"decoding": conditions.DecodingDigest, "tool_capabilities": conditions.ToolCapabilitiesDigest,
			"environment": conditions.EnvironmentDigest, "budget": conditions.BudgetDigest,
			"privacy": conditions.PrivacyDigest, "safety_boundary": conditions.SafetyBoundaryDigest,
			"adapter": conditions.AdapterDigest, "prompt": conditions.PromptDigest,
			"kernel": conditions.KernelDigest, "dependency": conditions.DependencyDigest,
			"pricing": conditions.PricingDigest,
		} {
			want[role+"\x00\x00"+arm.ID] = digest
		}
	}
	return want
}

func validateAssetBindings(protocol Protocol, index AssetIndex) error {
	files := map[string]AssetFile{}
	for _, file := range index.Files {
		files[file.Path] = file
	}
	want := expectedBindings(protocol)
	seen := map[string]bool{}
	used := map[string]bool{}
	last := ""
	for _, binding := range index.Bindings {
		if !safeDatasetPath(binding.Path) || len(binding.Role) > 64 || binding.Role == "" {
			return c.Fail(c.InvalidArgument, "invalid evaluation asset binding")
		}
		key := bindingKey(binding)
		if key <= last || seen[key] {
			return c.Fail(c.InvalidArgument, "evaluation asset bindings must be unique and sorted")
		}
		last = key
		seen[key] = true
		file, ok := files[binding.Path]
		if !ok {
			return c.Fail(c.PolicyDenied, "evaluation asset binding points outside the frozen dataset")
		}
		expected, ok := want[key]
		if !ok || expected != file.Digest {
			return c.Fail(c.PolicyDenied, "evaluation task/condition asset digest differs from protocol")
		}
		used[binding.Path] = true
		delete(want, key)
	}
	if len(want) != 0 {
		return c.Fail(c.PolicyDenied, "evaluation registration is missing task, evaluator, harness, or condition assets")
	}
	for _, file := range index.Files {
		if file.Path != index.DatasetManifestPath && !used[file.Path] {
			return c.Fail(c.PolicyDenied, "unbound file in frozen evaluation dataset")
		}
	}
	return nil
}

func fileManifestDigest(manifest RegistrationManifest) (string, error) {
	core := struct {
		SchemaVersion      int                `json:"schema_version"`
		Mode               string             `json:"mode"`
		RegisteredAtUTC    string             `json:"registered_at_utc"`
		ProtocolDigest     string             `json:"protocol_digest"`
		ProtocolFileDigest string             `json:"source_protocol_file_digest"`
		AssetPlanDigest    string             `json:"source_asset_plan_file_digest"`
		DatasetDigest      string             `json:"dataset_digest"`
		AssetIndex         string             `json:"asset_index_digest"`
		Files              []RegistrationFile `json:"files"`
	}{manifest.SchemaVersion, manifest.Mode, manifest.RegisteredAtUTC, manifest.ProtocolDigest, manifest.ProtocolFileDigest, manifest.AssetPlanDigest, manifest.DatasetDigest, manifest.AssetIndexDigest, manifest.Files}
	return c.Digest(core)
}

func assignmentsFor(protocol Protocol, digest string) []RegisteredAssignment {
	assignments := make([]RegisteredAssignment, 0, len(protocol.Tasks)*len(protocol.Arms)*protocol.Repeats)
	for _, arm := range protocol.Arms {
		for _, task := range protocol.Tasks {
			for repeat := 1; repeat <= protocol.Repeats; repeat++ {
				assignments = append(assignments, RegisteredAssignment{ID: AssignmentID(digest, arm.ID, task.ID, repeat), Arm: arm.ID, Task: task.ID, Repeat: repeat, HarnessDigest: arm.HarnessDigest})
			}
		}
	}
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].ID < assignments[j].ID })
	return assignments
}

func readTree(root *os.Root) (map[string][]byte, []string, error) {
	files := map[string][]byte{}
	directories := []string{}
	foldedNames := map[string]string{}
	total := int64(0)
	var visit func(string) error
	visit = func(directory string) error {
		entries, err := fs.ReadDir(root.FS(), directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			rel := entry.Name()
			if directory != "." {
				rel = filepath.Join(directory, entry.Name())
			}
			rel = filepath.ToSlash(rel)
			info, err := root.Lstat(filepath.FromSlash(rel))
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || !safeDatasetPath(rel) {
				return c.Fail(c.PolicyDenied, "dataset contains a symlink or unsafe path")
			}
			folded := strings.ToLower(rel)
			if previous, exists := foldedNames[folded]; exists && previous != rel {
				return c.Fail(c.PolicyDenied, "dataset paths collide on case-insensitive filesystems")
			}
			foldedNames[folded] = rel
			if info.IsDir() {
				if len(directories)+len(files) >= registrationMaxFiles {
					return c.Fail(c.UnsupportedCapability, "evaluation dataset file bound exceeded")
				}
				directories = append(directories, rel)
				if err = visit(filepath.FromSlash(rel)); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() {
				return c.Fail(c.PolicyDenied, "dataset contains a nonregular file")
			}
			if len(files) >= registrationMaxFiles || info.Size() < 0 || info.Size() > registrationMaxFile || total+info.Size() > registrationMaxBytes {
				return c.Fail(c.UnsupportedCapability, "evaluation dataset byte bound exceeded")
			}
			raw, readErr := fileguard.ReadRegular(root, filepath.FromSlash(rel), registrationMaxFile)
			if readErr != nil {
				return readErr
			}
			if int64(len(raw)) != info.Size() {
				return c.Fail(c.Conflict, "dataset file changed during registration")
			}
			total += int64(len(raw))
			files[rel] = raw
		}
		return nil
	}
	if err := visit("."); err != nil {
		return nil, nil, err
	}
	sort.Strings(directories)
	return files, directories, nil
}

// RegisterDataset freezes protocol, filesystem data and every planned
// assignment into a fresh private directory. A protocol whose registration
// time is already after a task's declared start is rejected as post-hoc.
func RegisterDataset(protocolFile, datasetRoot, assetPlanFile, output string) (RegistrationManifest, error) {
	return registerDataset(context.Background(), protocolFile, datasetRoot, assetPlanFile, output, nil)
}

// RegisterDatasetWithQuota reserves the complete immutable registration
// bundle against the originating owner's shared physical quota before the
// first directory or file write.
func RegisterDatasetWithQuota(ctx context.Context, protocolFile, datasetRoot, assetPlanFile, output string, ledger *resource.Ledger) (RegistrationManifest, error) {
	if ledger == nil {
		return RegistrationManifest{}, c.Fail(c.PolicyDenied, "shared physical quota ledger required for evaluation registration")
	}
	return registerDataset(ctx, protocolFile, datasetRoot, assetPlanFile, output, ledger)
}

func registerDataset(ctx context.Context, protocolFile, datasetRoot, assetPlanFile, output string, ledger *resource.Ledger) (RegistrationManifest, error) {
	return registerDatasetAt(ctx, protocolFile, datasetRoot, assetPlanFile, output, ledger, time.Now().UTC())
}

func registerDatasetAt(ctx context.Context, protocolFile, datasetRoot, assetPlanFile, output string, ledger *resource.Ledger, registeredAt time.Time) (RegistrationManifest, error) {
	var manifest RegistrationManifest
	protocolRaw, err := regularInput(protocolFile, 8<<20)
	if err != nil {
		return manifest, err
	}
	var protocol Protocol
	if err = c.DecodeStrict(protocolRaw, &protocol); err != nil {
		return manifest, err
	}
	if err = protocol.Validate(); err != nil {
		return manifest, err
	}
	protocolDigest, err := protocol.Digest()
	if err != nil {
		return manifest, err
	}
	planRaw, err := regularInput(assetPlanFile, 8<<20)
	if err != nil {
		return manifest, err
	}
	var planInput AssetPlan
	if err = c.DecodeStrict(planRaw, &planInput); err != nil {
		return manifest, err
	}
	if planInput.SchemaVersion != 1 || !safeDatasetPath(planInput.DatasetManifestPath) || len(planInput.Bindings) > 30000 {
		return manifest, c.Fail(c.InvalidArgument, "invalid evaluation asset plan")
	}
	datasetPath, err := filepath.Abs(datasetRoot)
	if err != nil {
		return manifest, err
	}
	dataset, err := os.OpenRoot(datasetPath)
	if err != nil {
		return manifest, err
	}
	defer dataset.Close()
	filesRaw, directories, err := readTree(dataset)
	if err != nil {
		return manifest, err
	}
	manifestBytes, ok := filesRaw[planInput.DatasetManifestPath]
	if !ok || c.HashBytes(manifestBytes) != protocol.DatasetDigest {
		return manifest, c.Fail(c.PolicyDenied, "protocol dataset_digest does not match the frozen dataset manifest")
	}
	var datasetManifest DatasetManifest
	if err = c.DecodeStrict(manifestBytes, &datasetManifest); err != nil {
		return manifest, err
	}
	if datasetManifest.SchemaVersion != 1 || len(datasetManifest.Files) != len(filesRaw)-1 || len(datasetManifest.Directories) != len(directories) {
		return manifest, c.Fail(c.StoreIntegrityError, "dataset manifest inventory differs from the filesystem")
	}
	actualFiles := make([]AssetFile, 0, len(filesRaw)-1)
	for name, raw := range filesRaw {
		if name == planInput.DatasetManifestPath {
			continue
		}
		actualFiles = append(actualFiles, AssetFile{Path: name, Digest: c.HashBytes(raw), Size: int64(len(raw))})
	}
	sort.Slice(actualFiles, func(i, j int) bool { return actualFiles[i].Path < actualFiles[j].Path })
	if !sameAssetFiles(datasetManifest.Files, actualFiles) || !sameStrings(datasetManifest.Directories, directories) {
		return manifest, c.Fail(c.StoreIntegrityError, "dataset manifest file or directory digest differs from filesystem")
	}
	allFiles := append([]AssetFile(nil), actualFiles...)
	allFiles = append(allFiles, AssetFile{Path: planInput.DatasetManifestPath, Digest: c.HashBytes(manifestBytes), Size: int64(len(manifestBytes))})
	sort.Slice(allFiles, func(i, j int) bool { return allFiles[i].Path < allFiles[j].Path })
	bindings := append([]AssetBinding(nil), planInput.Bindings...)
	sort.Slice(bindings, func(i, j int) bool { return bindingKey(bindings[i]) < bindingKey(bindings[j]) })
	index := AssetIndex{SchemaVersion: 1, DatasetManifestPath: planInput.DatasetManifestPath, Files: allFiles, Directories: directories, Bindings: bindings}
	if err = validateAssetBindings(protocol, index); err != nil {
		return manifest, err
	}
	assetIndexDigest, err := c.Digest(index)
	if err != nil {
		return manifest, err
	}
	canonicalProtocol, err := c.CanonicalV1(protocol)
	if err != nil {
		return manifest, err
	}
	canonicalIndex, err := c.CanonicalV1(index)
	if err != nil {
		return manifest, err
	}
	assignments := assignmentsFor(protocol, protocolDigest)
	canonicalAssignments, err := c.CanonicalV1(assignments)
	if err != nil {
		return manifest, err
	}
	manifest = RegistrationManifest{SchemaVersion: 1, Mode: RegistrationMode, RegisteredAtUTC: registeredAt.Format(time.RFC3339Nano), ProtocolDigest: protocolDigest, ProtocolFileDigest: c.HashBytes(protocolRaw), AssetPlanDigest: c.HashBytes(planRaw), DatasetDigest: protocol.DatasetDigest, AssetIndexDigest: assetIndexDigest, Files: []RegistrationFile{}}
	for _, file := range []struct {
		path string
		raw  []byte
	}{{"protocol.json", canonicalProtocol}, {"asset_index.json", canonicalIndex}, {"assignments.json", canonicalAssignments}} {
		manifest.Files = append(manifest.Files, RegistrationFile{Path: file.path, Digest: c.HashBytes(file.raw), Size: int64(len(file.raw))})
	}
	for _, file := range allFiles {
		manifest.Files = append(manifest.Files, RegistrationFile{Path: "assets/" + file.Path, Digest: file.Digest, Size: file.Size})
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	manifest.RegistrationDigest, err = fileManifestDigest(manifest)
	if err != nil {
		return RegistrationManifest{}, err
	}
	for _, task := range protocol.Tasks {
		start, parseErr := time.Parse(time.RFC3339, task.StartUTC)
		cutoff, cutoffErr := time.Parse(time.RFC3339, task.HistoryCutoffUTC)
		if parseErr != nil || cutoffErr != nil || !registeredAt.Before(start) || cutoff.After(registeredAt) {
			return RegistrationManifest{}, c.Fail(c.PolicyDenied, "registration must precede the declared task start and history cutoff must already be closed")
		}
	}
	destination, err := fileguard.ResolveProspective(output)
	if err != nil {
		return RegistrationManifest{}, err
	}
	if _, statErr := os.Lstat(destination); statErr == nil {
		return RegistrationManifest{}, c.Fail(c.Conflict, "fresh evaluation registration directory required")
	} else if !os.IsNotExist(statErr) {
		return RegistrationManifest{}, statErr
	}
	if !fileguard.Disjoint(destination, datasetPath) {
		return RegistrationManifest{}, c.Fail(c.PolicyDenied, "evaluation registration output must be outside its frozen dataset root")
	}
	var allocation resource.Allocation
	if ledger != nil {
		var logicalBytes uint64
		for _, file := range manifest.Files {
			logicalBytes += uint64(file.Size)
		}
		upperEntries := uint64(len(allFiles)*2 + len(directories) + 16)
		upperBytes := logicalBytes*2 + upperEntries*(64<<10) + (32 << 20)
		requestDigest, digestErr := c.Digest(struct {
			Registration string `json:"registration_digest"`
			Target       string `json:"target"`
		}{manifest.RegistrationDigest, destination})
		if digestErr != nil {
			return RegistrationManifest{}, digestErr
		}
		allocation, err = ledger.Reserve(ctx, resource.Request{Writer: "evaluation", Family: "EVAL_REGISTRATION_" + protocol.ID, RequestDigest: requestDigest, TargetRoot: destination, UpperBytes: upperBytes, UpperEntries: upperEntries, Class: resource.Payload})
		if err != nil {
			return RegistrationManifest{}, err
		}
		if err = allocation.Begin(ctx); err != nil {
			return RegistrationManifest{}, err
		}
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return RegistrationManifest{}, c.Fail(c.Conflict, "fresh evaluation registration directory required")
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return RegistrationManifest{}, err
	}
	defer root.Close()
	if err = fileguard.Private(root); err != nil {
		return RegistrationManifest{}, err
	}
	for _, file := range []struct {
		name string
		raw  []byte
	}{{"protocol.json", canonicalProtocol}, {"asset_index.json", canonicalIndex}, {"assignments.json", canonicalAssignments}} {
		if err = fileguard.Publish(root, file.name, file.raw); err != nil {
			return RegistrationManifest{}, err
		}
	}
	for _, file := range allFiles {
		if err = fileguard.Publish(root, filepath.ToSlash(filepath.Join("assets", filepath.FromSlash(file.Path))), filesRaw[file.Path]); err != nil {
			return RegistrationManifest{}, err
		}
	}
	manifestRaw, err := c.CanonicalV1(manifest)
	if err != nil {
		return RegistrationManifest{}, err
	}
	if err = fileguard.Publish(root, "manifest.json", manifestRaw); err != nil {
		return RegistrationManifest{}, err
	}
	if err = root.MkdirAll("attempts", 0700); err != nil {
		return RegistrationManifest{}, err
	}
	if err = fileguard.SyncParents(root, "."); err != nil {
		return RegistrationManifest{}, err
	}
	if ledger != nil {
		inventory, measureErr := resource.Measure(root, true)
		if measureErr != nil {
			return RegistrationManifest{}, measureErr
		}
		if err = allocation.Commit(ctx, inventory); err != nil {
			return RegistrationManifest{}, err
		}
	}
	return manifest, nil
}

func sameAssetFiles(a, b []AssetFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] || !safeDatasetPath(a[i].Path) || !c.ValidDigest(a[i].Digest) || a[i].Size < 0 {
			return false
		}
	}
	return true
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] || !safeDatasetPath(a[i]) {
			return false
		}
	}
	return true
}

func readRegistration(pathName string) (Registration, error) {
	var registration Registration
	directory, err := filepath.Abs(pathName)
	if err != nil {
		return registration, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return registration, err
	}
	defer root.Close()
	manifestRaw, err := fileguard.ReadRegular(root, "manifest.json", 8<<20)
	if err != nil {
		return registration, err
	}
	if err = c.DecodeStrict(manifestRaw, &registration.Manifest); err != nil {
		return registration, err
	}
	manifest := registration.Manifest
	registeredAt, registeredErr := time.Parse(time.RFC3339Nano, manifest.RegisteredAtUTC)
	if manifest.SchemaVersion != 1 || manifest.Mode != RegistrationMode || registeredErr != nil || registeredAt.Location() != time.UTC || !c.ValidDigest(manifest.ProtocolDigest) || !c.ValidDigest(manifest.ProtocolFileDigest) || !c.ValidDigest(manifest.AssetPlanDigest) || !c.ValidDigest(manifest.DatasetDigest) || !c.ValidDigest(manifest.AssetIndexDigest) || len(manifest.Files) > registrationMaxFiles+3 {
		return registration, c.Fail(c.StoreIntegrityError, "invalid evaluation registration manifest")
	}
	previous := ""
	listed := map[string]bool{}
	for _, file := range manifest.Files {
		if file.Path <= previous || file.Size < 0 || file.Size > registrationMaxFile || !c.ValidDigest(file.Digest) {
			return registration, c.Fail(c.StoreIntegrityError, "invalid evaluation registration file inventory")
		}
		previous = file.Path
		listed[file.Path] = true
		raw, readErr := fileguard.ReadRegular(root, file.Path, registrationMaxFile)
		if readErr != nil || int64(len(raw)) != file.Size || c.HashBytes(raw) != file.Digest {
			if readErr != nil {
				return registration, readErr
			}
			return registration, c.Fail(c.StoreIntegrityError, "frozen evaluation file digest changed")
		}
	}
	for _, required := range []string{"protocol.json", "asset_index.json", "assignments.json"} {
		if !listed[required] {
			return registration, c.Fail(c.StoreIntegrityError, "registration is missing a required frozen file")
		}
	}
	fileDigest, err := fileManifestDigest(manifest)
	if err != nil || fileDigest != manifest.RegistrationDigest {
		return registration, c.Fail(c.StoreIntegrityError, "evaluation registration digest changed")
	}
	readStatic := func(name string, target any) ([]byte, error) {
		raw, readErr := fileguard.ReadRegular(root, name, registrationMaxFile)
		if readErr != nil {
			return nil, readErr
		}
		if readErr = c.DecodeStrict(raw, target); readErr != nil {
			return nil, readErr
		}
		return raw, nil
	}
	protocolRaw, err := fileguard.ReadRegular(root, "protocol.json", 8<<20)
	protocolFile, protocolFileFound := registrationFile(manifest.Files, "protocol.json")
	if err != nil || !protocolFileFound || c.HashBytes(protocolRaw) != protocolFile.Digest {
		if err != nil {
			return registration, err
		}
		return registration, c.Fail(c.StoreIntegrityError, "frozen protocol file changed")
	}
	if err = c.DecodeStrict(protocolRaw, &registration.Protocol); err != nil {
		return registration, err
	}
	registration.ProtocolDigest, err = registration.Protocol.Digest()
	if err != nil || registration.ProtocolDigest != manifest.ProtocolDigest || registration.Protocol.DatasetDigest != manifest.DatasetDigest {
		return registration, c.Fail(c.StoreIntegrityError, "registration protocol digest changed")
	}
	var index AssetIndex
	if _, err = readStatic("asset_index.json", &index); err != nil {
		return registration, err
	}
	registration.AssetIndex = index
	indexDigest, err := c.Digest(index)
	if err != nil || indexDigest != manifest.AssetIndexDigest {
		return registration, c.Fail(c.StoreIntegrityError, "registration asset index changed")
	}
	if err = validateAssetBindings(registration.Protocol, index); err != nil {
		return registration, c.Fail(c.StoreIntegrityError, "registration asset bindings changed")
	}
	var assignments []RegisteredAssignment
	if _, err = readStatic("assignments.json", &assignments); err != nil {
		return registration, err
	}
	registration.Assignments = assignments
	expectedAssignments := assignmentsFor(registration.Protocol, registration.ProtocolDigest)
	if !sameAssignments(assignments, expectedAssignments) {
		return registration, c.Fail(c.StoreIntegrityError, "fixed assignment denominator changed")
	}
	for _, file := range index.Files {
		if _, err = fileguard.ReadRegular(root, "assets/"+filepath.ToSlash(filepath.FromSlash(file.Path)), registrationMaxFile); err != nil {
			return registration, err
		}
	}
	if err = validateRegistrationLayout(root, manifest, index, assignments); err != nil {
		return registration, err
	}
	registration.Directory = directory
	return registration, nil
}

func validateRegistrationLayout(root *os.Root, manifest RegistrationManifest, index AssetIndex, assignments []RegisteredAssignment) error {
	expectedFiles := map[string]bool{"manifest.json": true, "protocol.json": true, "asset_index.json": true, "assignments.json": true}
	expectedDirs := map[string]bool{"assets": true, "attempts": true}
	for _, file := range index.Files {
		expectedFiles["assets/"+file.Path] = true
	}
	for _, directory := range index.Directories {
		expectedDirs[filepath.ToSlash(filepath.Join("assets", filepath.FromSlash(directory)))] = true
	}
	allowedAttemptDirs := map[string]bool{}
	for _, assignment := range assignments {
		allowedAttemptDirs[filepath.ToSlash(filepath.Join("attempts", assignment.ID))] = true
		expectedFiles[assignmentRecordPath(assignment.ID, "started")] = true
		expectedFiles[assignmentRecordPath(assignment.ID, "receipt")] = true
	}
	manifestFiles := map[string]bool{}
	for _, file := range manifest.Files {
		if file.Path != "protocol.json" && file.Path != "asset_index.json" && file.Path != "assignments.json" && !strings.HasPrefix(file.Path, "assets/") {
			return c.Fail(c.StoreIntegrityError, "unexpected path in frozen registration inventory")
		}
		manifestFiles[file.Path] = true
	}
	if len(manifestFiles) != 3+len(index.Files) {
		return c.Fail(c.StoreIntegrityError, "frozen registration inventory does not match asset index")
	}
	for name := range expectedFiles {
		if strings.HasPrefix(name, "attempts/") {
			continue
		}
		if name == "manifest.json" {
			continue
		}
		if !manifestFiles[name] {
			return c.Fail(c.StoreIntegrityError, "registration asset is missing from manifest inventory")
		}
	}
	actualFiles := map[string]bool{}
	actualDirs := map[string]bool{}
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		name = filepath.ToSlash(name)
		info, err := root.Lstat(filepath.FromSlash(name))
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return c.Fail(c.StoreIntegrityError, "symlink in evaluation registration")
		}
		if info.IsDir() {
			actualDirs[name] = true
			if !expectedDirs[name] && !allowedAttemptDirs[name] {
				return c.Fail(c.StoreIntegrityError, "unexpected directory in evaluation registration")
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return c.Fail(c.StoreIntegrityError, "nonregular file in evaluation registration")
		}
		if strings.HasPrefix(name, "attempts/") {
			if !expectedFiles[name] {
				return c.Fail(c.StoreIntegrityError, "unknown assignment receipt in registration")
			}
		} else if !expectedFiles[name] {
			return c.Fail(c.StoreIntegrityError, "unexpected file in evaluation registration")
		}
		actualFiles[name] = true
		return nil
	})
	if err != nil {
		return err
	}
	for name := range expectedFiles {
		if !actualFiles[name] && !strings.HasPrefix(name, "attempts/") {
			return c.Fail(c.StoreIntegrityError, "required evaluation registration file is missing")
		}
	}
	for name := range expectedDirs {
		if !actualDirs[name] {
			return c.Fail(c.StoreIntegrityError, "required evaluation registration directory is missing")
		}
	}
	for attemptDir := range allowedAttemptDirs {
		if !actualDirs[attemptDir] {
			continue
		}
		id := strings.TrimPrefix(attemptDir, "attempts/")
		started := actualFiles[assignmentRecordPath(id, "started")]
		completed := actualFiles[assignmentRecordPath(id, "receipt")]
		if !started || completed && !started {
			return c.Fail(c.StoreIntegrityError, "assignment records have no immutable start receipt")
		}
	}
	return nil
}

func registrationFile(files []RegistrationFile, name string) (RegistrationFile, bool) {
	for i := range files {
		if files[i].Path == name {
			return files[i], true
		}
	}
	return RegistrationFile{}, false
}

func sameAssignments(a, b []RegisteredAssignment) bool {
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

func (r Registration) Asset(role, task, arm string) (AssetFile, error) {
	for _, binding := range r.AssetIndex.Bindings {
		if binding.Role == role && binding.Task == task && binding.Arm == arm {
			for _, file := range r.AssetIndex.Files {
				if file.Path == binding.Path {
					return file, nil
				}
			}
		}
	}
	return AssetFile{}, c.Fail(c.StoreIntegrityError, fmt.Sprintf("registered asset missing: %s", role))
}

func (r Registration) ReadAsset(role, task, arm string) ([]byte, AssetFile, error) {
	file, err := r.Asset(role, task, arm)
	if err != nil {
		return nil, AssetFile{}, err
	}
	root, err := os.OpenRoot(r.Directory)
	if err != nil {
		return nil, AssetFile{}, err
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, filepath.ToSlash(filepath.Join("assets", filepath.FromSlash(file.Path))), registrationMaxFile)
	if err != nil {
		return nil, AssetFile{}, err
	}
	if int64(len(raw)) != file.Size || c.HashBytes(raw) != file.Digest {
		return nil, AssetFile{}, c.Fail(c.StoreIntegrityError, "registered asset content changed")
	}
	return raw, file, nil
}

func LoadRegistration(pathName string) (Registration, error) { return readRegistration(pathName) }

func findAssignment(registration Registration, id string) (RegisteredAssignment, bool) {
	for _, assignment := range registration.Assignments {
		if assignment.ID == id {
			return assignment, true
		}
	}
	return RegisteredAssignment{}, false
}

func assignmentRecordPath(id, suffix string) string {
	return filepath.ToSlash(filepath.Join("attempts", id, suffix+".json"))
}

func validQuotaAllocationID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}

func readAssignmentRecord(root *os.Root, name string, target any) ([]byte, error) {
	raw, err := fileguard.ReadRegular(root, name, 1<<20)
	if err != nil {
		return nil, err
	}
	if err = c.DecodeStrict(raw, target); err != nil {
		return nil, err
	}
	return raw, nil
}

// StartAssignment publishes a single-use assignment receipt after the owner
// has durably created and bound its task, and before Run can dispatch any
// native work. A crash after this point permanently consumes that assignment.
func StartAssignment(ctx context.Context, ledger *resource.Ledger, registrationDir string, binding agent.EvaluationAssignmentBinding, assignment RegisteredAssignment, ownerTask, bundle string) (AssignmentStart, resource.Allocation, error) {
	return startAssignmentAt(ctx, ledger, registrationDir, binding, assignment, ownerTask, bundle, time.Now().UTC())
}

func startAssignmentAt(ctx context.Context, ledger *resource.Ledger, registrationDir string, binding agent.EvaluationAssignmentBinding, assignment RegisteredAssignment, ownerTask, bundle string, startedAt time.Time) (AssignmentStart, resource.Allocation, error) {
	var start AssignmentStart
	var allocation resource.Allocation
	if ledger == nil {
		return start, allocation, c.Fail(c.PolicyDenied, "shared physical quota ledger required for assignment start")
	}
	registration, err := readRegistration(registrationDir)
	if err != nil {
		return start, allocation, err
	}
	wanted, ok := findAssignment(registration, assignment.ID)
	task, taskFound := protocolTask(registration.Protocol, assignment.Task)
	if !ok || !taskFound || wanted != assignment || binding.ProtocolDigest != registration.ProtocolDigest || binding.RegistrationDigest != registration.Manifest.RegistrationDigest || binding.AssignmentID != assignment.ID || binding.Arm != assignment.Arm || binding.Task != assignment.Task || binding.Repeat != assignment.Repeat || binding.BuildDigest != assignment.HarnessDigest || binding.BuildDigest != registration.Protocol.EvaluatorDigest || binding.RecipeAssetDigest != task.AcceptanceDigest || !c.ValidDigest(binding.SourceDigest) || !c.ValidDigest(binding.SourceTreeDigest) || !c.ValidDigest(binding.RecipeDigest) || !c.ValidDigest(binding.RecipeAssetDigest) || binding.StartReceiptDigest != "" || !strings.HasPrefix(ownerTask, "eval-") || len(ownerTask) > 96 {
		return start, allocation, c.Fail(c.PolicyDenied, "registered assignment does not match its frozen owner binding")
	}
	root, err := os.OpenRoot(registration.Directory)
	if err != nil {
		return start, allocation, err
	}
	defer root.Close()
	startedAt = startedAt.UTC()
	startTime, _ := time.Parse(time.RFC3339, task.StartUTC)
	cutoff, _ := time.Parse(time.RFC3339, task.HistoryCutoffUTC)
	if startedAt.Before(startTime) || cutoff.After(startedAt) {
		return start, allocation, c.Fail(c.PolicyDenied, "registered assignment cannot start before its frozen start and history cutoff")
	}
	resolvedBundle, err := fileguard.ResolveProspective(bundle)
	if err != nil {
		return start, allocation, err
	}
	start = AssignmentStart{SchemaVersion: 1, RegistrationDigest: registration.Manifest.RegistrationDigest, ProtocolDigest: registration.ProtocolDigest, AssignmentID: assignment.ID, Arm: assignment.Arm, Task: assignment.Task, Repeat: assignment.Repeat, RecipeDigest: binding.RecipeDigest, RecipeAssetDigest: binding.RecipeAssetDigest, BuildDigest: binding.BuildDigest, SourceDigest: binding.SourceDigest, SourceTreeDigest: binding.SourceTreeDigest, OwnerTask: ownerTask, BundleDirectory: resolvedBundle, StartedAtUTC: startedAt.Format(time.RFC3339Nano)}
	raw, err := c.CanonicalV1(start)
	if err != nil {
		return AssignmentStart{}, allocation, err
	}
	attemptDir := filepath.Join(registration.Directory, "attempts", assignment.ID)
	if _, statErr := os.Lstat(attemptDir); statErr == nil {
		return AssignmentStart{}, allocation, c.Fail(c.CommandIDConflict, "registered assignment already started or unavailable")
	} else if !os.IsNotExist(statErr) {
		return AssignmentStart{}, allocation, statErr
	}
	requestDigest, err := c.Digest(struct {
		Registration string `json:"registration_digest"`
		Assignment   string `json:"assignment_id"`
		Target       string `json:"target"`
	}{registration.Manifest.RegistrationDigest, assignment.ID, attemptDir})
	if err != nil {
		return AssignmentStart{}, allocation, err
	}
	allocation, err = ledger.Reserve(ctx, resource.Request{Writer: "evaluation", Family: "EVAL_ASSIGNMENT_" + registration.Protocol.ID, RequestDigest: requestDigest, TargetRoot: attemptDir, UpperBytes: 2 << 20, UpperEntries: 8, Class: resource.Payload})
	if err != nil {
		return AssignmentStart{}, allocation, err
	}
	if err = allocation.Begin(ctx); err != nil {
		return AssignmentStart{}, allocation, err
	}
	if err = os.Mkdir(attemptDir, 0700); err != nil {
		return AssignmentStart{}, allocation, c.Fail(c.CommandIDConflict, "registered assignment already started or unavailable")
	}
	attemptRoot, err := os.OpenRoot(attemptDir)
	if err != nil {
		return AssignmentStart{}, allocation, err
	}
	defer attemptRoot.Close()
	if err = fileguard.Private(attemptRoot); err != nil {
		return AssignmentStart{}, allocation, err
	}
	if err = fileguard.Publish(attemptRoot, "started.json", raw); err != nil {
		return AssignmentStart{}, allocation, c.Fail(c.CommandIDConflict, "registered assignment already started or unavailable")
	}
	return start, allocation, nil
}

func AssignmentStartDigest(start AssignmentStart) (string, error) {
	raw, err := c.CanonicalV1(start)
	if err != nil {
		return "", err
	}
	return c.HashBytes(raw), nil
}

func protocolTask(protocol Protocol, id string) (Task, bool) {
	for _, task := range protocol.Tasks {
		if task.ID == id {
			return task, true
		}
	}
	return Task{}, false
}

func ValidateAssignmentStart(registration Registration, start AssignmentStart) error {
	assignment, ok := findAssignment(registration, start.AssignmentID)
	task, taskFound := protocolTask(registration.Protocol, start.Task)
	started, parseErr := time.Parse(time.RFC3339Nano, start.StartedAtUTC)
	if !ok || !taskFound || start.SchemaVersion != 1 || start.RegistrationDigest != registration.Manifest.RegistrationDigest || start.ProtocolDigest != registration.ProtocolDigest || assignment.ID != start.AssignmentID || assignment.Arm != start.Arm || assignment.Task != start.Task || assignment.Repeat != start.Repeat || assignment.HarnessDigest != start.BuildDigest || registration.Protocol.EvaluatorDigest != start.BuildDigest || !c.ValidDigest(start.RecipeDigest) || start.RecipeAssetDigest != task.AcceptanceDigest || !c.ValidDigest(start.RecipeAssetDigest) || !c.ValidDigest(start.SourceDigest) || !c.ValidDigest(start.SourceTreeDigest) || !c.ValidDigest(start.BuildDigest) || !strings.HasPrefix(start.OwnerTask, "eval-") || len(start.OwnerTask) > 96 || !filepath.IsAbs(start.BundleDirectory) || filepath.Clean(start.BundleDirectory) != start.BundleDirectory || parseErr != nil || started.Location() != time.UTC || started.Before(mustUTC(task.StartUTC)) || started.Before(mustUTC(task.HistoryCutoffUTC)) {
		return c.Fail(c.StoreIntegrityError, "registered assignment start receipt binding changed")
	}
	return nil
}

func mustUTC(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, value)
	return parsed
}

// CompleteAssignment writes the bundle receipt first, then the registration
// ledger receipt. If the process dies between them, Inspect reports the
// assignment as interrupted instead of counting a result that is not durable.
func CompleteAssignment(ctx context.Context, allocation resource.Allocation, registrationDir string, root *os.Root, start AssignmentStart, createdState, finalState c.TaskState, resultDigest string) (AssignmentReceipt, error) {
	var receipt AssignmentReceipt
	if allocation.ID == "" {
		return receipt, c.Fail(c.PolicyDenied, "started physical assignment allocation required")
	}
	registration, err := readRegistration(registrationDir)
	if err != nil {
		return receipt, err
	}
	if err = ValidateAssignmentStart(registration, start); err != nil {
		return receipt, err
	}
	if root == nil {
		return receipt, c.Fail(c.PolicyDenied, "complete durable owner and result are required for assignment receipt")
	}
	rootPath, pathErr := filepath.Abs(root.Name())
	if pathErr == nil {
		rootPath, pathErr = filepath.EvalSymlinks(rootPath)
	}
	if pathErr != nil || rootPath != start.BundleDirectory || createdState.TaskSeq < 1 || !c.ValidDigest(createdState.DocumentDigest) || finalState.TaskSeq <= createdState.TaskSeq || !c.ValidDigest(finalState.DocumentDigest) || !c.ValidDigest(resultDigest) {
		return receipt, c.Fail(c.PolicyDenied, "complete durable owner and result are required for assignment receipt")
	}
	regRoot, err := os.OpenRoot(registration.Directory)
	if err != nil {
		return receipt, err
	}
	defer regRoot.Close()
	startRaw, err := fileguard.ReadRegular(regRoot, assignmentRecordPath(start.AssignmentID, "started"), 1<<20)
	if err != nil {
		return receipt, err
	}
	var persisted AssignmentStart
	if err = c.DecodeStrict(startRaw, &persisted); err != nil || persisted != start {
		return receipt, c.Fail(c.StoreIntegrityError, "assignment start receipt was replaced")
	}
	receipt = AssignmentReceipt{SchemaVersion: 1, RegistrationDigest: registration.Manifest.RegistrationDigest, ProtocolDigest: registration.ProtocolDigest, AssignmentID: start.AssignmentID, StartDigest: c.HashBytes(startRaw), OwnerTask: start.OwnerTask, OwnerCreatedSequence: createdState.TaskSeq, OwnerCreatedDocument: createdState.DocumentDigest, OwnerTaskSequence: finalState.TaskSeq, OwnerDocument: finalState.DocumentDigest, ResultDigest: resultDigest, QuotaAllocationID: allocation.ID, CompletedAtUTC: time.Now().UTC().Format(time.RFC3339Nano)}
	receiptRaw, err := c.CanonicalV1(receipt)
	if err != nil {
		return AssignmentReceipt{}, err
	}
	if err = fileguard.Publish(root, "assignment_receipt.json", receiptRaw); err != nil {
		return AssignmentReceipt{}, err
	}
	attemptDir := filepath.Join(registration.Directory, "attempts", start.AssignmentID)
	if !allocation.Covers(attemptDir) {
		return AssignmentReceipt{}, c.Fail(c.PolicyDenied, "assignment quota reservation does not cover its receipt directory")
	}
	attemptRoot, err := os.OpenRoot(attemptDir)
	if err != nil {
		return AssignmentReceipt{}, err
	}
	defer attemptRoot.Close()
	if err = fileguard.Publish(attemptRoot, "receipt.json", receiptRaw); err != nil {
		return AssignmentReceipt{}, c.Fail(c.Conflict, "registered assignment receipt already exists")
	}
	inventory, err := resource.Measure(attemptRoot, true)
	if err != nil {
		return AssignmentReceipt{}, err
	}
	if err = allocation.Commit(ctx, inventory); err != nil {
		return AssignmentReceipt{}, err
	}
	return receipt, nil
}

func InspectRegistration(pathName string) (RegistrationStatus, error) {
	var status RegistrationStatus
	registration, err := readRegistration(pathName)
	if err != nil {
		return status, err
	}
	status.Manifest = registration.Manifest
	status.Assignments = make([]AssignmentStatus, 0, len(registration.Assignments))
	status.Planned = len(registration.Assignments)
	root, err := os.OpenRoot(registration.Directory)
	if err != nil {
		return status, err
	}
	defer root.Close()
	for _, assignment := range registration.Assignments {
		entry := AssignmentStatus{Assignment: assignment, State: "PENDING"}
		startName := assignmentRecordPath(assignment.ID, "started")
		receiptName := assignmentRecordPath(assignment.ID, "receipt")
		startRaw, startErr := fileguard.ReadRegular(root, startName, 1<<20)
		if startErr == nil {
			var start AssignmentStart
			if err = c.DecodeStrict(startRaw, &start); err != nil {
				return status, err
			}
			if err = ValidateAssignmentStart(registration, start); err != nil {
				return status, err
			}
			entry.Start = &start
			entry.State = "INTERRUPTED"
			status.Started++
		} else if !os.IsNotExist(startErr) {
			return status, startErr
		}
		receiptRaw, receiptErr := fileguard.ReadRegular(root, receiptName, 1<<20)
		if receiptErr == nil {
			if entry.Start == nil {
				return status, c.Fail(c.StoreIntegrityError, "assignment completion has no started receipt")
			}
			var receipt AssignmentReceipt
			if err = c.DecodeStrict(receiptRaw, &receipt); err != nil {
				return status, err
			}
			if receipt.SchemaVersion != 1 || receipt.RegistrationDigest != registration.Manifest.RegistrationDigest || receipt.ProtocolDigest != registration.ProtocolDigest || receipt.AssignmentID != assignment.ID || receipt.StartDigest != c.HashBytes(startRaw) || receipt.OwnerTask != entry.Start.OwnerTask || receipt.OwnerCreatedSequence < 1 || receipt.OwnerTaskSequence <= receipt.OwnerCreatedSequence || !c.ValidDigest(receipt.OwnerCreatedDocument) || !c.ValidDigest(receipt.OwnerDocument) || !c.ValidDigest(receipt.ResultDigest) || !validQuotaAllocationID(receipt.QuotaAllocationID) {
				return status, c.Fail(c.StoreIntegrityError, "assignment completion receipt binding changed")
			}
			bundleRoot, openErr := os.OpenRoot(entry.Start.BundleDirectory)
			if openErr != nil {
				return status, openErr
			}
			bundleReceipt, readErr := fileguard.ReadRegular(bundleRoot, "assignment_receipt.json", 1<<20)
			bundleRoot.Close()
			if readErr != nil || c.HashBytes(bundleReceipt) != c.HashBytes(receiptRaw) {
				if readErr != nil {
					return status, readErr
				}
				return status, c.Fail(c.StoreIntegrityError, "bundle and registration assignment receipts differ")
			}
			entry.Receipt = &receipt
			entry.State = "COMPLETED"
			status.Completed++
		} else if !os.IsNotExist(receiptErr) {
			return status, receiptErr
		}
		status.Assignments = append(status.Assignments, entry)
	}
	status.Pending = status.Planned - status.Started
	status.Interrupted = status.Started - status.Completed
	return status, nil
}

// CurrentExecutableDigest pins the exact application image used by a
// registered native fixture run. It makes no signature or release claim.
func CurrentExecutableDigest() (string, error) {
	name, err := os.Executable()
	if err != nil {
		return "", err
	}
	name, err = filepath.EvalSymlinks(name)
	if err != nil {
		return "", err
	}
	raw, err := regularInput(name, 256<<20)
	if err != nil {
		return "", err
	}
	return c.HashBytes(raw), nil
}

func RegisteredRecipe(registration Registration, task string) (NativeRecipe, string, AssetFile, error) {
	var recipe NativeRecipe
	raw, file, err := registration.ReadAsset("acceptance", task, "")
	if err != nil {
		return recipe, "", AssetFile{}, err
	}
	if c.HashBytes(raw) != file.Digest {
		return recipe, "", AssetFile{}, c.Fail(c.StoreIntegrityError, "registered acceptance asset digest changed")
	}
	if err = c.DecodeStrict(raw, &recipe); err != nil {
		return recipe, "", AssetFile{}, err
	}
	if err = recipe.Validate(); err != nil {
		return recipe, "", AssetFile{}, err
	}
	digest, err := c.Digest(recipe)
	return recipe, digest, file, err
}

func AssignmentFor(registration Registration, arm, task string, repeat int) (RegisteredAssignment, error) {
	for _, assignment := range registration.Assignments {
		if assignment.Arm == arm && assignment.Task == task && assignment.Repeat == repeat {
			return assignment, nil
		}
	}
	return RegisteredAssignment{}, c.Fail(c.PolicyDenied, "requested arm/task/repeat is not a preregistered assignment")
}

func NativeAssignmentBinding(registration Registration, assignment RegisteredAssignment, source agent.EvaluationSource, recipeDigest, recipeAssetDigest, buildDigest string) (agent.EvaluationAssignmentBinding, error) {
	var binding agent.EvaluationAssignmentBinding
	if _, ok := findAssignment(registration, assignment.ID); !ok {
		return binding, c.Fail(c.PolicyDenied, "assignment does not belong to this registration")
	}
	protocolTaskValue, ok := protocolTask(registration.Protocol, assignment.Task)
	if !ok || source.TaskID != assignment.Task || source.InitialSnapshot != protocolTaskValue.InitialSnapshot || source.SpecDigest != protocolTaskValue.RequirementsDigest || source.CheckSetDigest != protocolTaskValue.CheckSetDigest || protocolTaskValue.AcceptanceDigest != recipeAssetDigest {
		return binding, c.Fail(c.PolicyDenied, "frozen source or recipe differs from preregistered task assets")
	}
	sourceDigest, err := c.Digest(source)
	if err != nil {
		return binding, err
	}
	binding = agent.EvaluationAssignmentBinding{ProtocolDigest: registration.ProtocolDigest, RegistrationDigest: registration.Manifest.RegistrationDigest, AssignmentID: assignment.ID, Arm: assignment.Arm, Task: assignment.Task, Repeat: assignment.Repeat, SourceDigest: sourceDigest, SourceTreeDigest: source.ContentDigest, RecipeDigest: recipeDigest, RecipeAssetDigest: recipeAssetDigest, BuildDigest: buildDigest}
	if !c.ValidDigest(binding.ProtocolDigest) || !c.ValidDigest(binding.RegistrationDigest) || !c.ValidDigest(binding.AssignmentID) || !c.ValidDigest(binding.SourceDigest) || !c.ValidDigest(binding.SourceTreeDigest) || !c.ValidDigest(binding.RecipeDigest) || !c.ValidDigest(binding.RecipeAssetDigest) || !c.ValidDigest(binding.BuildDigest) {
		return agent.EvaluationAssignmentBinding{}, c.Fail(c.InvalidArgument, "invalid preregistered assignment inputs")
	}
	return binding, nil
}
