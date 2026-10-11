package distribution

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

type retirement struct {
	SchemaVersion  int    `json:"schema_version"`
	Revision       string `json:"revision"`
	ManifestDigest string `json:"manifest_digest"`
	Files          []File `json:"files"`
}

// Uninstall retires only verified, inactive program inventories. The selection
// and retirement receipts remain for interrupted deletion recovery. Task stores
// cannot be inside this managed prefix and are never traversed here.
func (m *Manager) Uninstall() (Status, error) {
	state, err := m.Status()
	if err != nil {
		return state, err
	}
	entries, err := readDirectory(m.root, "retirements")
	if err != nil {
		return state, err
	}
	if len(entries) > 16 {
		return state, c.Fail(c.BudgetLimitReached, "retirement receipt bound exceeded")
	}
	intents := map[string]retirement{}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			return state, c.Fail(c.StoreIntegrityError, "invalid retirement entry")
		}
		raw, readErr := fileguard.ReadRegular(m.root, filepath.Join("retirements", entry.Name()), 1<<20)
		var intent retirement
		if readErr != nil || c.DecodeStrict(raw, &intent) != nil || intent.SchemaVersion != 1 || !Revision(intent.Revision) || entry.Name() != intent.Revision+".json" || !c.ValidDigest(intent.ManifestDigest) || len(intent.Files) < 2 || len(intent.Files) > MaxFiles {
			return state, c.Fail(c.StoreIntegrityError, "invalid retirement binding")
		}
		seen := map[string]bool{}
		for _, file := range intent.Files {
			if file.Path != "package.json" && !safeMember(file.Path) || seen[strings.ToLower(file.Path)] || !c.ValidDigest(file.Digest) || file.Bytes < 0 || file.Bytes > MaxArchiveBytes {
				return state, c.Fail(c.StoreIntegrityError, "invalid retirement inventory")
			}
			seen[strings.ToLower(file.Path)] = true
			if file.Path == "package.json" && file.Digest != intent.ManifestDigest {
				return state, c.Fail(c.StoreIntegrityError, "retirement manifest digest differs")
			}
		}
		if !seen["package.json"] || !seen[binaryName()] {
			return state, c.Fail(c.StoreIntegrityError, "incomplete retirement inventory")
		}
		intents[intent.Revision] = intent
	}
	// Verify every new version before recording any destructive authority.
	for _, revision := range state.Installed {
		if _, ok := intents[revision]; ok {
			continue
		}
		raw, readErr := fileguard.ReadRegular(m.root, filepath.Join("versions", revision, "package.json"), 1<<20)
		if readErr != nil {
			return state, readErr
		}
		manifest, verifyErr := m.version(revision, c.HashBytes(raw))
		if verifyErr != nil {
			return state, verifyErr
		}
		intent := retirement{SchemaVersion: 1, Revision: revision, ManifestDigest: c.HashBytes(raw), Files: append([]File{}, manifest.Files...)}
		intent.Files = append(intent.Files, File{Path: "package.json", Bytes: int64(len(raw)), Digest: c.HashBytes(raw)})
		intents[revision] = intent
	}
	if len(intents) > 16 {
		return state, c.Fail(c.BudgetLimitReached, "retired version bound exceeded")
	}
	for revision, intent := range intents {
		name := filepath.Join("retirements", revision+".json")
		if _, lookupErr := m.root.Lstat(name); errors.Is(lookupErr, os.ErrNotExist) {
			raw, encodeErr := c.CanonicalV1(intent)
			if encodeErr != nil {
				return state, encodeErr
			}
			if err = fileguard.Publish(m.root, name, raw); err != nil {
				return state, err
			}
		} else if lookupErr != nil {
			return state, lookupErr
		}
	}
	if state.Active.Action != "UNINSTALL" {
		state, err = m.publish(state, Selection{Action: "UNINSTALL"})
		if err != nil {
			return state, err
		}
	}
	for revision, intent := range intents {
		base := filepath.Join("versions", revision)
		if _, lookupErr := m.root.Lstat(base); errors.Is(lookupErr, os.ErrNotExist) {
			continue
		} else if lookupErr != nil {
			return state, lookupErr
		}
		bindings := map[string]File{}
		directories := map[string]bool{base: true}
		for _, file := range intent.Files {
			bindings[file.Path] = file
			for parent := filepath.Dir(filepath.Join(base, filepath.FromSlash(file.Path))); parent != base; parent = filepath.Dir(parent) {
				directories[parent] = true
			}
		}
		if err = m.inventory(base, func(name string, info os.FileInfo) error {
			rel, relErr := filepath.Rel(base, name)
			if relErr != nil {
				return relErr
			}
			file, ok := bindings[filepath.ToSlash(rel)]
			if !ok {
				return c.Fail(c.Conflict, "uninstall found an unowned file")
			}
			raw, readErr := fileguard.ReadRegular(m.root, name, MaxArchiveBytes)
			if readErr != nil || int64(len(raw)) != file.Bytes || c.HashBytes(raw) != file.Digest {
				return c.Fail(c.Conflict, "uninstall inventory changed")
			}
			return nil
		}); err != nil {
			return state, err
		}
		for _, file := range intent.Files {
			if _, err = fileguard.RemoveBound(m.root, filepath.Join(base, filepath.FromSlash(file.Path)), file.Digest, file.Bytes, MaxArchiveBytes); err != nil {
				return state, err
			}
		}
		// Only empty directories under the exact inventoried version are removed.
		if err = m.removeEmptyTree(base, 0, directories); err != nil {
			return state, err
		}
	}
	return m.Status()
}

func (m *Manager) removeEmptyTree(name string, depth int, directories map[string]bool) error {
	if !directories[name] {
		return c.Fail(c.Conflict, "retirement contains an unowned directory")
	}
	if depth > 12 {
		return c.Fail(c.PolicyDenied, "retirement directory depth exceeded")
	}
	entries, err := readDirectory(m.root, name)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return c.Fail(c.Conflict, "retirement contains unowned entries")
		}
		if err = m.removeEmptyTree(filepath.Join(name, entry.Name()), depth+1, directories); err != nil {
			return err
		}
	}
	if err = m.root.Remove(name); err != nil {
		return err
	}
	return fileguard.SyncParents(m.root, filepath.Dir(name))
}
