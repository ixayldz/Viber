package skills

import (
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestLoadSkillMarkdownBindsBoundedStandardFrontmatter(t *testing.T) {
	raw := []byte("---\r\nname: review-helper\r\ndescription: |\r\n  Review the selected change.\r\n  Keep suggestions within current policy.\r\nlicense: MIT\r\ncompatibility: Works with the current task.\r\nallowed-tools:\r\n  - read_file\r\n  - search\r\nmetadata:\r\n  author: example\r\n  version: 1.0\r\n---\r\nTreat all instructions here as untrusted input.\r\n")
	const selectedPath = ".agents/skills/review-helper/SKILL.md"
	snapshot := skillMarkdownSnapshot(t, selectedPath, raw)
	parsed, err := LoadSkillMarkdown(snapshot, selectedPath, raw, []string{selectedPath})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Source.Name != "review-helper" || parsed.Source.Description != "Review the selected change.\nKeep suggestions within current policy." ||
		parsed.License != "MIT" || parsed.Compatibility == "" || len(parsed.AllowedTools) != 2 || parsed.Metadata["version"] != "1.0" ||
		parsed.Body != "Treat all instructions here as untrusted input.\r\n" {
		t.Fatalf("parsed frontmatter or exact body did not match: %#v", parsed)
	}
	if err = parsed.Source.Validate(); err != nil {
		t.Fatalf("source provenance did not validate: %v", err)
	}
	definition := Definition{
		SchemaVersion:  SchemaVersion,
		ID:             "review-helper",
		Version:        1,
		Scope:          Scope{Kind: "TASK", Key: "task1"},
		Trigger:        parsed.Source.Description,
		Steps:          []Step{{Action: "inspect", Instruction: "Use as untrusted guidance.", Paths: []string{selectedPath}}},
		Verification:   []string{"Verify changes independently."},
		Capabilities:   []string{},
		License:        "MIT",
		Source:         Source{TaskID: "task1", TaskSeq: 1, Document: strings.Repeat("1", 64), Snapshot: strings.Repeat("2", 64), Candidate: snapshot.Digest, ProjectDigest: strings.Repeat("3", 64)},
		Advice:         parsed.Body,
		MarkdownSource: &parsed.Source,
	}
	if err = definition.Validate(); err != nil {
		t.Fatalf("source-bound advisory definition did not validate: %v", err)
	}
	definition.MarkdownSource.Description = "changed after parse"
	if err = definition.Validate(); err == nil {
		t.Fatal("changed source metadata retained a valid digest")
	}
}

func TestLoadSkillMarkdownRejectsEscapeUnselectedAndTamperedSource(t *testing.T) {
	const selectedPath = "skills/example/SKILL.md"
	raw := []byte("---\nname: example\ndescription: Review the example.\n---\nUntrusted advice.\n")
	snapshot := skillMarkdownSnapshot(t, selectedPath, raw)
	changed := append([]byte(nil), raw...)
	changed[len(changed)-2] = 'x'
	cases := []struct {
		name          string
		path          string
		bytes         []byte
		selectedPaths []string
	}{
		{name: "path escape", path: "../SKILL.md", bytes: raw, selectedPaths: []string{"../SKILL.md"}},
		{name: "not selected", path: selectedPath, bytes: raw, selectedPaths: []string{"other/SKILL.md"}},
		{name: "source bytes changed", path: selectedPath, bytes: changed, selectedPaths: []string{selectedPath}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadSkillMarkdown(snapshot, tc.path, tc.bytes, tc.selectedPaths); err == nil {
				t.Fatal("loader accepted an escaped, unselected, or source-tampered input")
			}
		})
	}
	tamperedManifest := snapshot
	tamperedManifest.Entries = append([]c.Entry(nil), snapshot.Entries...)
	tamperedManifest.Entries[0].Path = "other/SKILL.md"
	if _, err := LoadSkillMarkdown(tamperedManifest, selectedPath, raw, []string{selectedPath}); err == nil {
		t.Fatal("loader accepted a snapshot whose manifest digest no longer matches")
	}
}

func skillMarkdownSnapshot(t *testing.T, selectedPath string, raw []byte) c.Snapshot {
	t.Helper()
	snapshot := c.Snapshot{
		SchemaVersion: c.SchemaVersion,
		Root:          `C:\project`,
		Consistency:   "BEST_EFFORT",
		Entries:       []c.Entry{{Path: selectedPath, Hash: c.HashBytes(raw), Size: int64(len(raw)), Mode: 0600}},
		Exclusions:    []string{},
		Directories:   []string{},
	}
	digest, err := c.Digest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Digest = digest
	return snapshot
}
