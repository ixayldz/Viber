package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
)

func write(t *testing.T, root, path string, data []byte) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func capture(t *testing.T, root string) Capture {
	t.Helper()
	v, err := CaptureDirectory(root, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func proposal(base Capture, path string, after []byte) (c.Proposal, []policy.Policy, c.TaskState) {
	e := entryMap(base.Snapshot)[path]
	p := c.Proposal{SchemaVersion: 1, ID: "p", TaskID: "t", SpecVersion: 1, BaseSnapshot: base.Snapshot.Digest, PolicyEpoch: 1, KernelGeneration: 1,
		ReadSet: []c.ReadCondition{{Path: path, Kind: "FILE", Digest: e.Hash}}, Changes: []c.Change{{Path: path, BeforeDigest: e.Hash, After: after}}}
	layers := []policy.Policy{{SchemaVersion: 1, Epoch: 1, Generation: 1, Effects: []string{"snapshot.read", "candidate.write"}, Paths: []string{"**"}}}
	state := c.TaskState{SchemaVersion: 1, TaskID: "t", SpecVersion: 1, Execution: c.Running, PolicyEpoch: 1, KernelGeneration: 1}
	return p, layers, state
}
func TestExactCaptureAndNonmutatingPreview(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/a.txt", []byte("one\r\n"))
	write(t, root, ".env", []byte("secret"))
	write(t, root, ".git/config", []byte("untrusted helper"))
	if err := os.Mkdir(filepath.Join(root, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	base := capture(t, root)
	if len(base.Snapshot.Entries) != 1 || len(base.Snapshot.Exclusions) != 2 || len(base.Snapshot.Directories) != 2 {
		t.Fatal(base.Snapshot)
	}
	if !bytes.Equal(base.Contents["src/a.txt"], []byte("one\r\n")) {
		t.Fatal("CRLF normalized")
	}
	p, layers, authority := proposal(base, "src/a.txt", []byte("two\r\n"))
	next, err := Preview(base, base, p, layers, authority)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(next.Contents["src/a.txt"], p.Changes[0].After) {
		t.Fatal("candidate patch missing")
	}
	onDisk, _ := os.ReadFile(filepath.Join(root, "src/a.txt"))
	if !bytes.Equal(onDisk, base.Contents["src/a.txt"]) {
		t.Fatal("live user data changed")
	}
	next.Contents["src/a.txt"][0] = 'X'
	if base.Contents["src/a.txt"][0] != 'o' {
		t.Fatal("candidate aliases base bytes")
	}
}
func TestUserEditStaleProposalAndUnrelatedPreservation(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", []byte("base"))
	write(t, root, "b.txt", []byte("user"))
	base := capture(t, root)
	p, layers, state := proposal(base, "a.txt", []byte("candidate"))
	write(t, root, "a.txt", []byte("user edit"))
	if _, err := Preview(base, capture(t, root), p, layers, state); err == nil {
		t.Fatal("stale overwrite admitted")
	}
	write(t, root, "a.txt", []byte("base"))
	write(t, root, "b.txt", []byte("new independent edit"))
	next, err := Preview(base, capture(t, root), p, layers, state)
	if err != nil {
		t.Fatal(err)
	}
	if string(next.Contents["b.txt"]) != "new independent edit" {
		t.Fatal("independent user edit lost")
	}
}
func TestNegativeReadAndListingInvalidate(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", []byte("base"))
	base := capture(t, root)
	p, layers, state := proposal(base, "a.txt", []byte("candidate"))
	p.ReadSet = append(p.ReadSet, c.ReadCondition{Path: "new/config", Kind: "ABSENT"})
	write(t, root, "new/config", []byte("now present"))
	if _, err := Preview(base, capture(t, root), p, layers, state); err == nil {
		t.Fatal("phantom path not invalidated")
	}
	root = t.TempDir()
	write(t, root, "a.txt", []byte("base"))
	base = capture(t, root)
	p, layers, state = proposal(base, "a.txt", []byte("candidate"))
	listing, _ := ListingDigest(base.Snapshot, ".")
	p.ReadSet = append(p.ReadSet, c.ReadCondition{Path: ".", Kind: "LISTING", Digest: listing})
	if err := os.Mkdir(filepath.Join(root, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Preview(base, capture(t, root), p, layers, state); err == nil {
		t.Fatal("new empty directory not observed")
	}
}
func TestPathAndReadPolicyCannotBeExpanded(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/a.txt", []byte("base"))
	write(t, root, "private.txt", []byte("private"))
	base := capture(t, root)
	p, layers, state := proposal(base, "src/a.txt", []byte("candidate"))
	restricted := layers[0]
	restricted.Paths = []string{"src/**"}
	layers = append(layers, restricted)
	p.ReadSet = append(p.ReadSet, c.ReadCondition{Path: "private.txt", Kind: "FILE", Digest: entryMap(base.Snapshot)["private.txt"].Hash})
	if _, err := Preview(base, base, p, layers, state); err == nil {
		t.Fatal("read escaped second policy layer")
	}
	p, layers, state = proposal(base, "src/a.txt", []byte("candidate"))
	p.SpecVersion++
	if _, err := Preview(base, base, p, layers, state); err == nil {
		t.Fatal("stale spec accepted")
	}
	p.SpecVersion--
	state.InputBarrier = true
	if _, err := Preview(base, base, p, layers, state); err == nil {
		t.Fatal("input barrier bypass")
	}
}
func TestExcludedAbsenceAndSymlinksCannotBeAssumed(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", []byte("base"))
	write(t, root, ".env", []byte("secret"))
	base := capture(t, root)
	p, layers, state := proposal(base, "a.txt", []byte("candidate"))
	p.ReadSet = append(p.ReadSet, c.ReadCondition{Path: ".env", Kind: "ABSENT"})
	if _, err := Preview(base, base, p, layers, state); err == nil {
		t.Fatal("excluded secret declared absent")
	}
	target := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "linked")); err != nil {
		t.Skip("OS symlink privilege unavailable")
	}
	if _, err := CaptureDirectory(root, DefaultLimits()); err == nil {
		t.Fatal("symlink followed")
	}
}
func TestQuotaAndCaseCollision(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", []byte("long"))
	if _, err := CaptureDirectory(root, Limits{MaxFiles: 1, MaxFileBytes: 2, MaxTotalBytes: 2}); err == nil {
		t.Fatal("quota bypass")
	}
	base := capture(t, root)
	p, layers, state := proposal(base, "a.txt", []byte("candidate"))
	p.ReadSet = append(p.ReadSet, c.ReadCondition{Path: "A.txt", Kind: "ABSENT"})
	p.Changes = append(p.Changes, c.Change{Path: "A.txt", After: []byte("alias")})
	if _, err := Preview(base, base, p, layers, state); err == nil {
		t.Fatal("case alias accepted")
	}
}

func TestCaptureCannotSilentlyTreatFileAsEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", []byte("data"))
	if _, err := CaptureDirectory(filepath.Join(root, "a.txt"), DefaultLimits()); err == nil {
		t.Fatal("regular file root accepted as empty source")
	}
	limits := DefaultLimits()
	limits.MaxFileBytes = 1 << 62
	if _, err := CaptureDirectory(root, limits); err == nil {
		t.Fatal("unbounded read quota accepted")
	}
}
