package distribution

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

type cappedOutput struct {
	bytes    int
	overflow bool
}

func (w *cappedOutput) Write(raw []byte) (int, error) {
	w.bytes += len(raw)
	if w.bytes > 64<<10 {
		w.overflow = true
		return 0, c.Fail(c.InvalidArgument, "provenance verifier output bound exceeded")
	}
	return len(raw), nil
}

// VerifyProvenance executes the operator-selected absolute GitHub CLI against a
// private snapshot. Neither PATH nor a repository config selects the verifier.
// The source digest is the operator's expected commit, never archive metadata.
func VerifyProvenance(ctx context.Context, archivePath, ghPath, expectedRevision string) ([]byte, string, error) {
	if !Revision(expectedRevision) || !filepath.IsAbs(ghPath) {
		return nil, "", c.Fail(c.InvalidArgument, "exact source SHA and absolute operator-selected gh executable required")
	}
	ghInfo, err := os.Lstat(ghPath)
	if err != nil || !ghInfo.Mode().IsRegular() {
		return nil, "", c.Fail(c.InvalidArgument, "regular gh executable required")
	}
	abs, err := filepath.Abs(archivePath)
	if err != nil {
		return nil, "", err
	}
	parent, err := os.OpenRoot(filepath.Dir(abs))
	if err != nil {
		return nil, "", err
	}
	defer parent.Close()
	raw, err := fileguard.ReadRegular(parent, filepath.Base(abs), MaxArchiveBytes)
	if err != nil {
		return nil, "", err
	}
	kind := ""
	suffix := ""
	if strings.HasSuffix(abs, ".zip") {
		kind = "zip"
		suffix = ".zip"
	} else if strings.HasSuffix(abs, ".tar.gz") {
		kind = "tar.gz"
		suffix = ".tar.gz"
	} else {
		return nil, "", c.Fail(c.InvalidArgument, "portable archive extension required")
	}
	temp, err := os.MkdirTemp(filepath.Dir(abs), ".viber-provenance-")
	if err != nil {
		return nil, "", err
	}
	// Cleanup only the single exact file and its empty directory; no recursive
	// removal, glob, computed parent cleanup or extraction occurs here.
	defer os.Remove(temp)
	private, err := os.OpenRoot(temp)
	if err != nil {
		return nil, "", err
	}
	defer private.Close()
	if err = fileguard.Private(private); err != nil {
		return nil, "", err
	}
	name := "archive" + suffix
	if err = fileguard.Publish(private, name, raw); err != nil {
		return nil, "", err
	}
	defer private.Remove(name)
	verifyCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	command := exec.CommandContext(verifyCtx, ghPath, "attestation", "verify", filepath.Join(temp, name), "-R", "ixayldz/Viber", "--signer-workflow", "ixayldz/Viber/.github/workflows/ci.yml", "--source-ref", "refs/heads/main", "--source-digest", expectedRevision)
	output := &cappedOutput{}
	command.Stdout = output
	command.Stderr = output
	command.Stdin = nil
	command.WaitDelay = 2 * time.Second
	if err = command.Run(); err != nil || output.overflow {
		return nil, "", c.Fail(c.PolicyDenied, "exact source GitHub provenance verification failed; raw verifier output omitted")
	}
	verified, err := fileguard.ReadRegular(private, name, MaxArchiveBytes)
	if err != nil || c.HashBytes(verified) != c.HashBytes(raw) {
		return nil, "", c.Fail(c.StoreIntegrityError, "archive changed during provenance verification")
	}
	return raw, kind, nil
}

// RunActive preserves terminal handles and the child's exit contract. The
// install lock stays held while the child runs, preventing concurrent switches.
func (m *Manager) RunActive(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	state, err := m.Status()
	if err != nil {
		return 4, err
	}
	if state.Binary == "" {
		return 4, c.Fail(c.UnsupportedCapability, "no active installed version")
	}
	manifest, err := m.version(state.Active.Revision, state.Active.ManifestDigest)
	if err != nil {
		return 4, err
	}
	if !hasAllRuntimeCapabilities(manifest) {
		locked, required, lockErr := m.lockRegisteredStoreCapabilities(ctx)
		if lockErr != nil {
			return 4, lockErr
		}
		compatibilityErr := requireRuntimeCapabilities(manifest, required)
		if closeErr := closeStoreLocks(locked); closeErr != nil {
			return 4, errors.Join(compatibilityErr, closeErr)
		}
		if compatibilityErr != nil {
			return 4, compatibilityErr
		}
	}
	command := exec.CommandContext(ctx, state.Binary, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	err = command.Run()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 4, err
}

// A package declaring every known runtime capability needs no store inspection
// before launch: its bounded support set already covers every feature this
// launcher can discover. Legacy and partial packages still require the
// exclusive, fail-closed compatibility scan.
func hasAllRuntimeCapabilities(manifest Manifest) bool {
	declared := make(map[string]bool, len(manifest.RuntimeCapabilities))
	for _, capability := range manifest.RuntimeCapabilities {
		declared[capability] = true
	}
	for _, capability := range supportedRuntimeCapabilities {
		if !declared[capability] {
			return false
		}
	}
	return true
}
