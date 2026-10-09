//go:build windows

// Package testutil supplies measurements to acceptance tests only.
package testutil

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

// PeakResidentBytes is the OS process-wide peak working set, including test
// runner/setup. It is never attributed solely to an index or used as a quota.
func PeakResidentBytes() (int64, error) {
	counters := struct {
		Size, PageFaults                                                                                             uint32
		PeakWorkingSet, WorkingSet, PeakPagedPool, PagedPool, PeakNonPagedPool, NonPagedPool, Pagefile, PeakPagefile uintptr
	}{}
	counters.Size = uint32(unsafe.Sizeof(counters))
	procedure := windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")
	if err := procedure.Find(); err != nil {
		return 0, err
	}
	success, _, err := procedure.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size))
	if success == 0 {
		return 0, err
	}
	return int64(counters.PeakWorkingSet), nil
}
