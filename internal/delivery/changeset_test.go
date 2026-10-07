package delivery

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

func TestUnifiedPatchRoundTripExactBytesWithRealGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable for external patch interoperability test")
	}
	source := t.TempDir()
	oldFiles := map[string][]byte{"a.txt": []byte("one\r\ntwo\r\nthree\r\n"), "no-final.txt": []byte("old without newline"), "deleted.txt": []byte("remove\n"), "empty-delete": []byte{}, "unicode ü.txt": []byte("unchanged\n")}
	for name, raw := range oldFiles {
		if err = os.WriteFile(filepath.Join(source, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, err := workspace.CaptureDirectory(source, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	newFiles := map[string][]byte{"a.txt": []byte("one\r\nchanged\r\nthree\r\n"), "no-final.txt": []byte("new without newline"), "empty-add": []byte{}, "new ü file.txt": []byte("new\n"), "unicode ü.txt": []byte("updated\n")}
	for name := range oldFiles {
		if _, ok := newFiles[name]; !ok {
			if err = os.Remove(filepath.Join(source, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, raw := range newFiles {
		if err = os.WriteFile(filepath.Join(source, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	after, err := workspace.CaptureDirectory(source, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	changes, patch, err := Build(before, after)
	if err != nil || !changes.PatchComplete || len(changes.Changes) != 7 {
		t.Fatal(changes, err)
	}
	target := t.TempDir()
	for name, raw := range oldFiles {
		if err = os.WriteFile(filepath.Join(target, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	input := t.TempDir()
	patchPath := filepath.Join(input, "changes.patch")
	configPath := filepath.Join(input, "empty-config")
	if err = os.WriteFile(patchPath, patch, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"apply", "--check", "--whitespace=nowarn", patchPath}, {"apply", "--whitespace=nowarn", patchPath}} {
		command := exec.Command(git, args...)
		command.Dir = target
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "SystemRoot=" + os.Getenv("SystemRoot"), "HOME=" + input, "USERPROFILE=" + input, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + configPath}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("patch rejected: %v\n%s\n%s", err, output, patch)
		}
	}
	for name, expected := range newFiles {
		raw, err := os.ReadFile(filepath.Join(target, name))
		if err != nil || !bytes.Equal(raw, expected) {
			t.Fatal("patch byte mismatch", name, err)
		}
	}
	for name := range oldFiles {
		if _, ok := newFiles[name]; !ok {
			if _, err = os.Stat(filepath.Join(target, name)); !os.IsNotExist(err) {
				t.Fatal("deletion absent", name, err)
			}
		}
	}
}
func TestBinaryCandidateMakesTextPatchExplicitlyIncomplete(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := workspace.CaptureDirectory(source, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	state := c.TaskState{SchemaVersion: 1, TaskID: "t", SpecVersion: 1, Execution: c.Running, PolicyEpoch: 1, KernelGeneration: 1}
	proposal := c.Proposal{SchemaVersion: 1, ID: "p", TaskID: "t", SpecVersion: 1, BaseSnapshot: before.Snapshot.Digest, PolicyEpoch: 1, KernelGeneration: 1, ReadSet: []c.ReadCondition{{Path: "a.txt", Kind: "FILE", Digest: c.HashBytes([]byte("base"))}}, Changes: []c.Change{{Path: "a.txt", BeforeDigest: c.HashBytes([]byte("base")), After: []byte{0, 1, 255}}}}
	after, err := workspace.Preview(before, before, proposal, []policy.Policy{{SchemaVersion: 1, Epoch: 1, Generation: 1, Effects: []string{"snapshot.read", "candidate.write"}, Paths: []string{"**"}}}, state)
	if err != nil {
		t.Fatal(err)
	}
	result, patch, err := Build(before, after)
	if err != nil || result.PatchComplete || result.Changes[0].TextPatch || len(patch) != 0 {
		t.Fatal("binary silently presented as complete patch", result, err)
	}
}
