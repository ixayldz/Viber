package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func TestPrivacyOperationCrashHelper(t *testing.T) {
	raw := os.Getenv("VIBER_OPERATION_TEST_AUTHORITY")
	if raw == "" {
		t.Skip("actual subprocess helper")
	}
	var authority PrivacyAuthority
	if c.DecodeStrict([]byte(raw), &authority) != nil {
		t.Fatal("invalid helper authority")
	}
	step := os.Getenv("VIBER_OPERATION_TEST_STEP")
	if step == "fd-limit" {
		if err := operationTestDescriptorLimit(); err != nil {
			t.Fatal(err)
		}
		root, err := openPrivacyRoot(authority)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		lock, err := privacyLock(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		retired, err := retirePrivacyOperationLeases(context.Background(), root, authority, "")
		if err != nil || retired != 512 {
			t.Fatal("bounded native descriptor retirement", retired, err)
		}
		return
	}
	operation, err := privacyOperationWithFault(context.Background(), authority, func(boundary string) error {
		if step == boundary {
			os.Exit(83)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if step == "released" {
		lock, err := privacyLock(context.Background(), operation.root)
		if err != nil {
			t.Fatal(err)
		}
		_ = lock // process death releases the family lock as well
		if err = operation.lease.Close(); err != nil {
			t.Fatal(err)
		}
		os.Exit(83)
	}
	if step == "active" {
		fmt.Println("OPERATION_LEASE_ACTIVE")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(83) // no cleanup: native lock must be released by process death
	}
	t.Fatal("expected crash boundary missing")
}

func operationChild(t *testing.T, ctx context.Context, authority PrivacyAuthority, step string) *exec.Cmd {
	t.Helper()
	raw, _ := c.CanonicalV1(authority)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPrivacyOperationCrashHelper$")
	command.Env = append(os.Environ(), "VIBER_OPERATION_TEST_AUTHORITY="+string(raw), "VIBER_OPERATION_TEST_STEP="+step)
	return command
}

func TestPrivacyOperationLeasesRetireOnCloseAndActualCrashWithoutChangingPins(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := readPrivacy(root)
	if err != nil {
		t.Fatal(err)
	}
	scopes, scopeDigest, err := readPrivacyScopes(root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Journal.SnapshotInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := s.Journal.ResourceLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count, err := admitPrivacyCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	for retry := 0; retry < 10; retry++ {
		operation, err := privacyOperation(ctx, *s.privacy)
		if err != nil {
			t.Fatal(err)
		}
		name := operation.name
		if err = operation.Close(); err != nil {
			t.Fatal(err)
		}
		if err = operation.Close(); err != nil {
			t.Fatal("close retry", err)
		}
		if _, err = root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("completed operation accumulated", err)
		}
	}
	for _, step := range []string{"published", "leased", "released"} {
		t.Run(step, func(t *testing.T) {
			childCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			output, err := operationChild(t, childCtx, *s.privacy, step).CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 83 {
				t.Fatal("child did not die at exact boundary", err, string(output))
			}
			pressure, err := admitPrivacyCatalog(root)
			if err != nil || pressure != count+1 {
				t.Fatal("crash lease was not durable", pressure, err)
			}
			for reopen := 0; reopen < 10; reopen++ {
				capacity, err := s.RetirePrivacyOperationLeases(ctx)
				if err != nil || capacity.CatalogEntries != count {
					t.Fatal("crash retirement/dedup", capacity, err)
				}
			}
		})
	}
	after, err := readPrivacy(root)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("retirement altered registry/watermark", err)
	}
	currentScopes, currentDigest, err := readPrivacyScopes(root)
	if err != nil || scopeDigest != currentDigest || !reflect.DeepEqual(scopes, currentScopes) {
		t.Fatal("owner scope retired", err)
	}
	current, err := s.Journal.SnapshotInfo(ctx)
	if err != nil || current != info {
		t.Fatal("kernel snapshot changed", err)
	}
	currentLedger, err := s.Journal.ResourceLedger(ctx)
	if err != nil || !reflect.DeepEqual(ledger, currentLedger) {
		t.Fatal("risk settlement manufactured", err)
	}
}

func TestPrivacyOperationRetirementPreservesActiveChildAndForeignCatalog(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	count, err := admitPrivacyCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	command := operationChild(t, ctx, *s.privacy, "active")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			command.Process.Kill()
			command.Wait()
		}
	})
	var line []byte
	var one [1]byte
	for len(line) < 4096 {
		if _, err = output.Read(one[:]); err != nil {
			t.Fatal(err)
		}
		line = append(line, one[0])
		if one[0] == '\n' {
			break
		}
	}
	if string(line) != "OPERATION_LEASE_ACTIVE\n" {
		t.Fatal("active child signal", string(line))
	}
	capacity, err := s.RetirePrivacyOperationLeases(ctx)
	if err != nil || capacity.CatalogEntries != count+1 {
		t.Fatal("active process lease retired", capacity, err)
	}
	if err = input.Close(); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 83 {
		t.Fatal(err)
	}
	capacity, err = s.RetirePrivacyOperationLeases(ctx)
	if err != nil || capacity.CatalogEntries != count {
		t.Fatal("native death lease not reclaimed", capacity, err)
	}
	// A valid owned descriptor must not be removed before a later foreign entry
	// has been audited. Operation IDs sort deterministically for this contract.
	digest, _ := c.Digest(s.privacy)
	owned := filepath.Join("owners", fmt.Sprintf("%032x.lock", 1))
	raw, _ := c.CanonicalV1(privacyOperationLease{1, "RESTORE_OPERATION", owned, digest})
	if err = fileguard.Publish(root, owned, raw); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join("owners", fmt.Sprintf("%032x.lock", 2))
	if err = fileguard.Publish(root, foreign, []byte("foreign")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetirePrivacyOperationLeases(ctx); err == nil {
		t.Fatal("foreign inactive lease granted retirement")
	}
	if got, err := fileguard.ReadRegular(root, owned, 4096); err != nil || string(got) != string(raw) {
		t.Fatal("mutation before whole inventory validation", err)
	}
	if got, err := fileguard.ReadRegular(root, foreign, 4096); err != nil || string(got) != "foreign" {
		t.Fatal("foreign file removed", err)
	}
}

func TestPrivacyOperationRetirementAtCatalogLimitRestoresOnlyTypedCapacity(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	count, err := admitPrivacyCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := c.Digest(s.privacy)
	for index := 1; count < privacyWorkCatalogLimit; index++ {
		name := filepath.Join("owners", fmt.Sprintf("%032x.lock", index))
		raw := []byte{}
		if index <= 2 {
			raw, _ = c.CanonicalV1(privacyOperationLease{1, "RESTORE_OPERATION", name, digest})
		}
		if err = root.WriteFile(name, raw, 0600); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if err = fileguard.SyncDirectory(root, "owners"); err != nil {
		t.Fatal(err)
	}
	operation, err := privacyOperation(ctx, *s.privacy)
	if err != nil {
		t.Fatal("typed dead operations prevented new restore admission", err)
	}
	if err = operation.Close(); err != nil {
		t.Fatal(err)
	}
	capacity, err := s.PrivacyMetadataCapacity(ctx)
	if err != nil || capacity.CatalogEntries != privacyWorkCatalogLimit-2 || !capacity.WorkReady {
		t.Fatal("typed cleanup did not restore exact capacity", capacity, err)
	}
	legacy := filepath.Join("owners", fmt.Sprintf("%032x.lock", 3))
	if raw, err := fileguard.ReadRegular(root, legacy, 0); err != nil || len(raw) != 0 {
		t.Fatal("untyped legacy lease was pruned", err)
	}
}

func TestPrivacyOperationRetirementRejectsWrongAuthorityScopeAndHardlink(t *testing.T) {
	for _, variant := range []string{"authority", "name", "scope", "hardlink", "noncanonical", "oversized", "invalid-id"} {
		t.Run(variant, func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			root, err := openPrivacyRoot(*s.privacy)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			digest, _ := c.Digest(s.privacy)
			id := fmt.Sprintf("%032x", 1)
			if variant == "invalid-id" {
				id = "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"
			}
			name := filepath.Join("owners", id+".lock")
			lease := privacyOperationLease{1, "RESTORE_OPERATION", name, digest}
			if variant == "authority" {
				lease.AuthorityDigest = c.HashBytes([]byte("other"))
			}
			if variant == "name" {
				lease.Name = filepath.Join("owners", fmt.Sprintf("%032x.lock", 2))
			}
			raw, _ := c.CanonicalV1(lease)
			if variant == "noncanonical" {
				raw = append(raw, '\n')
			}
			if variant == "oversized" {
				raw = make([]byte, 4097)
			}
			if err = fileguard.Publish(root, name, raw); err != nil {
				t.Fatal(err)
			}
			if variant == "scope" {
				if err = publishPrivacyScope(root, privacyScope{id, s.directory, s.instance.PhysicalRoot}); err != nil {
					t.Fatal(err)
				}
			}
			if variant == "hardlink" {
				if err = os.Link(filepath.Join(root.Name(), name), filepath.Join(root.Name(), "linked-operation")); err != nil {
					t.Fatal("native hardlink fixture", err)
				}
			}
			if _, err = s.RetirePrivacyOperationLeases(ctx); err == nil {
				t.Fatal("invalid retirement descriptor granted cleanup")
			}
			info, err := root.Lstat(name)
			if err != nil || info.Size() != int64(len(raw)) {
				t.Fatal("foreign/pinned file removed", err)
			}
		})
	}
}

func TestPrivacyOperationRetirementBoundsNativeDescriptorsAcrossLargeInventory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := admitPrivacyCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := c.Digest(s.privacy)
	for index := 1; index <= 512; index++ {
		name := filepath.Join("owners", fmt.Sprintf("%032x.lock", index))
		raw, _ := c.CanonicalV1(privacyOperationLease{1, "RESTORE_OPERATION", name, digest})
		if err = fileguard.Publish(root, name, raw); err != nil {
			t.Fatal(err)
		}
	}
	output, err := operationChild(t, ctx, *s.privacy, "fd-limit").CombinedOutput()
	if err != nil {
		t.Fatal("actual low-descriptor child failed", err, string(output))
	}
	count, err := admitPrivacyCatalog(root)
	if err != nil || count != before {
		t.Fatal("large inactive inventory was not reclaimed", count, err)
	}
}
