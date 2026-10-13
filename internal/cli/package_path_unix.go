//go:build linux || darwin

package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/distribution"
	"github.com/ixayldz/Viber/internal/fileguard"
)

const (
	unixLauncherFile  = "viber"
	unixReceiptFile   = "viber-path.json"
	unixBinaryLimit   = int64(512 << 20)
	unixLauncherLimit = int64(16 << 10)
)

type unixLauncherReceipt struct {
	SchemaVersion         int    `json:"schema_version"`
	Prefix                string `json:"prefix"`
	Directory             string `json:"directory"`
	Revision              string `json:"revision,omitempty"`
	BinaryPath            string `json:"binary_path,omitempty"`
	BinaryDigest          string `json:"binary_digest,omitempty"`
	LauncherDigest        string `json:"launcher_digest,omitempty"`
	LauncherBytes         int64  `json:"launcher_bytes,omitempty"`
	PendingRevision       string `json:"pending_revision,omitempty"`
	PendingBinaryPath     string `json:"pending_binary_path,omitempty"`
	PendingBinaryDigest   string `json:"pending_binary_digest,omitempty"`
	PendingLauncherDigest string `json:"pending_launcher_digest,omitempty"`
	PendingLauncherBytes  int64  `json:"pending_launcher_bytes,omitempty"`
}

func defaultManagedPrefix() (string, error) {
	if data := os.Getenv("XDG_DATA_HOME"); data != "" && filepath.IsAbs(data) {
		return filepath.Join(data, "viber", "program"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("per-user install location unavailable; provide --prefix")
	}
	return filepath.Join(home, ".local", "share", "viber", "program"), nil
}

func unixLauncherDirectory() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" || !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", errors.New("per-user launcher location unavailable")
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "viber", "managed-bin"), nil
}

func managedPathInstruction() string {
	directory, err := unixLauncherDirectory()
	if err != nil {
		return "Launcher installed; add its managed-bin directory to PATH manually."
	}
	return "For this shell, run: export PATH=" + shellQuote(directory) + `:"$PATH"`
}

func checkManagedPathOperation(prefix, _ string, requested bool) error {
	directory, err := unixLauncherDirectory()
	if err != nil {
		if requested {
			return err
		}
		return nil
	}
	if requested {
		prefixAbs, prefixErr := canonicalPrefix(prefix)
		if prefixErr != nil {
			return prefixErr
		}
		if !fileguard.Disjoint(prefixAbs, directory) {
			return c.Fail(c.PolicyDenied, "install prefix overlaps the per-user launcher directory")
		}
		if err = validateShellPath(filepath.Join(prefixAbs, "versions", strings.Repeat("0", 40), "viber")); err != nil {
			return err
		}
		if err = ensureNoExistingUnixViber(directory); err != nil {
			return err
		}
	}
	root, _, err := unixLauncherRoot(requested)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	entries, err := unixRootEntries(root)
	if err != nil {
		return err
	}
	receipt, found, err := readUnixLauncherReceipt(root, directory)
	if err != nil {
		return err
	}
	if !found {
		if requested && len(entries) != 0 {
			return c.Fail(c.Conflict, "managed launcher directory contains files without an ownership receipt")
		}
		return nil
	}
	prefixAbs, err := canonicalPrefix(prefix)
	if err != nil {
		return err
	}
	if receipt.Prefix != prefixAbs || receipt.Directory != directory {
		return c.Fail(c.Conflict, "managed launcher belongs to a different install prefix")
	}
	if hasForeignUnixLauncherFiles(entries) {
		return c.Fail(c.Conflict, "managed launcher directory contains unowned files")
	}
	_, _, err = currentUnixLauncher(root, receipt)
	return err
}

func syncManagedPath(prefix, binary string, requested bool) error {
	directory, err := unixLauncherDirectory()
	if err != nil {
		if requested {
			return err
		}
		return nil
	}
	root, _, err := unixLauncherRoot(requested)
	if errors.Is(err, os.ErrNotExist) && !requested {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	entries, err := unixRootEntries(root)
	if err != nil {
		return err
	}
	receipt, found, err := readUnixLauncherReceipt(root, directory)
	if err != nil {
		return err
	}
	prefixAbs, err := canonicalPrefix(prefix)
	if err != nil {
		return err
	}
	if !found {
		if !requested {
			return nil
		}
		if len(entries) != 0 {
			return c.Fail(c.Conflict, "managed launcher directory contains files without an ownership receipt")
		}
		if err = fileguard.Private(root); err != nil {
			return err
		}
		receipt = unixLauncherReceipt{SchemaVersion: 1, Prefix: prefixAbs, Directory: directory}
	} else if receipt.Prefix != prefixAbs || receipt.Directory != directory {
		return c.Fail(c.Conflict, "managed launcher belongs to a different install prefix")
	}
	if hasForeignUnixLauncherFiles(entries) {
		return c.Fail(c.Conflict, "managed launcher directory contains unowned files")
	}
	binaryPath, revision, binaryDigest, err := verifyUnixActiveBinary(binary, prefixAbs)
	if err != nil {
		return err
	}
	launcher, err := unixShellLauncher(binaryPath)
	if err != nil {
		return err
	}
	if receipt.PendingLauncherDigest != "" {
		if err = reconcileUnixLauncher(root, &receipt); err != nil {
			return err
		}
	}
	_, exists, err := currentUnixLauncher(root, receipt)
	if err != nil {
		return err
	}
	receipt.PendingRevision = revision
	receipt.PendingBinaryPath = binaryPath
	receipt.PendingBinaryDigest = binaryDigest
	receipt.PendingLauncherDigest = digestUnixBytes(launcher)
	receipt.PendingLauncherBytes = int64(len(launcher))
	if err = publishUnixReceipt(root, receipt, found); err != nil {
		return err
	}
	if exists {
		err = replaceUnixPrivateFile(root, unixLauncherFile, launcher, 0700)
	} else {
		err = publishUnixPrivateFile(root, unixLauncherFile, launcher, 0700)
	}
	if err != nil {
		return err
	}
	applyPendingUnixLauncher(&receipt)
	return publishUnixReceipt(root, receipt, true)
}

func removeManagedPath(prefix string) error {
	directory, err := unixLauncherDirectory()
	if err != nil {
		return nil
	}
	root, _, err := unixLauncherRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	receipt, found, err := readUnixLauncherReceipt(root, directory)
	if err != nil || !found {
		return err
	}
	prefixAbs, err := canonicalPrefix(prefix)
	if err != nil {
		return err
	}
	if receipt.Prefix != prefixAbs || receipt.Directory != directory {
		return c.Fail(c.Conflict, "managed launcher belongs to a different install prefix")
	}
	if receipt.PendingLauncherDigest != "" {
		if err = reconcileUnixLauncher(root, &receipt); err != nil {
			return err
		}
	}
	entries, err := unixRootEntries(root)
	if err != nil {
		return err
	}
	launcherEntry, exists := findUnixEntry(entries, unixLauncherFile)
	if exists {
		if !launcherEntry.Type().IsRegular() {
			return c.Fail(c.Conflict, "managed launcher changed; preserving it")
		}
		raw, readErr := fileguard.ReadRegular(root, unixLauncherFile, unixLauncherLimit)
		if readErr != nil || !unixLauncherReceiptAccepts(receipt, raw) {
			return c.Fail(c.Conflict, "managed launcher changed; preserving it")
		}
		if _, err = fileguard.RemoveBound(root, unixLauncherFile, digestUnixBytes(raw), int64(len(raw)), unixLauncherLimit); err != nil {
			return err
		}
	}
	receiptRaw, err := fileguard.ReadRegular(root, unixReceiptFile, unixLauncherLimit)
	if err != nil {
		return err
	}
	if _, err = fileguard.RemoveBound(root, unixReceiptFile, digestUnixBytes(receiptRaw), int64(len(receiptRaw)), unixLauncherLimit); err != nil {
		return err
	}
	entries, err = unixRootEntries(root)
	if err == nil && len(entries) == 0 {
		_ = root.Close()
		if removeErr := os.Remove(directory); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
	}
	return err
}

func unixLauncherRoot(create bool) (*os.Root, string, error) {
	directory, err := unixLauncherDirectory()
	if err != nil {
		return nil, "", err
	}
	if info, statErr := os.Lstat(directory); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, "", c.Fail(c.PolicyDenied, "managed launcher path is not a real directory")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
			return nil, "", c.Fail(c.PolicyDenied, "managed launcher directory must be private and owned by this user")
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		if !create {
			return nil, directory, os.ErrNotExist
		}
		if err = os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
			return nil, "", err
		}
		if err = os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, "", c.Fail(c.PolicyDenied, "managed launcher directory changed during creation")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
			return nil, "", c.Fail(c.PolicyDenied, "managed launcher directory must be private and owned by this user")
		}
	} else {
		return nil, "", statErr
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, "", err
	}
	return root, directory, nil
}

func verifyUnixActiveBinary(binary, prefix string) (string, string, string, error) {
	binaryPath, err := filepath.Abs(binary)
	if err != nil {
		return "", "", "", err
	}
	relative, err := filepath.Rel(prefix, binaryPath)
	if err != nil {
		return "", "", "", err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 3 || parts[0] != "versions" || !distribution.Revision(parts[1]) || parts[2] != "viber" {
		return "", "", "", c.Fail(c.PolicyDenied, "active binary is outside the managed version inventory")
	}
	root, err := os.OpenRoot(filepath.Dir(binaryPath))
	if err != nil {
		return "", "", "", err
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, filepath.Base(binaryPath), unixBinaryLimit)
	if err != nil {
		return "", "", "", err
	}
	return binaryPath, parts[1], digestUnixBytes(raw), nil
}

func unixShellLauncher(binary string) ([]byte, error) {
	if err := validateShellPath(binary); err != nil {
		return nil, err
	}
	return []byte("#!/bin/sh\nexec " + shellQuote(binary) + " \"$@\"\n"), nil
}

func validateShellPath(value string) error {
	for _, r := range value {
		if r == 0 || r == '\n' || r == '\r' || r < 0x20 || r == 0x7f {
			return c.Fail(c.UnsupportedCapability, "launcher path contains unsupported control characters")
		}
	}
	return nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func unixRootEntries(root *os.Root) ([]os.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(129)
	if len(entries) > 128 {
		return nil, c.Fail(c.BudgetLimitReached, "managed launcher directory entry bound exceeded")
	}
	if errors.Is(err, io.EOF) {
		return entries, nil
	}
	return entries, err
}

func findUnixEntry(entries []os.DirEntry, name string) (os.DirEntry, bool) {
	for _, entry := range entries {
		if entry.Name() == name {
			return entry, true
		}
	}
	return nil, false
}

func hasForeignUnixLauncherFiles(entries []os.DirEntry) bool {
	for _, entry := range entries {
		if entry.Name() == unixLauncherFile || entry.Name() == unixReceiptFile || strings.HasPrefix(entry.Name(), ".publish-") {
			continue
		}
		return true
	}
	return false
}

func readUnixLauncherReceipt(root *os.Root, directory string) (unixLauncherReceipt, bool, error) {
	var receipt unixLauncherReceipt
	info, err := root.Lstat(unixReceiptFile)
	if errors.Is(err, os.ErrNotExist) {
		return receipt, false, nil
	}
	if err != nil {
		return receipt, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > unixLauncherLimit {
		return receipt, false, c.Fail(c.PolicyDenied, "managed launcher receipt is not a regular file")
	}
	raw, err := fileguard.ReadRegular(root, unixReceiptFile, unixLauncherLimit)
	if err != nil || c.DecodeStrict(raw, &receipt) != nil || receipt.SchemaVersion != 1 || !filepath.IsAbs(receipt.Prefix) || receipt.Directory != directory || receipt.BinaryDigest != "" && !c.ValidDigest(receipt.BinaryDigest) || receipt.LauncherDigest != "" && !c.ValidDigest(receipt.LauncherDigest) || receipt.LauncherBytes < 0 || receipt.LauncherBytes > unixLauncherLimit || receipt.PendingBinaryDigest != "" && !c.ValidDigest(receipt.PendingBinaryDigest) || receipt.PendingLauncherDigest != "" && !c.ValidDigest(receipt.PendingLauncherDigest) || receipt.PendingLauncherBytes < 0 || receipt.PendingLauncherBytes > unixLauncherLimit {
		return unixLauncherReceipt{}, false, c.Fail(c.StoreIntegrityError, "managed launcher receipt invalid")
	}
	if receipt.Revision != "" && !distribution.Revision(receipt.Revision) || receipt.PendingRevision != "" && !distribution.Revision(receipt.PendingRevision) {
		return unixLauncherReceipt{}, false, c.Fail(c.StoreIntegrityError, "managed launcher revision receipt invalid")
	}
	canonical, err := c.CanonicalV1(receipt)
	if err != nil || string(canonical) != string(raw) {
		return unixLauncherReceipt{}, false, c.Fail(c.StoreIntegrityError, "managed launcher receipt is not canonical")
	}
	return receipt, true, nil
}

func unixLauncherReceiptAccepts(receipt unixLauncherReceipt, raw []byte) bool {
	digest, size := digestUnixBytes(raw), int64(len(raw))
	return digest == receipt.LauncherDigest && size == receipt.LauncherBytes || receipt.PendingLauncherDigest != "" && digest == receipt.PendingLauncherDigest && size == receipt.PendingLauncherBytes
}

func currentUnixLauncher(root *os.Root, receipt unixLauncherReceipt) ([]byte, bool, error) {
	entries, err := unixRootEntries(root)
	if err != nil {
		return nil, false, err
	}
	entry, found := findUnixEntry(entries, unixLauncherFile)
	if !found {
		return nil, false, nil
	}
	if !entry.Type().IsRegular() {
		return nil, false, c.Fail(c.PolicyDenied, "managed launcher is not a regular file")
	}
	raw, err := fileguard.ReadRegular(root, unixLauncherFile, unixLauncherLimit)
	if err != nil {
		return nil, false, err
	}
	if !unixLauncherReceiptAccepts(receipt, raw) {
		return nil, false, c.Fail(c.Conflict, "managed launcher changed; preserving it")
	}
	return raw, true, nil
}

func reconcileUnixLauncher(root *os.Root, receipt *unixLauncherReceipt) error {
	_, exists, err := currentUnixLauncher(root, *receipt)
	if err != nil || !exists {
		return err
	}
	raw, err := fileguard.ReadRegular(root, unixLauncherFile, unixLauncherLimit)
	if err != nil {
		return err
	}
	if digestUnixBytes(raw) == receipt.PendingLauncherDigest && int64(len(raw)) == receipt.PendingLauncherBytes {
		applyPendingUnixLauncher(receipt)
		return publishUnixReceipt(root, *receipt, true)
	}
	return nil
}

func applyPendingUnixLauncher(receipt *unixLauncherReceipt) {
	receipt.Revision = receipt.PendingRevision
	receipt.BinaryPath = receipt.PendingBinaryPath
	receipt.BinaryDigest = receipt.PendingBinaryDigest
	receipt.LauncherDigest = receipt.PendingLauncherDigest
	receipt.LauncherBytes = receipt.PendingLauncherBytes
	receipt.PendingRevision = ""
	receipt.PendingBinaryPath = ""
	receipt.PendingBinaryDigest = ""
	receipt.PendingLauncherDigest = ""
	receipt.PendingLauncherBytes = 0
}

func publishUnixReceipt(root *os.Root, receipt unixLauncherReceipt, exists bool) error {
	raw, err := c.CanonicalV1(receipt)
	if err != nil {
		return err
	}
	if !exists {
		return fileguard.Publish(root, unixReceiptFile, raw)
	}
	return replaceUnixPrivateFile(root, unixReceiptFile, raw, 0600)
}

func publishUnixPrivateFile(root *os.Root, name string, raw []byte, mode os.FileMode) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := ".publish-" + hex.EncodeToString(nonce[:])
	f, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Link(filepath.Join(root.Name(), temporary), filepath.Join(root.Name(), name)); err != nil {
		return err
	}
	if err = root.Remove(temporary); err != nil {
		return err
	}
	return fileguard.SyncDirectory(root, ".")
}

func replaceUnixPrivateFile(root *os.Root, name string, raw []byte, mode os.FileMode) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := ".publish-" + hex.EncodeToString(nonce[:])
	f, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(filepath.Join(root.Name(), temporary), filepath.Join(root.Name(), name)); err != nil {
		return err
	}
	return fileguard.SyncDirectory(root, ".")
}

func ensureNoExistingUnixViber(managedDirectory string) error {
	managed := filepath.Clean(managedDirectory)
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry == "" {
			continue
		}
		absolute, err := filepath.Abs(entry)
		if err != nil || filepath.Clean(absolute) == managed {
			continue
		}
		candidate := filepath.Join(absolute, unixLauncherFile)
		if info, err := os.Lstat(candidate); err == nil && !info.IsDir() {
			return c.Fail(c.Conflict, "an existing Viber command is already on PATH; it was preserved")
		}
	}
	return nil
}

func digestUnixBytes(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
