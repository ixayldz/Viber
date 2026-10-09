//go:build !windows && !linux && !darwin

package cli

import "os/exec"

func detachProcess(cmd *exec.Cmd) {}
