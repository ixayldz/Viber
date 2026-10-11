package intelligence

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/discovery"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/verify"
	"github.com/ixayldz/Viber/internal/workspace"
)

const (
	GenerationVersion      = "DETERMINISTIC_BLUEPRINT_PUBLISHER_V1"
	GenerationManifestName = "VIBER_GENERATION.json"
	GenerationFileLimit    = 512
	GenerationFileBytes    = int64(2 << 20)
	GenerationTotalBytes   = int64(32 << 20)
)

// GenerationFile is exact caller-supplied output. The publisher treats bytes
// as data: it never runs compilers, generators, hooks, or repository commands.
type GenerationFile struct {
	Path string `json:"path"`
	Data []byte `json:"data_base64"`
	Mode uint32 `json:"mode"`
}

type GeneratedFileRecord struct {
	Path string `json:"path"`
	Hash string `json:"sha256"`
	Size int64  `json:"size"`
	Mode uint32 `json:"mode"`
}

type SourceAuthorityHashes struct {
	SnapshotDigest string              `json:"snapshot_digest"`
	GitStateDigest string              `json:"git_state_digest,omitempty"`
	HeadObject     string              `json:"head_object,omitempty"`
	IndexDigest    string              `json:"index_digest,omitempty"`
	ObjectFormat   string              `json:"object_format,omitempty"`
	Evidence       []BlueprintEvidence `json:"source_evidence"`
}

type GenerationManifest struct {
	SchemaVersion             int                   `json:"schema_version"`
	GeneratorVersion          string                `json:"generator_version"`
	BlueprintDigest           string                `json:"blueprint_digest"`
	BlueprintReviewBinding    string                `json:"blueprint_review_binding"`
	SourceAuthority           SourceAuthorityHashes `json:"source_authority_hashes"`
	SourceAuthorityDigest     string                `json:"source_authority_digest"`
	Files                     []GeneratedFileRecord `json:"files"`
	FileSetDigest             string                `json:"file_set_digest"`
	RequiredIndependentChecks []string              `json:"required_independent_checks"`
	CheckSetDigest            string                `json:"check_set_digest"`
	Execution                 string                `json:"execution"`
	Network                   string                `json:"network"`
	Hooks                     string                `json:"hooks"`
	AcceptanceState           string                `json:"acceptance_state"`
	ValidationState           string                `json:"validation_state"`
	ManifestDigest            string                `json:"manifest_digest"`
}

// GenerationPlan is an immutable-by-convention in-memory plan. Data is
// excluded from JSON; FileRecords bind every byte sequence by hash and length.
type GenerationPlan struct {
	SchemaVersion          int                   `json:"schema_version"`
	GeneratorVersion       string                `json:"generator_version"`
	BlueprintDigest        string                `json:"blueprint_digest"`
	BlueprintReviewBinding string                `json:"blueprint_review_binding"`
	SourceAuthority        SourceAuthorityHashes `json:"source_authority_hashes"`
	SourceAuthorityDigest  string                `json:"source_authority_digest"`
	Files                  []GenerationFile      `json:"-"`
	FileRecords            []GeneratedFileRecord `json:"files"`
	FileSetDigest          string                `json:"file_set_digest"`
	RequiredChecks         []string              `json:"required_independent_checks"`
	CheckSetDigest         string                `json:"check_set_digest"`
	PlanDigest             string                `json:"plan_digest"`
}

type GenerationReceipt struct {
	SchemaVersion           int                `json:"schema_version"`
	OutputDirectory         string             `json:"output_directory"`
	PublishedSnapshotDigest string             `json:"published_snapshot_digest"`
	Manifest                GenerationManifest `json:"manifest"`
	PublicationState        string             `json:"publication_state"`
}

type GenerationValidationBinding struct {
	SchemaVersion            int                `json:"schema_version"`
	BlueprintDigest          string             `json:"blueprint_digest"`
	GenerationManifestDigest string             `json:"generation_manifest_digest"`
	CandidateSnapshotDigest  string             `json:"candidate_snapshot_digest"`
	CheckSetDigest           string             `json:"check_set_digest"`
	CheckOriginDigest        string             `json:"protected_check_origin_digest"`
	DiscoveryDigest          string             `json:"source_discovery_digest,omitempty"`
	DiscoveryCoverage        string             `json:"source_discovery_coverage,omitempty"`
	RequiredCheckIDs         []string           `json:"required_check_ids"`
	CheckOrigin              verify.CheckOrigin `json:"protected_check_origin"`
	State                    string             `json:"state"`
	BindingDigest            string             `json:"binding_digest"`
}

// BuildGenerationPlan binds exact proposed output bytes to a valid blueprint
// and source capture. selectedBlueprintDigest is a caller-selected review
// guard; matching it is not a human attestation or an acceptance decision.
func BuildGenerationPlan(source workspace.Capture, blueprint BlueprintManifest, selectedBlueprintDigest string, files []GenerationFile) (GenerationPlan, error) {
	plan := GenerationPlan{SchemaVersion: c.SchemaVersion, GeneratorVersion: GenerationVersion,
		BlueprintDigest: blueprint.ManifestDigest, BlueprintReviewBinding: "SELECTED_BLUEPRINT_DIGEST_MATCH_NOT_HUMAN_ATTESTATION",
		Files: []GenerationFile{}, FileRecords: []GeneratedFileRecord{}, RequiredChecks: append([]string(nil), blueprint.RequiredIndependentChecks...)}
	if err := ValidateBlueprint(blueprint, source); err != nil {
		return plan, err
	}
	if selectedBlueprintDigest != blueprint.ManifestDigest || !c.ValidDigest(selectedBlueprintDigest) {
		return plan, c.Fail(c.Conflict, "selected blueprint digest does not match current source-bound blueprint")
	}
	if len(files) == 0 || len(files) > GenerationFileLimit || len(files) != len(blueprint.GeneratedPaths) {
		return plan, c.Fail(c.InvalidArgument, "generated files must exactly cover the bounded blueprint closure")
	}
	var err error
	plan.SourceAuthority, plan.SourceAuthorityDigest, err = sourceAuthority(source, blueprint)
	if err != nil {
		return plan, err
	}
	if !c.ValidDigest(plan.SourceAuthorityDigest) {
		return plan, c.Fail(c.StoreIntegrityError, "source authority digest could not be built")
	}
	copyFiles := append([]GenerationFile(nil), files...)
	sort.Slice(copyFiles, func(i, j int) bool { return copyFiles[i].Path < copyFiles[j].Path })
	plannedPaths := append([]string(nil), blueprint.GeneratedPaths...)
	sort.Strings(plannedPaths)
	var total int64
	for i, file := range copyFiles {
		if !policy.SafePath(file.Path) || len(file.Path) > 1024 || strings.EqualFold(file.Path, GenerationManifestName) ||
			(file.Mode != 0644 && file.Mode != 0755) || len(file.Data) == 0 || int64(len(file.Data)) > GenerationFileBytes {
			return plan, c.Fail(c.InvalidArgument, "generated file path, mode, or bounded bytes are invalid")
		}
		if i > 0 && strings.EqualFold(copyFiles[i-1].Path, file.Path) {
			return plan, c.Fail(c.InvalidArgument, "generated file paths collide case-insensitively")
		}
		if plannedPaths[i] != file.Path {
			return plan, c.Fail(c.Conflict, "generated files do not match the exact planned path closure")
		}
		total += int64(len(file.Data))
		if total > GenerationTotalBytes {
			return plan, c.Fail(c.InvalidArgument, "generated file byte budget exceeded")
		}
		plan.Files = append(plan.Files, GenerationFile{Path: file.Path, Data: append([]byte(nil), file.Data...), Mode: file.Mode})
		plan.FileRecords = append(plan.FileRecords, GeneratedFileRecord{Path: file.Path, Hash: c.HashBytes(file.Data), Size: int64(len(file.Data)), Mode: file.Mode})
	}
	if err := checkPathAliases(plan.FileRecords); err != nil {
		return plan, err
	}
	plan.FileSetDigest, err = c.Digest(plan.FileRecords)
	if err != nil {
		return plan, err
	}
	plan.CheckSetDigest = blueprint.CheckSetDigest
	plan.PlanDigest, err = generationPlanDigest(plan)
	if err != nil {
		return plan, err
	}
	return plan, nil
}

func ValidateGenerationPlan(source workspace.Capture, blueprint BlueprintManifest, plan GenerationPlan) error {
	if err := ValidateBlueprint(blueprint, source); err != nil {
		return err
	}
	if plan.SchemaVersion != c.SchemaVersion || plan.GeneratorVersion != GenerationVersion ||
		plan.BlueprintDigest != blueprint.ManifestDigest || plan.BlueprintReviewBinding != "SELECTED_BLUEPRINT_DIGEST_MATCH_NOT_HUMAN_ATTESTATION" ||
		plan.SourceAuthorityDigest == "" || plan.CheckSetDigest != blueprint.CheckSetDigest ||
		!equalStrings(plan.RequiredChecks, blueprint.RequiredIndependentChecks) {
		return c.Fail(c.StoreIntegrityError, "generation plan is stale or malformed")
	}
	expected, err := BuildGenerationPlan(source, blueprint, blueprint.ManifestDigest, plan.Files)
	if err != nil {
		return err
	}
	actualPlanDigest, err := generationPlanDigest(plan)
	if err != nil || actualPlanDigest != plan.PlanDigest {
		return c.Fail(c.StoreIntegrityError, "generation plan digest mismatch")
	}
	if expected.FileSetDigest != plan.FileSetDigest || expected.PlanDigest != plan.PlanDigest ||
		expected.SourceAuthorityDigest != plan.SourceAuthorityDigest || !equalSourceAuthority(expected.SourceAuthority, plan.SourceAuthority) ||
		!equalFileRecords(expected.FileRecords, plan.FileRecords) {
		return c.Fail(c.StoreIntegrityError, "generation plan bytes, authority, or digest changed")
	}
	return nil
}

// PublishGeneration writes an exact-byte repository into a fresh sibling
// staging directory, verifies its bounded capture, and atomically publishes it
// without replacing any existing output. No repository code is executed.
func PublishGeneration(source workspace.Capture, blueprint BlueprintManifest, plan GenerationPlan, outputDirectory string) (GenerationReceipt, error) {
	receipt := GenerationReceipt{SchemaVersion: c.SchemaVersion, PublicationState: "NOT_PUBLISHED"}
	if err := ValidateGenerationPlan(source, blueprint, plan); err != nil {
		return receipt, err
	}
	plan = cloneGenerationPlan(plan)
	if err := ValidateGenerationPlan(source, blueprint, plan); err != nil {
		return receipt, err
	}
	fresh, err := captureCurrentSource(source)
	if err != nil {
		return receipt, err
	}
	if fresh.Snapshot.Digest != source.Snapshot.Digest {
		return receipt, c.Fail(c.Conflict, "source changed after blueprint capture")
	}
	destination, err := fileguard.ResolveProspective(outputDirectory)
	if err != nil {
		return receipt, err
	}
	if !fileguard.Disjoint(source.Snapshot.Root, destination) {
		return receipt, c.Fail(c.PolicyDenied, "generated output must be outside the captured source tree")
	}
	parentPath, finalName := filepath.Dir(destination), filepath.Base(destination)
	if destination == parentPath || finalName == "." || finalName == ".." || finalName == string(os.PathSeparator) {
		return receipt, c.Fail(c.InvalidArgument, "fresh output directory path required")
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return receipt, c.Fail(c.UnsupportedCapability, "output parent must already exist")
	}
	defer parent.Close()
	stageName, err := createGenerationStage(parent)
	if err != nil {
		return receipt, err
	}
	published := false
	defer func() {
		if !published {
			_ = parent.RemoveAll(stageName)
		}
	}()
	stage, err := parent.OpenRoot(stageName)
	if err != nil {
		return receipt, err
	}
	if err = fileguard.Private(stage); err != nil {
		stage.Close()
		return receipt, err
	}
	manifest, err := generationManifest(source, blueprint, plan)
	if err != nil {
		stage.Close()
		return receipt, err
	}
	for _, file := range plan.Files {
		if err = fileguard.Publish(stage, filepath.FromSlash(file.Path), file.Data); err != nil {
			stage.Close()
			return receipt, err
		}
		if err = stage.Chmod(filepath.FromSlash(file.Path), os.FileMode(file.Mode)); err != nil {
			stage.Close()
			return receipt, err
		}
	}
	manifestBytes, err := c.CanonicalV1(manifest)
	if err != nil || len(manifestBytes) > 1<<20 {
		stage.Close()
		if err == nil {
			err = c.Fail(c.InvalidArgument, "generation manifest exceeds size limit")
		}
		return receipt, err
	}
	if err = fileguard.Publish(stage, GenerationManifestName, manifestBytes); err != nil {
		stage.Close()
		return receipt, err
	}
	if err = stage.Chmod(GenerationManifestName, 0644); err != nil {
		stage.Close()
		return receipt, err
	}
	if err = fileguard.SyncParents(stage, "."); err != nil {
		stage.Close()
		return receipt, err
	}
	if err = stage.Close(); err != nil {
		return receipt, err
	}
	staged, err := workspace.CaptureDirectory(filepath.Join(parentPath, stageName), workspace.DefaultLimits())
	if err != nil {
		return receipt, err
	}
	if err = validateGeneratedCapture(manifest, staged); err != nil {
		return receipt, err
	}
	fresh, err = captureCurrentSource(source)
	if err != nil {
		return receipt, err
	}
	if fresh.Snapshot.Digest != source.Snapshot.Digest {
		return receipt, c.Fail(c.Conflict, "source changed while generation was being staged")
	}
	if err = fileguard.RenameNoReplace(parent, stageName, finalName); err != nil {
		return receipt, err
	}
	published = true
	actualPath, err := fileguard.ResolveProspective(destination)
	if err != nil {
		receipt.OutputDirectory = destination
		receipt.Manifest = manifest
		receipt.PublicationState = "PUBLISHED_CANDIDATE_CAPTURE_UNKNOWN"
		return receipt, err
	}
	finalCapture, err := workspace.CaptureDirectory(actualPath, workspace.DefaultLimits())
	if err != nil {
		receipt.OutputDirectory = actualPath
		receipt.Manifest = manifest
		receipt.PublicationState = "PUBLISHED_CANDIDATE_CAPTURE_UNKNOWN"
		return receipt, err
	}
	if err = validateGeneratedCapture(manifest, finalCapture); err != nil {
		receipt.OutputDirectory = actualPath
		receipt.Manifest = manifest
		receipt.PublicationState = "PUBLISHED_CANDIDATE_CAPTURE_UNKNOWN"
		return receipt, err
	}
	receipt.OutputDirectory = actualPath
	receipt.PublishedSnapshotDigest = finalCapture.Snapshot.Digest
	receipt.Manifest = manifest
	receipt.PublicationState = "PUBLISHED_PENDING_INDEPENDENT_VALIDATION"
	return receipt, nil
}

// BindGenerationValidation reuses the protected check-origin path for the
// exact published candidate. It binds a reviewed check plan but never runs it
// and never upgrades the generated repository to VERIFIED.
func BindGenerationValidation(source workspace.Capture, blueprint BlueprintManifest, receipt GenerationReceipt,
	candidate workspace.Capture, taskID, inputDigest string, checkPlan verify.CheckPlan, supplied *discovery.Report) (GenerationValidationBinding, error) {
	binding := GenerationValidationBinding{SchemaVersion: c.SchemaVersion, BlueprintDigest: blueprint.ManifestDigest,
		GenerationManifestDigest: receipt.Manifest.ManifestDigest, CandidateSnapshotDigest: candidate.Snapshot.Digest,
		CheckSetDigest: blueprint.CheckSetDigest, RequiredCheckIDs: append([]string(nil), blueprint.RequiredIndependentChecks...),
		State: "PROTECTED_CHECK_ORIGIN_BOUND_NOT_EXECUTED"}
	if err := ValidateBlueprint(blueprint, source); err != nil {
		return binding, err
	}
	if err := ValidateGenerationReceipt(source, blueprint, receipt, candidate); err != nil {
		return binding, err
	}
	if err := ValidateGenerationCheckPlan(blueprint, checkPlan); err != nil {
		return binding, err
	}
	origin, err := verify.BuildCheckOriginWithDiscovery(taskID, candidate, inputDigest, checkPlan, supplied)
	if err != nil {
		return binding, err
	}
	binding.CheckOriginDigest, err = c.Digest(origin)
	if err != nil {
		return binding, err
	}
	binding.CheckOrigin = origin
	binding.DiscoveryDigest, binding.DiscoveryCoverage = origin.DiscoveryDigest, origin.DiscoveryCoverage
	binding.BindingDigest, err = generationValidationDigest(binding)
	if err != nil {
		return binding, err
	}
	return binding, nil
}

// ValidateGenerationCheckPlan lets a caller reject a missing, extra, or
// malformed protected check recipe before it creates the output directory.
// It validates the declaration only; candidate closure is checked later by
// BindGenerationValidation against the captured generated repository.
func ValidateGenerationCheckPlan(blueprint BlueprintManifest, plan verify.CheckPlan) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if len(blueprint.RequiredIndependentChecks) == 0 || !equalStrings(sortedCheckIDs(plan), blueprint.RequiredIndependentChecks) {
		return c.Fail(c.PolicyDenied, "protected check plan must exactly match blueprint required check IDs")
	}
	return nil
}

func ValidateGenerationValidationBinding(source workspace.Capture, blueprint BlueprintManifest, receipt GenerationReceipt,
	candidate workspace.Capture, taskID, inputDigest string, checkPlan verify.CheckPlan, supplied *discovery.Report,
	binding GenerationValidationBinding) error {
	expected, err := BindGenerationValidation(source, blueprint, receipt, candidate, taskID, inputDigest, checkPlan, supplied)
	if err != nil {
		return err
	}
	actualDigest, err := generationValidationDigest(binding)
	if err != nil || actualDigest != binding.BindingDigest || expected.BindingDigest != binding.BindingDigest {
		return c.Fail(c.StoreIntegrityError, "generation validation binding was altered")
	}
	return nil
}

func ValidateGenerationReceipt(source workspace.Capture, blueprint BlueprintManifest, receipt GenerationReceipt, candidate workspace.Capture) error {
	if err := ValidateBlueprint(blueprint, source); err != nil {
		return err
	}
	if err := workspace.VerifyCapture(candidate); err != nil {
		return err
	}
	if receipt.SchemaVersion != c.SchemaVersion || receipt.PublicationState != "PUBLISHED_PENDING_INDEPENDENT_VALIDATION" ||
		receipt.Manifest.BlueprintDigest != blueprint.ManifestDigest || receipt.Manifest.ManifestDigest == "" ||
		receipt.PublishedSnapshotDigest != candidate.Snapshot.Digest || !c.ValidDigest(receipt.PublishedSnapshotDigest) {
		return c.Fail(c.StoreIntegrityError, "generation receipt is stale or claims an invalid publication state")
	}
	resolved, err := fileguard.ResolveProspective(receipt.OutputDirectory)
	if err != nil || resolved != candidate.Snapshot.Root {
		return c.Fail(c.StoreIntegrityError, "generation output path does not match captured candidate")
	}
	manifestRaw, ok := candidate.Contents[GenerationManifestName]
	if !ok {
		return c.Fail(c.StoreIntegrityError, "generation manifest missing from captured candidate")
	}
	var saved GenerationManifest
	if err = c.DecodeStrict(manifestRaw, &saved); err != nil {
		return err
	}
	canonical, err := c.CanonicalV1(saved)
	if err != nil || !equalBytes(canonical, manifestRaw) {
		return c.Fail(c.StoreIntegrityError, "generation manifest is not canonical")
	}
	if saved.ManifestDigest != receipt.Manifest.ManifestDigest || !equalGenerationManifest(saved, receipt.Manifest) {
		return c.Fail(c.StoreIntegrityError, "generation manifest differs from receipt")
	}
	if err = validateManifestAgainstBlueprint(source, blueprint, saved); err != nil {
		return err
	}
	return validateGeneratedCapture(saved, candidate)
}

func validateGeneratedCapture(manifest GenerationManifest, candidate workspace.Capture) error {
	if err := workspace.VerifyCapture(candidate); err != nil {
		return err
	}
	manifestDigest, err := generationManifestDigest(manifest)
	if err != nil || manifestDigest != manifest.ManifestDigest {
		return c.Fail(c.StoreIntegrityError, "generation manifest digest mismatch")
	}
	manifestBytes, err := c.CanonicalV1(manifest)
	if err != nil {
		return err
	}
	expected := map[string]GeneratedFileRecord{}
	for _, record := range manifest.Files {
		expected[record.Path] = record
	}
	if len(candidate.Snapshot.Entries) != len(expected)+1 {
		return c.Fail(c.StoreIntegrityError, "generated candidate contains missing or undeclared files")
	}
	manifestEntry, manifestOK := false, false
	for _, entry := range candidate.Snapshot.Entries {
		if entry.Path == GenerationManifestName {
			manifestEntry = true
			raw, ok := candidate.Contents[GenerationManifestName]
			manifestOK = ok && entry.Hash == c.HashBytes(manifestBytes) && entry.Size == int64(len(manifestBytes)) && equalBytes(raw, manifestBytes)
			continue
		}
		record, ok := expected[entry.Path]
		if !ok || entry.Hash != record.Hash || entry.Size != record.Size || int64(len(candidate.Contents[entry.Path])) != record.Size || c.HashBytes(candidate.Contents[entry.Path]) != record.Hash {
			return c.Fail(c.StoreIntegrityError, "generated candidate bytes do not match the exact file set")
		}
		// Windows does not represent Unix executable bits. The manifest keeps
		// requested portable modes; exact bytes and bounded paths remain checked.
		if os.PathSeparator != '\\' && entry.Mode != record.Mode {
			return c.Fail(c.StoreIntegrityError, "generated candidate file mode differs from manifest")
		}
	}
	if !manifestEntry || !manifestOK {
		return c.Fail(c.StoreIntegrityError, "generated candidate manifest hash mismatch")
	}
	return nil
}

func generationManifest(source workspace.Capture, blueprint BlueprintManifest, plan GenerationPlan) (GenerationManifest, error) {
	manifest := GenerationManifest{SchemaVersion: c.SchemaVersion, GeneratorVersion: GenerationVersion,
		BlueprintDigest: blueprint.ManifestDigest, BlueprintReviewBinding: plan.BlueprintReviewBinding,
		SourceAuthority: plan.SourceAuthority, SourceAuthorityDigest: plan.SourceAuthorityDigest,
		Files: append([]GeneratedFileRecord(nil), plan.FileRecords...), FileSetDigest: plan.FileSetDigest,
		RequiredIndependentChecks: append([]string(nil), blueprint.RequiredIndependentChecks...), CheckSetDigest: blueprint.CheckSetDigest,
		Execution: "NONE", Network: "NONE", Hooks: "NOT_RUN", AcceptanceState: "EXPERIMENTAL_NOT_ACCEPTANCE",
		ValidationState: "PENDING_INDEPENDENT_VALIDATION"}
	if err := validateSourceAuthority(source, blueprint, manifest.SourceAuthority, manifest.SourceAuthorityDigest); err != nil {
		return GenerationManifest{}, err
	}
	if len(manifest.Files) != len(blueprint.GeneratedPaths) || manifest.FileSetDigest != plan.FileSetDigest {
		return GenerationManifest{}, c.Fail(c.StoreIntegrityError, "generation manifest does not cover the planned closure")
	}
	var err error
	manifest.ManifestDigest, err = generationManifestDigest(manifest)
	return manifest, err
}

func validateManifestAgainstBlueprint(source workspace.Capture, blueprint BlueprintManifest, manifest GenerationManifest) error {
	if manifest.SchemaVersion != c.SchemaVersion || manifest.GeneratorVersion != GenerationVersion ||
		manifest.BlueprintDigest != blueprint.ManifestDigest || manifest.BlueprintReviewBinding != "SELECTED_BLUEPRINT_DIGEST_MATCH_NOT_HUMAN_ATTESTATION" ||
		manifest.Execution != "NONE" || manifest.Network != "NONE" || manifest.Hooks != "NOT_RUN" ||
		manifest.AcceptanceState != "EXPERIMENTAL_NOT_ACCEPTANCE" || manifest.ValidationState != "PENDING_INDEPENDENT_VALIDATION" ||
		manifest.CheckSetDigest != blueprint.CheckSetDigest || !equalStrings(manifest.RequiredIndependentChecks, blueprint.RequiredIndependentChecks) ||
		len(manifest.Files) == 0 || len(manifest.Files) > GenerationFileLimit || len(manifest.Files) != len(blueprint.GeneratedPaths) {
		return c.Fail(c.StoreIntegrityError, "generation manifest is stale, incomplete, or overclaims validation")
	}
	if err := validateSourceAuthority(source, blueprint, manifest.SourceAuthority, manifest.SourceAuthorityDigest); err != nil {
		return err
	}
	paths := append([]string(nil), blueprint.GeneratedPaths...)
	sort.Strings(paths)
	var total int64
	for i, record := range manifest.Files {
		if record.Path != paths[i] || !policy.SafePath(record.Path) || strings.EqualFold(record.Path, GenerationManifestName) ||
			!c.ValidDigest(record.Hash) || record.Size < 1 || record.Size > GenerationFileBytes || (record.Mode != 0644 && record.Mode != 0755) {
			return c.Fail(c.StoreIntegrityError, "generation file record differs from planned closure")
		}
		total += record.Size
		if total > GenerationTotalBytes {
			return c.Fail(c.StoreIntegrityError, "generation manifest exceeds byte budget")
		}
	}
	if err := checkPathAliases(manifest.Files); err != nil {
		return err
	}
	fileSetDigest, err := c.Digest(manifest.Files)
	if err != nil || fileSetDigest != manifest.FileSetDigest {
		return c.Fail(c.StoreIntegrityError, "generation file set digest mismatch")
	}
	manifestDigest, err := generationManifestDigest(manifest)
	if err != nil || manifestDigest != manifest.ManifestDigest {
		return c.Fail(c.StoreIntegrityError, "generation manifest digest mismatch")
	}
	return nil
}

func captureCurrentSource(source workspace.Capture) (workspace.Capture, error) {
	limits := workspace.DefaultLimits()
	if source.Snapshot.Git != nil {
		return workspace.CaptureRepository(source.Snapshot.Root, limits)
	}
	return workspace.CaptureDirectory(source.Snapshot.Root, limits)
}

func sourceAuthority(source workspace.Capture, blueprint BlueprintManifest) (SourceAuthorityHashes, string, error) {
	authority := SourceAuthorityHashes{SnapshotDigest: source.Snapshot.Digest, Evidence: []BlueprintEvidence{}}
	seen := map[string]string{}
	for _, decision := range blueprint.Decisions {
		for _, evidence := range decision.Evidence {
			if digest, ok := seen[evidence.Path]; ok && digest != evidence.FileDigest {
				return authority, "", c.Fail(c.StoreIntegrityError, "conflicting source evidence hashes")
			}
			seen[evidence.Path] = evidence.FileDigest
		}
	}
	for path, digest := range seen {
		authority.Evidence = append(authority.Evidence, BlueprintEvidence{Path: path, FileDigest: digest})
	}
	sort.Slice(authority.Evidence, func(i, j int) bool { return authority.Evidence[i].Path < authority.Evidence[j].Path })
	if source.Snapshot.Git != nil {
		git := *source.Snapshot.Git
		var err error
		authority.GitStateDigest, err = c.Digest(git)
		if err != nil {
			return authority, "", err
		}
		authority.HeadObject, authority.IndexDigest, authority.ObjectFormat = git.HeadObject, git.IndexDigest, git.ObjectFormat
	}
	digest, err := c.Digest(authority)
	return authority, digest, err
}

func validateSourceAuthority(source workspace.Capture, blueprint BlueprintManifest, authority SourceAuthorityHashes, digest string) error {
	expected, expectedDigest, err := sourceAuthority(source, blueprint)
	if err != nil {
		return err
	}
	if digest != expectedDigest || !equalSourceAuthority(authority, expected) {
		return c.Fail(c.StoreIntegrityError, "generation source authority is stale or forged")
	}
	return nil
}

func generationManifestDigest(manifest GenerationManifest) (string, error) {
	manifest.ManifestDigest = ""
	return c.Digest(manifest)
}
func generationPlanDigest(plan GenerationPlan) (string, error) {
	plan.PlanDigest = ""
	return c.Digest(plan)
}
func generationValidationDigest(binding GenerationValidationBinding) (string, error) {
	binding.BindingDigest = ""
	return c.Digest(binding)
}

func createGenerationStage(parent *os.Root) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", err
		}
		name := ".viber-generation-" + hex.EncodeToString(nonce[:])
		if err := parent.Mkdir(name, 0700); err == nil {
			return name, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", c.Fail(c.Conflict, "could not allocate a fresh generation staging directory")
}

func cloneGenerationPlan(plan GenerationPlan) GenerationPlan {
	plan.Files = append([]GenerationFile(nil), plan.Files...)
	for i := range plan.Files {
		plan.Files[i].Data = append([]byte(nil), plan.Files[i].Data...)
	}
	plan.FileRecords = append([]GeneratedFileRecord(nil), plan.FileRecords...)
	plan.RequiredChecks = append([]string(nil), plan.RequiredChecks...)
	plan.SourceAuthority.Evidence = append([]BlueprintEvidence(nil), plan.SourceAuthority.Evidence...)
	return plan
}

func checkPathAliases(records []GeneratedFileRecord) error {
	spellings := map[string]string{}
	files := map[string]bool{}
	for _, record := range records {
		parts := strings.Split(record.Path, "/")
		for n := range parts {
			path := strings.Join(parts[:n+1], "/")
			key := strings.ToLower(path)
			if previous, ok := spellings[key]; ok && previous != path {
				return c.Fail(c.InvalidArgument, "generated file paths have case-insensitive aliases")
			}
			spellings[key] = path
			if n < len(parts)-1 {
				if files[key] {
					return c.Fail(c.InvalidArgument, "generated file path is also a required directory")
				}
				continue
			}
			for existing := range spellings {
				if strings.HasPrefix(existing, key+"/") {
					return c.Fail(c.InvalidArgument, "generated file path is also a required directory")
				}
			}
			files[key] = true
		}
	}
	return nil
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
func sortedCheckIDs(plan verify.CheckPlan) []string {
	ids := make([]string, len(plan.Checks))
	for i, check := range plan.Checks {
		ids[i] = check.ID
	}
	sort.Strings(ids)
	return ids
}
func equalFileRecords(a, b []GeneratedFileRecord) bool {
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
func equalSourceAuthority(a, b SourceAuthorityHashes) bool {
	if a.SnapshotDigest != b.SnapshotDigest || a.GitStateDigest != b.GitStateDigest || a.HeadObject != b.HeadObject || a.IndexDigest != b.IndexDigest || a.ObjectFormat != b.ObjectFormat || len(a.Evidence) != len(b.Evidence) {
		return false
	}
	for i := range a.Evidence {
		if a.Evidence[i] != b.Evidence[i] {
			return false
		}
	}
	return true
}
func equalGenerationManifest(a, b GenerationManifest) bool {
	if a.SchemaVersion != b.SchemaVersion || a.GeneratorVersion != b.GeneratorVersion || a.BlueprintDigest != b.BlueprintDigest || a.BlueprintReviewBinding != b.BlueprintReviewBinding ||
		a.SourceAuthorityDigest != b.SourceAuthorityDigest || a.FileSetDigest != b.FileSetDigest || a.CheckSetDigest != b.CheckSetDigest || a.Execution != b.Execution || a.Network != b.Network || a.Hooks != b.Hooks ||
		a.AcceptanceState != b.AcceptanceState || a.ValidationState != b.ValidationState || a.ManifestDigest != b.ManifestDigest || !equalSourceAuthority(a.SourceAuthority, b.SourceAuthority) ||
		!equalFileRecords(a.Files, b.Files) || !equalStrings(a.RequiredIndependentChecks, b.RequiredIndependentChecks) {
		return false
	}
	return true
}
func equalBytes(a, b []byte) bool {
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
