package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// A restore holds a family admission lease before it materializes any bytes,
// through final publication. Deletion cannot race a temporarily unowned stage.
func privacyOperation(ctx context.Context, authority PrivacyAuthority) (*privacyOwner, error) {
	return privacyOperationWithFault(ctx, authority, nil)
}

func privacyOperationWithFault(ctx context.Context, authority PrivacyAuthority, fault func(string) error) (*privacyOwner, error) {
	root, err := openPrivacyRoot(authority)
	if err != nil {
		return nil, err
	}
	lock, err := privacyLock(ctx, root)
	if err != nil {
		root.Close()
		return nil, err
	}
	_, err = readPrivacy(root)
	if err == nil {
		_, err = retirePrivacyOperationLeases(ctx, root, authority, "")
	}
	if err != nil {
		lock.Close()
		root.Close()
		return nil, err
	}
	name := filepath.Join("owners", newID("")+".lock")
	if _, err = admitPrivacyCatalog(root, name); err != nil {
		lock.Close()
		root.Close()
		return nil, err
	}
	digest, _ := c.Digest(authority)
	raw, err := c.CanonicalV1(privacyOperationLease{1, "RESTORE_OPERATION", name, digest})
	if err == nil {
		err = admitPrivacyDisk(root, int64(len(raw))*2, false)
	}
	if err == nil {
		err = fileguard.Publish(root, name, raw)
	}
	if err == nil && fault != nil {
		err = fault("published")
	}
	var lease *os.File
	if err == nil {
		lease, err = fileguard.LockExisting(root, name)
	}
	if err == nil && fault != nil {
		err = fault("leased")
	}
	err = errors.Join(err, lock.Close())
	if err != nil {
		if lease != nil {
			lease.Close()
		}
		root.Close()
		return nil, err
	}
	return &privacyOwner{root: root, lease: lease, name: name, operation: &authority}, nil
}
func registerRestoredScope(ctx context.Context, authority PrivacyAuthority, instance RuntimeInstance, target string) error {
	root, err := openPrivacyRoot(authority)
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return err
	}
	defer lock.Close()
	return publishPrivacyScope(root, privacyScope{instance.ID, target, instance.PhysicalRoot})
}
