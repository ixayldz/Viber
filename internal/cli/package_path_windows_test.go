//go:build windows

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedPathLauncherExecutesVersionedBinary(t *testing.T) {
	if os.Getenv("VIBER_MANAGED_LAUNCHER_CHILD") == "1" {
		return
	}
	root := t.TempDir()
	prefix := filepath.Join(root, "managed install")
	revision := strings.Repeat("a", 40)
	versionDir := filepath.Join(prefix, "versions", revision)
	if err := os.MkdirAll(versionDir, 0700); err != nil {
		t.Fatal(err)
	}
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(versionDir, "viber.exe")
	if err = os.WriteFile(binaryPath, binary, 0700); err != nil {
		t.Fatal(err)
	}
	verifiedPath, verifiedRevision, _, err := verifyActiveBinary(binaryPath, prefix)
	if err != nil || verifiedRevision != revision {
		t.Fatalf("verify active binary: revision=%q err=%v", verifiedRevision, err)
	}
	launcher, err := managedCommand(verifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	launcherPath := filepath.Join(root, managedPathLauncher)
	if err = os.WriteFile(launcherPath, launcher, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd.exe", "/d", "/c", launcherPath, "-test.run=TestManagedPathLauncherExecutesVersionedBinary")
	cmd.Env = append(os.Environ(), "VIBER_MANAGED_LAUNCHER_CHILD=1")
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("launcher failed: %v: %s", runErr, output)
	}
}

func TestManagedPathPreflightPreservesExistingViberCommand(t *testing.T) {
	path := t.TempDir()
	managed := filepath.Join(t.TempDir(), managedPathDirectory)
	foreign := filepath.Join(path, "viber.cmd")
	const contents = "@echo off\r\necho foreign\r\n"
	if err := os.WriteFile(foreign, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", path)
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	if err := ensureNoExistingViberOnPath(managed); err == nil {
		t.Fatal("expected existing foreign Viber command to block launcher installation")
	}
	raw, err := os.ReadFile(foreign)
	if err != nil || string(raw) != contents {
		t.Fatalf("foreign command changed: err=%v contents=%q", err, raw)
	}
}

func TestManagedCommandRejectsCMDExpansionCharacters(t *testing.T) {
	for _, value := range []string{`C:\Users\A&B\viber.exe`, `C:\Users\A%TEMP%\viber.exe`, `C:\Users\A!\viber.exe`} {
		if err := validateCommandPath(value); err == nil {
			t.Fatalf("expected unsafe path %q to be rejected", value)
		}
	}
}
