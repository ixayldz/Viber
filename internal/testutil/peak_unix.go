//go:build linux || darwin

package testutil

import (
	"golang.org/x/sys/unix"
	"runtime"
)

func PeakResidentBytes() (int64, error) {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return 0, err
	}
	peak := int64(usage.Maxrss)
	if runtime.GOOS == "linux" {
		peak *= 1024
	}
	return peak, nil
}
