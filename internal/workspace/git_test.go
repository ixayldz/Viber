package workspace

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func fixtureGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	binary, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git fixture binary unavailable")
	}
	base := []string{"-c", "core.autocrlf=false", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + filepath.Join(root, "disabled-hooks"), "-c", "commit.gpgSign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-C", root}
	command := exec.Command(binary, append(base, args...)...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "SystemRoot=" + os.Getenv("SystemRoot"), "HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0"}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatal(args, err, string(output))
	}
	return string(output)
}
func gitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	fixtureGit(t, root, "init")
	write(t, root, "a.txt", []byte("baseline\r\n"))
	write(t, root, "b.txt", []byte("baseline-b\n"))
	fixtureGit(t, root, "add", "a.txt", "b.txt")
	fixtureGit(t, root, "commit", "-m", "fixture")
	return root
}
func TestNativeGitCapturePreservesDirtyIndexAndIgnoredCoverage(t *testing.T) {
	root := gitFixture(t)
	write(t, root, "a.txt", []byte("staged\r\n"))
	fixtureGit(t, root, "add", "a.txt")
	write(t, root, "a.txt", []byte("unstaged\r\n"))
	write(t, root, "new.txt", []byte("untracked"))
	write(t, root, ".gitignore", []byte("private/\n!private/public.txt\nignored*.txt\n!ignored-keep.txt\n/nested-only.txt\n"))
	write(t, root, "private/token.txt", []byte("private"))
	write(t, root, "private/public.txt", []byte("still excluded because parent excluded"))
	write(t, root, "ignored-secret.txt", []byte("secret"))
	write(t, root, "ignored-keep.txt", []byte("included"))
	write(t, root, "nested/nested-only.txt", []byte("included"))
	beforeIndex, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	// All these config entries would be dangerous if initial discovery invoked
	// ordinary host Git. The native capture never loads or executes them.
	fixtureGit(t, root, "config", "core.fsmonitor", "malicious-fsmonitor")
	fixtureGit(t, root, "config", "filter.hostile.clean", "malicious-clean")
	fixtureGit(t, root, "config", "include.path", filepath.Join(root, "must-not-be-loaded"))
	capture, err := CaptureRepository(root, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if string(capture.Contents["a.txt"]) != "unstaged\r\n" || string(capture.Contents["new.txt"]) != "untracked" {
		t.Fatal("dirty bytes lost")
	}
	for _, name := range []string{"private/token.txt", "private/public.txt", "ignored-secret.txt"} {
		if _, ok := capture.Contents[name]; ok {
			t.Fatal("ignored source leaked", name)
		}
	}
	for _, name := range []string{"ignored-keep.txt", "nested/nested-only.txt"} {
		if _, ok := capture.Contents[name]; !ok {
			t.Fatal("ignore negation/anchoring wrong", name)
		}
	}
	if capture.Snapshot.Git == nil || capture.Snapshot.Git.IndexDigest != c.HashBytes(beforeIndex) || !bytes.Equal(capture.IndexBytes, beforeIndex) {
		t.Fatal("staged index lost")
	}
	afterIndex, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	if !bytes.Equal(beforeIndex, afterIndex) {
		t.Fatal("host index mutated")
	}
	if err := VerifyCapture(capture); err != nil {
		t.Fatal(err)
	}
}
func TestNativeGitIndexVersionsAndUnknownExtensions(t *testing.T) {
	for _, version := range []int{2, 3, 4} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			root := gitFixture(t)
			write(t, root, "long/directory/file-one.txt", []byte("one"))
			write(t, root, "long/directory/file-two.txt", []byte("two"))
			fixtureGit(t, root, "add", ".")
			fixtureGit(t, root, "update-index", "--index-version="+strconv.Itoa(version))
			capture, err := CaptureRepository(root, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if len(capture.Snapshot.Git.Entries) != 4 {
				t.Fatal("compressed entries lost")
			}
			raw := append([]byte(nil), capture.IndexBytes...)
			raw[20] ^= 1
			if _, _, err := parseIndex(raw); err == nil {
				t.Fatal("index corruption accepted")
			}
			raw = append([]byte(nil), capture.IndexBytes[:len(capture.IndexBytes)-20]...)
			raw = append(raw, []byte("link")...)
			raw = append(raw, 0, 0, 0, 0)
			checksum := sha1.Sum(raw)
			raw = append(raw, checksum[:]...)
			if _, _, err := parseIndex(raw); err == nil {
				t.Fatal("split index silently accepted")
			}
		})
	}
}
func TestGitHeadIndexAndIgnoreChangesInvalidateProposal(t *testing.T) {
	root := gitFixture(t)
	base, err := CaptureRepository(root, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	p, layers, state := proposal(base, "a.txt", []byte("candidate"))
	write(t, root, "b.txt", []byte("independent"))
	current, err := CaptureRepository(root, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	next, err := Preview(base, current, p, layers, state)
	if err != nil {
		t.Fatal(err)
	}
	if string(next.Contents["b.txt"]) != "independent" {
		t.Fatal("independent user edit lost")
	}
	fixtureGit(t, root, "add", "b.txt")
	current, err = CaptureRepository(root, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Preview(base, current, p, layers, state); err == nil {
		t.Fatal("index change accepted")
	}
}
func TestGitSHA256AndLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	fixtureGit(t, root, "init", "--object-format=sha256")
	write(t, root, "a.txt", []byte("sha256"))
	fixtureGit(t, root, "add", ".")
	fixtureGit(t, root, "commit", "-m", "sha256")
	capture, err := CaptureRepository(root, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if capture.Snapshot.Git.ObjectFormat != "sha256" {
		t.Fatal("object format lost")
	}
	linked := filepath.Join(t.TempDir(), "linked")
	fixtureGit(t, root, "worktree", "add", "-b", "linked", linked)
	other, err := CaptureRepository(linked, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if other.Snapshot.Git.CommonDir != capture.Snapshot.Git.CommonDir || other.Snapshot.Git.GitDir == capture.Snapshot.Git.GitDir {
		t.Fatal("shared Git identity lost")
	}
}
func TestIgnoreVectors(t *testing.T) {
	sources := map[string][]byte{".gitignore": []byte("cache/\n!cache/keep.txt\n*.log\n!important.log\nroot/*.tmp\na/**/b\n\\#name\nspace\\ \n"), "nested/.gitignore": []byte("!keep.log\nprivate\n")}
	rules, err := compileIgnores(sources)
	if err != nil {
		t.Fatal(err)
	}
	vectors := map[string]bool{"cache/keep.txt": true, "nested/file.log": true, "important.log": false, "nested/keep.log": false, "root/one.tmp": true, "nested/root/one.tmp": false, "a/b": true, "a/one/two/b": true, "#name": true, "space ": true, "nested/private": true, "private": false}
	for name, expected := range vectors {
		if actual := ignored(rules, name, false); actual != expected {
			t.Errorf("%s: got %v want %v", name, actual, expected)
		}
	}
}
func FuzzGitIndex(f *testing.F) {
	raw := []byte("DIRC")
	raw = binary.BigEndian.AppendUint32(raw, 2)
	raw = binary.BigEndian.AppendUint32(raw, 0)
	sum := sha1.Sum(raw)
	raw = append(raw, sum[:]...)
	f.Add(raw)
	f.Add([]byte("invalid"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		entries, format, err := parseIndex(raw)
		if err == nil {
			if format != "sha1" && format != "sha256" {
				t.Fatal("bad format")
			}
			for _, e := range entries {
				if strings.Contains(e.Path, "..") && e.Path == ".." {
					t.Fatal("unsafe path")
				}
			}
		}
	})
}
