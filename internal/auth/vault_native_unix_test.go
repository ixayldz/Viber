//go:build !windows

package auth

import (
	"bytes"
	"crypto/rand"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
	"runtime"
	"testing"
)

func TestActualOSVaultRoundTripAndEnvironmentIsolation(t *testing.T) {
	if os.Getenv("VIBER_OS_VAULT_TEST") != "1" {
		t.Skip("opt-in real desktop credential vault")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Fatal("unsupported native acceptance platform")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	handle := c.HashBytes(append([]byte("Viber native vault acceptance unique item"), random[:]...))
	// Only this fresh digest-addressed item is removed; no keychain/default
	// selection, existing item, account or credential directory is modified.
	cleanup := func() {
		executable := "/usr/bin/secret-tool"
		args := []string{"clear", "service", "com.viber.auth.v2", "account", handle}
		if runtime.GOOS == "darwin" {
			executable = "/usr/bin/security"
			args = []string{"delete-generic-password", "-s", "com.viber.auth.v2", "-a", handle}
		}
		result := execVaultHelper(executable, args, nil)
		clear(result.Output)
		if result.Err != nil || result.Exit != 0 {
			t.Errorf("could not remove unique acceptance key item")
		}
	}
	t.Cleanup(cleanup)
	// Invalid loader paths would produce stderr if accidentally inherited.
	// No attack executable or library is created or loaded by this fixture.
	t.Setenv("PATH", "/viber-missing-injection-directory")
	t.Setenv("LD_PRELOAD", "/viber-missing-injection-library.so")
	t.Setenv("DYLD_INSERT_LIBRARIES", "/viber-missing-injection-library.dylib")
	t.Setenv("PYTHONPATH", "/viber-missing-injection-directory")
	vault := commandVault{}
	if _, err := vault.Get(handle); !errors.Is(err, errVaultKeyMissing) {
		t.Fatal("native missing-item contract", err)
	}
	if err := vault.Put(handle, random[:]); err != nil {
		t.Fatal("native key write", err)
	}
	actual, err := vault.Get(handle)
	defer clear(actual)
	if err != nil || !bytes.Equal(actual, random[:]) {
		t.Fatal("native vault exact readback failed")
	}
	t.Logf("actual %s exact roundtrip PASS; unique fixture item cleanup required; no provider credentials", vault.Backend())
}
