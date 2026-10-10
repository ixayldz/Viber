package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// A typed, genesis-bound operation lease is separate from persistent owner
// scopes and process fences. Empty legacy leases have no retirement proof.
type privacyOperationLease struct {
	SchemaVersion   int    `json:"schema_version"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	AuthorityDigest string `json:"authority_digest"`
}

func validPrivacyLeaseFilename(base string) bool {
	if len(base) != 37 || !strings.HasSuffix(base, ".lock") {
		return false
	}
	for _, r := range base[:32] {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

type privacyRetirementCandidate struct {
	identity os.FileInfo
	name     string
	raw      []byte
}

func inspectPrivacyOperationLease(root *os.Root, name, digest string, pinned bool) (candidate *privacyRetirementCandidate, resultErr error) {
	f, err := fileguard.LockExisting(root, name)
	var typed *c.Error
	if errors.As(err, &typed) && typed.Code == c.StoreOwned {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return nil, nil
	} // untyped lease has no retirement proof
	if info.Size() > 4096 {
		return nil, c.Fail(c.StoreIntegrityError, "oversized operation lease")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return nil, err
	}
	var lease privacyOperationLease
	if c.DecodeStrict(raw, &lease) != nil || lease.SchemaVersion != 1 || lease.Kind != "RESTORE_OPERATION" || lease.Name != name || lease.AuthorityDigest != digest || pinned {
		return nil, c.Fail(c.StoreIntegrityError, "operation lease is foreign or pinned by an owner scope")
	}
	canonical, err := c.CanonicalV1(lease)
	if err != nil || string(canonical) != string(raw) {
		return nil, c.Fail(c.StoreIntegrityError, "noncanonical operation retirement descriptor")
	}
	return &privacyRetirementCandidate{info, name, raw}, nil
}

// Caller holds the family registry lock throughout inventory, probing and
// unlink. A new owner cannot start using a replacement inode while retirement
// drops the old handle (required on Windows). No pathname is reused by restore
// operations, and any scope referring to that ID makes retirement invalid.
func retirePrivacyOperationLeases(ctx context.Context, root *os.Root, authority PrivacyAuthority, only string) (int, error) {
	scopes, _, err := readPrivacyScopes(root)
	if err != nil {
		return 0, err
	}
	pinned := map[string]bool{}
	for _, scope := range scopes {
		pinned[filepath.Join("owners", scope.InstanceID+".lock")] = true
	}
	directory, err := root.Open("owners")
	if err != nil {
		return 0, err
	}
	names, err := directory.Readdirnames(privacyCatalogLimit + 1)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	err = errors.Join(err, directory.Close())
	if err != nil {
		return 0, err
	}
	if len(names) > privacyCatalogLimit {
		return 0, c.Fail(c.BudgetLimitReached, "operation retirement catalog bound exceeded")
	}
	sort.Strings(names)
	digest, err := c.Digest(authority)
	if err != nil {
		return 0, err
	}
	candidates := []privacyRetirementCandidate{}
	for _, base := range names {
		if err = ctx.Err(); err != nil {
			return 0, err
		}
		if strings.HasSuffix(base, ".json") && c.ValidDigest(strings.TrimSuffix(base, ".json")) {
			continue
		}
		if !validPrivacyLeaseFilename(base) {
			return 0, c.Fail(c.StoreIntegrityError, "foreign retirement catalog member")
		}
		name := filepath.Join("owners", base)
		if only != "" && name != only {
			continue
		}
		candidate, inspectErr := inspectPrivacyOperationLease(root, name, digest, pinned[name])
		if inspectErr != nil {
			return 0, inspectErr
		}
		if candidate != nil {
			candidates = append(candidates, *candidate)
		} else if only != "" {
			return 0, c.Fail(c.StaleBase, "closing operation lost its exact typed lease")
		}
	}
	retired := 0
	// Audit the entire inactive inventory before the first removal. Active locks,
	// untyped leases, source scopes, managed-copy pins and journal remain intact.
	for index := range candidates {
		candidate := &candidates[index]
		if err = ctx.Err(); err != nil {
			return retired, err
		}
		// Only one native probe handle is open at a time, even at 8192 entries.
		// Reacquisition rechecks both liveness and the exact preflight inode/bytes.
		current, inspectErr := inspectPrivacyOperationLease(root, candidate.name, digest, false)
		if inspectErr != nil {
			return retired, inspectErr
		}
		if current == nil {
			return retired, c.Fail(c.StaleBase, "operation lease liveness or content changed after preflight")
		}
		if !os.SameFile(current.identity, candidate.identity) || string(current.raw) != string(candidate.raw) {
			return retired, c.Fail(c.StaleBase, "operation lease changed after retirement preflight")
		}
		removed, removeErr := fileguard.RemoveBoundIdentity(root, candidate.name, candidate.identity, c.HashBytes(candidate.raw), int64(len(candidate.raw)), 4096)
		if removeErr != nil {
			return retired, removeErr
		}
		if removed {
			retired++
		}
	}
	return retired, nil
}

// RetirePrivacyOperationLeases is bounded remediation, not owner/fence pruning.
func (s *Session) RetirePrivacyOperationLeases(ctx context.Context) (*PrivacyMetadataStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.privacy == nil {
		return nil, c.Fail(c.UnsupportedCapability, "bound privacy authority required")
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	lock, err := privacyLock(ctx, root)
	if err != nil {
		return nil, err
	}
	if _, err = readPrivacy(root); err == nil {
		_, err = retirePrivacyOperationLeases(ctx, root, *s.privacy, "")
	}
	err = errors.Join(err, lock.Close())
	if err != nil {
		return nil, err
	}
	return s.PrivacyMetadataCapacity(ctx)
}
