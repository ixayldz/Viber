package distribution

import (
	"fmt"
	"os"
	"path/filepath"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

type RegisteredStore struct {
	Path     string `json:"path"`
	Identity string `json:"directory_identity"`
}
type storeRegistration struct {
	SchemaVersion int             `json:"schema_version"`
	Sequence      int             `json:"sequence"`
	Previous      string          `json:"previous_digest,omitempty"`
	Store         RegisteredStore `json:"store"`
}

func (m *Manager) RegisteredStores() ([]RegisteredStore, error) {
	entries, err := readDirectory(m.root, "stores")
	if err != nil {
		return nil, err
	}
	if len(entries) > 64 {
		return nil, c.Fail(c.BudgetLimitReached, "registered store bound exceeded")
	}
	stores := []RegisteredStore{}
	previous := ""
	for i := 1; i <= len(entries); i++ {
		raw, readErr := fileguard.ReadRegular(m.root, filepath.Join("stores", storeRecordName(i)), 16<<10)
		var record storeRegistration
		if readErr != nil || c.DecodeStrict(raw, &record) != nil || record.SchemaVersion != 1 || record.Sequence != i || record.Previous != previous || !filepath.IsAbs(record.Store.Path) || !c.ValidDigest(record.Store.Identity) || !fileguard.Disjoint(m.prefix, record.Store.Path) {
			return nil, c.Fail(c.StoreIntegrityError, "registered store chain invalid")
		}
		for _, old := range stores {
			if !fileguard.Disjoint(old.Path, record.Store.Path) {
				return nil, c.Fail(c.StoreIntegrityError, "registered stores overlap")
			}
		}
		stores = append(stores, record.Store)
		previous = c.HashBytes(raw)
	}
	return stores, nil
}

func storeRecordName(sequence int) string { return fmt.Sprintf("%020d.json", sequence) }

// RegisterStore persists the stores used through this managed launcher. Update
// operations acquire all of their OS owner locks, including when flags omit them.
func (m *Manager) RegisterStore(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if !fileguard.Disjoint(m.prefix, abs) {
		return c.Fail(c.PolicyDenied, "store and program prefix overlap")
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return err
	}
	defer root.Close()
	id, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return err
	}
	stores, err := m.RegisteredStores()
	if err != nil {
		return err
	}
	for _, known := range stores {
		if known.Path == abs {
			if known.Identity != id {
				return c.Fail(c.StaleAuthority, "registered store directory replaced")
			}
			return nil
		}
		if !fileguard.Disjoint(known.Path, abs) {
			return c.Fail(c.PolicyDenied, "registered stores overlap")
		}
	}
	if len(stores) >= 64 {
		return c.Fail(c.BudgetLimitReached, "registered store bound exceeded")
	}
	previous := ""
	if len(stores) > 0 {
		raw, readErr := fileguard.ReadRegular(m.root, filepath.Join("stores", storeRecordName(len(stores))), 16<<10)
		if readErr != nil {
			return readErr
		}
		previous = c.HashBytes(raw)
	}
	record := storeRegistration{SchemaVersion: 1, Sequence: len(stores) + 1, Previous: previous, Store: RegisteredStore{Path: abs, Identity: id}}
	raw, err := c.CanonicalV1(record)
	if err != nil {
		return err
	}
	return fileguard.Publish(m.root, filepath.Join("stores", storeRecordName(record.Sequence)), raw)
}

func (known RegisteredStore) ValidateCurrent() error {
	root, err := os.OpenRoot(known.Path)
	if err != nil {
		return c.Fail(c.StaleAuthority, "registered store missing")
	}
	defer root.Close()
	id, err := fileguard.DirectoryIdentity(root)
	if err != nil || id != known.Identity {
		return c.Fail(c.StaleAuthority, "registered store directory identity changed")
	}
	return nil
}
