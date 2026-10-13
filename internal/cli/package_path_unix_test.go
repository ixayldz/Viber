//go:build linux || darwin

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUnixManagedLauncherRunsQuotedTargetAndForwardsArguments(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target's directory", "viber")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\nprintf '%s' \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	launcher, err := unixShellLauncher(target)
	if err != nil {
		t.Fatal(err)
	}
	launcherPath := filepath.Join(root, "managed-bin", unixLauncherFile)
	if err = os.Mkdir(filepath.Dir(launcherPath), 0700); err != nil {
		t.Fatal(err)
	}
	managedRoot, err := os.OpenRoot(filepath.Dir(launcherPath))
	if err != nil {
		t.Fatal(err)
	}
	if err = publishUnixPrivateFile(managedRoot, unixLauncherFile, launcher, 0700); err != nil {
		managedRoot.Close()
		t.Fatal(err)
	}
	managedRoot.Close()
	command := exec.Command(launcherPath, "hello world")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("launcher failed: %v: %s", err, output)
	}
	if string(output) != "hello world" {
		t.Fatalf("argument not preserved: got %q", output)
	}
}
