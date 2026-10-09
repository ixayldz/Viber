//go:build !windows

package auth

import (
	"bytes"
	"encoding/base64"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type fixtureVault struct {
	mu          sync.Mutex
	keys        map[string][]byte
	unavailable bool
}

func (*fixtureVault) Backend() string { return "OFFLINE_TEST_VAULT" }
func (v *fixtureVault) Get(handle string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.unavailable {
		return nil, errors.New("unavailable")
	}
	key, ok := v.keys[handle]
	if !ok {
		return nil, errVaultKeyMissing
	}
	return append([]byte{}, key...), nil
}
func (v *fixtureVault) Put(handle string, key []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.unavailable {
		return errors.New("unavailable")
	}
	v.keys[handle] = append([]byte{}, key...)
	return nil
}
func TestMain(m *testing.M) {
	// Offline auth tests verify encryption/rotation contracts. They do not claim
	// that a real desktop Secret Service or macOS Keychain was exercised.
	osCredentialVault = &fixtureVault{keys: map[string][]byte{}}
	os.Exit(m.Run())
}
func TestVaultEncryptionRejectsTamperMissingKeyAndPlaintextFallback(t *testing.T) {
	original := osCredentialVault
	fixture := &fixtureVault{keys: map[string][]byte{}}
	osCredentialVault = fixture
	defer func() { osCredentialVault = original }()
	data := state{SchemaVersion: 1, HostID: hostID(), Profiles: []Profile{}}
	plain, _ := c.CanonicalV1(data)
	encrypted, err := protect(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte(data.HostID)) {
		t.Fatal("host metadata written plaintext")
	}
	actual, err := unprotect(encrypted)
	if err != nil || !bytes.Equal(actual, plain) {
		t.Fatal("encrypted roundtrip", err)
	}
	if _, err = unprotect(plain); err == nil {
		t.Fatal("legacy plaintext silently accepted")
	}
	var envelope credentialEnvelope
	if c.DecodeStrict(encrypted, &envelope) != nil {
		t.Fatal("bad fixture envelope")
	}
	for _, attack := range []string{"cipher", "nonce", "handle", "backend"} {
		altered := envelope
		altered.Ciphertext = append([]byte{}, envelope.Ciphertext...)
		altered.Nonce = append([]byte{}, envelope.Nonce...)
		switch attack {
		case "cipher":
			altered.Ciphertext[0] ^= 1
		case "nonce":
			altered.Nonce[0] ^= 1
		case "handle":
			altered.Handle = c.HashBytes([]byte("wrong"))
		case "backend":
			altered.Backend = "OTHER"
		}
		raw, _ := c.CanonicalV1(altered)
		if _, err = unprotect(raw); err == nil {
			t.Fatal("credential mutation accepted", attack)
		}
	}
	delete(fixture.keys, envelope.Handle)
	if _, err = unprotect(encrypted); err == nil {
		t.Fatal("missing key reconstructed")
	}
	fixture.unavailable = true
	if _, err = protect(plain); err == nil {
		t.Fatal("vault outage allowed plaintext")
	}
}
func TestLegacyCredentialMigrationIsExplicitEncryptedAndIdentityPreserving(t *testing.T) {
	original := osCredentialVault
	osCredentialVault = &fixtureVault{keys: map[string][]byte{}}
	defer func() { osCredentialVault = original }()
	directory := t.TempDir()
	data := state{SchemaVersion: 1, HostID: hostID(), PendingClient: "viber-client-one", Profiles: []Profile{}}
	raw, _ := c.CanonicalV1(data)
	if err := os.WriteFile(filepath.Join(directory, "credentials.bin"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(directory, false); err == nil {
		opened.Close()
		t.Fatal("plaintext opened without migration")
	}
	view, err := MigrateLegacy(directory)
	if err != nil || view.CredentialProtection != credentialProtection {
		t.Fatal(view, err)
	}
	encrypted, err := os.ReadFile(filepath.Join(directory, "credentials.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("viber-client-one")) || bytes.Contains(encrypted, []byte(data.HostID)) {
		t.Fatal("migration retained plaintext")
	}
	opened, err := Open(directory, false)
	if err != nil {
		t.Fatal(err)
	}
	if opened.data.HostID != data.HostID || opened.data.PendingClient != data.PendingClient {
		t.Fatal("migration changed account identity")
	}
	opened.Close()
	if _, err = MigrateLegacy(directory); err != nil {
		t.Fatal("migration retry not idempotent", err)
	}
}
func TestVaultHelperNeverPlacesKeyInArgvAndRejectsNoisyMissingResult(t *testing.T) {
	original := runVaultHelper
	defer func() { runVaultHelper = original }()
	vault := commandVault{}
	handle := c.HashBytes([]byte("handle"))
	key := bytes.Repeat([]byte{7}, 32)
	encoded := base64.StdEncoding.EncodeToString(key)
	runVaultHelper = func(executable string, args []string, input []byte) vaultResponse {
		if strings.Contains(strings.Join(args, " "), encoded) {
			t.Fatal("encryption key in process argv")
		}
		if runtime.GOOS == "darwin" {
			if executable != "/usr/bin/security" || !bytes.Contains(input, []byte(encoded)) || strings.Join(args, " ") != "-q -i" {
				t.Fatal("Keychain stdin protocol")
			}
		} else {
			if executable != "/usr/bin/secret-tool" || string(input) != encoded || args[0] != "store" {
				t.Fatal("Secret Service stdin protocol")
			}
		}
		return vaultResponse{Exit: 0}
	}
	if err := vault.Put(handle, key); err != nil {
		t.Fatal(err)
	}
	runVaultHelper = func(string, []string, []byte) vaultResponse { return vaultResponse{Exit: 0, Output: []byte(encoded)} }
	got, err := vault.Get(handle)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatal(err)
	}
	runVaultHelper = func(string, []string, []byte) vaultResponse { return vaultResponse{Exit: 1, Stderr: true} }
	if _, err = vault.Get(handle); err == nil || errors.Is(err, errVaultKeyMissing) {
		t.Fatal("vault failure treated as a new missing key")
	}
}
