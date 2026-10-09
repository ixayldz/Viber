// Package config resolves preferences separately from intersecting restrictions.
// JSON files are secret-free; parser failures never echo their contents.
package config

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/policy"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Preferences struct {
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	Autonomy     string `json:"autonomy,omitempty"`
	ContextLimit int64  `json:"context_limit,omitempty"`
	OutputLimit  int64  `json:"output_limit,omitempty"`
}
type Privacy struct {
	RemoteInference    *bool     `json:"remote_inference,omitempty"`
	AllowedProviders   *[]string `json:"allowed_providers,omitempty"`
	EmbeddingEgress    *bool     `json:"embedding_egress,omitempty"`
	RerankerEgress     *bool     `json:"reranker_egress,omitempty"`
	ToolEgress         *bool     `json:"tool_mcp_browser_egress,omitempty"`
	TelemetryExport    *bool     `json:"telemetry_export,omitempty"`
	TrainingExport     *bool     `json:"training_export,omitempty"`
	CrossProjectMemory *bool     `json:"cross_project_memory,omitempty"`
	SensitivePaths     []string  `json:"sensitive_paths,omitempty"`
}
type File struct {
	SchemaVersion int         `json:"schema_version"`
	Preferences   Preferences `json:"preferences"`
	Restrictions  Privacy     `json:"restrictions"`
}
type Layer struct {
	Origin string `json:"origin"`
	Digest string `json:"file_digest"`
	File   File   `json:"file"`
}
type Resolved struct {
	SchemaVersion int         `json:"schema_version"`
	Layers        []Layer     `json:"layers"`
	Preferences   Preferences `json:"preferences"`
}

func (f File) Validate() error {
	p := f.Preferences
	r := f.Restrictions
	if f.SchemaVersion != 1 || p.Provider != "" && p.Provider != "ollama" && p.Provider != "openai" && p.Provider != "anthropic" && p.Provider != "chatgpt" ||
		len(p.Model) > 256 || strings.ContainsAny(p.Model, "\x00\r\n") || p.Autonomy != "" && p.Autonomy != "review" && p.Autonomy != "guided" && p.Autonomy != "auto" ||
		p.ContextLimit < 0 || p.ContextLimit > 8<<20 || p.OutputLimit < 0 || p.OutputLimit > 1<<20 || r.AllowedProviders != nil && len(*r.AllowedProviders) > 4 || len(r.SensitivePaths) > 256 {
		return c.Fail(c.InvalidArgument, "invalid secret-free config version/preferences/quota")
	}
	seen := map[string]bool{}
	providers := []string{}
	if r.AllowedProviders != nil {
		providers = *r.AllowedProviders
	}
	for _, provider := range providers {
		if seen[provider] || provider != "ollama" && provider != "openai" && provider != "anthropic" && provider != "chatgpt" {
			return c.Fail(c.InvalidArgument, "invalid config provider restriction")
		}
		seen[provider] = true
	}
	seen = map[string]bool{}
	for _, scope := range r.SensitivePaths {
		path := strings.TrimSuffix(scope, "/**")
		if !policy.SafePath(path) || len(scope) > 1024 || seen[strings.ToLower(scope)] {
			return c.Fail(c.InvalidArgument, "invalid portable sensitive scope")
		}
		seen[strings.ToLower(scope)] = true
	}
	return nil
}
func Read(path string) (File, error) {
	var f File
	absolute, err := filepath.Abs(path)
	if err != nil {
		return f, c.Fail(c.InvalidArgument, "config path unavailable")
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return f, c.Fail(c.InvalidArgument, "config file unavailable")
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, filepath.Base(absolute), 64<<10)
	if err != nil {
		return f, c.Fail(c.InvalidArgument, "config must be a bounded regular single-link file")
	}
	if c.DecodeStrict(raw, &f) != nil || f.Validate() != nil {
		return File{}, c.Fail(c.InvalidArgument, "invalid secret-free config; content omitted")
	}
	return f, nil
}
func merge(a, b Preferences) Preferences {
	if b.Provider != "" {
		a.Provider = b.Provider
	}
	if b.Model != "" {
		a.Model = b.Model
	}
	if b.Autonomy != "" {
		a.Autonomy = b.Autonomy
	}
	if b.ContextLimit != 0 {
		a.ContextLimit = b.ContextLimit
	}
	if b.OutputLimit != 0 {
		a.OutputLimit = b.OutputLimit
	}
	return a
}
func Resolve(user, project *File) (Resolved, error) {
	result := Resolved{SchemaVersion: 1, Layers: []Layer{}}
	for _, item := range []struct {
		origin string
		file   *File
	}{{"USER", user}, {"PROJECT", project}} {
		if item.file == nil {
			continue
		}
		if err := item.file.Validate(); err != nil {
			return result, err
		}
		raw, err := c.CanonicalV1(*item.file)
		if err != nil {
			return result, err
		}
		var clone File
		if err = c.DecodeStrict(raw, &clone); err != nil {
			return result, err
		}
		digest, _ := c.Digest(clone)
		result.Layers = append(result.Layers, Layer{item.origin, digest, clone})
		result.Preferences = merge(result.Preferences, clone.Preferences)
	}
	return result, result.Validate()
}
func (r Resolved) Validate() error {
	if r.SchemaVersion != 1 || len(r.Layers) > 2 {
		return c.Fail(c.StoreIntegrityError, "invalid config resolution")
	}
	var user, project *File
	for _, layer := range r.Layers {
		if layer.File.Validate() != nil {
			return c.Fail(c.StoreIntegrityError, "invalid retained config")
		}
		digest, _ := c.Digest(layer.File)
		if digest != layer.Digest {
			return c.Fail(c.StoreIntegrityError, "config lineage changed")
		}
		switch layer.Origin {
		case "USER":
			if user != nil || project != nil {
				return c.Fail(c.StoreIntegrityError, "config layer order")
			}
			user = &layer.File
		case "PROJECT":
			if project != nil {
				return c.Fail(c.StoreIntegrityError, "duplicate project config")
			}
			project = &layer.File
		default:
			return c.Fail(c.StoreIntegrityError, "unknown config authority")
		}
	}
	expected := Preferences{}
	if user != nil {
		expected = merge(expected, user.Preferences)
	}
	if project != nil {
		expected = merge(expected, project.Preferences)
	}
	a, _ := c.Digest(expected)
	b, _ := c.Digest(r.Preferences)
	if a != b {
		return c.Fail(c.StoreIntegrityError, "config preference resolution changed")
	}
	return nil
}
func (r Resolved) AdmitProvider(provider string, remote bool) error {
	for _, layer := range r.Layers {
		limits := layer.File.Restrictions
		if remote && limits.RemoteInference != nil && !*limits.RemoteInference {
			return c.Fail(c.PolicyDenied, "remote inference denied by retained config")
		}
		if limits.AllowedProviders != nil {
			found := false
			for _, p := range *limits.AllowedProviders {
				if p == provider {
					found = true
				}
			}
			if !found {
				return c.Fail(c.PolicyDenied, "provider denied by retained config")
			}
		}
	}
	return nil
}
func (r Resolved) SensitivePaths() []string {
	seen := map[string]bool{}
	for _, layer := range r.Layers {
		for _, scope := range layer.File.Restrictions.SensitivePaths {
			seen[scope] = true
		}
	}
	result := []string{}
	for scope := range seen {
		result = append(result, scope)
	}
	sort.Strings(result)
	return result
}
