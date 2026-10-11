package intelligence

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
	"sort"
	"strings"
)

const BlueprintVersion = "EXPERIMENTAL_BLUEPRINT_MANIFEST_V1"

type BlueprintEvidence struct {
	Path       string `json:"path"`
	FileDigest string `json:"file_digest"`
}
type BlueprintDecision struct {
	Area     string              `json:"area"`
	Choice   string              `json:"choice"`
	Reason   string              `json:"reason"`
	Evidence []BlueprintEvidence `json:"evidence"`
}
type BlueprintDependency struct {
	Ecosystem  string `json:"ecosystem"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	Source     string `json:"declared_source"`
	Resolution string `json:"resolution"`
}
type BlueprintManifest struct {
	SchemaVersion             int                   `json:"schema_version"`
	GeneratorVersion          string                `json:"generator_version"`
	Experimental              bool                  `json:"experimental"`
	SourceSnapshotDigest      string                `json:"source_snapshot_digest"`
	Decisions                 []BlueprintDecision   `json:"architecture_decisions"`
	Dependencies              []BlueprintDependency `json:"dependencies"`
	GeneratedPaths            []string              `json:"planned_generated_paths"`
	RequiredIndependentChecks []string              `json:"required_independent_checks"`
	CheckSetDigest            string                `json:"check_set_digest"`
	AcceptanceState           string                `json:"acceptance_state"`
	Coverage                  string                `json:"coverage"`
	ManifestDigest            string                `json:"manifest_digest"`
}

// BuildBlueprint creates a source-bound planning artifact only. It resolves no
// dependencies, generates no files, runs no compiler, and cannot mark acceptance.
func BuildBlueprint(source workspace.Capture, decisions []BlueprintDecision, dependencies []BlueprintDependency, generatedPaths, requiredChecks []string) (BlueprintManifest, error) {
	out := BlueprintManifest{SchemaVersion: c.SchemaVersion, GeneratorVersion: BlueprintVersion, Experimental: true,
		SourceSnapshotDigest: source.Snapshot.Digest, Decisions: append([]BlueprintDecision(nil), decisions...),
		Dependencies: append([]BlueprintDependency(nil), dependencies...), GeneratedPaths: append([]string(nil), generatedPaths...),
		RequiredIndependentChecks: append([]string(nil), requiredChecks...), AcceptanceState: "EXPERIMENTAL_NOT_ACCEPTANCE", Coverage: "DECLARATIONS_ONLY"}
	if err := workspace.VerifyCapture(source); err != nil {
		return out, err
	}
	if len(out.Decisions) > 128 || len(out.Dependencies) > 256 || len(out.GeneratedPaths) > 512 || len(out.RequiredIndependentChecks) == 0 || len(out.RequiredIndependentChecks) > 128 {
		return out, c.Fail(c.InvalidArgument, "bounded blueprint declarations and required checks needed")
	}
	entries := map[string]c.Entry{}
	for _, entry := range source.Snapshot.Entries {
		entries[entry.Path] = entry
	}
	for n := range out.Decisions {
		item := &out.Decisions[n]
		if strings.TrimSpace(item.Area) == "" || strings.TrimSpace(item.Choice) == "" || len(item.Area) > 128 || len(item.Choice) > 512 || len(item.Reason) > 2048 || len(item.Evidence) > 64 {
			return out, c.Fail(c.InvalidArgument, "invalid blueprint decision")
		}
		seen := map[string]bool{}
		for _, e := range item.Evidence {
			entry, ok := entries[e.Path]
			if !ok || seen[e.Path] || e.FileDigest != entry.Hash {
				return out, c.Fail(c.StoreIntegrityError, "blueprint evidence outside captured source")
			}
			seen[e.Path] = true
		}
		sort.Slice(item.Evidence, func(i, j int) bool { return item.Evidence[i].Path < item.Evidence[j].Path })
	}
	depSeen := map[string]bool{}
	for n := range out.Dependencies {
		dep := &out.Dependencies[n]
		key := strings.ToLower(dep.Ecosystem + "\x00" + dep.Name)
		if dep.Ecosystem == "" || dep.Name == "" || dep.Version == "" || len(dep.Ecosystem) > 32 || len(dep.Name) > 256 || len(dep.Version) > 128 || len(dep.Source) > 512 || depSeen[key] {
			return out, c.Fail(c.InvalidArgument, "blueprint dependency requires a unique explicit version")
		}
		depSeen[key] = true
		dep.Resolution = "DECLARED_NOT_RESOLVED"
	}
	paths := map[string]bool{}
	for _, path := range out.GeneratedPaths {
		if !policy.SafePath(path) || paths[path] {
			return out, c.Fail(c.InvalidArgument, "invalid planned generated path")
		}
		paths[path] = true
	}
	checks := map[string]bool{}
	for _, check := range out.RequiredIndependentChecks {
		if strings.TrimSpace(check) == "" || len(check) > 256 || checks[check] {
			return out, c.Fail(c.InvalidArgument, "invalid required check declaration")
		}
		checks[check] = true
	}
	sort.Strings(out.GeneratedPaths)
	sort.Strings(out.RequiredIndependentChecks)
	var err error
	out.CheckSetDigest, err = c.Digest(out.RequiredIndependentChecks)
	if err != nil {
		return BlueprintManifest{}, err
	}
	out.ManifestDigest, err = c.Digest(out)
	if err != nil {
		return BlueprintManifest{}, err
	}
	return out, nil
}

func ValidateBlueprint(manifest BlueprintManifest, source workspace.Capture) error {
	if err := workspace.VerifyCapture(source); err != nil {
		return err
	}
	if manifest.SchemaVersion != c.SchemaVersion || manifest.GeneratorVersion != BlueprintVersion || !manifest.Experimental ||
		manifest.SourceSnapshotDigest != source.Snapshot.Digest || manifest.AcceptanceState != "EXPERIMENTAL_NOT_ACCEPTANCE" ||
		manifest.Coverage != "DECLARATIONS_ONLY" || len(manifest.RequiredIndependentChecks) == 0 || len(manifest.Decisions) > 128 ||
		len(manifest.Dependencies) > 256 || len(manifest.GeneratedPaths) > 512 {
		return c.Fail(c.StoreIntegrityError, "blueprint is stale or claims acceptance")
	}
	checkDigest, err := c.Digest(manifest.RequiredIndependentChecks)
	if err != nil || checkDigest != manifest.CheckSetDigest {
		return c.Fail(c.StoreIntegrityError, "blueprint check set changed")
	}
	expected := manifest.ManifestDigest
	manifest.ManifestDigest = ""
	actual, err := c.Digest(manifest)
	if err != nil {
		return err
	}
	if actual != expected {
		return c.Fail(c.StoreIntegrityError, "blueprint manifest digest mismatch")
	}
	entries := map[string]c.Entry{}
	for _, entry := range source.Snapshot.Entries {
		entries[entry.Path] = entry
	}
	for _, decision := range manifest.Decisions {
		for _, evidence := range decision.Evidence {
			entry, ok := entries[evidence.Path]
			if !ok || entry.Hash != evidence.FileDigest {
				return c.Fail(c.StoreIntegrityError, "blueprint evidence changed")
			}
		}
	}
	for _, path := range manifest.GeneratedPaths {
		if !policy.SafePath(path) {
			return c.Fail(c.InvalidArgument, "unsafe generated path")
		}
	}
	for _, dep := range manifest.Dependencies {
		if dep.Resolution != "DECLARED_NOT_RESOLVED" {
			return c.Fail(c.StoreIntegrityError, "blueprint cannot claim dependency resolution")
		}
	}
	expectedManifest, buildErr := BuildBlueprint(source, manifest.Decisions, manifest.Dependencies, manifest.GeneratedPaths, manifest.RequiredIndependentChecks)
	if buildErr != nil || expectedManifest.ManifestDigest != expected {
		return c.Fail(c.StoreIntegrityError, "blueprint declarations are invalid or noncanonical")
	}
	return nil
}
