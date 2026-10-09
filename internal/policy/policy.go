// Package policy computes restrictions. Repository preferences never grant authority.
package policy

import (
	"path"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type Policy struct {
	DeniedPaths     []string `json:"denied_paths,omitempty"`
	SchemaVersion   int      `json:"schema_version"`
	Epoch           int64    `json:"epoch"`
	Generation      int64    `json:"generation"`
	Effects         []string `json:"effects"`
	Paths           []string `json:"paths"`
	RemoteInference bool     `json:"remote_inference"`
	Providers       []string `json:"providers"`
}
type Action struct {
	Epoch        int64
	Generation   int64
	Effect       string
	Path         string
	Provider     string
	Remote       bool
	InputBarrier bool
}

func SafePath(p string) bool {
	if p == "" || !utf8.ValidString(p) {
		return false
	}
	if strings.ContainsAny(p, "\\:\x00\r\n") || strings.HasPrefix(p, "/") || path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		upper := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		switch upper {
		case "CON", "PRN", "AUX", "NUL", "CLOCK$":
			return false
		}
		if len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9' {
			return false
		}
		for _, r := range part {
			if r < 32 || r == 127 || strings.ContainsRune("<>\"|?*", r) {
				return false
			}
		}
	}
	return true
}

func PathAllowed(p string, scopes []string) bool {
	if !SafePath(p) {
		return false
	}
	for _, scope := range scopes {
		if scope == "**" {
			return true
		}
		if strings.HasSuffix(scope, "/**") {
			prefix := strings.TrimSuffix(scope, "/**")
			if SafePath(prefix) && strings.HasPrefix(p, prefix+"/") {
				return true
			}
		} else if SafePath(scope) && p == scope {
			return true
		}
	}
	return false
}
func contains(items []string, v string) bool {
	for _, i := range items {
		if i == v {
			return true
		}
	}
	return false
}

// Admit checks every restriction independently. There is no preference winner
// which can broaden a deny at another policy layer.
func Admit(layers []Policy, a Action) error {
	if len(layers) == 0 {
		return c.Fail(c.PolicyDenied, "no authoritative policy")
	}
	if a.InputBarrier {
		return c.Fail(c.PolicyDenied, "durable user input awaits interpretation")
	}
	if a.Effect == "" {
		return c.Fail(c.PolicyDenied, "effect descriptor required")
	}
	for _, p := range layers {
		if p.SchemaVersion != c.SchemaVersion || p.Epoch < 1 || p.Generation < 1 {
			return c.Fail(c.PolicyDenied, "unsupported policy version")
		}
		if a.Epoch != p.Epoch || a.Generation != p.Generation {
			return c.Fail(c.StaleAuthority, "epoch or kernel generation mismatch")
		}
		if !contains(p.Effects, a.Effect) {
			return c.Fail(c.PolicyDenied, "effect denied by a policy layer")
		}
		if a.Path != "" && (DeniedPath(a.Path, p.DeniedPaths) || !PathAllowed(a.Path, p.Paths)) {
			return c.Fail(c.PolicyDenied, "path outside permitted scope")
		}
		if a.Remote && (!p.RemoteInference || a.Provider == "" || !contains(p.Providers, a.Provider)) {
			return c.Fail(c.PolicyDenied, "remote inference/provider denied")
		}
	}
	return nil
}
