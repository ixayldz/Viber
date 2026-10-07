// Package delivery builds candidate-bound review artifacts. It never applies a
// patch to a live workspace and cannot promote verification quality.
package delivery

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/workspace"
)

type Change struct {
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	BeforeDigest string `json:"before_digest"`
	AfterDigest  string `json:"after_digest"`
	BeforeMode   uint32 `json:"before_mode"`
	AfterMode    uint32 `json:"after_mode"`
	TextPatch    bool   `json:"text_patch"`
}
type Changeset struct {
	SchemaVersion int      `json:"schema_version"`
	Baseline      string   `json:"baseline"`
	Candidate     string   `json:"candidate"`
	Changes       []Change `json:"changes"`
	PatchComplete bool     `json:"patch_complete"`
}

func modes(capture workspace.Capture) map[string]uint32 {
	result := map[string]uint32{}
	for _, e := range capture.Snapshot.Entries {
		result[e.Path] = e.Mode
	}
	if capture.Snapshot.Git != nil {
		for _, e := range capture.Snapshot.Git.Entries {
			if _, ok := result[e.Path]; ok {
				result[e.Path] = e.Mode & 0777
			}
		}
	}
	return result
}
func Build(before, after workspace.Capture) (Changeset, []byte, error) {
	result := Changeset{SchemaVersion: 1, Baseline: before.Snapshot.Digest, Candidate: after.Snapshot.Digest, Changes: []Change{}, PatchComplete: true}
	if err := workspace.VerifyCapture(before); err != nil {
		return result, nil, err
	}
	if err := workspace.VerifyCapture(after); err != nil {
		return result, nil, err
	}
	if before.Snapshot.Root != after.Snapshot.Root {
		return result, nil, c.Fail(c.Conflict, "changeset source identities differ")
	}
	oldModes, newModes := modes(before), modes(after)
	paths := map[string]bool{}
	for name := range before.Contents {
		paths[name] = true
	}
	for name := range after.Contents {
		paths[name] = true
	}
	ordered := make([]string, 0, len(paths))
	for name := range paths {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	var patch bytes.Buffer
	for _, name := range ordered {
		old, oldOK := before.Contents[name]
		next, nextOK := after.Contents[name]
		if oldOK == nextOK && bytes.Equal(old, next) && oldModes[name] == newModes[name] {
			continue
		}
		change := Change{Path: name, Kind: "MODIFIED", BeforeMode: oldModes[name], AfterMode: newModes[name], TextPatch: utf8.Valid(old) && utf8.Valid(next) && !bytes.ContainsRune(old, 0) && !bytes.ContainsRune(next, 0)}
		if oldOK {
			change.BeforeDigest = c.HashBytes(old)
		} else {
			change.Kind = "ADDED"
		}
		if nextOK {
			change.AfterDigest = c.HashBytes(next)
		} else {
			change.Kind = "DELETED"
		}
		result.Changes = append(result.Changes, change)
		if !change.TextPatch {
			result.PatchComplete = false
			continue
		}
		writePatch(&patch, change, old, next)
		if patch.Len() > 64<<20 {
			return result, nil, c.Fail(c.UnsupportedCapability, "text patch exceeds export limit")
		}
	}
	return result, patch.Bytes(), nil
}
func quotePath(name string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, b := range []byte(name) {
		if b < 32 || b >= 127 {
			fmt.Fprintf(&out, "\\%03o", b)
		} else if b == '"' || b == '\\' {
			out.WriteByte('\\')
			out.WriteByte(b)
		} else {
			out.WriteByte(b)
		}
	}
	out.WriteByte('"')
	return out.String()
}
func gitMode(mode uint32) uint32 {
	if mode&0111 != 0 {
		return 0100755
	}
	return 0100644
}
func lines(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	result := strings.SplitAfter(string(raw), "\n")
	if result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}
	return result
}
func writePatch(out *bytes.Buffer, change Change, old, next []byte) {
	a, b := quotePath("a/"+change.Path), quotePath("b/"+change.Path)
	fmt.Fprintf(out, "diff --git %s %s\n", a, b)
	switch change.Kind {
	case "ADDED":
		fmt.Fprintf(out, "new file mode %06o\n", gitMode(change.AfterMode))
	case "DELETED":
		fmt.Fprintf(out, "deleted file mode %06o\n", gitMode(change.BeforeMode))
	default:
		if gitMode(change.BeforeMode) != gitMode(change.AfterMode) {
			fmt.Fprintf(out, "old mode %06o\nnew mode %06o\n", gitMode(change.BeforeMode), gitMode(change.AfterMode))
		}
	}
	if bytes.Equal(old, next) {
		return
	}
	if change.Kind == "ADDED" {
		a = "/dev/null"
	}
	if change.Kind == "DELETED" {
		b = "/dev/null"
	}
	fmt.Fprintf(out, "--- %s\n+++ %s\n", a, b)
	left, right := lines(old), lines(next)
	prefix := 0
	for prefix < len(left) && prefix < len(right) && left[prefix] == right[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(left)-prefix && suffix < len(right)-prefix && left[len(left)-1-suffix] == right[len(right)-1-suffix] {
		suffix++
	}
	start := prefix - 3
	if start < 0 {
		start = 0
	}
	contextTail := suffix
	if contextTail > 3 {
		contextTail = 3
	}
	oldEnd, newEnd := len(left)-suffix+contextTail, len(right)-suffix+contextTail
	oldCount, newCount := oldEnd-start, newEnd-start
	oldStart, newStart := start+1, start+1
	if oldCount == 0 {
		oldStart = start
	}
	if newCount == 0 {
		newStart = start
	}
	fmt.Fprintf(out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
	emit := func(marker byte, line string) {
		out.WriteByte(marker)
		out.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			out.WriteString("\n\\ No newline at end of file\n")
		}
	}
	for _, line := range left[start:prefix] {
		emit(' ', line)
	}
	for _, line := range left[prefix : len(left)-suffix] {
		emit('-', line)
	}
	for _, line := range right[prefix : len(right)-suffix] {
		emit('+', line)
	}
	for _, line := range left[len(left)-suffix : oldEnd] {
		emit(' ', line)
	}
}
