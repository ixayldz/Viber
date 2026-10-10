package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// A failed/abrupt bind reuses the exact journal-pinned allocation, never creates
// another 32 MiB reserve and never infers ownership from a filename prefix.
func (s *Session) initializePrivacyAuthority(ctx context.Context) error {
	active, err := s.Journal.PrivacyAuthority(ctx)
	if err != nil {
		return err
	}
	if active != nil {
		return s.attachPrivacy(ctx)
	}
	raw, err := s.Journal.PendingPrivacyAuthority(ctx)
	if err != nil {
		return err
	}
	var authority PrivacyAuthority
	if raw == nil {
		directory, err := fileguard.ResolveProspective(filepath.Join(filepath.Dir(s.directory), ".viber-privacy-"+newID("")))
		if err != nil {
			return err
		}
		root, err := freshPrivate(directory)
		if err != nil {
			return err
		}
		physical, identityErr := fileguard.DirectoryIdentity(root)
		err = errors.Join(identityErr, root.Close())
		if err != nil {
			return err
		}
		authority = PrivacyAuthority{1, newID(""), directory, physical}
		raw, err = c.CanonicalV1(authority)
		if err == nil {
			err = s.Journal.ReservePrivacyAuthority(ctx, raw)
		}
		if err != nil {
			return err
		}
		if err = s.privacyInitializationFault("allocated"); err != nil {
			return err
		}
	} else if c.DecodeStrict(raw, &authority) != nil || authority.validate() != nil {
		return c.Fail(c.StoreIntegrityError, "invalid pending privacy allocation")
	}
	root, err := openPendingPrivacy(authority)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = preparePendingPrivacy(root, raw); err != nil {
		return err
	}
	if err = root.MkdirAll("records", 0700); err != nil {
		return err
	}
	if err = admitPrivacyDisk(root, 4096, false); err != nil {
		return err
	}
	if err = s.privacyInitializationFault("reserve"); err != nil {
		return err
	}
	genesis, err := fileguard.ReadRegular(root, "authority.json", 4096)
	if errors.Is(err, os.ErrNotExist) {
		err = fileguard.Publish(root, "authority.json", raw)
	} else if err == nil && !bytes.Equal(genesis, raw) {
		err = c.Fail(c.StoreIntegrityError, "pending authority genesis differs")
	}
	if err != nil {
		return err
	}
	if err = s.privacyInitializationFault("genesis"); err != nil {
		return err
	}
	if err = s.Journal.BindPrivacyAuthority(ctx, raw); err != nil {
		return err
	}
	if err = s.privacyInitializationFault("bound"); err != nil {
		return err
	}
	return s.attachPrivacy(ctx)
}

func (s *Session) privacyInitializationFault(step string) error {
	if s.privacyInitFault != nil {
		return s.privacyInitFault(step)
	}
	return nil
}

func openPendingPrivacy(authority PrivacyAuthority) (*os.Root, error) {
	if err := authority.validate(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(authority.Directory)
	if err != nil || !info.IsDir() {
		return nil, c.Fail(c.StaleAuthority, "pending privacy root unavailable; cannot allocate a substitute")
	}
	root, err := os.OpenRoot(authority.Directory)
	if err != nil {
		return nil, err
	}
	physical, err := fileguard.DirectoryIdentity(root)
	if err == nil && physical != authority.PhysicalRoot {
		err = c.Fail(c.StaleAuthority, "pending privacy physical root changed")
	}
	if err == nil {
		err = fileguard.Private(root)
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

// Only allocation-owned initialization bytes may exist before binding. Audit
// the whole bounded inventory before removing an interrupted genesis temp.
func preparePendingPrivacy(root *os.Root, genesis []byte) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := directory.Readdir(33)
	if err == io.EOF {
		err = nil
	}
	err = errors.Join(err, directory.Close())
	if err != nil {
		return err
	}
	if len(entries) > 32 {
		return c.Fail(c.BudgetLimitReached, "pending privacy inventory exceeds initialization bound")
	}
	temps := map[string][]byte{}
	for _, entry := range entries {
		name := entry.Name()
		switch name {
		case "records":
			if !entry.IsDir() {
				return c.Fail(c.StoreIntegrityError, "pending record directory replaced")
			}
			dir, err := root.Open(name)
			if err != nil {
				return err
			}
			names, readErr := dir.Readdirnames(1)
			if readErr == io.EOF {
				readErr = nil
			}
			if err = errors.Join(readErr, dir.Close()); err != nil {
				return err
			}
			if len(names) != 0 {
				return c.Fail(c.StoreIntegrityError, "unbound authority contains journal records")
			}
		case "authority.json":
			raw, err := fileguard.ReadRegular(root, name, 4096)
			if err != nil {
				return err
			}
			if !bytes.Equal(raw, genesis) {
				return c.Fail(c.StoreIntegrityError, "unbound authority genesis changed")
			}
		case "control.reserve":
			if err = fileguard.RegularPath(root, name, false); err != nil {
				return err
			}
		default:
			if !strings.HasPrefix(name, ".publish-") || !safeCopyPath("SUPPORT", name) {
				return c.Fail(c.StoreIntegrityError, "foreign file in pending privacy allocation")
			}
			raw, err := fileguard.ReadRegular(root, name, 4096)
			if err != nil {
				return err
			}
			if !bytes.HasPrefix(genesis, raw) {
				return c.Fail(c.StoreIntegrityError, "interrupted temp is not pending genesis content")
			}
			temps[name] = raw
		}
	}
	for name, raw := range temps {
		if _, err = fileguard.RemoveBound(root, name, c.HashBytes(raw), int64(len(raw)), 4096); err != nil {
			return err
		}
	}
	return nil
}
