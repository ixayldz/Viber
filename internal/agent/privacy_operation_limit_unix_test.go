//go:build linux || darwin

package agent

import "golang.org/x/sys/unix"

// Applied only inside a disposable test subprocess, never to the caller's host.
func operationTestDescriptorLimit() error {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		return err
	}
	if limit.Cur > 64 {
		limit.Cur = 64
	}
	return unix.Setrlimit(unix.RLIMIT_NOFILE, &limit)
}
