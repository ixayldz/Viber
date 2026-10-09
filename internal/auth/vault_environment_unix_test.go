//go:build !windows

package auth

import (
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"runtime"
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

func TestVaultIPCRejectsRemoteTransportAndMixedFallback(t *testing.T) {
	for _, address := range []string{
		"tcp:host=example.com,port=1234",
		"nonce-tcp:host=localhost,port=1234",
		"autolaunch:",
		"unix:path=/run/user/1000/bus;tcp:host=example.com,port=1234",
		"unix:path=relative/bus", "unix:path=/bus,path=/other",
		"unix:path=/bus,abstract=other", "unix:path=/bus,unknown=1",
	} {
		if localVaultAddress(address) {
			t.Fatal("unsafe vault transport admitted")
		}
		env := strings.Join(vaultEnvironment([]string{"DBUS_SESSION_BUS_ADDRESS=" + address}), "\n")
		if strings.Contains(env, "DBUS_SESSION_BUS_ADDRESS=") {
			t.Fatal("remote vault environment propagated")
		}
		if runtime.GOOS == "linux" {
			t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
			result := execVaultHelper("/usr/bin/secret-tool", nil, nil)
			var failure *c.Error
			if !errors.As(result.Err, &failure) || failure.Code != c.PolicyDenied {
				t.Fatal("explicit remote bus silently fell back", result.Err)
			}
		}
	}
	for _, address := range []string{"unix:path=/run/user/1000/bus", "unix:abstract=/tmp/dbus-fixture", "unix:path=/bus,guid=0123456789abcdef0123456789abcdef", "unix:path=/bus;unix:abstract=fallback"} {
		if !localVaultAddress(address) {
			t.Fatal("supported local desktop IPC refused", address)
		}
	}
}
