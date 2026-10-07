package workspace

import (
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type ignoreRule struct {
	base      string
	pattern   *regexp.Regexp
	negate    bool
	directory bool
}

// Git pattern semantics: precedence by depth/order, parent exclusion, escaped
// markers/spaces, anchored paths, slash-local wildcards, ** and character classes.
func compileIgnores(sources map[string][]byte) ([]ignoreRule, error) {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == ".git/info/exclude" {
			return true
		}
		if names[j] == ".git/info/exclude" {
			return false
		}
		di, dj := strings.Count(names[i], "/"), strings.Count(names[j], "/")
		if di != dj {
			return di < dj
		}
		return names[i] < names[j]
	})
	rules := []ignoreRule{}
	for _, name := range names {
		raw := sources[name]
		if !utf8.Valid(raw) || len(raw) > 1<<20 {
			return nil, c.Fail(c.UnsupportedCapability, "unsupported ignore source")
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSuffix(line, "\r")
			for strings.HasSuffix(line, " ") {
				escapes := 0
				for i := len(line) - 2; i >= 0 && line[i] == '\\'; i-- {
					escapes++
				}
				if escapes%2 == 1 {
					break
				}
				line = strings.TrimSuffix(line, " ")
			}
			if line == "" || line[0] == '#' {
				continue
			}
			if len(rules) >= 10000 || len(line) > 4096 {
				return nil, c.Fail(c.UnsupportedCapability, "ignore pattern quota exceeded")
			}
			negate := line[0] == '!'
			if negate {
				line = line[1:]
			}
			if line == "" {
				continue
			}
			directory := strings.HasSuffix(line, "/")
			if directory {
				line = strings.TrimSuffix(line, "/")
			}
			anchored := strings.HasPrefix(line, "/")
			line = strings.TrimPrefix(line, "/")
			expression, err := globExpression(line)
			if err != nil {
				return nil, err
			}
			if anchored || strings.Contains(line, "/") {
				expression = "^" + expression + "$"
			} else {
				expression = "(^|/)" + expression + "$"
			}
			compiled, err := regexp.Compile(expression)
			if err != nil {
				return nil, c.Fail(c.UnsupportedCapability, "unsupported Git ignore pattern")
			}
			rules = append(rules, ignoreRule{base: ignoreSourceDirectory(name), pattern: compiled, negate: negate, directory: directory})
		}
	}
	return rules, nil
}
func globExpression(pattern string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			i++
			if i >= len(pattern) {
				return "", c.Fail(c.UnsupportedCapability, "trailing ignore escape")
			}
			_, size := utf8.DecodeRuneInString(pattern[i:])
			out.WriteString(regexp.QuoteMeta(pattern[i : i+size]))
			i += size - 1
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' && (i == 0 || pattern[i-1] == '/') && (i+2 == len(pattern) || pattern[i+2] == '/') {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					out.WriteString("(?:[^/]+/)*")
					i++
				} else {
					out.WriteString(".*")
				}
			} else {
				out.WriteString("[^/]*")
			}
		case '?':
			out.WriteString("[^/]")
		case '[':
			close := strings.IndexByte(pattern[i+1:], ']')
			if close < 0 {
				out.WriteString("\\[")
				continue
			}
			close += i + 1
			body := pattern[i+1 : close]
			if body == "" || strings.ContainsAny(body, "/\\[") {
				return "", c.Fail(c.UnsupportedCapability, "unsupported ignore character class")
			}
			if body[0] == '!' {
				body = "^" + body[1:]
			}
			out.WriteString("[" + body + "]")
			i = close
		default:
			_, size := utf8.DecodeRuneInString(pattern[i:])
			out.WriteString(regexp.QuoteMeta(pattern[i : i+size]))
			i += size - 1
		}
	}
	return out.String(), nil
}
func ignored(rules []ignoreRule, name string, directory bool) bool {
	parts := strings.Split(name, "/")
	for n := 1; n <= len(parts); n++ {
		candidate := strings.Join(parts[:n], "/")
		isDir := n < len(parts) || directory
		exclude := false
		for _, rule := range rules {
			relative := candidate
			if rule.base != "." {
				prefix := rule.base + "/"
				if !strings.HasPrefix(candidate, prefix) {
					continue
				}
				relative = strings.TrimPrefix(candidate, prefix)
			}
			if rule.directory && !isDir {
				continue
			}
			if rule.pattern.MatchString(relative) {
				exclude = !rule.negate
			}
		}
		if exclude {
			return true
		}
	}
	return false
}
func parentIgnoreSource(name string) string { return path.Join(name, ".gitignore") }
