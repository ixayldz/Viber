package agent

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/ixayldz/Viber/internal/fileguard"
)

// A restore holds a family admission lease before it materializes any bytes,
// through final publication. Deletion cannot race a temporarily unowned stage.
func privacyOperation(ctx context.Context, authority PrivacyAuthority) (*privacyOwner, error) {
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
	if err != nil {
		lock.Close()
		root.Close()
		return nil, err
	}
	name := filepath.Join("owners", newID("")+".lock")
	lease, err := fileguard.Lock(root, name)
	err = errors.Join(err, lock.Close())
	if err != nil {
		if lease != nil {
			lease.Close()
		}
		root.Close()
		return nil, err
	}
	return &privacyOwner{root: root, lease: lease, name: name}, nil
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
