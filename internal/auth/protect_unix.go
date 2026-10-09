//go:build !windows

package auth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
)

const credentialProtection = "OS_VAULT_AES256_GCM_AND_PRIVATE_OWNER_STORE; NO_PLAINTEXT_FALLBACK"

type credentialEnvelope struct {
	SchemaVersion int    `json:"schema_version"`
	Backend       string `json:"os_vault_backend"`
	Handle        string `json:"key_handle"`
	Nonce         []byte `json:"nonce"`
	Ciphertext    []byte `json:"ciphertext"`
}
type credentialVault interface {
	Backend() string
	Get(string) ([]byte, error)
	Put(string, []byte) error
}

var errVaultKeyMissing = errors.New("OS vault key missing")
var osCredentialVault credentialVault = commandVault{}

func vaultHandle(host string) string {
	return c.HashBytes([]byte("viber:credential-key:v2\x00" + host))
}
func envelopeAAD(envelope credentialEnvelope) []byte {
	raw, _ := c.CanonicalV1(struct {
		Version         int
		Backend, Handle string
	}{2, envelope.Backend, envelope.Handle})
	return raw
}
func vaultAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, c.Fail(c.StoreIntegrityError, "OS vault key unavailable or invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func protect(raw []byte) ([]byte, error) {
	var data state
	if len(raw) == 0 || len(raw) > (1<<20)-4096 || c.DecodeStrict(raw, &data) != nil || !validHostID.MatchString(data.HostID) {
		return nil, c.Fail(c.StoreIntegrityError, "credential plaintext contract invalid")
	}
	handle := vaultHandle(data.HostID)
	key, err := osCredentialVault.Get(handle)
	if errors.Is(err, errVaultKeyMissing) {
		// Only a genuinely new, empty account store may create its encryption key.
		// A key disappearing during credential rotation never downgrades protection.
		if len(data.Profiles) > 0 || data.PendingClient != "" {
			return nil, c.Fail(c.StoreIntegrityError, "OS vault key missing; credential rotation stopped")
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		if err = osCredentialVault.Put(handle, key); err != nil {
			clear(key)
			return nil, c.Fail(c.UnsupportedCapability, "OS credential vault unavailable; plaintext fallback is disabled")
		}
		retained, readErr := osCredentialVault.Get(handle)
		if readErr != nil || !bytes.Equal(retained, key) {
			clear(key)
			clear(retained)
			return nil, c.Fail(c.StoreIntegrityError, "OS vault did not retain the requested key")
		}
		clear(retained)
		err = nil
	}
	if err != nil {
		return nil, c.Fail(c.UnsupportedCapability, "OS credential vault unavailable; unlock or configure it before login")
	}
	defer clear(key)
	aead, err := vaultAEAD(key)
	if err != nil {
		return nil, err
	}
	envelope := credentialEnvelope{SchemaVersion: 2, Backend: osCredentialVault.Backend(), Handle: handle, Nonce: make([]byte, aead.NonceSize())}
	if _, err = rand.Read(envelope.Nonce); err != nil {
		return nil, err
	}
	envelope.Ciphertext = aead.Seal(nil, envelope.Nonce, raw, envelopeAAD(envelope))
	return c.CanonicalV1(envelope)
}
func unprotect(raw []byte) ([]byte, error) {
	var envelope credentialEnvelope
	if len(raw) > 1<<20 || c.DecodeStrict(raw, &envelope) != nil || envelope.SchemaVersion != 2 || envelope.Backend != osCredentialVault.Backend() || !c.ValidDigest(envelope.Handle) || len(envelope.Nonce) != 12 || len(envelope.Ciphertext) < 16 {
		return nil, c.Fail(c.StoreIntegrityError, "encrypted OS vault credential envelope required; legacy plaintext requires explicit migration")
	}
	key, err := osCredentialVault.Get(envelope.Handle)
	if err != nil {
		return nil, c.Fail(c.StoreIntegrityError, "OS vault key unavailable; no credential fallback")
	}
	defer clear(key)
	aead, err := vaultAEAD(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, envelopeAAD(envelope))
	if err != nil {
		return nil, c.Fail(c.StoreIntegrityError, "credential authentication failed")
	}
	var data state
	if c.DecodeStrict(plaintext, &data) != nil || vaultHandle(data.HostID) != envelope.Handle {
		clear(plaintext)
		return nil, c.Fail(c.StoreIntegrityError, "credential identity differs from vault key")
	}
	return plaintext, nil
}
func replaceCredential(root *os.Root, from, to string) error { return root.Rename(from, to) }

func primeMigrationVault(data state) error {
	empty := state{SchemaVersion: 1, HostID: data.HostID, Profiles: []Profile{}}
	raw, err := c.CanonicalV1(empty)
	if err != nil {
		return err
	}
	_, err = protect(raw)
	return err
}
