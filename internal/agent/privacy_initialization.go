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
	"github.com/ixayldz/Viber/internal/resource"
	"github.com/ixayldz/Viber/internal/store"
)

// A failed/abrupt bind reuses the exact journal-pinned allocation, never creates
// another 32 MiB reserve and never infers ownership from a filename prefix.
func (s *Session) initializePrivacyAuthority(ctx context.Context) error {
	active, err := s.Journal.PrivacyAuthority(ctx)
	if err != nil {
		return err
	}
	bootstrap, err := s.Journal.PrivacyBootstrap(ctx)
	if err != nil {
		return err
	}
	if active != nil {
		var authority PrivacyAuthority
		if c.DecodeStrict(active, &authority) != nil || authority.validate() != nil {
			return c.Fail(c.StoreIntegrityError, "invalid active privacy authority")
		}
		if bootstrap != nil {
			if err = s.validatePrivacyBootstrapOwner(*bootstrap); err != nil {
				return err
			}
			if bootstrap.AuthorityID != authority.ID || bootstrap.Directory != authority.Directory || bootstrap.PhysicalRoot != authority.PhysicalRoot {
				return c.Fail(c.StaleAuthority, "active privacy authority differs from its bootstrap reservation")
			}
			digest := c.HashBytes(active)
			if bootstrap.State == "ROOT_BOUND" {
				bootstrap.State = "AUTHORITY_BOUND"
				bootstrap.AuthorityDigest = digest
				if err = s.Journal.AdvancePrivacyBootstrap(ctx, "ROOT_BOUND", *bootstrap); err != nil {
					return err
				}
			} else if bootstrap.State == "AUTHORITY_BOUND" || bootstrap.State == "IMPORTING" || bootstrap.State == "BOUND" || bootstrap.State == "RELEASED" {
				if bootstrap.AuthorityDigest != digest {
					return c.Fail(c.StaleAuthority, "privacy bootstrap authority receipt changed")
				}
			} else {
				return c.Fail(c.StaleAuthority, "active privacy authority has an incomplete bootstrap reservation")
			}
		}
		s.privacy = &authority
		return nil
	}
	raw, err := s.Journal.PendingPrivacyAuthority(ctx)
	if err != nil {
		return err
	}
	var authority PrivacyAuthority
	if raw == nil {
		if bootstrap == nil {
			bootstrap, err = s.newPrivacyBootstrapReservation()
			if err != nil {
				return err
			}
			if err = s.Journal.ReservePrivacyBootstrap(ctx, *bootstrap); err != nil {
				return err
			}
			if err = s.privacyInitializationFault("bootstrap-reserved"); err != nil {
				return err
			}
		}
		if err = s.validatePrivacyBootstrapOwner(*bootstrap); err != nil {
			return err
		}
		if bootstrap.State == "PENDING" {
			var root *os.Root
			root, err = createPrivacyBootstrapStage(*bootstrap)
			if err != nil {
				return err
			}
			physical, identityErr := fileguard.DirectoryIdentity(root)
			if err = identityErr; err != nil {
				root.Close()
				return err
			}
			if err = s.Journal.BindPrivacyBootstrapStage(ctx, root, physical); err != nil {
				root.Close()
				return err
			}
			if err = root.Close(); err != nil {
				return err
			}
			bootstrap, err = s.Journal.PrivacyBootstrap(ctx)
			if err != nil || bootstrap == nil {
				if err == nil {
					err = c.Fail(c.StaleAuthority, "privacy bootstrap root receipt was not committed")
				}
				return err
			}
			if err = s.privacyInitializationFault("bootstrap-stage-bound"); err != nil {
				return err
			}
		}
		if bootstrap.State == "STAGE_BOUND" {
			if err = publishPrivacyBootstrapStage(*bootstrap); err != nil {
				return err
			}
			root, err := openBoundPrivacyBootstrap(*bootstrap)
			if err != nil {
				return err
			}
			physical, identityErr := fileguard.DirectoryIdentity(root)
			closeErr := root.Close()
			if err = errors.Join(identityErr, closeErr); err != nil {
				return err
			}
			if err = s.Journal.BindPrivacyBootstrapRoot(ctx, physical); err != nil {
				return err
			}
			bootstrap, err = s.Journal.PrivacyBootstrap(ctx)
			if err != nil || bootstrap == nil {
				if err == nil {
					err = c.Fail(c.StaleAuthority, "privacy bootstrap root receipt was not committed")
				}
				return err
			}
			if err = s.privacyInitializationFault("bootstrap-root-bound"); err != nil {
				return err
			}
		}
		if bootstrap.State != "ROOT_BOUND" || !c.ValidDigest(bootstrap.PhysicalRoot) {
			return c.Fail(c.StaleAuthority, "privacy bootstrap is not bound to its exact physical root")
		}
		authority = PrivacyAuthority{1, bootstrap.AuthorityID, bootstrap.Directory, bootstrap.PhysicalRoot}
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
	if bootstrap == nil || bootstrap.AuthorityID != authority.ID || bootstrap.Directory != authority.Directory || bootstrap.PhysicalRoot != authority.PhysicalRoot || bootstrap.State != "ROOT_BOUND" {
		return c.Fail(c.StaleAuthority, "privacy authority is not bound to its owner-store bootstrap reservation")
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
	if err = s.privacyInitializationFault("authority-bound-before-bootstrap"); err != nil {
		return err
	}
	bootstrap.State = "AUTHORITY_BOUND"
	bootstrap.AuthorityDigest = c.HashBytes(raw)
	if err = s.Journal.AdvancePrivacyBootstrap(ctx, "ROOT_BOUND", *bootstrap); err != nil {
		return err
	}
	if err = s.privacyInitializationFault("bound"); err != nil {
		return err
	}
	s.privacy = &authority
	return nil
}

func (s *Session) newPrivacyBootstrapReservation() (*store.PrivacyBootstrapReservation, error) {
	directory, err := fileguard.ResolveProspective(filepath.Join(filepath.Dir(s.directory), ".viber-privacy-"+newID("")))
	if err != nil {
		return nil, err
	}
	parentPath := filepath.Dir(directory)
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, err
	}
	parentIdentity, identityErr := fileguard.DirectoryIdentity(parent)
	volumeIdentity, volumeErr := fileguard.VolumeIdentity(parent)
	closeErr := parent.Close()
	if err = errors.Join(identityErr, volumeErr, closeErr); err != nil {
		return nil, err
	}
	owner, err := os.OpenRoot(s.directory)
	if err != nil {
		return nil, err
	}
	ownerIdentity, ownerErr := fileguard.DirectoryIdentity(owner)
	ownerCloseErr := owner.Close()
	if err = errors.Join(ownerErr, ownerCloseErr); err != nil {
		return nil, err
	}
	overheadBytes, overheadEntries := resource.PrivacyBootstrapOverhead()
	bootstrap := store.PrivacyBootstrapReservation{
		SchemaVersion: 1, State: "PENDING", AuthorityID: newID(""), Directory: directory,
		StagingDirectory: filepath.Join(parentPath, ".viber-privacy-stage-"+newID("")),
		ParentPath:       parentPath, ParentIdentity: parentIdentity, VolumeIdentity: volumeIdentity,
		OwnerDirectory: filepath.Clean(s.directory), OwnerIdentity: ownerIdentity, OwnerInstanceID: s.instance.ID,
		OwnerGeneration: s.Journal.Generation(), UpperBytes: overheadBytes + 64<<10, UpperEntries: overheadEntries + 16,
	}
	bootstrap.RequestDigest, err = store.PrivacyBootstrapRequestDigest(bootstrap)
	if err != nil {
		return nil, err
	}
	return &bootstrap, nil
}

func (s *Session) validatePrivacyBootstrapOwner(bootstrap store.PrivacyBootstrapReservation) error {
	ownerDirectory, ownerIdentity, ownerInstance, ownerGeneration := bootstrap.OwnerDirectory, bootstrap.OwnerIdentity, bootstrap.OwnerInstanceID, bootstrap.OwnerGeneration
	ownerTarget := ""
	if bootstrap.RestoreBinding != "" {
		ownerDirectory, ownerIdentity = bootstrap.RestoreOwnerDirectory, bootstrap.RestoreOwnerIdentity
		ownerTarget = bootstrap.RestoreOwnerTarget
		ownerInstance, ownerGeneration = bootstrap.RestoreOwnerInstanceID, bootstrap.RestoreOwnerGeneration
	}
	owner, err := os.OpenRoot(s.directory)
	if err != nil {
		return err
	}
	identity, identityErr := fileguard.DirectoryIdentity(owner)
	closeErr := owner.Close()
	if err = errors.Join(identityErr, closeErr); err != nil {
		return err
	}
	// Restore receipts contain canonical stage/target paths. The caller can
	// reopen the same inode through a platform alias (for example /var on
	// macOS); compare resolved paths as well as the pinned physical identity.
	currentPath, err := fileguard.ResolveProspective(s.directory)
	if err != nil {
		return err
	}
	boundPath, err := fileguard.ResolveProspective(ownerDirectory)
	if err != nil {
		return err
	}
	pathMatches := boundPath == currentPath
	if !pathMatches && ownerTarget != "" {
		targetPath, resolveErr := fileguard.ResolveProspective(ownerTarget)
		if resolveErr != nil {
			return resolveErr
		}
		pathMatches = targetPath == currentPath
	}
	if !pathMatches || ownerIdentity != identity || ownerInstance != s.instance.ID ||
		ownerGeneration < 1 || s.Journal.Generation() < ownerGeneration || bootstrap.UpperBytes < 1 || bootstrap.UpperEntries < 1 {
		return c.Fail(c.StaleAuthority, "privacy bootstrap owner instance, generation, or store identity changed")
	}
	return nil
}

// openUnboundPrivacyBootstrap only recovers an empty directory at the exact
// reserved name. The owner-store descriptor exists before the initial mkdir;
// a foreign or partially populated tree remains reserved and fail-closed.
func createPrivacyBootstrapStage(bootstrap store.PrivacyBootstrapReservation) (*os.Root, error) {
	if err := bootstrapPathParentMatches(bootstrap); err != nil {
		return nil, err
	}
	for _, path := range []string{bootstrap.Directory, bootstrap.StagingDirectory} {
		if _, err := os.Lstat(path); err == nil {
			return nil, c.Fail(c.StaleAuthority, "unbound privacy bootstrap path exists without a creation witness")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return freshPrivate(bootstrap.StagingDirectory)
}

func publishPrivacyBootstrapStage(bootstrap store.PrivacyBootstrapReservation) error {
	if bootstrap.State != "STAGE_BOUND" || !c.ValidDigest(bootstrap.PhysicalRoot) {
		return c.Fail(c.StaleAuthority, "privacy bootstrap has no pinned staging root")
	}
	if err := bootstrapPathParentMatches(bootstrap); err != nil {
		return err
	}
	stageID, stageErr := directoryIdentity(bootstrap.StagingDirectory)
	finalID, finalErr := directoryIdentity(bootstrap.Directory)
	stageExists, finalExists := stageErr == nil, finalErr == nil
	if stageErr != nil && !errors.Is(stageErr, os.ErrNotExist) || finalErr != nil && !errors.Is(finalErr, os.ErrNotExist) {
		return errors.Join(stageErr, finalErr)
	}
	if stageExists && finalExists || stageExists && stageID != bootstrap.PhysicalRoot || finalExists && finalID != bootstrap.PhysicalRoot {
		return c.Fail(c.StaleAuthority, "privacy bootstrap staging or destination path changed")
	}
	if finalExists {
		return nil // crash after the no-replace rename; the owner CAS pins this inode.
	}
	parent, err := os.OpenRoot(bootstrap.ParentPath)
	if err != nil {
		return err
	}
	err = fileguard.RenameNoReplace(parent, filepath.Base(bootstrap.StagingDirectory), filepath.Base(bootstrap.Directory))
	closeErr := parent.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}
	finalID, err = directoryIdentity(bootstrap.Directory)
	if err != nil {
		return err
	}
	if finalID != bootstrap.PhysicalRoot {
		return c.Fail(c.StaleAuthority, "published privacy bootstrap root differs from its staging witness")
	}
	return nil
}

func openBoundPrivacyBootstrap(bootstrap store.PrivacyBootstrapReservation) (*os.Root, error) {
	if bootstrap.State != "STAGE_BOUND" || !c.ValidDigest(bootstrap.PhysicalRoot) {
		return nil, c.Fail(c.StaleAuthority, "privacy bootstrap is not bound to a staged root")
	}
	if err := bootstrapPathParentMatches(bootstrap); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(bootstrap.Directory)
	if err != nil {
		return nil, c.Fail(c.StaleAuthority, "published privacy bootstrap destination is missing")
	}
	physical, identityErr := fileguard.DirectoryIdentity(root)
	volume, volumeErr := fileguard.VolumeIdentity(root)
	if err = errors.Join(identityErr, volumeErr); err == nil && (physical != bootstrap.PhysicalRoot || volume != bootstrap.VolumeIdentity) {
		err = c.Fail(c.StaleAuthority, "published privacy bootstrap root identity or volume changed")
	}
	if err == nil {
		err = fileguard.Private(root)
	}
	if err == nil {
		var directory *os.File
		directory, err = root.Open(".")
		var entries []string
		if err == nil {
			var readErr error
			entries, readErr = directory.Readdirnames(1)
			if errors.Is(readErr, io.EOF) {
				readErr = nil
			}
			err = errors.Join(readErr, directory.Close())
		}
		if err == nil && len(entries) != 0 {
			err = c.Fail(c.StaleAuthority, "staged privacy bootstrap root contains unexpected entries")
		}
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func directoryIdentity(path string) (string, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return "", err
	}
	identity, identityErr := fileguard.DirectoryIdentity(root)
	closeErr := root.Close()
	return identity, errors.Join(identityErr, closeErr)
}

func bootstrapPathParentMatches(bootstrap store.PrivacyBootstrapReservation) error {
	parent, err := os.OpenRoot(bootstrap.ParentPath)
	if err != nil {
		return err
	}
	identity, identityErr := fileguard.DirectoryIdentity(parent)
	volume, volumeErr := fileguard.VolumeIdentity(parent)
	closeErr := parent.Close()
	if err = errors.Join(identityErr, volumeErr, closeErr); err != nil {
		return err
	}
	if identity != bootstrap.ParentIdentity || volume != bootstrap.VolumeIdentity {
		return c.Fail(c.StaleAuthority, "privacy bootstrap parent or volume changed")
	}
	return nil
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

type privacyQuotaAnchor struct {
	SchemaVersion    int    `json:"schema_version"`
	AuthorityID      string `json:"authority_id"`
	PhysicalRoot     string `json:"physical_root"`
	LedgerID         string `json:"ledger_id"`
	DatabaseIdentity string `json:"database_identity"`
}

func validatePrivacyQuotaAnchor(root *os.Root, genesis []byte) error {
	raw, err := fileguard.ReadRegular(root, "physical-quota.anchor", 4096)
	if err != nil {
		return err
	}
	var authority PrivacyAuthority
	var anchor privacyQuotaAnchor
	if c.DecodeStrict(genesis, &authority) != nil || authority.validate() != nil ||
		c.DecodeStrict(raw, &anchor) != nil || anchor.SchemaVersion != 1 ||
		anchor.AuthorityID != authority.ID || anchor.PhysicalRoot != authority.PhysicalRoot ||
		!privacyLowerHex(anchor.LedgerID, 32) || !c.ValidDigest(anchor.DatabaseIdentity) {
		return c.Fail(c.StoreIntegrityError, "physical quota anchor binding is invalid")
	}
	canonical, err := c.CanonicalV1(anchor)
	if err != nil || string(canonical) != string(raw) {
		return c.Fail(c.StoreIntegrityError, "physical quota anchor is not canonical")
	}
	database, err := root.Open("physical-quota.sqlite")
	if err != nil {
		return c.Fail(c.StaleAuthority, "physical quota anchor has no database")
	}
	identity, identityErr := fileguard.FileIdentity(database)
	closeErr := database.Close()
	if err = errors.Join(identityErr, closeErr); err != nil {
		return err
	}
	if identity != anchor.DatabaseIdentity {
		return c.Fail(c.StaleAuthority, "physical quota database identity differs from its anchor")
	}
	return nil
}

func privacyLowerHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
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
	quotaAnchor, quotaDatabase := false, false
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
		case "physical-quota.anchor":
			if err = validatePrivacyQuotaAnchor(root, genesis); err != nil {
				return err
			}
			quotaAnchor = true
		case "physical-quota.sqlite":
			if err = fileguard.RegularPath(root, name, false); err != nil {
				return err
			}
			quotaDatabase = true
		case "physical-quota.sqlite-wal", "physical-quota.sqlite-shm", "physical-quota.lock":
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
	if quotaAnchor && !quotaDatabase {
		return c.Fail(c.StaleAuthority, "physical quota anchor has no database")
	}
	for name, raw := range temps {
		if _, err = fileguard.RemoveBound(root, name, c.HashBytes(raw), int64(len(raw)), 4096); err != nil {
			return err
		}
	}
	return nil
}
