package retrieval

import (
	"context"
	"sort"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
)

const PartitionVersion = "AUTHORIZED_SOURCE_PARTITIONS_V1"
const PartitionMaxBytes = 4 << 20
const PartitionMaxDocuments = 256
const PartitionMaxCount = 64

type Partition struct {
	Ordinal     int    `json:"ordinal"`
	Digest      string `json:"digest"`
	Documents   int    `json:"documents"`
	SourceBytes int64  `json:"source_bytes"`
	FirstPath   string `json:"first_path"`
	LastPath    string `json:"last_path"`
}

type PartitionPlan struct {
	Version     string      `json:"version"`
	Binding     Binding     `json:"binding"`
	SourceSet   string      `json:"source_set"`
	Documents   int         `json:"documents"`
	SourceBytes int64       `json:"source_bytes"`
	Partitions  []Partition `json:"partitions"`
	Digest      string      `json:"plan_digest"`
	documents   [][]Document
}

// Planning hashes every authorized file before selecting one lazy index. No
// source outside that authorization is retained in the plan or index metadata.
func PlanPartitions(ctx context.Context, binding Binding, documents []Document) (PartitionPlan, error) {
	plan := PartitionPlan{Version: PartitionVersion, Binding: binding, Documents: len(documents), Partitions: []Partition{}, documents: [][]Document{}}
	if !c.ValidDigest(binding.Candidate) || !c.ValidDigest(binding.Policy) || len(documents) > 10000 {
		return plan, c.Fail(c.InvalidArgument, "bounded source binding required for partitions")
	}
	ordered := append([]Document{}, documents...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	refs := []struct{ Path, Digest string }{}
	previous := ""
	for _, doc := range ordered {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		if !policy.SafePath(doc.Path) || len(doc.Path) > 4096 || doc.Path <= previous || !c.ValidDigest(doc.Digest) || c.HashBytes(doc.Bytes) != doc.Digest {
			return plan, c.Fail(c.StoreIntegrityError, "partition source is unsafe, duplicated or changed")
		}
		if len(doc.Bytes) > PartitionMaxBytes || int64(len(doc.Bytes)) > MaxBytes-plan.SourceBytes {
			return plan, c.Fail(c.UnsupportedCapability, "partition source byte bound exceeded")
		}
		plan.SourceBytes += int64(len(doc.Bytes))
		refs = append(refs, struct{ Path, Digest string }{doc.Path, doc.Digest})
		previous = doc.Path
	}
	var err error
	plan.SourceSet, err = c.Digest(refs)
	if err != nil {
		return plan, err
	}
	for _, doc := range ordered {
		if len(plan.Partitions) == 0 || plan.Partitions[len(plan.Partitions)-1].Documents == PartitionMaxDocuments || int64(len(doc.Bytes)) > PartitionMaxBytes-plan.Partitions[len(plan.Partitions)-1].SourceBytes {
			if len(plan.Partitions) == PartitionMaxCount {
				return plan, c.Fail(c.UnsupportedCapability, "partition count bound exceeded")
			}
			plan.Partitions = append(plan.Partitions, Partition{Ordinal: len(plan.Partitions), FirstPath: doc.Path})
			plan.documents = append(plan.documents, []Document{})
		}
		n := len(plan.Partitions) - 1
		plan.Partitions[n].Documents++
		plan.Partitions[n].SourceBytes += int64(len(doc.Bytes))
		plan.Partitions[n].LastPath = doc.Path
		plan.documents[n] = append(plan.documents[n], doc)
	}
	if len(plan.Partitions) == 0 {
		plan.Partitions = append(plan.Partitions, Partition{Ordinal: 0})
		plan.documents = append(plan.documents, []Document{})
	}
	for n := range plan.Partitions {
		selected := []struct{ Path, Digest string }{}
		for _, doc := range plan.documents[n] {
			selected = append(selected, struct{ Path, Digest string }{doc.Path, doc.Digest})
		}
		plan.Partitions[n].Digest, err = c.Digest(struct {
			Version   string
			Binding   Binding
			SourceSet string
			Ordinal   int
			Sources   any
		}{PartitionVersion, binding, plan.SourceSet, n, selected})
		if err != nil {
			return plan, err
		}
	}
	plan.Digest, err = c.Digest(plan)
	return plan, err
}

type PartitionEvidence struct {
	Version             string      `json:"version"`
	PlanDigest          string      `json:"plan_digest"`
	SourceSet           string      `json:"authorized_source_set"`
	AuthorizedDocuments int         `json:"authorized_documents"`
	AuthorizedBytes     int64       `json:"authorized_bytes"`
	Selected            Partition   `json:"selected"`
	Partitions          []Partition `json:"partitions"`
	Next                *int        `json:"next_partition,omitempty"`
	Scope               string      `json:"scope"`
}

func (plan PartitionPlan) Select(ordinal int) ([]Document, PartitionEvidence, error) {
	evidence := PartitionEvidence{Version: PartitionVersion, PlanDigest: plan.Digest, SourceSet: plan.SourceSet, AuthorizedDocuments: plan.Documents, AuthorizedBytes: plan.SourceBytes, Partitions: append([]Partition{}, plan.Partitions...), Scope: "RANKED_POOL_OF_ONE_AUTHORIZED_PARTITION; NO_REPOSITORY_ABSENCE_PROOF"}
	if len(plan.documents) != len(plan.Partitions) || len(plan.Partitions) < 1 || len(plan.Partitions) > PartitionMaxCount || ordinal < 0 || ordinal >= len(plan.Partitions) {
		return nil, evidence, c.Fail(c.InvalidArgument, "partition ordinal is outside the authorized plan")
	}
	expected := plan.Digest
	plan.Digest = ""
	actual, err := c.Digest(plan)
	if err != nil || !c.ValidDigest(expected) || actual != expected {
		return nil, evidence, c.Fail(c.StoreIntegrityError, "partition plan identity changed")
	}
	plan.Digest = expected
	evidence.Selected = plan.Partitions[ordinal]
	if ordinal+1 < len(plan.Partitions) {
		next := ordinal + 1
		evidence.Next = &next
	}
	return append([]Document{}, plan.documents[ordinal]...), evidence, nil
}
