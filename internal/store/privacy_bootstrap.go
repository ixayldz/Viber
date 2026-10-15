package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

const privacyBootstrapPrefix = "privacy_bootstrap_v1:"

// PrivacyBootstrapReservation is the owner-store authority for the one-time
// creation of an external privacy root. The initial PENDING value is committed
// before the destination directory is created. Root identity is filled only
// after the exact reserved path exists.
type PrivacyBootstrapReservation struct {
	SchemaVersion          int    `json:"schema_version"`
	State                  string `json:"state"`
	AuthorityID            string `json:"authority_id"`
	Directory              string `json:"directory"`
	StagingDirectory       string `json:"staging_directory"`
	ParentPath             string `json:"parent_path"`
	ParentIdentity         string `json:"parent_identity"`
	VolumeIdentity         string `json:"volume_identity"`
	OwnerDirectory         string `json:"owner_directory"`
	OwnerIdentity          string `json:"owner_identity"`
	OwnerInstanceID        string `json:"owner_instance_id"`
	OwnerGeneration        int64  `json:"owner_generation"`
	RequestDigest          string `json:"request_digest"`
	UpperBytes             uint64 `json:"upper_bytes"`
	UpperEntries           uint64 `json:"upper_entries"`
	PhysicalRoot           string `json:"physical_root,omitempty"`
	AuthorityDigest        string `json:"authority_digest,omitempty"`
	LedgerID               string `json:"ledger_id,omitempty"`
	InventoryDigest        string `json:"inventory_digest,omitempty"`
	RestoreBinding         string `json:"restore_binding,omitempty"`
	RestoreOwnerDirectory  string `json:"restore_owner_directory,omitempty"`
	RestoreOwnerTarget     string `json:"restore_owner_target,omitempty"`
	RestoreOwnerIdentity   string `json:"restore_owner_identity,omitempty"`
	RestoreOwnerInstanceID string `json:"restore_owner_instance_id,omitempty"`
	RestoreOwnerGeneration int64  `json:"restore_owner_generation,omitempty"`
}

// PrivacyBootstrapRequestDigest seals the immutable portion of a bootstrap
// request. Later state transitions add physical receipts without changing it.
func PrivacyBootstrapRequestDigest(p PrivacyBootstrapReservation) (string, error) {
	p.State = "PENDING"
	p.RequestDigest = ""
	p.PhysicalRoot = ""
	p.AuthorityDigest = ""
	p.LedgerID = ""
	p.InventoryDigest = ""
	p.RestoreBinding = ""
	p.RestoreOwnerDirectory = ""
	p.RestoreOwnerTarget = ""
	p.RestoreOwnerIdentity = ""
	p.RestoreOwnerInstanceID = ""
	p.RestoreOwnerGeneration = 0
	return c.Digest(p)
}

func (p PrivacyBootstrapReservation) validate() error {
	if p.SchemaVersion != 1 || (p.State != "PENDING" && p.State != "STAGE_BOUND" && p.State != "ROOT_BOUND" && p.State != "AUTHORITY_BOUND" && p.State != "IMPORTING" && p.State != "BOUND" && p.State != "RELEASED") ||
		!validPhysicalQuotaID(p.AuthorityID) || !filepath.IsAbs(p.Directory) || len(p.Directory) > 2048 || filepath.Clean(p.Directory) != p.Directory ||
		!filepath.IsAbs(p.StagingDirectory) || len(p.StagingDirectory) > 2048 || filepath.Clean(p.StagingDirectory) != p.StagingDirectory || filepath.Clean(filepath.Dir(p.StagingDirectory)) != p.ParentPath || p.StagingDirectory == p.Directory ||
		!filepath.IsAbs(p.ParentPath) || filepath.Clean(p.ParentPath) != p.ParentPath || filepath.Clean(filepath.Dir(p.Directory)) != p.ParentPath ||
		!c.ValidDigest(p.ParentIdentity) || !c.ValidDigest(p.VolumeIdentity) || !filepath.IsAbs(p.OwnerDirectory) || filepath.Clean(p.OwnerDirectory) != p.OwnerDirectory ||
		!c.ValidDigest(p.OwnerIdentity) || !validPhysicalQuotaID(p.OwnerInstanceID) || p.OwnerGeneration < 1 || !c.ValidDigest(p.RequestDigest) ||
		p.UpperBytes == 0 || p.UpperBytes > 1<<40 || p.UpperEntries == 0 || p.UpperEntries > 1<<30 ||
		(p.PhysicalRoot != "" && !c.ValidDigest(p.PhysicalRoot)) || (p.AuthorityDigest != "" && !c.ValidDigest(p.AuthorityDigest)) ||
		(p.LedgerID != "" && !validPhysicalQuotaID(p.LedgerID)) || (p.InventoryDigest != "" && !c.ValidDigest(p.InventoryDigest)) {
		return c.Fail(c.InvalidArgument, "invalid bounded privacy bootstrap reservation")
	}
	expected, err := PrivacyBootstrapRequestDigest(p)
	if err != nil || expected != p.RequestDigest {
		return c.Fail(c.StoreIntegrityError, "privacy bootstrap request digest mismatch")
	}
	if p.RestoreBinding == "" && (p.RestoreOwnerDirectory != "" || p.RestoreOwnerTarget != "" || p.RestoreOwnerIdentity != "" || p.RestoreOwnerInstanceID != "" || p.RestoreOwnerGeneration != 0) ||
		p.RestoreBinding != "" && (!c.ValidDigest(p.RestoreBinding) || !filepath.IsAbs(p.RestoreOwnerDirectory) || filepath.Clean(p.RestoreOwnerDirectory) != p.RestoreOwnerDirectory || !filepath.IsAbs(p.RestoreOwnerTarget) || filepath.Clean(p.RestoreOwnerTarget) != p.RestoreOwnerTarget || filepath.Clean(p.RestoreOwnerTarget) == filepath.Clean(p.RestoreOwnerDirectory) || !c.ValidDigest(p.RestoreOwnerIdentity) || !validPhysicalQuotaID(p.RestoreOwnerInstanceID) || p.RestoreOwnerGeneration < 1) {
		return c.Fail(c.StoreIntegrityError, "privacy bootstrap restore owner receipt is invalid")
	}
	if p.State == "PENDING" && (p.PhysicalRoot != "" || p.AuthorityDigest != "" || p.LedgerID != "" || p.InventoryDigest != "" || p.RestoreBinding != "") ||
		p.State == "STAGE_BOUND" && (!c.ValidDigest(p.PhysicalRoot) || p.AuthorityDigest != "" || p.LedgerID != "" || p.InventoryDigest != "" || p.RestoreBinding != "") ||
		p.State == "ROOT_BOUND" && (!c.ValidDigest(p.PhysicalRoot) || p.AuthorityDigest != "" || p.LedgerID != "" || p.InventoryDigest != "" || p.RestoreBinding != "") ||
		p.State == "AUTHORITY_BOUND" && (!c.ValidDigest(p.PhysicalRoot) || !c.ValidDigest(p.AuthorityDigest) || p.LedgerID != "" || p.InventoryDigest != "") ||
		p.State == "IMPORTING" && (!c.ValidDigest(p.PhysicalRoot) || !c.ValidDigest(p.AuthorityDigest) || !validPhysicalQuotaID(p.LedgerID) || p.InventoryDigest != "") ||
		(p.State == "BOUND" || p.State == "RELEASED") && (!c.ValidDigest(p.PhysicalRoot) || !c.ValidDigest(p.AuthorityDigest) || !validPhysicalQuotaID(p.LedgerID) || !c.ValidDigest(p.InventoryDigest)) {
		return c.Fail(c.StoreIntegrityError, "privacy bootstrap phase lacks its required receipts")
	}
	return nil
}

func (s *Store) PrivacyBootstrap(ctx context.Context) (*PrivacyBootstrapReservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, os.ErrClosed
	}
	p, err := privacyBootstrapFromDB(ctx, s.db)
	if err != nil || p == nil {
		return p, err
	}
	if err = p.validate(); err != nil {
		return nil, err
	}
	if err = verifyBootstrapWitness(*p); err != nil {
		return nil, err
	}
	return p, nil
}

func privacyBootstrapFromDB(ctx context.Context, db *sql.DB) (*PrivacyBootstrapReservation, error) {
	if db == nil {
		return nil, os.ErrClosed
	}
	rows, err := db.QueryContext(ctx, "SELECT key,value FROM meta WHERE substr(key,1,?)=?", len(privacyBootstrapPrefix), privacyBootstrapPrefix)
	if err != nil {
		return nil, err
	}
	var key string
	var value int64
	count := 0
	for rows.Next() {
		if err = rows.Scan(&key, &value); err != nil {
			break
		}
		count++
		if count > 1 || value != 1 {
			err = c.Fail(c.StoreIntegrityError, "ambiguous privacy bootstrap reservation")
			break
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil || count == 0 {
		return nil, err
	}
	digest := strings.TrimPrefix(key, privacyBootstrapPrefix)
	if !c.ValidDigest(digest) {
		return nil, c.Fail(c.StoreIntegrityError, "invalid privacy bootstrap marker")
	}
	var raw []byte
	if err = db.QueryRowContext(ctx, "SELECT body FROM payloads WHERE task_id='' AND digest=?", digest).Scan(&raw); err != nil {
		return nil, err
	}
	var p PrivacyBootstrapReservation
	if len(raw) == 0 || len(raw) > 4096 || c.HashBytes(raw) != digest || c.DecodeStrict(raw, &p) != nil || p.validate() != nil {
		return nil, c.Fail(c.StoreIntegrityError, "privacy bootstrap reservation CAS mismatch")
	}
	canonical, err := c.CanonicalV1(p)
	if err != nil || string(canonical) != string(raw) {
		return nil, c.Fail(c.StoreIntegrityError, "privacy bootstrap reservation is not canonical")
	}
	return &p, nil
}

func (s *Store) ReservePrivacyBootstrap(ctx context.Context, p PrivacyBootstrapReservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return err
	}
	if p.State != "PENDING" || p.PhysicalRoot != "" || p.AuthorityDigest != "" || p.LedgerID != "" || p.InventoryDigest != "" {
		return c.Fail(c.InvalidArgument, "initial privacy bootstrap descriptor required")
	}
	if err := p.validate(); err != nil {
		return err
	}
	if err := verifyBootstrapWitness(p); err != nil {
		return err
	}
	active, err := s.privacyAuthorityLocked(ctx)
	if err != nil {
		return err
	}
	if active != nil {
		return c.Fail(c.Conflict, "current privacy authority cannot start another bootstrap")
	}
	prior, err := privacyBootstrapFromDB(ctx, s.db)
	if err != nil {
		return err
	}
	if prior != nil {
		if err = prior.validate(); err != nil {
			return err
		}
		if *prior != p {
			return c.Fail(c.Conflict, "pending privacy bootstrap cannot be replaced")
		}
		return nil
	}
	return s.writePrivacyBootstrapLocked(ctx, nil, p)
}

func (s *Store) BindPrivacyBootstrapRoot(ctx context.Context, physicalRoot string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prior, err := privacyBootstrapFromDB(ctx, s.db)
	if err != nil || prior == nil {
		if err == nil {
			err = c.Fail(c.StaleAuthority, "pending privacy bootstrap reservation is missing")
		}
		return err
	}
	if err = prior.validate(); err != nil {
		return err
	}
	if err = verifyBootstrapWitness(*prior); err != nil {
		return err
	}
	if prior.State == "ROOT_BOUND" && prior.PhysicalRoot == physicalRoot {
		return nil
	}
	if prior.State != "STAGE_BOUND" || !c.ValidDigest(physicalRoot) {
		return c.Fail(c.Conflict, "privacy bootstrap root can only bind its exact pending allocation")
	}
	root, err := os.OpenRoot(prior.Directory)
	if err != nil {
		return err
	}
	actual, identityErr := fileguard.DirectoryIdentity(root)
	volume, volumeErr := fileguard.VolumeIdentity(root)
	closeErr := root.Close()
	if err = errors.Join(identityErr, volumeErr, closeErr); err != nil {
		return err
	}
	if actual != physicalRoot || volume != prior.VolumeIdentity {
		return c.Fail(c.StaleAuthority, "created privacy root differs from its pending parent and volume reservation")
	}
	next := *prior
	next.State, next.PhysicalRoot = "ROOT_BOUND", physicalRoot
	return s.writePrivacyBootstrapLocked(ctx, prior, next)
}

// BindPrivacyBootstrapStage commits the identity of a freshly-created private
// staging directory while the caller still holds the creation handle. A path
// that merely appeared after a crash cannot be adopted from PENDING.
func (s *Store) BindPrivacyBootstrapStage(ctx context.Context, created *os.Root, physicalRoot string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if created == nil || !c.ValidDigest(physicalRoot) {
		return c.Fail(c.InvalidArgument, "created privacy bootstrap stage and identity required")
	}
	prior, err := privacyBootstrapFromDB(ctx, s.db)
	if err != nil || prior == nil {
		if err == nil {
			err = c.Fail(c.StaleAuthority, "pending privacy bootstrap reservation is missing")
		}
		return err
	}
	if err = prior.validate(); err != nil {
		return err
	}
	if prior.State != "PENDING" {
		return c.Fail(c.Conflict, "privacy bootstrap stage can only bind from PENDING")
	}
	if err = verifyBootstrapBaseWitness(*prior); err != nil {
		return err
	}
	createdID, err := fileguard.DirectoryIdentity(created)
	if err != nil || createdID != physicalRoot {
		return errors.Join(err, c.Fail(c.StaleAuthority, "privacy bootstrap creation handle differs from its identity receipt"))
	}
	stage, err := os.OpenRoot(prior.StagingDirectory)
	if err != nil {
		return err
	}
	stageID, stageErr := fileguard.DirectoryIdentity(stage)
	stageVolume, volumeErr := fileguard.VolumeIdentity(stage)
	closeErr := stage.Close()
	if err = errors.Join(stageErr, volumeErr, closeErr); err != nil {
		return err
	}
	if stageID != physicalRoot || stageVolume != prior.VolumeIdentity {
		return c.Fail(c.StaleAuthority, "privacy bootstrap stage path differs from the held creation handle")
	}
	if _, err = os.Lstat(prior.Directory); err == nil {
		return c.Fail(c.StaleAuthority, "privacy bootstrap destination appeared before stage publication")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next := *prior
	next.State, next.PhysicalRoot = "STAGE_BOUND", physicalRoot
	return s.writePrivacyBootstrapLocked(ctx, prior, next)
}

// RebindPrivacyBootstrapForRestore records a new local owner only after the
// caller has validated the external restore provenance. The source owner
// fields remain immutable historical evidence.
func (s *Store) RebindPrivacyBootstrapForRestore(ctx context.Context, authorityRaw []byte, binding, ownerDirectory, ownerTarget, ownerIdentity, instanceID string, generation int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writableLocked(); err != nil {
		return err
	}
	prior, err := privacyBootstrapFromDB(ctx, s.db)
	if err != nil {
		return err
	}
	if prior == nil {
		active, activeErr := s.privacyAuthorityLocked(ctx)
		if activeErr != nil || len(authorityRaw) == 0 || string(active) != string(authorityRaw) {
			return errors.Join(activeErr, c.Fail(c.StaleAuthority, "legacy restore lacks the exact active privacy authority"))
		}
		return nil // legacy v2 stores predate bootstrap metadata and remain unmodified.
	}
	if err = prior.validate(); err != nil {
		return err
	}
	if (prior.State != "AUTHORITY_BOUND" && prior.State != "IMPORTING" && prior.State != "BOUND" && prior.State != "RELEASED") || len(authorityRaw) == 0 || c.HashBytes(authorityRaw) != prior.AuthorityDigest || !c.ValidDigest(binding) || filepath.Clean(ownerDirectory) != filepath.Clean(s.directory) || !filepath.IsAbs(ownerTarget) || filepath.Clean(ownerTarget) != ownerTarget || filepath.Clean(ownerTarget) == filepath.Clean(ownerDirectory) || !c.ValidDigest(ownerIdentity) || !validPhysicalQuotaID(instanceID) || generation < 1 {
		return c.Fail(c.StaleAuthority, "restore cannot rebind a nonterminal privacy bootstrap owner")
	}
	active, err := s.privacyAuthorityLocked(ctx)
	if err != nil || string(active) != string(authorityRaw) {
		return errors.Join(err, c.Fail(c.StaleAuthority, "restore bootstrap authority differs from the active owner pin"))
	}
	owner, err := os.OpenRoot(s.directory)
	if err != nil {
		return err
	}
	actualOwner, identityErr := fileguard.DirectoryIdentity(owner)
	closeErr := owner.Close()
	if err = errors.Join(identityErr, closeErr); err != nil {
		return err
	}
	if actualOwner != ownerIdentity {
		return c.Fail(c.StaleAuthority, "restored owner root identity changed before rebind")
	}
	next := *prior
	if prior.RestoreBinding != "" {
		if prior.RestoreBinding == binding && prior.RestoreOwnerDirectory == filepath.Clean(ownerDirectory) && prior.RestoreOwnerTarget == filepath.Clean(ownerTarget) && prior.RestoreOwnerIdentity == ownerIdentity && prior.RestoreOwnerInstanceID == instanceID && generation >= prior.RestoreOwnerGeneration {
			return nil
		}
		return c.Fail(c.Conflict, "restored privacy bootstrap owner is already rebound")
	}
	root, err := os.OpenRoot(prior.Directory)
	if err != nil {
		return c.Fail(c.StaleAuthority, "restored privacy bootstrap root is missing")
	}
	rootID, rootErr := fileguard.DirectoryIdentity(root)
	volume, volumeErr := fileguard.VolumeIdentity(root)
	closeErr = root.Close()
	if err = errors.Join(rootErr, volumeErr, closeErr); err != nil {
		return err
	}
	if rootID != prior.PhysicalRoot || volume != prior.VolumeIdentity {
		return c.Fail(c.StaleAuthority, "restored privacy bootstrap root differs from source receipt")
	}
	if _, err = os.Lstat(ownerTarget); err == nil {
		return c.Fail(c.Conflict, "restore target already exists before bootstrap rebind")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next.RestoreBinding = binding
	next.RestoreOwnerDirectory = filepath.Clean(ownerDirectory)
	next.RestoreOwnerTarget = filepath.Clean(ownerTarget)
	next.RestoreOwnerIdentity = ownerIdentity
	next.RestoreOwnerInstanceID = instanceID
	next.RestoreOwnerGeneration = generation
	if err = next.validate(); err != nil {
		return err
	}
	return s.writePrivacyBootstrapLocked(ctx, prior, next)
}

func (s *Store) AdvancePrivacyBootstrap(ctx context.Context, expectedState string, next PrivacyBootstrapReservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prior, err := privacyBootstrapFromDB(ctx, s.db)
	if err != nil || prior == nil {
		if err == nil {
			err = c.Fail(c.StaleAuthority, "privacy bootstrap reservation is missing")
		}
		return err
	}
	if err = prior.validate(); err != nil {
		return err
	}
	if err = verifyBootstrapWitness(*prior); err != nil {
		return err
	}
	if next.validate() != nil || !sameBootstrapRequest(*prior, next) {
		return c.Fail(c.StaleAuthority, "privacy bootstrap transition changed its immutable reservation")
	}
	if prior.State == next.State && *prior == next {
		return nil
	}
	if prior.State != expectedState || !validBootstrapTransition(prior.State, next.State) {
		return c.Fail(c.Conflict, "privacy bootstrap transition is out of order")
	}
	return s.writePrivacyBootstrapLocked(ctx, prior, next)
}

func (s *Store) writePrivacyBootstrapLocked(ctx context.Context, prior *PrivacyBootstrapReservation, next PrivacyBootstrapReservation) error {
	if err := s.writableLocked(); err != nil {
		return err
	}
	raw, err := c.CanonicalV1(next)
	if err != nil || len(raw) > 4096 {
		return c.Fail(c.BudgetLimitReached, "privacy bootstrap descriptor exceeds its fixed bound")
	}
	digest := c.HashBytes(raw)
	requestDigest := c.HashBytes([]byte("store-privacy-bootstrap-sqlite:" + digest))
	if err = s.admitSQLiteMutation(ctx, 1<<20, true, requestDigest, uint64(len(raw)+64<<10)); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if prior != nil {
		oldRaw, canonicalErr := c.CanonicalV1(*prior)
		if canonicalErr != nil {
			return canonicalErr
		}
		oldDigest := c.HashBytes(oldRaw)
		var stored []byte
		if err = tx.QueryRowContext(ctx, "SELECT body FROM payloads WHERE task_id='' AND digest=?", oldDigest).Scan(&stored); err != nil || string(stored) != string(oldRaw) {
			return c.Fail(c.StoreIntegrityError, "privacy bootstrap prior CAS changed")
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM meta WHERE key=?", privacyBootstrapPrefix+oldDigest); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM payloads WHERE task_id='' AND digest=?", oldDigest); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO payloads(task_id,digest,body) VALUES('',?,?)", digest, raw); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,1)", privacyBootstrapPrefix+digest); err != nil {
		return err
	}
	return tx.Commit()
}

func sameBootstrapRequest(a, b PrivacyBootstrapReservation) bool {
	return a.SchemaVersion == b.SchemaVersion && a.AuthorityID == b.AuthorityID && a.Directory == b.Directory && a.StagingDirectory == b.StagingDirectory && a.ParentPath == b.ParentPath &&
		a.ParentIdentity == b.ParentIdentity && a.VolumeIdentity == b.VolumeIdentity && a.OwnerDirectory == b.OwnerDirectory && a.OwnerIdentity == b.OwnerIdentity &&
		a.OwnerInstanceID == b.OwnerInstanceID && a.OwnerGeneration == b.OwnerGeneration && a.RequestDigest == b.RequestDigest && a.UpperBytes == b.UpperBytes && a.UpperEntries == b.UpperEntries &&
		a.RestoreBinding == b.RestoreBinding && a.RestoreOwnerDirectory == b.RestoreOwnerDirectory && a.RestoreOwnerTarget == b.RestoreOwnerTarget && a.RestoreOwnerIdentity == b.RestoreOwnerIdentity &&
		a.RestoreOwnerInstanceID == b.RestoreOwnerInstanceID && a.RestoreOwnerGeneration == b.RestoreOwnerGeneration
}

func validBootstrapTransition(from, to string) bool {
	switch from + ">" + to {
	case "PENDING>STAGE_BOUND", "STAGE_BOUND>ROOT_BOUND", "ROOT_BOUND>AUTHORITY_BOUND", "AUTHORITY_BOUND>IMPORTING", "IMPORTING>BOUND", "BOUND>RELEASED":
		return true
	default:
		return false
	}
}

func verifyBootstrapWitness(p PrivacyBootstrapReservation) error {
	if err := verifyBootstrapBaseWitness(p); err != nil {
		return err
	}
	if p.RestoreBinding != "" {
		stageID, stageErr := directoryIdentityPath(p.RestoreOwnerDirectory)
		targetID, targetErr := directoryIdentityPath(p.RestoreOwnerTarget)
		stageExists, targetExists := stageErr == nil, targetErr == nil
		if stageErr != nil && !errors.Is(stageErr, os.ErrNotExist) || targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
			return errors.Join(stageErr, targetErr)
		}
		if stageExists == targetExists || stageExists && stageID != p.RestoreOwnerIdentity || targetExists && targetID != p.RestoreOwnerIdentity {
			return c.Fail(c.StaleAuthority, "restored bootstrap owner is not at exactly one pinned handoff path")
		}
	} else {
		owner, err := os.OpenRoot(p.OwnerDirectory)
		if err != nil {
			return err
		}
		ownerID, ownerErr := fileguard.DirectoryIdentity(owner)
		ownerCloseErr := owner.Close()
		if err = errors.Join(ownerErr, ownerCloseErr); err != nil {
			return err
		}
		if ownerID != p.OwnerIdentity {
			return c.Fail(c.StaleAuthority, "privacy bootstrap owner store root changed")
		}
	}
	if p.PhysicalRoot != "" {
		if p.State == "STAGE_BOUND" {
			stageID, stageErr := directoryIdentityPath(p.StagingDirectory)
			finalID, finalErr := directoryIdentityPath(p.Directory)
			stageExists, finalExists := stageErr == nil, finalErr == nil
			if stageErr != nil && !errors.Is(stageErr, os.ErrNotExist) || finalErr != nil && !errors.Is(finalErr, os.ErrNotExist) {
				return errors.Join(stageErr, finalErr)
			}
			if stageExists == finalExists || stageExists && stageID != p.PhysicalRoot || finalExists && finalID != p.PhysicalRoot {
				return c.Fail(c.StaleAuthority, "privacy bootstrap stage and destination do not match the pinned inode")
			}
			return nil
		}
		root, openErr := os.OpenRoot(p.Directory)
		if openErr != nil {
			return c.Fail(c.StaleAuthority, "bound privacy bootstrap root is missing")
		}
		rootID, rootErr := fileguard.DirectoryIdentity(root)
		rootVolume, volumeErr := fileguard.VolumeIdentity(root)
		rootCloseErr := root.Close()
		if err := errors.Join(rootErr, volumeErr, rootCloseErr); err != nil {
			return err
		}
		if rootID != p.PhysicalRoot || rootVolume != p.VolumeIdentity {
			return c.Fail(c.StaleAuthority, "bound privacy bootstrap root or volume changed")
		}
		if _, err := os.Lstat(p.StagingDirectory); err == nil {
			return c.Fail(c.StaleAuthority, "published privacy bootstrap stage path still exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if p.State != "PENDING" {
		return c.Fail(c.StoreIntegrityError, "privacy bootstrap phase lacks a physical root")
	}
	for _, path := range []string{p.Directory, p.StagingDirectory} {
		if _, err := os.Lstat(path); err == nil {
			return c.Fail(c.StaleAuthority, "unbound privacy bootstrap path exists without an identity witness")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func verifyBootstrapBaseWitness(p PrivacyBootstrapReservation) error {
	resolvedParent, err := fileguard.ResolveProspective(p.ParentPath)
	if err != nil || filepath.Clean(resolvedParent) != p.ParentPath {
		return c.Fail(c.StaleAuthority, "privacy bootstrap parent path changed")
	}
	parent, err := os.OpenRoot(p.ParentPath)
	if err != nil {
		return err
	}
	parentID, identityErr := fileguard.DirectoryIdentity(parent)
	volume, volumeErr := fileguard.VolumeIdentity(parent)
	parentCloseErr := parent.Close()
	if err = errors.Join(identityErr, volumeErr, parentCloseErr); err != nil {
		return err
	}
	if parentID != p.ParentIdentity || volume != p.VolumeIdentity {
		return c.Fail(c.StaleAuthority, "privacy bootstrap parent identity or volume changed")
	}
	return nil
}

func directoryIdentityPath(path string) (string, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return "", err
	}
	identity, identityErr := fileguard.DirectoryIdentity(root)
	closeErr := root.Close()
	return identity, errors.Join(identityErr, closeErr)
}
