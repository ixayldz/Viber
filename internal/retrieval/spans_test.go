package retrieval

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestBodyEvidencePrecedesPathAndMultipleIndependentSpansSurvive(t *testing.T) {
	body := "Needle first\n" + strings.Repeat("unrelated padding\n", 600) + "Needle second\n" + strings.Repeat("unrelated padding\n", 600) + "Needle third\n"
	docs := []Document{document("Needle/path.go", strings.Repeat("no match here\n", 1200)), document("src/body.go", body)}
	index := openIndex(t, docs)
	result, err := index.Search(context.Background(), fixtureBinding(), "Needle", "IDENTIFIER", 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 4 || result.Hits[0].Path != "src/body.go" {
		t.Fatal("path flooded body evidence", result)
	}
	bodyHits, pathHits := 0, 0
	var prior []Hit
	for _, hit := range result.Hits {
		if hit.Path == "Needle/path.go" {
			pathHits++
			continue
		}
		bodyHits++
		if !bytes.Contains(hit.Bytes, []byte("Needle")) {
			t.Fatal("missing actual body match")
		}
		for _, other := range prior {
			if hit.Start < other.End && other.Start < hit.End {
				t.Fatal("overlapping evidence duplicated")
			}
		}
		prior = append(prior, hit)
	}
	if bodyHits != 3 || pathHits != 1 {
		t.Fatal("span quotas", bodyHits, pathHits)
	}
}
func TestPerFileSpanLimitExplicitlyReportsMissingCoverage(t *testing.T) {
	text := ""
	for n := 0; n < 8; n++ {
		text += "Needle\n" + strings.Repeat("padding\n", 1600)
	}
	index := openIndex(t, []Document{document("src/body.go", text)})
	result, err := index.Search(context.Background(), fixtureBinding(), "Needle", "ERROR", 0, 128)
	if err != nil || len(result.Hits) != 3 || !result.PoolTruncated {
		t.Fatal("hidden per-file truncation", result, err)
	}
}
