package auth

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"os"
	"path/filepath"
)

// MigrateLegacy is explicit, local-only and preserves account identities. It
// never keeps a plaintext backup or performs registration, refresh or inference.
func MigrateLegacy(directory string) (View, error) {
	if current, err := Open(directory, false); err == nil {
		view := current.View()
		return view, current.Close()
	}
	var result View
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return result, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return result, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, c.Fail(c.PolicyDenied, "auth migration requires a private real directory")
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return result, err
	}
	if err = fileguard.Private(root); err != nil {
		root.Close()
		return result, err
	}
	lock, err := fileguard.Lock(root, "auth.lock")
	if err != nil {
		root.Close()
		return result, err
	}
	store := &Store{root: root, lock: lock}
	defer store.Close()
	raw, err := fileguard.ReadRegular(root, "credentials.bin", 1<<20)
	if err != nil {
		return result, err
	}
	defer clear(raw)
	if c.DecodeStrict(raw, &store.data) != nil || store.validate() != nil {
		return result, c.Fail(c.StoreIntegrityError, "only a valid legacy plaintext credential state can migrate")
	}
	if err = primeMigrationVault(store.data); err != nil {
		return result, err
	}
	if err = store.save(); err != nil {
		return result, err
	}
	// Re-read and authenticate the published bytes before returning completion.
	retained, err := fileguard.ReadRegular(root, "credentials.bin", 1<<20)
	if err != nil {
		return result, err
	}
	plaintext, err := unprotect(retained)
	if err != nil {
		return result, err
	}
	defer clear(plaintext)
	var stateCheck state
	if c.DecodeStrict(plaintext, &stateCheck) != nil {
		return result, c.Fail(c.StoreIntegrityError, "migrated credential state invalid")
	}
	expected, _ := c.Digest(store.data)
	actual, _ := c.Digest(stateCheck)
	if actual != expected {
		return result, c.Fail(c.StoreIntegrityError, "auth migration changed account data")
	}
	return store.View(), nil
}
