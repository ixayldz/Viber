//go:build windows

package delivery

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func nativeTestIntent(t *testing.T, root, path string, before, after []byte) NativeFileIntent {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, before, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	return NativeFileIntent{Path: path, Before: before, After: after, Mode: uint32(info.Mode().Perm())}
}

func TestNativeTargetExclusiveLeaseAppliesOnlySealedExistingFile(t *testing.T) {
	root := t.TempDir()
	intent := nativeTestIntent(t, root, "nested/a.txt", []byte("before\r\n"), []byte("after\r\n"))
	seal, err := InspectNativeTarget(root, []NativeFileIntent{intent})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := AcquireNativeTarget(context.Background(), seal, map[string]string{"nested/a.txt": c.HashBytes(intent.Before)})
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.WriteFile(context.Background(), intent.Path, c.HashBytes(intent.Before), intent.After); err != nil {
		lease.Close()
		t.Fatal(err)
	}
	readback, err := lease.ReadFile(intent.Path, nativeMaxFileBytes)
	if err != nil || c.HashBytes(readback) != c.HashBytes(intent.After) {
		lease.Close()
		t.Fatalf("sealed write did not read back exactly: %q %v", readback, err)
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	readback, err = os.ReadFile(filepath.Join(root, filepath.FromSlash(intent.Path)))
	if err != nil || c.HashBytes(readback) != c.HashBytes(intent.After) {
		t.Fatalf("sealed write did not persist: %q %v", readback, err)
	}
}

func TestNativeTargetAdmissionRejectsExistingOpenWriterWithoutChangingIt(t *testing.T) {
	root := t.TempDir()
	intent := nativeTestIntent(t, root, "a.txt", []byte("before"), []byte("candidate"))
	seal, err := InspectNativeTarget(root, []NativeFileIntent{intent})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := os.OpenFile(filepath.Join(root, intent.Path), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	lease, acquireErr := AcquireNativeTarget(context.Background(), seal, map[string]string{intent.Path: c.HashBytes(intent.Before)})
	if acquireErr == nil {
		_ = lease.Close()
		writer.Close()
		t.Fatal("exclusive target admission accepted an already-open writer")
	}
	userEdit := []byte("user edit")
	if err = writer.Truncate(0); err == nil {
		_, err = writer.WriteAt(userEdit, 0)
	}
	if err == nil {
		err = writer.Sync()
	}
	closeErr := writer.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	got, err := os.ReadFile(filepath.Join(root, intent.Path))
	if err != nil || string(got) != string(userEdit) {
		t.Fatalf("failed admission disturbed the pre-existing writer: %q %v", got, err)
	}
}
