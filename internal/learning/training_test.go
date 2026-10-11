package learning

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestRankingTrainingRequiresDisjointHoldoutAndExactSelectedWeights(t *testing.T) {
	definition, holdout := rankingFixture(t)
	registry, err := Open(filepath.Join(t.TempDir(), "ranking"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	dev := holdout
	dev.Split = "DEV"
	devDigest, _ := c.Digest(dev)
	trained, err := registry.Train(context.Background(), definition.ID, 1, "MIT", dev, devDigest)
	if err != nil {
		t.Fatal(err)
	}
	if len(trained.Receipt.Scores) != 16 || trained.Receipt.Selected.BM25 != 1 || trained.Receipt.Selected.Trigram != 1 {
		t.Fatal("grid did not preserve baseline on ties", trained.Receipt)
	}
	if _, err = registry.Train(context.Background(), definition.ID, 2, "MIT", dev, devDigest); err == nil {
		t.Fatal("frozen training assignment replay admitted")
	}
	holdout.Cases = append([]Case(nil), holdout.Cases...)
	holdout.Cases[0].Query = "  " + strings.ToUpper(holdout.Cases[0].Query) + "  "
	holdout.Cases[0].Intent = "FEATURE"
	digest, _ := c.Digest(holdout)
	if _, err = registry.Evaluate(context.Background(), trained.Definition, holdout, digest); err == nil || !strings.Contains(err.Error(), "DEV and HOLDOUT queries overlap") {
		t.Fatal("normalized DEV query leaked into HOLDOUT")
	}
	for i := range holdout.Cases {
		holdout.Cases[i].Query += "unseen"
	}
	digest, _ = c.Digest(holdout)
	forged := trained.Definition
	forged.Weights.BM25 = 8
	if _, err = registry.Evaluate(context.Background(), forged, holdout, digest); err == nil {
		t.Fatal("declared weights bypassed actual DEV-selected definition")
	}
	if _, err = registry.Evaluate(context.Background(), trained.Definition, holdout, digest); err != nil {
		t.Fatal(err)
	}
}
