package intelligence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/verify"
	"github.com/ixayldz/Viber/internal/workspace"
)

func generationFixture(t *testing.T) (workspace.Capture, BlueprintManifest, []GenerationFile) {
	t.Helper()
	source := testCapture(t)
	blueprint, err := BuildBlueprint(source,
		[]BlueprintDecision{{Area: "package", Choice: "minimal Go package", Reason: "captured source", Evidence: []BlueprintEvidence{{Path: "target.go", FileDigest: c.HashBytes(source.Contents["target.go"])}}}},
		nil, []string{"README.md", "pkg/main.go"}, []string{"static-1"})
	if err != nil {
		t.Fatal(err)
	}
	files := []GenerationFile{{Path: "README.md", Data: []byte("# Generated project\n"), Mode: 0644},
		{Path: "pkg/main.go", Data: []byte("package main\nfunc Main() {}\n"), Mode: 0644}}
	return source, blueprint, files
}

func TestGenerationPlanBindsExactFilesAndSourceAuthority(t *testing.T) {
	source, blueprint, files := generationFixture(t)
	plan, err := BuildGenerationPlan(source, blueprint, blueprint.ManifestDigest, files)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateGenerationPlan(source, blueprint, plan); err != nil {
		t.Fatal(err)
	}
	manifest, err := generationManifest(source, blueprint, plan)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SourceAuthority.SnapshotDigest != source.Snapshot.Digest || manifest.SourceAuthority.GitStateDigest == "" ||
		manifest.SourceAuthority.HeadObject != source.Snapshot.Git.HeadObject || manifest.AcceptanceState != "EXPERIMENTAL_NOT_ACCEPTANCE" ||
		manifest.ValidationState != "PENDING_INDEPENDENT_VALIDATION" || manifest.Execution != "NONE" || manifest.Network != "NONE" || manifest.Hooks != "NOT_RUN" {
		t.Fatalf("generation manifest overclaimed status or omitted authority: %#v", manifest)
	}
	if err = validateManifestAgainstBlueprint(source, blueprint, manifest); err != nil {
		t.Fatal(err)
	}

	changed := cloneGenerationPlan(plan)
	changed.Files[0].Data[0] ^= 1
	if err = ValidateGenerationPlan(source, blueprint, changed); err == nil {
		t.Fatal("mutated exact output bytes passed their original file digest")
	}
	forgedAuthority := plan
	forgedAuthority.SourceAuthority.HeadObject = strings.Repeat("b", 40)
	forgedAuthority.PlanDigest, _ = generationPlanDigest(forgedAuthority)
	if err = ValidateGenerationPlan(source, blueprint, forgedAuthority); err == nil {
		t.Fatal("rehash did not legitimize forged source authority")
	}
	if _, err = BuildGenerationPlan(source, blueprint, c.HashBytes([]byte("forged review")), files); err == nil {
		t.Fatal("mismatched selected blueprint digest was admitted")
	}
	if _, err = BuildGenerationPlan(source, blueprint, blueprint.ManifestDigest,
		[]GenerationFile{{Path: "../outside.go", Data: []byte("unsafe"), Mode: 0644}, files[1]}); err == nil {
		t.Fatal("unsafe output path was admitted")
	}
	if _, err = BuildGenerationPlan(source, blueprint, blueprint.ManifestDigest,
		[]GenerationFile{files[0]}); err == nil {
		t.Fatal("incomplete generated closure was admitted")
	}
}

func TestGenerationManifestRejectsRehashedFalseValidationAndAuthority(t *testing.T) {
	source, blueprint, files := generationFixture(t)
	plan, err := BuildGenerationPlan(source, blueprint, blueprint.ManifestDigest, files)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := generationManifest(source, blueprint, plan)
	if err != nil {
		t.Fatal(err)
	}
	forged := manifest
	forged.AcceptanceState = "VERIFIED"
	forged.ManifestDigest, _ = generationManifestDigest(forged)
	if err = validateManifestAgainstBlueprint(source, blueprint, forged); err == nil {
		t.Fatal("rehash did not legitimize false validation")
	}
	forged = manifest
	forged.SourceAuthority.SnapshotDigest = c.HashBytes([]byte("different source"))
	forged.SourceAuthorityDigest, _ = c.Digest(forged.SourceAuthority)
	forged.ManifestDigest, _ = generationManifestDigest(forged)
	if err = validateManifestAgainstBlueprint(source, blueprint, forged); err == nil {
		t.Fatal("rehash did not legitimize forged source authority")
	}
}

func TestGenerationCheckPlanMustMatchRequiredProtectedIDs(t *testing.T) {
	_, blueprint, _ := generationFixture(t)
	plan := verify.CheckPlan{SchemaVersion: 1, Checks: []verify.CheckDefinition{{
		ID: "static-1", Kind: "STATIC", RequirementIDs: []string{"requirement-1"},
		Argv: []string{"protected-static-check"}, RunnerDigest: c.HashBytes([]byte("runner")),
		Selection: "all", Closure: []verify.CheckScope{{Path: ".", Recursive: true}},
	}}}
	if err := ValidateGenerationCheckPlan(blueprint, plan); err != nil {
		t.Fatal(err)
	}
	plan.Checks[0].ID = "extra-check"
	if err := ValidateGenerationCheckPlan(blueprint, plan); err == nil {
		t.Fatal("extra protected check ID passed generation check-plan gate")
	}
}

func TestGenerationPlanRejectsCaseFoldedDirectoryAliases(t *testing.T) {
	source := testCapture(t)
	blueprint, err := BuildBlueprint(source, nil, nil, []string{"a/x.go", "A/y.go"}, []string{"static-1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildGenerationPlan(source, blueprint, blueprint.ManifestDigest, []GenerationFile{
		{Path: "a/x.go", Data: []byte("package x\n"), Mode: 0644},
		{Path: "A/y.go", Data: []byte("package y\n"), Mode: 0644},
	})
	if err == nil {
		t.Fatal("case-folded directory alias was admitted")
	}
}

func TestPublishGenerationWritesExactCandidateAndNeverReplacesIt(t *testing.T) {
	parent := t.TempDir()
	sourceRoot := filepath.Join(parent, "source")
	if err := os.Mkdir(sourceRoot, 0755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"target.go":  []byte("package target\nfunc Changed() {}\n"),
		"related.go": []byte("package related\nfunc Helper() {}\n"),
	} {
		if err := os.WriteFile(filepath.Join(sourceRoot, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	source, err := workspace.CaptureDirectory(sourceRoot, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := BuildBlueprint(source,
		[]BlueprintDecision{{Area: "package", Choice: "minimal Go package", Reason: "captured source", Evidence: []BlueprintEvidence{{Path: "target.go", FileDigest: c.HashBytes(source.Contents["target.go"])}}}},
		nil, []string{"README.md", "pkg/main.go"}, []string{"static-1"})
	if err != nil {
		t.Fatal(err)
	}
	files := []GenerationFile{
		{Path: "README.md", Data: []byte("# Generated project\n"), Mode: 0644},
		{Path: "pkg/main.go", Data: []byte("package main\nfunc Main() {}\n"), Mode: 0644},
	}
	plan, err := BuildGenerationPlan(source, blueprint, blueprint.ManifestDigest, files)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "generated")
	canonicalOutput, err := fileguard.ResolveProspective(output)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := PublishGeneration(source, blueprint, plan, output)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.PublicationState != "PUBLISHED_PENDING_INDEPENDENT_VALIDATION" || receipt.OutputDirectory != canonicalOutput || receipt.PublishedSnapshotDigest == "" {
		t.Fatalf("unexpected publication receipt: %#v", receipt)
	}
	for _, file := range files {
		actual, readErr := os.ReadFile(filepath.Join(output, filepath.FromSlash(file.Path)))
		if readErr != nil || string(actual) != string(file.Data) {
			t.Fatalf("published file %q differs from reviewed bytes: %q, %v", file.Path, actual, readErr)
		}
	}
	if _, err = os.Stat(filepath.Join(output, GenerationManifestName)); err != nil {
		t.Fatalf("generation manifest missing: %v", err)
	}
	if _, err = PublishGeneration(source, blueprint, plan, output); err == nil {
		t.Fatal("publisher replaced an existing output directory")
	}
	actual, err := os.ReadFile(filepath.Join(output, "README.md"))
	if err != nil || string(actual) != string(files[0].Data) {
		t.Fatalf("existing candidate changed after rejected replacement: %q, %v", actual, err)
	}
}
