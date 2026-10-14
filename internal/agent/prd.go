package agent

import (
	"bytes"
	"sort"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

const (
	maxPRDSections  = 128
	maxPRDReadPages = 1024
)

type markdownHeading struct {
	Start   int64
	Level   int
	Heading string
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
	headings := markdownHeadings(raw)
	source.HeadingCount = len(headings)
	if len(headings) == 0 {
		source.SectionExtraction = "NO_HEADINGS"
		return source, nil
	}
	limit := len(headings)
	if limit > maxPRDSections {
		limit = maxPRDSections
		source.OmittedHeadingCount = len(headings) - limit
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
		if len(heading.Heading) > 2048 {
			source.SectionExtraction = "REVIEW_REQUIRED"
			source.UnsupportedHeadingCount++
			// The source span is retained in full; only the advisory label is
			// bounded so a long line cannot become control metadata.
			end := 2048
			for end > 0 && !utf8.ValidString(heading.Heading[:end]) {
				end--
			}
			heading.Heading = heading.Heading[:end]
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

func markdownHeadings(raw []byte) []markdownHeading {
	type line struct {
		start int64
		text  []byte
	}
	lines := make([]line, 0, 256)
	for offset := 0; offset < len(raw); {
		end := bytes.IndexByte(raw[offset:], '\n')
		lineEnd := len(raw)
		advance := len(raw) - offset
		if end >= 0 {
			lineEnd = offset + end
			advance = end + 1
		}
		text := raw[offset:lineEnd]
		if len(text) > 0 && text[len(text)-1] == '\r' {
			text = text[:len(text)-1]
		}
		lines = append(lines, line{start: int64(offset), text: append([]byte(nil), text...)})
		offset += advance
	}
	if len(raw) == 0 || len(lines) == 0 {
		return nil
	}
	headings := make([]markdownHeading, 0, 32)
	seen := map[int64]bool{}
	var fence byte
	fenceLength := 0
	previousFenced := false
	for i, current := range lines {
		trimmed := bytes.TrimLeft(current.text, " ")
		indented := len(current.text)-len(trimmed) > 3
		if !indented && len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
			marker, count := trimmed[0], 0
			for count < len(trimmed) && trimmed[count] == marker {
				count++
			}
			if count >= 3 {
				if fence == 0 {
					fence, fenceLength, previousFenced = marker, count, true
					continue
				}
				if marker == fence && count >= fenceLength && len(bytes.TrimSpace(trimmed[count:])) == 0 {
					fence, fenceLength, previousFenced = 0, 0, true
					continue
				}
			}
		}
		if fence != 0 || indented || len(current.text) > 0 && current.text[0] == '\t' {
			previousFenced = true
			continue
		}
		wasFenced := previousFenced
		previousFenced = false
		if heading, ok := atxHeading(current.text); ok {
			headings = append(headings, markdownHeading{Start: current.start, Level: heading.Level, Heading: heading.Heading})
			seen[current.start] = true
			continue
		}
		// Setext headings are bounded and unambiguous when the preceding line
		// is non-empty. They share the same byte-span contract as ATX headings.
		if i == 0 || wasFenced || len(bytes.TrimSpace(current.text)) == 0 {
			continue
		}
		underline := strings.TrimSpace(string(current.text))
		level := 0
		if allRunes(underline, '=') {
			level = 1
		} else if allRunes(underline, '-') {
			level = 2
		}
		if level != 0 && !seen[lines[i-1].start] && len(bytes.TrimSpace(lines[i-1].text)) > 0 {
			headings = append(headings, markdownHeading{Start: lines[i-1].start, Level: level, Heading: strings.TrimSpace(string(lines[i-1].text))})
			seen[lines[i-1].start] = true
		}
	}
	sort.Slice(headings, func(i, j int) bool { return headings[i].Start < headings[j].Start })
	return headings
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
	text := strings.TrimSpace(string(trimmed[level:]))
	text = strings.TrimRight(text, "# \t")
	return markdownHeading{Level: level, Heading: text}, true
}

func allRunes(value string, expected rune) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r != expected {
			return false
		}
	}
	return true
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
	if receipt.SchemaVersion != 1 || receipt.Trust != "UNTRUSTED_SOURCE" || receipt.Candidate != prd.CandidateDigest || receipt.Path != prd.Path || receipt.Read.Path != prd.Path || receipt.Read.Kind != "FILE" || receipt.Read.Digest != prd.Digest || receipt.Coverage.Total != prd.ByteLength || receipt.Coverage.Start < 0 || receipt.Coverage.End <= receipt.Coverage.Start || receipt.Coverage.End > receipt.Coverage.Total || int64(len(receipt.Bytes)) != receipt.Coverage.End-receipt.Coverage.Start {
		return c.Fail(c.StaleBase, "PRD read receipt is not bound to the exact source candidate")
	}
	if doc.PRDCoverage == nil {
		refreshPRDCoverage(doc)
	}
	coverage := doc.PRDCoverage
	if coverage.ReadCandidateDigest != "" && coverage.ReadCandidateDigest != receipt.Candidate || coverage.ReadDigest != "" && coverage.ReadDigest != receipt.Read.Digest {
		return c.Fail(c.StaleBase, "PRD read coverage source changed")
	}
	intervals := append(append([]c.PRDReadInterval(nil), coverage.ReadIntervals...), c.PRDReadInterval{Start: receipt.Coverage.Start, End: receipt.Coverage.End})
	intervals = mergePRDIntervals(intervals)
	if len(intervals) > maxPRDReadPages {
		return c.Fail(c.BudgetLimitReached, "PRD read coverage page bound exceeded")
	}
	coverage.ReadCandidateDigest = receipt.Candidate
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
