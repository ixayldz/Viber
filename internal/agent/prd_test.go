package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

func TestPRDHeadingOffsetsHandleATXSetextFencesAndCRLF(t *testing.T) {
	raw := []byte("# Alpha\r\nTitle from Setext\r\n----------------\r\n~~~md\r\n# Example\r\n~~~\r\n## Omega ###\r\n")
	prd, _ := buildPRDSource(raw, c.HashBytes([]byte("candidate")), "PRD.md", "prd-source", c.HashBytes(raw))
	if prd.SectionExtraction != "COMPLETE" || prd.HeadingCount != 3 || len(prd.Sections) != 3 {
		t.Fatalf("unexpected extraction: %#v", prd)
	}
	want := []struct {
		heading string
		start   int
	}{
		{heading: "Alpha", start: 0},
		{heading: "Title from Setext", start: strings.Index(string(raw), "Title from Setext")},
		{heading: "Omega", start: strings.Index(string(raw), "## Omega")},
	}
	for i, section := range prd.Sections {
		if section.Heading != want[i].heading || section.Start != int64(want[i].start) {
			t.Fatalf("section %d = %#v, want heading %q at %d", i, section, want[i].heading, want[i].start)
		}
	}
	if prd.Sections[0].End != prd.Sections[1].Start || prd.Sections[1].End != prd.Sections[2].Start || prd.Sections[2].End != int64(len(raw)) {
		t.Fatal("section offsets do not partition the original CRLF bytes", prd.Sections)
	}
}

func TestPRDHeadingCapAndLongLabelsRequireWholeSourceReview(t *testing.T) {
	t.Run("section cap retains a mandatory full-source criterion", func(t *testing.T) {
		var source strings.Builder
		for i := 0; i < maxPRDSections+1; i++ {
			fmt.Fprintf(&source, "# Section %03d\r\n", i+1)
		}
		testPRDFullSourceRequirement(t, []byte(source.String()), func(prd c.PRDSource) {
			if prd.SectionExtraction != "REVIEW_REQUIRED" || prd.HeadingCount != maxPRDSections+1 || prd.OmittedHeadingCount != 1 || len(prd.Sections) != maxPRDSections {
				t.Fatalf("section overflow was not explicit and bounded: %#v", prd)
			}
			omittedStart := int64(strings.LastIndex(source.String(), "# Section 129"))
			if prd.Sections[len(prd.Sections)-1].End != omittedStart {
				t.Fatal("last retained section does not stop at the omitted heading", prd.Sections[len(prd.Sections)-1])
			}
		})
	})

	t.Run("oversized label is bounded and requires review", func(t *testing.T) {
		raw := []byte("# " + strings.Repeat("x", maxPRDHeadingLabelBytes+100) + "\nbody\n")
		testPRDFullSourceRequirement(t, raw, func(prd c.PRDSource) {
			if prd.SectionExtraction != "REVIEW_REQUIRED" || prd.UnsupportedHeadingCount != 1 || len(prd.Sections) != 1 || len(prd.Sections[0].Heading) > maxPRDHeadingLabelBytes {
				t.Fatalf("oversized heading did not request bounded review: %#v", prd)
			}
			if prd.Sections[0].End != int64(len(raw)) {
				t.Fatal("bounded label truncated its authoritative source span", prd.Sections[0])
			}
		})
	})
}

func testPRDFullSourceRequirement(t *testing.T, raw []byte, check func(c.PRDSource)) {
	t.Helper()
	goal := []byte("implement the requested work")
	goalDigest := c.HashBytes(goal)
	prdDigest := c.HashBytes(raw)
	prd, sectionRequirements := buildPRDSource(raw, c.HashBytes([]byte("candidate")), "PRD.md", "prd-source", prdDigest)
	check(prd)
	requirements := []c.Requirement{
		{ID: "user-goal", Source: c.SourceSpan{InputID: "initial", Start: 0, End: int64(len(goal))}, Required: true, Risk: "NORMAL", VerificationMethod: "UNRESOLVED_GOAL_COVERAGE"},
		{ID: "prd-source", Source: c.SourceSpan{InputID: "prd-source", Start: 0, End: int64(len(raw))}, Required: true, Risk: "NORMAL", VerificationMethod: "PRD_REQUIREMENT_COVERAGE"},
	}
	requirements = append(requirements, sectionRequirements...)
	spec := c.TaskSpec{
		SchemaVersion: 1,
		TaskID:        "prd-test",
		Version:       1,
		Goal:          string(goal),
		Inputs: []c.InputSource{
			{ID: "initial", PayloadRef: "blob://prd-test/goal", Digest: goalDigest, ByteLength: int64(len(goal)), Integrity: c.Intact},
			{ID: "prd-source", PayloadRef: "blob://prd-test/prd", Digest: prdDigest, ByteLength: int64(len(raw)), Integrity: c.Intact},
		},
		Requirements:    requirements,
		PRD:             &prd,
		ProtectedOrigin: c.HashBytes([]byte("origin")),
		DeliveryPolicy:  "CANDIDATE_ONLY",
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("explicit full-source requirement invalidated PRD: %v", err)
	}
}

func TestPRDReadAcceptsExactSourceFromCurrentCandidateAfterUnrelatedEdit(t *testing.T) {
	bytes := []byte("# Original PRD\r\nRequirements stay byte exact.\r\n")
	originalCandidate := c.HashBytes([]byte("original workspace"))
	currentCandidate := c.HashBytes([]byte("workspace with an unrelated edit"))
	prd, sectionRequirements := buildPRDSource(bytes, originalCandidate, "PRD.md", "prd-source", c.HashBytes(bytes))
	doc := Document{
		Spec:      c.TaskSpec{PRD: &prd, Requirements: sectionRequirements},
		Candidate: artifact.Ref{SnapshotDigest: currentCandidate},
	}
	receipt := prdReadReceipt{
		SchemaVersion: 1,
		Trust:         "UNTRUSTED_SOURCE",
		Candidate:     currentCandidate,
		Path:          prd.Path,
		Bytes:         append([]byte(nil), bytes...),
		TextValidUTF8: true,
		Coverage:      pageCoverage{Start: 0, End: int64(len(bytes)), Total: int64(len(bytes))},
		Read:          c.ReadCondition{Path: prd.Path, Kind: "FILE", Digest: prd.Digest},
	}
	replyBytes, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err = recordPRDRead(&doc, model.Reply{Content: string(replyBytes)}); err != nil {
		t.Fatalf("exact PRD read from current candidate was rejected: %v", err)
	}
	if !prdReadComplete(doc) || doc.PRDCoverage.ReadCandidateDigest != originalCandidate || doc.PRDCoverage.ReadDigest != prd.Digest {
		t.Fatalf("source-bound complete read was not retained: %#v", doc.PRDCoverage)
	}
}

func TestPRDReadRejectsStaleCandidateOrChangedPRD(t *testing.T) {
	bytes := []byte("# Original PRD\n")
	originalCandidate := c.HashBytes([]byte("original workspace"))
	currentCandidate := c.HashBytes([]byte("workspace with an unrelated edit"))
	prd, sectionRequirements := buildPRDSource(bytes, originalCandidate, "PRD.md", "prd-source", c.HashBytes(bytes))
	for _, test := range []struct {
		name       string
		candidate  string
		readDigest string
	}{
		{name: "candidate is not current", candidate: originalCandidate, readDigest: prd.Digest},
		{name: "PRD bytes changed", candidate: currentCandidate, readDigest: c.HashBytes([]byte("# Changed PRD\n"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc := Document{Spec: c.TaskSpec{PRD: &prd, Requirements: sectionRequirements}, Candidate: artifact.Ref{SnapshotDigest: currentCandidate}}
			receipt := prdReadReceipt{
				SchemaVersion: 1,
				Trust:         "UNTRUSTED_SOURCE",
				Candidate:     test.candidate,
				Path:          prd.Path,
				Bytes:         bytes,
				Coverage:      pageCoverage{Start: 0, End: int64(len(bytes)), Total: int64(len(bytes))},
				Read:          c.ReadCondition{Path: prd.Path, Kind: "FILE", Digest: test.readDigest},
			}
			replyBytes, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if err = recordPRDRead(&doc, model.Reply{Content: string(replyBytes)}); err == nil {
				t.Fatal("stale PRD provenance accepted")
			}
		})
	}
}
