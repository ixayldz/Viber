//go:build windows

package delivery

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestExclusiveOpenRefusesMappedViewAfterSourceHandlesClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapped.bin")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	mapping, err := windows.CreateFileMapping(windows.Handle(file.Fd()), nil, windows.PAGE_READWRITE, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	view, err := windows.MapViewOfFile(mapping, windows.FILE_MAP_WRITE, 0, 0, uintptr(len("before")))
	if err != nil {
		windows.CloseHandle(mapping)
		file.Close()
		t.Fatal(err)
	}
	if err = windows.CloseHandle(mapping); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	locked, openErr := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	openedExclusively := openErr == nil
	if openErr == nil {
		_ = windows.CloseHandle(locked)
	}
	value := byte('X')
	var written uintptr
	if err = windows.WriteProcessMemory(windows.CurrentProcess(), view, &value, 1, &written); err != nil || written != 1 {
		t.Fatalf("write mapped view: %v, bytes=%d", err, written)
	}
	if err = windows.FlushViewOfFile(view, uintptr(len("before"))); err != nil {
		t.Fatal(err)
	}
	_ = windows.UnmapViewOfFile(view)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "Xefore" {
		t.Fatalf("mapped view did not remain writable across closed source handles: %q", raw)
	}
	if openedExclusively {
		t.Fatal("exclusive open admitted a live mapped writer after its file and mapping handles closed")
	}
}
