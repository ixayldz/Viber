package evaluation

import (
	"context"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"os"
	"path/filepath"
)

type NativeRegistration struct {
	Task          string `json:"evaluator_task,omitempty"`
	SchemaVersion int    `json:"schema_version"`
	Source        string `json:"source_digest"`
	Recipe        string `json:"recipe_digest"`
	Candidate     string `json:"evaluator_candidate"`
	Scope         string `json:"scope"`
}

func readManifestFiles(root *os.Root, files []OutputFile, expected []string) (map[string][]byte, error) {
	if len(files) != len(expected) {
		return nil, c.Fail(c.StoreIntegrityError, "evaluation manifest file count changed")
	}
	values := map[string][]byte{}
	for i, file := range files {
		if file.Path != expected[i] || !c.ValidDigest(file.Digest) || file.Size < 1 || file.Size > 32<<20 {
			return nil, c.Fail(c.StoreIntegrityError, "evaluation manifest file envelope invalid")
		}
		raw, err := fileguard.ReadRegular(root, file.Path, file.Size)
		if err != nil {
			return nil, err
		}
		if int64(len(raw)) != file.Size || c.HashBytes(raw) != file.Digest {
			return nil, c.Fail(c.StoreIntegrityError, "evaluation bundle file integrity failure")
		}
		values[file.Path] = raw
	}
	return values, nil
}

// ReadNativeResult recomputes the result from the separate durable owner;
// copying a JSON verdict or integrity manifest alone never supplies evidence.
func ReadNativeResult(ctx context.Context, path string) (NativeResult, error) {
	var result NativeResult
	root, err := os.OpenRoot(path)
	if err != nil {
		return result, err
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, "manifest.json", 16<<10)
	if err != nil {
		return result, err
	}
	var manifest NativeManifest
	if err = c.DecodeStrict(raw, &manifest); err != nil {
		return result, err
	}
	if manifest.SchemaVersion != 1 || manifest.EvidenceMode != NativeMode || !c.ValidDigest(manifest.SourceDigest) || !c.ValidDigest(manifest.RecipeDigest) {
		return result, c.Fail(c.StoreIntegrityError, "invalid native independent manifest")
	}
	files, err := readManifestFiles(root, manifest.Files, []string{"source.json", "registration.json", "result.json"})
	if err != nil {
		return result, err
	}
	if c.HashBytes(files["source.json"]) != manifest.SourceDigest {
		return result, c.Fail(c.StoreIntegrityError, "source manifest binding changed")
	}
	var source agent.EvaluationSource
	if err = c.DecodeStrict(files["source.json"], &source); err != nil {
		return result, err
	}
	if err = validateEvaluationSource(source); err != nil {
		return result, err
	}
	var registration NativeRegistration
	if err = c.DecodeStrict(files["registration.json"], &registration); err != nil {
		return result, err
	}
	if (registration.SchemaVersion != 1 && registration.SchemaVersion != 2) || registration.Source != manifest.SourceDigest || registration.Recipe != manifest.RecipeDigest || registration.Scope != NativeMode || !c.ValidDigest(registration.Candidate) {
		return result, c.Fail(c.StoreIntegrityError, "native registration binding changed")
	}
	owner, err := agent.OpenExisting(ctx, filepath.Join(root.Name(), "owner"))
	if err != nil {
		return result, err
	}
	defer owner.Close()
	task := evaluatorTask
	if registration.SchemaVersion == 2 {
		task = registration.Task
		if task == "" {
			return result, c.Fail(c.StoreIntegrityError, "missing evaluator task")
		}
	} else if registration.Task != "" {
		return result, c.Fail(c.StoreIntegrityError, "legacy registration has unexpected task")
	}
	_, doc, err := owner.Load(ctx, task)
	if err != nil {
		return result, err
	}
	if registration.SchemaVersion == 2 && (doc.EvaluationOrigin == nil || doc.EvaluationOrigin.ParentTask != source.TaskID || doc.EvaluationOrigin.ParentSequence != source.TaskSequence || doc.EvaluationOrigin.ParentDocument != source.DocumentDigest) {
		return result, c.Fail(c.StoreIntegrityError, "registered source differs from durable evaluator lineage")
	}
	if doc.Protection == nil || doc.CheckRuntime == nil {
		return result, c.Fail(c.StoreIntegrityError, "independent owner recipe unavailable")
	}
	recipe := NativeRecipe{1, doc.Protection.Plan, *doc.CheckRuntime}
	result, err = deriveNativeResultForTask(ctx, owner, task, source, recipe)
	if err != nil {
		return result, err
	}
	if result.RecipeDigest != manifest.RecipeDigest || result.EvaluatorCandidate != registration.Candidate {
		return result, c.Fail(c.StoreIntegrityError, "owner differs from registered evaluation")
	}
	computed, err := c.CanonicalV1(result)
	if err != nil {
		return result, err
	}
	if c.HashBytes(computed) != c.HashBytes(files["result.json"]) {
		return result, c.Fail(c.StoreIntegrityError, "native verdict does not match durable owner evidence")
	}
	return result, nil
}
