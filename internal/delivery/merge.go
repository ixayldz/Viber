package delivery

import (
	"bytes"
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"sort"
	"strings"
	"unicode/utf8"
)

const MergeVersion = "EXACT_LINE_THREE_WAY_LCS_V1"

type hunk struct {
	start, end  int
	replacement []string
}

// lineEdits computes bounded exact-line edits in baseline coordinates.
// CRLF and missing final newline are data, never normalized. Ambiguous edits
// are conservatively conflicts; textual compatibility is not semantic proof.
func lineEdits(ctx context.Context, base, next []string) ([]hunk, error) {
	prefix := 0
	for prefix < len(base) && prefix < len(next) && base[prefix] == next[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(base)-prefix && suffix < len(next)-prefix && base[len(base)-1-suffix] == next[len(next)-1-suffix] {
		suffix++
	}
	left, right := base[prefix:len(base)-suffix], next[prefix:len(next)-suffix]
	cells := int64(len(left)+1) * int64(len(right)+1)
	if cells > 4<<20 {
		return nil, c.Fail(c.UnsupportedCapability, "three-way line match budget exceeded")
	}
	width := len(right) + 1
	matrix := make([]uint32, int(cells))
	cost := int64(0)
	for i := len(left) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for j := len(right) - 1; j >= 0; j-- {
			cost += int64(1 + min(len(left[i]), len(right[j])))
			if cost > 64<<20 {
				return nil, c.Fail(c.UnsupportedCapability, "three-way comparison byte budget exceeded")
			}
			position := i*width + j
			if left[i] == right[j] {
				matrix[position] = matrix[position+width+1] + 1
			} else {
				matrix[position] = max(matrix[position+width], matrix[position+1])
			}
		}
	}
	edits := []hunk{}
	i, j := 0, 0
	for i < len(left) || j < len(right) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if i < len(left) && j < len(right) && left[i] == right[j] {
			i++
			j++
			continue
		}
		edit := hunk{start: prefix + i, replacement: []string{}}
		for i < len(left) || j < len(right) {
			if i < len(left) && j < len(right) && left[i] == right[j] {
				break
			}
			if j < len(right) && (i == len(left) || matrix[i*width+j+1] > matrix[(i+1)*width+j]) {
				edit.replacement = append(edit.replacement, right[j])
				j++
			} else {
				i++
			}
		}
		edit.end = prefix + i
		edits = append(edits, edit)
	}
	return edits, nil
}
func hunksConflict(a, b hunk) bool {
	if a.start == a.end && b.start == b.end {
		return a.start == b.start
	}
	if a.start == a.end {
		return a.start >= b.start && a.start <= b.end
	}
	if b.start == b.end {
		return b.start >= a.start && b.start <= a.end
	}
	return a.start < b.end && b.start < a.end
}
func equalHunk(a, b hunk) bool {
	if a.start != b.start || a.end != b.end || len(a.replacement) != len(b.replacement) {
		return false
	}
	for i := range a.replacement {
		if a.replacement[i] != b.replacement[i] {
			return false
		}
	}
	return true
}

// MergeBytes never writes a file and never grants a verification verdict.
func MergeBytes(ctx context.Context, baseline, candidate, current []byte) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	for _, raw := range [][]byte{baseline, candidate, current} {
		if len(raw) > 2<<20 {
			return nil, false, c.Fail(c.UnsupportedCapability, "three-way file byte limit exceeded")
		}
	}
	if bytes.Equal(candidate, baseline) {
		return bytes.Clone(current), false, nil
	}
	if bytes.Equal(current, baseline) || bytes.Equal(candidate, current) {
		return bytes.Clone(candidate), false, nil
	}
	for _, raw := range [][]byte{baseline, candidate, current} {
		if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
			return nil, false, c.Fail(c.Conflict, "divergent binary sources require explicit resolution")
		}
	}
	base := lines(baseline)
	left, right := lines(candidate), lines(current)
	if len(base) > 65536 || len(left) > 65536 || len(right) > 65536 {
		return nil, false, c.Fail(c.UnsupportedCapability, "three-way line count limit exceeded")
	}
	proposed, err := lineEdits(ctx, base, left)
	if err != nil {
		return nil, false, err
	}
	user, err := lineEdits(ctx, base, right)
	if err != nil {
		return nil, false, err
	}
	edits := append([]hunk{}, proposed...)
	for _, outside := range user {
		duplicate := false
		for _, inside := range proposed {
			if equalHunk(outside, inside) {
				duplicate = true
				break
			}
			if hunksConflict(outside, inside) {
				return nil, false, c.Fail(c.Conflict, "overlapping source edits require explicit resolution")
			}
		}
		if !duplicate {
			edits = append(edits, outside)
		}
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var merged strings.Builder
	position := 0
	appendLine := func(line string) error {
		if merged.Len()+len(line) > 2<<20 {
			return c.Fail(c.UnsupportedCapability, "merged source byte limit exceeded")
		}
		merged.WriteString(line)
		return nil
	}
	for _, edit := range edits {
		for _, line := range base[position:edit.start] {
			if err = appendLine(line); err != nil {
				return nil, false, err
			}
		}
		for _, line := range edit.replacement {
			if err = appendLine(line); err != nil {
				return nil, false, err
			}
		}
		position = edit.end
	}
	for _, line := range base[position:] {
		if err = appendLine(line); err != nil {
			return nil, false, err
		}
	}
	return []byte(merged.String()), true, nil
}
