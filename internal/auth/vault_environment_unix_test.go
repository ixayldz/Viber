//go:build !windows

package auth

import (
	"strings"
	"testing"
)

func TestVaultHelperEnvironmentDropsLoaderAndExecutableInjection(t *testing.T) {
	source := []string{"PATH=/repo/bin", "LD_PRELOAD=/repo/attack.so", "LD_LIBRARY_PATH=/repo",
		"DYLD_INSERT_LIBRARIES=/repo/attack.dylib", "PYTHONPATH=/repo", "GCONV_PATH=/repo",
		"BASH_ENV=/repo/shell", "HTTP_PROXY=http://repo", "SECRET_TOKEN=canary",
		"HOME=/home/user", "XDG_RUNTIME_DIR=/run/user/1000", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "USER=old", "USER=current", "LOGNAME=bad\nvalue"}
	joined := strings.Join(vaultEnvironment(source), "\n")
	for _, attack := range []string{"/repo", "canary", "bad\nvalue", "USER=old"} {
		if strings.Contains(joined, attack) {
			t.Fatal("injected helper environment", attack)
		}
	}
	for _, required := range []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "HOME=/home/user", "USER=current", "XDG_RUNTIME_DIR=/run/user/1000", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus"} {
		if !strings.Contains(joined, required) {
			t.Fatal("desktop IPC identity missing", required)
		}
	}
}
