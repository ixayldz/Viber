package retrieval

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestPartitionPlanCoversAllSourcesDeterministicallyWithinBounds(t *testing.T) {
	ctx := context.Background()
	docs := []Document{}
	for n := 0; n < 10000; n++ {
		docs = append(docs, document(fmt.Sprintf("src/%05d.go", n), fmt.Sprintf("symbol%d\n", n)))
	}
	first, err := PlanPartitions(ctx, fixtureBinding(), docs)
	if err != nil || len(first.Partitions) != 40 {
		t.Fatal(first.Partitions, err)
	}
	reversed := append([]Document{}, docs...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	second, err := PlanPartitions(ctx, fixtureBinding(), reversed)
	if err != nil || first.Digest != second.Digest || !reflect.DeepEqual(first.Partitions, second.Partitions) {
		t.Fatal("source order changed partition identity", err)
	}
	seen := map[string]bool{}
	for n := range first.Partitions {
		selected, evidence, err := first.Select(n)
		if err != nil || evidence.Selected.Documents > PartitionMaxDocuments || evidence.Selected.SourceBytes > PartitionMaxBytes || evidence.AuthorizedDocuments != 10000 || evidence.SourceSet != first.SourceSet || evidence.PlanDigest != first.Digest {
			t.Fatal(evidence, err)
		}
		for _, doc := range selected {
			if seen[doc.Path] {
				t.Fatal("source appears in multiple partitions", doc.Path)
			}
			seen[doc.Path] = true
		}
	}
	if len(seen) != len(docs) {
		t.Fatal("sources silently omitted", len(seen))
	}
	lastDocs, last, err := first.Select(39)
	if err != nil || last.Next != nil || lastDocs[len(lastDocs)-1].Path != "src/09999.go" {
		t.Fatal(last, err)
	}
	first.Partitions[0].FirstPath = "forged/path.go"
	if _, _, err = first.Select(0); err == nil {
		t.Fatal("mutated plan selected")
	}
	if _, _, err = (PartitionPlan{}).Select(0); err == nil {
		t.Fatal("uninitialized plan accepted")
	}
}

func TestPartitionByteBoundsAndSourceIdentityFailures(t *testing.T) {
	ctx := context.Background()
	docs := []Document{}
	for n := 0; n < 9; n++ {
		docs = append(docs, document(fmt.Sprintf("large/%02d.txt", n), strings.Repeat("a", 1<<20)))
	}
	plan, err := PlanPartitions(ctx, fixtureBinding(), docs)
	if err != nil || len(plan.Partitions) != 3 || plan.Partitions[0].SourceBytes != PartitionMaxBytes || plan.Partitions[2].SourceBytes != 1<<20 {
		t.Fatal(plan.Partitions, err)
	}
	for _, bad := range [][]Document{
		{document("../outside", "private")},
		{docs[0], docs[0]},
		{{Path: "changed.txt", Digest: c.HashBytes([]byte("before")), Bytes: []byte("after")}},
		{document("oversized.txt", strings.Repeat("a", PartitionMaxBytes+1))},
	} {
		if _, err = PlanPartitions(ctx, fixtureBinding(), bad); err == nil {
			t.Fatal("unsafe or unbounded partition source accepted")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = PlanPartitions(cancelled, fixtureBinding(), docs); err == nil {
		t.Fatal("cancelled planning continued")
	}
}
