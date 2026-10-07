package workspace

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"strings"
)

func validateShape(capture Capture) error {
	s := capture.Snapshot
	if s.Root == "" || len(s.Entries) > 100000 || len(s.Directories) > 100000 || len(s.Exclusions) > 100000 {
		return c.Fail(c.InvalidArgument, "invalid manifest shape")
	}
	seen := map[string]string{}
	filePaths := map[string]bool{}
	total := int64(0)
	claim := func(p, kind string) error {
		if !policy.SafePath(p) || seen[strings.ToLower(p)] != "" {
			return c.Fail(c.InvalidArgument, "unsafe or aliased manifest path")
		}
		for _, part := range strings.Split(p, "/") {
			if strings.EqualFold(part, ".git") && kind != "excluded" {
				return c.Fail(c.PolicyDenied, "Git internals cannot be candidate content")
			}
		}
		seen[strings.ToLower(p)] = kind
		return nil
	}
	for _, e := range s.Entries {
		if err := claim(e.Path, "file"); err != nil {
			return err
		}
		if !c.ValidDigest(e.Hash) || e.Size < 0 || e.Size > 64<<20 || e.Mode & ^uint32(0777) != 0 {
			return c.Fail(c.InvalidArgument, "invalid manifest entry")
		}
		total += e.Size
		if total > 1<<30 {
			return c.Fail(c.InvalidArgument, "manifest byte quota exceeded")
		}
		filePaths[e.Path] = true
	}
	for _, d := range s.Directories {
		if err := claim(d, "directory"); err != nil {
			return err
		}
	}
	for _, p := range s.Exclusions {
		if err := claim(strings.TrimSuffix(p, "/"), "excluded"); err != nil {
			return err
		}
	}
	for name := range seen {
		parts := strings.Split(name, "/")
		for n := 1; n < len(parts); n++ {
			parent := strings.Join(parts[:n], "/")
			if seen[parent] == "file" || seen[parent] == "excluded" {
				return c.Fail(c.InvalidArgument, "overlapping manifest entries")
			}
			if seen[parent] != "directory" {
				return c.Fail(c.InvalidArgument, "manifest parent directory unavailable")
			}
		}
	}
	if s.Git != nil {
		g := s.Git
		if g.IndexPresent {
			if c.HashBytes(capture.IndexBytes) != g.IndexDigest {
				return c.Fail(c.StoreIntegrityError, "Git index bytes unavailable or corrupt")
			}
			entries, format, err := parseIndex(capture.IndexBytes)
			if err != nil {
				return err
			}
			a, _ := c.Digest(entries)
			b, _ := c.Digest(g.Entries)
			if a != b || format != g.ObjectFormat {
				return c.Fail(c.StoreIntegrityError, "Git index metadata mismatch")
			}
		} else if len(capture.IndexBytes) != 0 || g.IndexDigest != "" || len(g.Entries) != 0 {
			return c.Fail(c.StoreIntegrityError, "unmanifested Git index")
		}
		digest, err := c.Digest(capture.IgnoreSources)
		if err != nil {
			return err
		}
		if digest != g.IgnoreSourcesDigest {
			return c.Fail(c.StoreIntegrityError, "ignore sources mismatch")
		}
	} else if len(capture.IndexBytes) != 0 || len(capture.IgnoreSources) != 0 {
		return c.Fail(c.StoreIntegrityError, "unexpected Git artifacts")
	}
	return nil
}
func GitPrecondition(base, current Capture) error {
	if base.Snapshot.Git == nil && current.Snapshot.Git == nil {
		return nil
	}
	if base.Snapshot.Git == nil || current.Snapshot.Git == nil {
		return stale("repository identity changed")
	}
	a, b := base.Snapshot.Git, current.Snapshot.Git
	if a.GitDir != b.GitDir || a.CommonDir != b.CommonDir || a.HeadRef != b.HeadRef || a.HeadObject != b.HeadObject || a.IndexPresent != b.IndexPresent || a.IndexDigest != b.IndexDigest || a.IgnoreSourcesDigest != b.IgnoreSourcesDigest {
		return stale("HEAD/index/ignore authority changed")
	}
	return nil
}
