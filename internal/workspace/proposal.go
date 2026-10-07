package workspace

import (
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
)

// Preview validates all preconditions and creates a new in-memory candidate.
// It never edits the live filesystem or silently applies to a changed target.
func Preview(base, current Capture, p c.Proposal, layers []policy.Policy, authority c.TaskState) (Capture, error) {
	if err := VerifyCapture(base); err != nil {
		return Capture{}, err
	}
	if err := VerifyCapture(current); err != nil {
		return Capture{}, err
	}
	if err := GitPrecondition(base, current); err != nil {
		return Capture{}, err
	}
	if base.Snapshot.Root != current.Snapshot.Root {
		return Capture{}, stale("target identity changed")
	}
	if p.SchemaVersion != c.SchemaVersion || p.ID == "" || p.TaskID == "" || p.SpecVersion < 1 || len(p.Changes) == 0 {
		return Capture{}, c.Fail(c.InvalidArgument, "invalid proposal envelope")
	}
	if authority.SchemaVersion != c.SchemaVersion || authority.TaskID != p.TaskID || authority.SpecVersion != p.SpecVersion || authority.PolicyEpoch != p.PolicyEpoch || authority.KernelGeneration != p.KernelGeneration || authority.Execution != c.Running {
		return Capture{}, c.Fail(c.StaleAuthority, "proposal does not match active task/spec/authority")
	}
	if authority.InputBarrier {
		return Capture{}, c.Fail(c.PolicyDenied, "unresolved input barrier")
	}
	if p.BaseSnapshot != base.Snapshot.Digest {
		return Capture{}, stale("base snapshot")
	}
	before, now := entryMap(base.Snapshot), entryMap(current.Snapshot)
	readPaths := map[string]bool{}
	for _, r := range p.ReadSet {
		readPath := r.Path
		if r.Kind == "LISTING" {
			readPath = ""
		}
		if err := policy.Admit(layers, policy.Action{Epoch: p.PolicyEpoch, Generation: p.KernelGeneration, Effect: "snapshot.read", Path: readPath}); err != nil {
			return Capture{}, err
		}
		if r.Kind == "LISTING" {
			// Listing metadata itself is scoped; whole-tree listings require **.
			if !listingAllowed(r.Path, layers) {
				return Capture{}, c.Fail(c.PolicyDenied, "listing outside policy")
			}
		}
		switch r.Kind {
		case "FILE":
			if e, ok := before[r.Path]; !ok || e.Hash != r.Digest {
				return Capture{}, stale("read receipt")
			}
			if e, ok := now[r.Path]; !ok || e.Hash != r.Digest {
				return Capture{}, stale("source changed since read")
			}
			readPaths[r.Path] = true
		case "ABSENT":
			if r.Digest != "" || coverageExcluded(base, r.Path) || coverageExcluded(current, r.Path) {
				return Capture{}, c.Fail(c.InvalidArgument, "absence outside captured coverage")
			}
			if capturedPathExists(base.Snapshot, r.Path) {
				return Capture{}, stale("base path was present")
			}
			if capturedPathExists(current.Snapshot, r.Path) {
				return Capture{}, stale("negative read invalidated")
			}
			readPaths[r.Path] = true
		case "LISTING":
			b, err := ListingDigest(base.Snapshot, r.Path)
			if err != nil {
				return Capture{}, err
			}
			n, err := ListingDigest(current.Snapshot, r.Path)
			if err != nil {
				return Capture{}, err
			}
			if b != r.Digest || n != r.Digest {
				return Capture{}, stale("listing changed")
			}
		default:
			return Capture{}, c.Fail(c.InvalidArgument, "unknown read condition")
		}
	}
	seen := map[string]bool{}
	next := Capture{Snapshot: current.Snapshot, Contents: map[string][]byte{}, IndexBytes: append([]byte(nil), current.IndexBytes...), IgnoreSources: map[string][]byte{}}
	for name, raw := range current.IgnoreSources {
		next.IgnoreSources[name] = append([]byte(nil), raw...)
	}
	next.Snapshot.Entries = append([]c.Entry(nil), current.Snapshot.Entries...)
	next.Snapshot.Exclusions = append([]string(nil), current.Snapshot.Exclusions...)
	next.Snapshot.Directories = append([]string(nil), current.Snapshot.Directories...)
	for name, data := range current.Contents {
		next.Contents[name] = append([]byte(nil), data...)
	}
	for _, change := range p.Changes {
		if !policy.SafePath(change.Path) || coverageExcluded(current, change.Path) {
			return Capture{}, c.Fail(c.PolicyDenied, "invalid or excluded write path")
		}
		key := strings.ToLower(change.Path)
		if seen[key] {
			return Capture{}, c.Fail(c.InvalidArgument, "duplicate changes")
		}
		seen[key] = true
		if !readPaths[change.Path] {
			return Capture{}, c.Fail(c.InvalidArgument, "write requires FILE or ABSENT read receipt")
		}
		if err := policy.Admit(layers, policy.Action{Epoch: p.PolicyEpoch, Generation: p.KernelGeneration, Effect: "candidate.write", Path: change.Path}); err != nil {
			return Capture{}, err
		}
		old, present := before[change.Path]
		live, livePresent := now[change.Path]
		if present != livePresent || present && (old.Hash != live.Hash || old.Mode != live.Mode) {
			return Capture{}, stale("write preimage changed")
		}
		if !present && capturedPathExists(current.Snapshot, change.Path) {
			return Capture{}, c.Fail(c.Conflict, "write path is a directory")
		}
		if present && change.BeforeDigest != old.Hash || !present && change.BeforeDigest != "" {
			return Capture{}, stale("invalid before digest")
		}
		for path := range now {
			if strings.EqualFold(path, change.Path) && path != change.Path {
				return Capture{}, c.Fail(c.Conflict, "case collision")
			}
			if strings.HasPrefix(path, change.Path+"/") || strings.HasPrefix(change.Path, path+"/") {
				return Capture{}, c.Fail(c.Conflict, "file/directory collision")
			}
		}
		if change.Delete {
			if !present || len(change.After) != 0 {
				return Capture{}, c.Fail(c.InvalidArgument, "invalid deletion")
			}
			delete(next.Contents, change.Path)
			delete(now, change.Path)
		} else {
			if len(change.After) > 2<<20 {
				return Capture{}, c.Fail(c.InvalidArgument, "proposal file exceeds bounded preview")
			}
			mode := uint32(0644)
			if present {
				mode = old.Mode
			}
			next.Contents[change.Path] = append([]byte(nil), change.After...)
			parts := strings.Split(change.Path, "/")
			for i := 1; i < len(parts); i++ {
				dir := strings.Join(parts[:i], "/")
				found := false
				for _, d := range next.Snapshot.Directories {
					if d == dir {
						found = true
					}
				}
				if !found {
					next.Snapshot.Directories = append(next.Snapshot.Directories, dir)
				}
			}
			now[change.Path] = c.Entry{Path: change.Path, Hash: c.HashBytes(change.After), Size: int64(len(change.After)), Mode: mode}
		}
	}
	total := int64(0)
	next.Snapshot.Entries = []c.Entry{}
	for _, e := range now {
		total += e.Size
		next.Snapshot.Entries = append(next.Snapshot.Entries, e)
	}
	if len(now) > 10000 || total > 32<<20 {
		return Capture{}, c.Fail(c.InvalidArgument, "proposal exceeds candidate preview quota")
	}
	if err := validateShape(next); err != nil {
		return Capture{}, err
	}
	if err := seal(&next.Snapshot); err != nil {
		return Capture{}, err
	}
	return next, nil
}
func listingAllowed(directory string, layers []policy.Policy) bool {
	if len(layers) == 0 {
		return false
	}
	for _, p := range layers {
		if directory == "." {
			ok := false
			for _, scope := range p.Paths {
				if scope == "**" {
					ok = true
				}
			}
			if !ok {
				return false
			}
		} else if !policy.PathAllowed(directory+"/__listing_probe__", p.Paths) {
			return false
		}
	}
	return true
}

func coverageExcluded(capture Capture, p string) bool {
	if withinExcluded(capture.Snapshot, p) {
		return true
	}
	if capture.Snapshot.Git == nil {
		return false
	}
	rules, err := compileIgnores(capture.IgnoreSources)
	if err != nil {
		return true
	}
	for _, entry := range capture.Snapshot.Git.Entries {
		if entry.Path == p {
			return false
		}
	}
	return ignored(rules, p, false)
}

func ListingAllowed(directory string, layers []policy.Policy) bool {
	return listingAllowed(directory, layers)
}
