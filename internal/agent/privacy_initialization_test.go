package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/diskguard"
	"github.com/ixayldz/Viber/internal/store"
)

func TestPrivacyInitializationCrashHelper(t *testing.T) {
	directory := os.Getenv("VIBER_PRIVACY_INIT_HELPER_STORE")
	if directory == "" {
		t.Skip("subprocess helper")
	}
	_, err := openSessionWithPrivacyFault(context.Background(), directory, nil, func(step string) error {
		if step == os.Getenv("VIBER_PRIVACY_INIT_HELPER_STEP") {
			os.Exit(73) // No Close/defer; the allocation or bind must be durable.
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Fatal("initialization crash boundary was not reached")
}

func TestPrivacyBootstrapIsPinnedBeforeDirectoryCreationAndRecoversSamePath(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	directory := filepath.Join(parent, "owner")
	_, err := openSessionWithPrivacyFault(ctx, directory, nil, func(step string) error {
		if step == "bootstrap-reserved" {
			return context.Canceled
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("bootstrap fault hook was not reached", err)
	}
	if got := authorityDirectoryCount(t, parent); got != 0 {
		t.Fatal("privacy root was created before the owner-store bootstrap CAS", got)
	}

	journal, err := store.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := journal.PrivacyBootstrap(ctx)
	if err != nil || bootstrap == nil || bootstrap.State != "PENDING" {
		journal.Close()
		t.Fatal("pre-directory bootstrap descriptor was not durable", bootstrap, err)
	}
	pending, err := journal.PendingPrivacyAuthority(ctx)
	if err != nil || pending != nil {
		journal.Close()
		t.Fatal("final physical-root authority was written before mkdir", err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal("same-path bootstrap recovery failed", err)
	}
	defer s.Close()
	if got := authorityDirectoryCount(t, parent); got != 1 {
		t.Fatal("recovery did not reuse the reserved authority path", got)
	}
	active, err := s.Journal.PrivacyAuthority(ctx)
	if err != nil || active == nil {
		t.Fatal("recovered privacy authority is missing", err)
	}
	var authority PrivacyAuthority
	if c.DecodeStrict(active, &authority) != nil || authority.ID != bootstrap.AuthorityID || authority.Directory != bootstrap.Directory || authority.PhysicalRoot == "" {
		t.Fatal("recovery changed the bootstrap authority binding", authority)
	}
	bound, err := s.Journal.PrivacyBootstrap(ctx)
	if err != nil || bound == nil || bound.PhysicalRoot != authority.PhysicalRoot || bound.RequestDigest != bootstrap.RequestDigest {
		t.Fatal("recovery lost the exact owner-store bootstrap binding", bound, err)
	}
}

func TestPrivacyBootstrapRejectsForeignRootCreatedAtPendingDestination(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	directory := filepath.Join(parent, "owner")
	_, err := openSessionWithPrivacyFault(ctx, directory, nil, func(step string) error {
		if step == "bootstrap-reserved" {
			return context.Canceled
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("bootstrap reservation fault hook was not reached", err)
	}
	journal, err := store.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := journal.PrivacyBootstrap(ctx)
	if err != nil || bootstrap == nil || bootstrap.State != "PENDING" {
		journal.Close()
		t.Fatal("pre-directory bootstrap descriptor was not durable", bootstrap, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(bootstrap.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	if session, openErr := OpenExisting(ctx, directory); openErr == nil {
		session.Close()
		t.Fatal("foreign empty destination was adopted without a creation witness")
	}
	if _, err = os.Lstat(bootstrap.Directory); err != nil {
		t.Fatal("rejected foreign directory was removed", err)
	}
	journal, err = store.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if active, activeErr := journal.PrivacyAuthority(ctx); activeErr != nil || active != nil {
		t.Fatal("foreign destination became active authority", activeErr)
	}
}

func TestPrivacyBootstrapRecoversPinnedStagingDirectory(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	directory := filepath.Join(parent, "owner")
	_, err := openSessionWithPrivacyFault(ctx, directory, nil, func(step string) error {
		if step == "bootstrap-stage-bound" {
			return context.Canceled
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("staged-root fault hook was not reached", err)
	}
	journal, err := store.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := journal.PrivacyBootstrap(ctx)
	if err != nil || bootstrap == nil || bootstrap.State != "STAGE_BOUND" || bootstrap.PhysicalRoot == "" {
		journal.Close()
		t.Fatal("staging identity was not durably pinned", bootstrap, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	session, err := OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal("pinned staging directory was not published and recovered", err)
	}
	defer session.Close()
	recovered, err := session.Journal.PrivacyBootstrap(ctx)
	if err != nil || recovered == nil || recovered.State != "AUTHORITY_BOUND" || recovered.PhysicalRoot != bootstrap.PhysicalRoot {
		t.Fatal("recovery changed staging identity", recovered, err)
	}
}

func TestPrivacyBootstrapRecoversAuthorityBindBeforeBootstrapAdvance(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	directory := filepath.Join(parent, "owner")
	_, err := openSessionWithPrivacyFault(ctx, directory, nil, func(step string) error {
		if step == "authority-bound-before-bootstrap" {
			return context.Canceled
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("authority/bootstrap crash window was not reached", err)
	}

	journal, err := store.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	active, err := journal.PrivacyAuthority(ctx)
	bootstrap, bootstrapErr := journal.PrivacyBootstrap(ctx)
	if err = errors.Join(err, bootstrapErr); err != nil || active == nil || bootstrap == nil || bootstrap.State != "ROOT_BOUND" {
		journal.Close()
		t.Fatal("crash did not preserve the exact intermediate authority state", bootstrap, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}

	session, err := OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal("authority bind crash recovery failed", err)
	}
	defer session.Close()
	recovered, err := session.Journal.PrivacyBootstrap(ctx)
	if err != nil || recovered == nil || recovered.State == "ROOT_BOUND" || recovered.AuthorityDigest == "" {
		t.Fatal("recovery did not advance the exact authority receipt", recovered, err)
	}
}

func authorityDirectoryCount(t *testing.T, parent string) int {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".viber-privacy-") {
			count++
		}
	}
	return count
}

func TestPrivacyInitializationCrashRecoveryReusesPinnedPhysicalAllocation(t *testing.T) {
	for _, step := range []string{"allocated", "reserve", "genesis", "bound"} {
		t.Run(step, func(t *testing.T) {
			ctx := context.Background()
			parent := t.TempDir()
			directory := filepath.Join(parent, "owner")
			journal, err := store.Open(ctx, directory)
			if err != nil {
				t.Fatal(err)
			}
			before, err := journal.SnapshotInfo(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := journal.ResourceLedger(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err = journal.Close(); err != nil {
				t.Fatal(err)
			}
			childCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			command := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestPrivacyInitializationCrashHelper$")
			command.Env = append(os.Environ(), "VIBER_PRIVACY_INIT_HELPER_STORE="+directory, "VIBER_PRIVACY_INIT_HELPER_STEP="+step)
			output, err := command.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 73 {
				t.Fatal("actual child did not crash at boundary", err, string(output))
			}
			journal, err = store.Open(ctx, directory)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := journal.PendingPrivacyAuthority(ctx)
			if step == "bound" {
				if err != nil || raw != nil {
					t.Fatal("post-bind crash was not atomic", err)
				}
				raw, err = journal.PrivacyAuthority(ctx)
			}
			if closeErr := journal.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			var authority PrivacyAuthority
			if err != nil || c.DecodeStrict(raw, &authority) != nil || authority.validate() != nil {
				t.Fatal("crash lost physical allocation receipt", err)
			}
			if authorityDirectoryCount(t, parent) != 1 {
				t.Fatal("crash created duplicate roots")
			}
			if step == "allocated" {
				if _, err = os.Lstat(filepath.Join(authority.Directory, "control.reserve")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("reserve was written before durable allocation", err)
				}
			}
			s, err := OpenExisting(ctx, directory)
			if err != nil {
				t.Fatal("failed to reuse exact allocation", err)
			}
			defer func() {
				if s != nil {
					s.Close()
				}
			}()
			if *s.privacy != authority {
				t.Fatal("recovery changed authority identity")
			}
			if pending, err := s.Journal.PendingPrivacyAuthority(ctx); err != nil || pending != nil {
				t.Fatal("recovered bind retained pending allocation", err)
			}
			reserve, err := diskguard.Open(authority.Directory)
			if err != nil {
				t.Fatal(err)
			}
			status, err := reserve.Status()
			reserve.Close()
			if err != nil || status.RetainedReserveBytes != diskguard.ReserveBytes {
				t.Fatal("recovery lost physical reserve", status, err)
			}
			for retry := 0; retry < 10; retry++ {
				if err = s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = OpenExisting(ctx, directory)
				if err != nil {
					t.Fatal(err)
				}
				if *s.privacy != authority || authorityDirectoryCount(t, parent) != 1 {
					t.Fatal("reopen allocated another root")
				}
				if after, err := s.Journal.SnapshotInfo(ctx); err != nil || after != before {
					t.Fatal("initialization changed task journal", err)
				}
				if after, err := s.Journal.ResourceLedger(ctx); err != nil || !reflect.DeepEqual(after, ledger) {
					t.Fatal("recovery changed outstanding resource risk", err)
				}
			}
		})
	}
}

func TestPrivacyInitializationRetryRejectsForeignRootAndRecoversOnlyGenesisTemps(t *testing.T) {
	for _, kind := range []string{"foreign", "replaced", "missing", "genesis", "foreign-temp", "genesis-temp"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			parent := t.TempDir()
			directory := filepath.Join(parent, "owner")
			_, err := openSessionWithPrivacyFault(ctx, directory, nil, func(step string) error {
				if step == "reserve" {
					return context.Canceled
				}
				return nil
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatal("failure hook not reached", err)
			}
			journal, err := store.Open(ctx, directory)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := journal.PendingPrivacyAuthority(ctx)
			journal.Close()
			var authority PrivacyAuthority
			if err != nil || c.DecodeStrict(raw, &authority) != nil {
				t.Fatal(err)
			}
			name, content := "user-file.txt", []byte("foreign content must survive")
			switch kind {
			case "replaced", "missing":
				if err = os.Rename(authority.Directory, filepath.Join(parent, "retained-original")); err != nil {
					t.Fatal(err)
				}
				if kind == "replaced" {
					if err = os.Mkdir(authority.Directory, 0700); err != nil {
						t.Fatal(err)
					}
				} else {
					name = ""
				}
			case "genesis":
				name = "authority.json"
			case "foreign-temp", "genesis-temp":
				name = ".publish-" + strings.Repeat("a", 32)
				if kind == "genesis-temp" {
					content = raw[:len(raw)/2]
				}
			}
			if name != "" {
				if err = os.WriteFile(filepath.Join(authority.Directory, name), content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for retry := 0; retry < 2; retry++ {
				s, openErr := OpenExisting(ctx, directory)
				err = openErr
				if kind == "genesis-temp" {
					if err != nil || *s.privacy != authority {
						t.Fatal("owned interrupted genesis not recovered", err)
					}
					s.Close()
					continue
				}
				if err == nil {
					t.Fatal("foreign/missing/replaced allocation adopted")
				}
				journal, err := store.Open(ctx, directory)
				if err != nil {
					t.Fatal(err)
				}
				if active, err := journal.PrivacyAuthority(ctx); err != nil || active != nil {
					t.Fatal("invalid root became current authority", err)
				}
				if pending, err := journal.PendingPrivacyAuthority(ctx); err != nil || string(pending) != string(raw) {
					t.Fatal("failed admission changed allocation", err)
				}
				journal.Close()
				if name != "" {
					retained, err := os.ReadFile(filepath.Join(authority.Directory, name))
					if err != nil || string(retained) != string(content) {
						t.Fatal("foreign content removed", err)
					}
				}
			}
			if kind == "genesis-temp" {
				if _, err = os.Lstat(filepath.Join(authority.Directory, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("owned interrupted genesis temp retained", err)
				}
				root, err := openPrivacyRoot(authority)
				if err != nil {
					t.Fatal(err)
				}
				view, err := readPrivacy(root)
				root.Close()
				if err != nil || view.Sequence != 0 || view.Watermark != 0 {
					t.Fatal("initialization fabricated deletion state", err)
				}
			}
			if authorityDirectoryCount(t, parent) > 1 {
				t.Fatal("retry created another allocation")
			}
		})
	}
}
