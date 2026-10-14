package skills

import (
	"bytes"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
)

var skillMetadataKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// SkillMarkdown is a source-bound, bounded parse of one explicitly selected
// SKILL.md file. Body and metadata are inert advice; this loader never opens
// sibling files, scripts, or referenced resources.
type SkillMarkdown struct {
	Source        MarkdownSource    `json:"source"`
	Body          string            `json:"untrusted_body"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	AllowedTools  []string          `json:"allowed_tools,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type markdownManifestBody struct {
	SchemaVersion        int               `json:"schema_version"`
	SourceManifestDigest string            `json:"source_manifest_digest"`
	Path                 string            `json:"path"`
	FileDigest           string            `json:"file_digest"`
	Name                 string            `json:"name"`
	Description          string            `json:"description"`
	BodyDigest           string            `json:"body_digest"`
	License              string            `json:"license,omitempty"`
	Compatibility        string            `json:"compatibility,omitempty"`
	AllowedTools         []string          `json:"allowed_tools,omitempty"`
	Metadata             map[string]string `json:"metadata,omitempty"`
}

type parsedSkillFrontmatter struct {
	name          string
	description   string
	license       string
	compatibility string
	allowedTools  []string
	metadata      map[string]string
}

// LoadSkillMarkdown parses one exact SKILL.md path selected by the caller.
// The caller must obtain raw through its current policy-admitted source read;
// selectedPaths prevents this loader from discovering or widening that scope.
// snapshot is the retained source manifest from which raw was read.
// The parser supports a bounded YAML subset: scalar name/description,
// literal/folded description blocks, and optional license, compatibility,
// allowed-tools, and simple metadata mappings. Unknown forms are rejected;
// allowed-tools is preserved as inert metadata and never grants capabilities.
func LoadSkillMarkdown(snapshot c.Snapshot, selectedPath string, raw []byte, selectedPaths []string) (SkillMarkdown, error) {
	var empty SkillMarkdown
	if snapshot.SchemaVersion != c.SchemaVersion || snapshot.Consistency != "BEST_EFFORT" || !c.ValidDigest(snapshot.Digest) ||
		len(snapshot.Entries) > 100000 || !snapshotDigestValid(snapshot) || len(selectedPaths) == 0 || len(selectedPaths) > MaxSkillMarkdownPaths ||
		len(raw) == 0 || len(raw) > MaxSkillMarkdownBytes || !utf8.Valid(raw) ||
		len(selectedPath) > MaxSkillMarkdownPathBytes || !policy.SafePath(selectedPath) || path.Base(selectedPath) != "SKILL.md" || !selectedSkillPath(selectedPath, selectedPaths) {
		return empty, c.Fail(c.InvalidArgument, "bounded selected source SKILL.md path required")
	}
	var entry *c.Entry
	for i := range snapshot.Entries {
		candidate := &snapshot.Entries[i]
		if candidate.Path != selectedPath {
			continue
		}
		if entry != nil {
			return empty, c.Fail(c.StoreIntegrityError, "source manifest repeats the selected SKILL.md path")
		}
		entry = candidate
	}
	if entry == nil || entry.Size != int64(len(raw)) || !c.ValidDigest(entry.Hash) || c.HashBytes(raw) != entry.Hash {
		return empty, c.Fail(c.StaleBase, "selected SKILL.md bytes do not match the retained source manifest")
	}
	frontmatter, body, err := parseSkillMarkdown(raw)
	if err != nil {
		return empty, err
	}
	bodyDigest := c.HashBytes(body)
	source := MarkdownSource{
		SchemaVersion:        SchemaVersion,
		SourceManifestDigest: snapshot.Digest,
		Path:                 selectedPath,
		FileDigest:           entry.Hash,
		Name:                 frontmatter.name,
		Description:          frontmatter.description,
		BodyDigest:           bodyDigest,
		License:              frontmatter.license,
		Compatibility:        frontmatter.compatibility,
		AllowedTools:         frontmatter.allowedTools,
		Metadata:             frontmatter.metadata,
	}
	source.ManifestDigest, err = markdownManifestDigest(source)
	if err != nil {
		return empty, err
	}
	return SkillMarkdown{
		Source:        source,
		Body:          string(body),
		License:       frontmatter.license,
		Compatibility: frontmatter.compatibility,
		AllowedTools:  frontmatter.allowedTools,
		Metadata:      frontmatter.metadata,
	}, nil
}

func snapshotDigestValid(snapshot c.Snapshot) bool {
	stored := snapshot.Digest
	snapshot.Digest = ""
	digest, err := c.Digest(snapshot)
	return err == nil && digest == stored
}

func selectedSkillPath(selectedPath string, selectedPaths []string) bool {
	seen := map[string]bool{}
	selected := false
	for _, candidate := range selectedPaths {
		if len(candidate) > MaxSkillMarkdownPathBytes || !policy.SafePath(candidate) || path.Base(candidate) != "SKILL.md" {
			return false
		}
		folded := strings.ToLower(candidate)
		if seen[folded] {
			return false
		}
		seen[folded] = true
		selected = selected || candidate == selectedPath
	}
	return selected
}

func parseSkillMarkdown(raw []byte) (parsedSkillFrontmatter, []byte, error) {
	fail := func() (parsedSkillFrontmatter, []byte, error) {
		return parsedSkillFrontmatter{}, nil, c.Fail(c.InvalidArgument, "SKILL.md frontmatter is outside the supported bounded format")
	}
	if !utf8.Valid(raw) {
		return fail()
	}
	position := 0
	if bytes.HasPrefix(raw, []byte("---\r\n")) {
		position = 5
	} else if bytes.HasPrefix(raw, []byte("---\n")) {
		position = 4
	} else {
		return fail()
	}
	headerStart := position
	var header []byte
	var body []byte
	for position <= len(raw) {
		lineStart := position
		lineEnd := len(raw)
		next := len(raw)
		if newline := bytes.IndexByte(raw[position:], '\n'); newline >= 0 {
			lineEnd = position + newline
			next = lineEnd + 1
		}
		contentEnd := lineEnd
		if contentEnd > lineStart && raw[contentEnd-1] == '\r' {
			contentEnd--
		}
		if bytes.Equal(raw[lineStart:contentEnd], []byte("---")) {
			if lineStart-headerStart > MaxSkillMarkdownFrontmatterBytes {
				return fail()
			}
			header, body = raw[headerStart:lineStart], raw[next:]
			break
		}
		if next == len(raw) && lineEnd == len(raw) || lineEnd-headerStart > MaxSkillMarkdownFrontmatterBytes {
			return fail()
		}
		position = next
	}
	if header == nil || len(body) > MaxSkillMarkdownBodyBytes || !validSkillAdvice(string(body)) {
		return parsedSkillFrontmatter{}, nil, c.Fail(c.UnsupportedCapability, "SKILL.md body exceeds its bounded untrusted-text format")
	}
	frontmatter, err := parseSkillFrontmatter(header)
	if err != nil {
		return parsedSkillFrontmatter{}, nil, err
	}
	return frontmatter, body, nil
}

func parseSkillFrontmatter(raw []byte) (parsedSkillFrontmatter, error) {
	var result parsedSkillFrontmatter
	result.allowedTools = []string{}
	result.metadata = map[string]string{}
	lines := bytes.Split(raw, []byte{'\n'})
	seen := map[string]bool{}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSuffix(string(lines[i]), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			return parsedSkillFrontmatter{}, c.Fail(c.InvalidArgument, "unexpected indented SKILL.md frontmatter line")
		}
		key, rawValue, ok := strings.Cut(line, ":")
		if !ok || key == "" || strings.TrimSpace(key) != key || seen[key] {
			return parsedSkillFrontmatter{}, c.Fail(c.InvalidArgument, "invalid or duplicate SKILL.md frontmatter key")
		}
		seen[key] = true
		value := strings.TrimSpace(rawValue)
		switch key {
		case "name":
			parsed, err := parseSkillScalar([]byte(value))
			if err != nil {
				return parsedSkillFrontmatter{}, err
			}
			result.name = parsed
		case "description":
			if isSkillBlockScalar(value) {
				result.description, i = parseSkillBlock(lines, i, value[0] == '>')
			} else {
				parsed, err := parseSkillScalar([]byte(value))
				if err != nil {
					return parsedSkillFrontmatter{}, err
				}
				result.description = parsed
			}
		case "license", "compatibility":
			parsed, err := parseSkillScalar([]byte(value))
			if err != nil {
				return parsedSkillFrontmatter{}, err
			}
			if key == "license" {
				result.license = parsed
			} else {
				result.compatibility = parsed
			}
		case "allowed-tools":
			if value == "" {
				items, next, err := parseSkillList(lines, i)
				if err != nil {
					return parsedSkillFrontmatter{}, err
				}
				result.allowedTools, i = items, next
			} else {
				parsed, err := parseSkillScalar([]byte(value))
				if err != nil || len(parsed) > 1024 {
					return parsedSkillFrontmatter{}, c.Fail(c.InvalidArgument, "invalid bounded SKILL.md allowed-tools metadata")
				}
				result.allowedTools = []string{parsed}
			}
		case "metadata":
			if value != "" {
				return parsedSkillFrontmatter{}, c.Fail(c.InvalidArgument, "SKILL.md metadata must use the supported indented mapping")
			}
			metadata, next, err := parseSkillMetadata(lines, i)
			if err != nil {
				return parsedSkillFrontmatter{}, err
			}
			result.metadata, i = metadata, next
		default:
			return parsedSkillFrontmatter{}, c.Fail(c.InvalidArgument, "unsupported SKILL.md frontmatter field")
		}
	}
	if !validText(result.name, 1, 128) || strings.ContainsAny(result.name, "\r\n") ||
		!validText(result.description, 1, 2048) || strings.ContainsRune(result.description, '\r') ||
		(result.license != "" && !validText(result.license, 1, 128)) || (result.compatibility != "" && !validText(result.compatibility, 1, 256)) {
		return parsedSkillFrontmatter{}, c.Fail(c.InvalidArgument, "SKILL.md needs bounded name and description metadata")
	}
	return result, nil
}

func isSkillBlockScalar(value string) bool {
	return value == "|" || value == "|-" || value == "|+" || value == ">" || value == ">-" || value == ">+"
}

func parseSkillBlock(lines [][]byte, start int, folded bool) (string, int) {
	parts := []string{}
	last := start
	for i := start + 1; i < len(lines); i++ {
		line := strings.TrimSuffix(string(lines[i]), "\r")
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			break
		}
		if strings.ContainsRune(line, '\t') {
			return "", i
		}
		parts = append(parts, strings.TrimSpace(line))
		last = i
	}
	if folded {
		var result strings.Builder
		for i, part := range parts {
			if i > 0 {
				if part == "" || parts[i-1] == "" {
					result.WriteByte('\n')
				} else {
					result.WriteByte(' ')
				}
			}
			result.WriteString(part)
		}
		return strings.TrimSpace(result.String()), last
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), last
}

func parseSkillList(lines [][]byte, start int) ([]string, int, error) {
	items := []string{}
	last := start
	for i := start + 1; i < len(lines); i++ {
		line := strings.TrimSuffix(string(lines[i]), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			last = i
			continue
		}
		if !strings.HasPrefix(line, "  - ") {
			if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				break
			}
			return nil, start, c.Fail(c.InvalidArgument, "unsupported SKILL.md allowed-tools list item")
		}
		item, err := parseSkillScalar([]byte(strings.TrimSpace(strings.TrimPrefix(line, "  - "))))
		if err != nil || len(item) > 128 || len(items) >= 32 {
			return nil, start, c.Fail(c.InvalidArgument, "invalid bounded SKILL.md allowed-tools list")
		}
		items = append(items, item)
		last = i
	}
	return items, last, nil
}

func parseSkillMetadata(lines [][]byte, start int) (map[string]string, int, error) {
	metadata := map[string]string{}
	last := start
	for i := start + 1; i < len(lines); i++ {
		line := strings.TrimSuffix(string(lines[i]), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			last = i
			continue
		}
		if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
			if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				break
			}
			return nil, start, c.Fail(c.InvalidArgument, "unsupported nested SKILL.md metadata")
		}
		key, rawValue, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !skillMetadataKeyPattern.MatchString(key) || len(metadata) >= 16 {
			return nil, start, c.Fail(c.InvalidArgument, "invalid bounded SKILL.md metadata key")
		}
		if _, exists := metadata[key]; exists {
			return nil, start, c.Fail(c.InvalidArgument, "duplicate SKILL.md metadata key")
		}
		value, err := parseSkillScalar([]byte(strings.TrimSpace(rawValue)))
		if err != nil || len(value) > 256 {
			return nil, start, c.Fail(c.InvalidArgument, "invalid bounded SKILL.md metadata value")
		}
		metadata[key] = value
		last = i
	}
	return metadata, last, nil
}

func parseSkillScalar(raw []byte) (string, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", c.Fail(c.InvalidArgument, "empty SKILL.md metadata scalar")
	}
	if value[0] == '"' {
		decoded, err := strconv.Unquote(value)
		if err != nil || strings.ContainsAny(decoded, "\r\n\t") {
			return "", c.Fail(c.InvalidArgument, "invalid double-quoted SKILL.md scalar")
		}
		return decoded, nil
	}
	if value[0] == '\'' {
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", c.Fail(c.InvalidArgument, "invalid single-quoted SKILL.md scalar")
		}
		inner := value[1 : len(value)-1]
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\'' {
				if i+1 >= len(inner) || inner[i+1] != '\'' {
					return "", c.Fail(c.InvalidArgument, "invalid single-quoted SKILL.md scalar")
				}
				i++
			}
		}
		decoded := strings.ReplaceAll(inner, "''", "'")
		if strings.ContainsAny(decoded, "\r\n\t") {
			return "", c.Fail(c.InvalidArgument, "invalid single-quoted SKILL.md scalar")
		}
		return decoded, nil
	}
	if strings.ContainsAny(value, "\t\r\n") || strings.Contains(value, " #") || strings.Contains(value, ": ") || strings.ContainsAny(value[:1], "!&*|>@`{}[]") {
		return "", c.Fail(c.InvalidArgument, "unsupported plain SKILL.md scalar syntax")
	}
	return value, nil
}

func markdownManifestDigest(source MarkdownSource) (string, error) {
	return c.Digest(markdownManifestBody{
		SchemaVersion:        source.SchemaVersion,
		SourceManifestDigest: source.SourceManifestDigest,
		Path:                 source.Path,
		FileDigest:           source.FileDigest,
		Name:                 source.Name,
		Description:          source.Description,
		BodyDigest:           source.BodyDigest,
		License:              source.License,
		Compatibility:        source.Compatibility,
		AllowedTools:         source.AllowedTools,
		Metadata:             source.Metadata,
	})
}

func (source MarkdownSource) Validate() error {
	if source.SchemaVersion != SchemaVersion || !c.ValidDigest(source.SourceManifestDigest) || len(source.Path) > MaxSkillMarkdownPathBytes || !policy.SafePath(source.Path) || path.Base(source.Path) != "SKILL.md" ||
		!c.ValidDigest(source.FileDigest) || !validSkillMetadataText(source.Name, 128) ||
		!validText(source.Description, 1, 2048) || strings.ContainsAny(source.Description, "\r\t") || !c.ValidDigest(source.BodyDigest) ||
		(source.License != "" && !validSkillMetadataText(source.License, 128)) || (source.Compatibility != "" && !validSkillMetadataText(source.Compatibility, 256)) ||
		len(source.AllowedTools) > 32 || len(source.Metadata) > 16 {
		return c.Fail(c.InvalidArgument, "invalid source-bound SKILL.md metadata")
	}
	for _, item := range source.AllowedTools {
		if !validSkillMetadataText(item, 128) {
			return c.Fail(c.InvalidArgument, "invalid source-bound SKILL.md tool declaration")
		}
	}
	for key, value := range source.Metadata {
		if !skillMetadataKeyPattern.MatchString(key) || !validSkillMetadataText(value, 256) {
			return c.Fail(c.InvalidArgument, "invalid source-bound SKILL.md metadata entry")
		}
	}
	if !c.ValidDigest(source.ManifestDigest) {
		return c.Fail(c.InvalidArgument, "invalid source-bound SKILL.md digest")
	}
	digest, err := markdownManifestDigest(source)
	if err != nil || digest != source.ManifestDigest {
		return c.Fail(c.StaleBase, "SKILL.md manifest metadata digest changed")
	}
	return nil
}

func validSkillMetadataText(value string, maxBytes int) bool {
	return validText(value, 1, maxBytes) && !strings.ContainsAny(value, "\r\n\t")
}

func validSkillAdvice(body string) bool {
	if len(body) == 0 || len(body) > MaxSkillMarkdownBodyBytes || !utf8.ValidString(body) || strings.TrimSpace(body) == "" {
		return false
	}
	for i, r := range body {
		if r == 0 || r == '\r' && (i+1 >= len(body) || body[i+1] != '\n') || r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}
