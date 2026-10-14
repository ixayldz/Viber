package agent

import (
	"bytes"
	"sort"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

const (
	maxPRDSections          = 128
	maxPRDReadPages         = 1024
	maxPRDHeadingLabelBytes = 2048
)

type markdownHeading struct {
	Start          int64
	Level          int
	Heading        string
	HeadingTooLong bool
}

// buildPRDSource extracts only deterministic byte spans. It does not infer
// requirements from prose. The complete raw PRD remains the authoritative
// input; section extraction is an aid for plan coverage and can explicitly
// require review when its bounded parser cannot represent the document.
func buildPRDSource(raw []byte, candidate, path, inputID, digest string) (c.PRDSource, []c.Requirement) {
	source := c.PRDSource{SchemaVersion: c.SchemaVersion, CandidateDigest: candidate, Path: path, InputID: inputID, Digest: digest, ByteLength: int64(len(raw)), Sections: []c.PRDSection{}}
	if !utf8.Valid(raw) {
		source.SectionExtraction = "REVIEW_REQUIRED"
		source.UnsupportedHeadingCount = 1
		return source, nil
	}
	headings, headingCount := markdownHeadings(raw)
	source.HeadingCount = headingCount
	if headingCount == 0 {
		source.SectionExtraction = "NO_HEADINGS"
		return source, nil
	}
	limit := len(headings)
	if headingCount > maxPRDSections {
		limit = maxPRDSections
		source.OmittedHeadingCount = headingCount - limit
		source.SectionExtraction = "REVIEW_REQUIRED"
	} else {
		source.SectionExtraction = "COMPLETE"
	}
	requirements := make([]c.Requirement, 0, limit)
	for i := 0; i < limit; i++ {
		end := int64(len(raw))
		if i+1 < len(headings) {
			end = headings[i+1].Start
		}
		heading := headings[i]
		if end <= heading.Start {
			source.SectionExtraction = "REVIEW_REQUIRED"
			source.UnsupportedHeadingCount++
			continue
		}
		if heading.HeadingTooLong || len(heading.Heading) > maxPRDHeadingLabelBytes {
			source.SectionExtraction = "REVIEW_REQUIRED"
			source.UnsupportedHeadingCount++
		}
		id := "prd-section-" + ordinal(i+1)
		source.Sections = append(source.Sections, c.PRDSection{ID: id, Start: heading.Start, End: end, Level: heading.Level, Heading: heading.Heading})
		requirements = append(requirements, c.Requirement{ID: id, Source: c.SourceSpan{InputID: inputID, Start: heading.Start, End: end}, Required: true, Risk: "NORMAL", VerificationMethod: "PRD_SECTION_COVERAGE"})
	}
	if len(source.Sections) != limit {
		source.UnsupportedHeadingCount += limit - len(source.Sections)
		source.SectionExtraction = "REVIEW_REQUIRED"
	}
	return source, requirements
}

func markdownHeadings(raw []byte) ([]markdownHeading, int) {
	type priorLine struct {
		start    int64
		text     []byte
		eligible bool
	}
	// One look-ahead heading is enough to close the final retained section.
	// Count the full document while retaining only this bounded prefix.
	headings := make([]markdownHeading, 0, maxPRDSections+1)
	headingCount := 0
	appendHeading := func(heading markdownHeading) {
		headingCount++
		if len(headings) < maxPRDSections+1 {
			headings = append(headings, heading)
		}
	}
	var previous priorLine
	var fence byte
	fenceLength := 0
	for offset := 0; offset < len(raw); {
		lineStart := offset
		lineEnd := len(raw)
		next := len(raw)
		if newline := bytes.IndexByte(raw[offset:], '\n'); newline >= 0 {
			lineEnd = offset + newline
			next = lineEnd + 1
		}
		if lineEnd > lineStart && raw[lineEnd-1] == '\r' {
			lineEnd--
		}
		text := raw[lineStart:lineEnd]
		trimmed := bytes.TrimLeft(text, " ")
		indent := len(text) - len(trimmed)
		marker, count := fenceLine(trimmed, indent)
		if fence != 0 {
			if marker == fence && count >= fenceLength && len(bytes.TrimSpace(trimmed[count:])) == 0 {
				fence, fenceLength = 0, 0
			}
			previous = priorLine{}
			offset = next
			continue
		}
		if indent <= 3 && count >= 3 {
			fence, fenceLength = marker, count
			previous = priorLine{}
			offset = next
			continue
		}
		if indent > 3 || len(text) > 0 && text[0] == '\t' {
			previous = priorLine{}
			offset = next
			continue
		}
		if heading, ok := atxHeading(text); ok {
			heading.Start = int64(lineStart)
			appendHeading(heading)
			previous = priorLine{}
			offset = next
			continue
		}
		if level := setextHeadingLevel(text); level != 0 && previous.eligible {
			label, tooLong := boundedHeading(previous.text)
			appendHeading(markdownHeading{Start: previous.start, Level: level, Heading: label, HeadingTooLong: tooLong})
			previous = priorLine{}
			offset = next
			continue
		}
		if len(bytes.TrimSpace(text)) > 0 {
			previous = priorLine{start: int64(lineStart), text: text, eligible: true}
		} else {
			previous = priorLine{}
		}
		offset = next
	}
	return headings, headingCount
}

func atxHeading(raw []byte) (markdownHeading, bool) {
	trimmed := raw
	spaces := 0
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t') && spaces < 4 {
		trimmed = trimmed[1:]
		spaces++
	}
	if spaces > 3 || len(trimmed) < 2 || trimmed[0] != '#' {
		return markdownHeading{}, false
	}
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level < 1 || level > 6 || level == len(trimmed) || (trimmed[level] != ' ' && trimmed[level] != '\t') {
		return markdownHeading{}, false
	}
	text := bytes.TrimSpace(trimmed[level:])
	text = bytes.TrimRight(text, "# \t")
	label, tooLong := boundedHeading(text)
	return markdownHeading{Level: level, Heading: label, HeadingTooLong: tooLong}, true
}

func fenceLine(trimmed []byte, indent int) (byte, int) {
	if indent > 3 || len(trimmed) < 3 || trimmed[0] != '`' && trimmed[0] != '~' {
		return 0, 0
	}
	marker, count := trimmed[0], 0
	for count < len(trimmed) && trimmed[count] == marker {
		count++
	}
	return marker, count
}

func setextHeadingLevel(raw []byte) int {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0
	}
	marker := trimmed[0]
	if marker != '=' && marker != '-' {
		return 0
	}
	for _, b := range trimmed[1:] {
		if b != marker {
			return 0
		}
	}
	if marker == '=' {
		return 1
	}
	return 2
}

func boundedHeading(raw []byte) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	tooLong := len(trimmed) > maxPRDHeadingLabelBytes
	if tooLong {
		trimmed = trimmed[:maxPRDHeadingLabelBytes]
		for len(trimmed) > 0 && !utf8.Valid(trimmed) {
			trimmed = trimmed[:len(trimmed)-1]
		}
	}
	return string(trimmed), tooLong
}

func ordinal(value int) string {
	if value < 10 {
		return "000" + string(rune('0'+value))
	}
	if value < 100 {
		return "00" + string(rune('0'+value/10)) + string(rune('0'+value%10))
	}
	if value < 1000 {
		return "0" + string(rune('0'+value/100)) + string(rune('0'+(value/10)%10)) + string(rune('0'+value%10))
	}
	return string([]byte{byte('0' + value/1000), byte('0' + (value/100)%10), byte('0' + (value/10)%10), byte('0' + value%10)})
}

type prdReadReceipt struct {
	SchemaVersion int             `json:"schema_version"`
	Trust         string          `json:"trust"`
	Candidate     string          `json:"candidate"`
	Path          string          `json:"path"`
	Bytes         []byte          `json:"exact_bytes"`
	Content       *string         `json:"content,omitempty"`
	TextValidUTF8 bool            `json:"text_valid_utf8"`
	Coverage      pageCoverage    `json:"coverage"`
	Read          c.ReadCondition `json:"read_condition"`
}

func recordPRDRead(doc *Document, reply model.Reply) error {
	if doc.Spec.PRD == nil {
		return nil
	}
	var receipt prdReadReceipt
	if err := c.DecodeStrict([]byte(reply.Content), &receipt); err != nil {
		return c.Fail(c.StoreIntegrityError, "successful PRD read returned an invalid receipt")
	}
	prd := doc.Spec.PRD
	if receipt.Path != prd.Path {
		return nil // Ordinary successful reads do not contribute PRD coverage.
	}
	if receipt.SchemaVersion != 1 || receipt.Trust != "UNTRUSTED_SOURCE" || !c.ValidDigest(receipt.Candidate) || receipt.Candidate != doc.Candidate.SnapshotDigest || receipt.Path != prd.Path || receipt.Read.Path != prd.Path || receipt.Read.Kind != "FILE" || receipt.Read.Digest != prd.Digest || receipt.Coverage.Total != prd.ByteLength || receipt.Coverage.Start < 0 || receipt.Coverage.End <= receipt.Coverage.Start || receipt.Coverage.End > receipt.Coverage.Total || int64(len(receipt.Bytes)) != receipt.Coverage.End-receipt.Coverage.Start {
		return c.Fail(c.StaleBase, "PRD read receipt is not bound to the exact source candidate")
	}
	if doc.PRDCoverage == nil {
		refreshPRDCoverage(doc)
	}
	coverage := doc.PRDCoverage
	if coverage.ReadCandidateDigest != "" && coverage.ReadCandidateDigest != prd.CandidateDigest || coverage.ReadDigest != "" && coverage.ReadDigest != receipt.Read.Digest {
		return c.Fail(c.StaleBase, "PRD read coverage source changed")
	}
	intervals := append(append([]c.PRDReadInterval(nil), coverage.ReadIntervals...), c.PRDReadInterval{Start: receipt.Coverage.Start, End: receipt.Coverage.End})
	intervals = mergePRDIntervals(intervals)
	if len(intervals) > maxPRDReadPages {
		return c.Fail(c.BudgetLimitReached, "PRD read coverage page bound exceeded")
	}
	// The page was authorized against the current candidate above. Persist the
	// original PRD candidate binding so unrelated candidate edits do not make
	// otherwise exact PRD bytes stale across pages or restores.
	coverage.ReadCandidateDigest = prd.CandidateDigest
	coverage.ReadDigest = receipt.Read.Digest
	coverage.ReadTotalBytes = receipt.Coverage.Total
	coverage.ReadIntervals = intervals
	refreshPRDReadStatus(prd, coverage)
	return nil
}

func mergePRDIntervals(intervals []c.PRDReadInterval) []c.PRDReadInterval {
	if len(intervals) == 0 {
		return []c.PRDReadInterval{}
	}
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i].Start != intervals[j].Start {
			return intervals[i].Start < intervals[j].Start
		}
		return intervals[i].End < intervals[j].End
	})
	merged := make([]c.PRDReadInterval, 0, len(intervals))
	for _, current := range intervals {
		if current.End <= current.Start {
			continue
		}
		if len(merged) == 0 || current.Start > merged[len(merged)-1].End {
			merged = append(merged, current)
			continue
		}
		if current.End > merged[len(merged)-1].End {
			merged[len(merged)-1].End = current.End
		}
	}
	return merged
}

func refreshPRDReadStatus(prd *c.PRDSource, coverage *c.PRDCoverage) {
	coverage.ReadTotalBytes = prd.ByteLength
	coverage.ReadBytes = 0
	coverage.CoveredSectionIDs = []string{}
	if len(coverage.ReadIntervals) == 0 && coverage.ReadCandidateDigest == "" && coverage.ReadDigest == "" {
		coverage.ReadStatus = "NOT_READ"
		return
	}
	if coverage.ReadCandidateDigest != prd.CandidateDigest || coverage.ReadDigest != prd.Digest {
		coverage.ReadStatus = "STALE"
		return
	}
	for _, interval := range coverage.ReadIntervals {
		if interval.Start < 0 || interval.End <= interval.Start || interval.End > prd.ByteLength {
			coverage.ReadStatus = "INVALID"
			return
		}
		coverage.ReadBytes += interval.End - interval.Start
	}
	if coverage.ReadBytes == 0 {
		coverage.ReadStatus = "NOT_READ"
	} else if coverage.ReadBytes == prd.ByteLength && len(coverage.ReadIntervals) == 1 && coverage.ReadIntervals[0].Start == 0 && coverage.ReadIntervals[0].End == prd.ByteLength {
		coverage.ReadStatus = "COMPLETE"
	} else {
		coverage.ReadStatus = "PARTIAL"
	}
	for _, section := range prd.Sections {
		for _, interval := range coverage.ReadIntervals {
			if interval.Start <= section.Start && interval.End >= section.End {
				coverage.CoveredSectionIDs = append(coverage.CoveredSectionIDs, section.ID)
				break
			}
		}
	}
}

func prdReadComplete(doc Document) bool {
	return doc.Spec.PRD != nil && doc.PRDCoverage != nil && doc.PRDCoverage.ReadStatus == "COMPLETE" && doc.PRDCoverage.ReadCandidateDigest == doc.Spec.PRD.CandidateDigest && doc.PRDCoverage.ReadDigest == doc.Spec.PRD.Digest
}

func validatePRDReadState(prd *c.PRDSource, coverage *c.PRDCoverage) bool {
	if prd == nil || coverage == nil || len(coverage.ReadIntervals) > maxPRDReadPages {
		return false
	}
	if coverage.ReadTotalBytes != prd.ByteLength {
		return false
	}
	if len(coverage.ReadIntervals) == 0 {
		return coverage.ReadStatus == "NOT_READ" && coverage.ReadBytes == 0 && coverage.ReadCandidateDigest == "" && coverage.ReadDigest == ""
	}
	if coverage.ReadCandidateDigest != prd.CandidateDigest || coverage.ReadDigest != prd.Digest {
		return false
	}
	merged := mergePRDIntervals(append([]c.PRDReadInterval(nil), coverage.ReadIntervals...))
	if len(merged) != len(coverage.ReadIntervals) {
		return false
	}
	bytesRead := int64(0)
	for i, interval := range coverage.ReadIntervals {
		if interval.Start < 0 || interval.End <= interval.Start || interval.End > prd.ByteLength || i > 0 && interval.Start <= coverage.ReadIntervals[i-1].End {
			return false
		}
		bytesRead += interval.End - interval.Start
	}
	if bytesRead != coverage.ReadBytes {
		return false
	}
	if coverage.ReadStatus != "PARTIAL" && coverage.ReadStatus != "COMPLETE" {
		return false
	}
	complete := len(coverage.ReadIntervals) == 1 && coverage.ReadIntervals[0].Start == 0 && coverage.ReadIntervals[0].End == prd.ByteLength
	return (coverage.ReadStatus == "COMPLETE") == complete
}
