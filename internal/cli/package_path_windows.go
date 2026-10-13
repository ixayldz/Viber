//go:build windows

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
	"unsafe"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/distribution"
	"github.com/ixayldz/Viber/internal/fileguard"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	managedPathDirectory   = "managed-bin"
	managedPathLauncher    = "viber.cmd"
	managedPathReceiptFile = "viber-path.json"
	managedBinaryLimit     = int64(512 << 20)
)

type managedPathReceipt struct {
	SchemaVersion         int    `json:"schema_version"`
	Prefix                string `json:"prefix"`
	Path                  string `json:"path"`
	PathAdded             bool   `json:"path_added"`
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
	base := os.Getenv("LOCALAPPDATA")
	if base == "" || !filepath.IsAbs(base) {
		return "", c.Fail(c.InvalidArgument, "per-user install location unavailable; provide --prefix")
	}
	return filepath.Join(base, "ViberApps", "managed"), nil
}

func managedPathRoot(create bool) (*os.Root, string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" || !filepath.IsAbs(base) {
		return nil, "", c.Fail(c.UnsupportedCapability, "Windows per-user PATH integration requires LOCALAPPDATA")
	}
	directory := filepath.Join(base, "ViberApps", managedPathDirectory)
	if info, err := os.Lstat(directory); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, "", c.Fail(c.PolicyDenied, "managed PATH directory is not a private directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	} else if !create {
		return nil, directory, os.ErrNotExist
	} else {
		if err = os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
			return nil, "", err
		}
		if err = os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
		info, statErr := os.Lstat(directory)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, "", c.Fail(c.PolicyDenied, "managed PATH directory changed during creation")
		}
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, "", err
	}
	return root, directory, nil
}

func checkManagedPathOperation(prefix, action string, requested bool) error {
	if action == "uninstall" {
		executable, err := os.Executable()
		if err != nil {
			return c.Fail(c.PolicyDenied, "cannot prove the running executable is outside the managed version inventory")
		}
		managed, err := isManagedVersionBinary(executable, prefix)
		if err != nil {
			return c.Fail(c.PolicyDenied, "cannot resolve the running executable and install prefix before uninstall")
		}
		if managed {
			return c.Fail(c.PolicyDenied, "run uninstall from the verified portable bootstrap; Windows cannot retire the executable currently running")
		}
	}
	directory := managedPathDirNoError()
	if directory == "" {
		if requested {
			return c.Fail(c.UnsupportedCapability, "Windows per-user PATH integration requires LOCALAPPDATA")
		}
		return nil
	}
	if requested {
		abs, err := canonicalPrefix(prefix)
		if err != nil {
			return err
		}
		if !fileguard.Disjoint(abs, directory) {
			return c.Fail(c.PolicyDenied, "install prefix overlaps the per-user launcher directory")
		}
		// Validate the eventual launcher target before package activation mutates
		// the installed version selection.
		if err = validateCommandPath(filepath.Join(abs, "versions", strings.Repeat("0", 40), "viber.exe")); err != nil {
			return err
		}
		if err = validateCommandPath(filepath.Join(directory, managedPathLauncher)); err != nil {
			return err
		}
		pathValue, _, _, pathErr := readUserPath()
		if pathErr != nil {
			return pathErr
		}
		contains, pathErr := userPathContains(directory)
		if pathErr != nil {
			return pathErr
		}
		if !contains {
			updated := pathValue
			if updated != "" && !strings.HasSuffix(updated, ";") {
				updated += ";"
			}
			updated += directory
			if chars, _ := syscall.UTF16FromString(updated); len(chars) > 32767 {
				return c.Fail(c.BudgetLimitReached, "user PATH is too long to append the Viber directory")
			}
		}
		if pathErr := ensureNoExistingViberOnPath(directory); pathErr != nil {
			return pathErr
		}
	}
	root, _, err := managedPathRoot(requested)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	entries, err := pathRootEntries(root)
	if err != nil {
		return err
	}
	receipt, found, err := readManagedPathReceipt(root, directory)
	if err != nil {
		return err
	}
	if !found {
		if requested && len(entries) != 0 {
			return c.Fail(c.Conflict, "managed PATH directory contains files without a Viber ownership receipt")
		}
		if requested {
			if err = fileguard.Private(root); err != nil {
				return err
			}
			return nil
		}
		return nil
	}
	abs, err := canonicalPrefix(prefix)
	if err != nil {
		return err
	}
	if receipt.Prefix != abs || receipt.Path != directory {
		return c.Fail(c.Conflict, "managed PATH launcher belongs to a different install prefix")
	}
	if hasForeignViberCommand(entries) {
		return c.Fail(c.Conflict, "a foreign Viber command exists in the managed PATH directory")
	}
	if err = fileguard.Private(root); err != nil {
		return err
	}
	if requested {
		return nil
	}
	return nil
}

func managedPathDirNoError() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" || !filepath.IsAbs(base) {
		return ""
	}
	return filepath.Join(base, "ViberApps", managedPathDirectory)
}

func managedPathInstruction() string {
	return "Open a new terminal to use `viber` from your per-user PATH."
}

func syncManagedPath(prefix, binary string, requested bool) error {
	root, directory, err := managedPathRoot(requested)
	if errors.Is(err, os.ErrNotExist) && !requested {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	entries, err := pathRootEntries(root)
	if err != nil {
		return err
	}
	receipt, found, err := readManagedPathReceipt(root, directory)
	if err != nil {
		return err
	}
	abs, err := canonicalPrefix(prefix)
	if err != nil {
		return err
	}
	if !found {
		if !requested {
			return nil
		}
		if len(entries) != 0 {
			return c.Fail(c.Conflict, "managed PATH directory contains files without a Viber ownership receipt")
		}
		receipt = managedPathReceipt{SchemaVersion: 1, Prefix: abs, Path: directory}
	} else if receipt.Prefix != abs || receipt.Path != directory {
		return c.Fail(c.Conflict, "managed PATH launcher belongs to a different install prefix")
	}
	if hasForeignViberCommand(entries) {
		return c.Fail(c.Conflict, "a foreign Viber command exists in the managed PATH directory")
	}
	if found || requested {
		if err = fileguard.Private(root); err != nil {
			return err
		}
	}

	binaryPath, revision, binaryDigest, err := verifyActiveBinary(binary, prefix)
	if err != nil {
		return err
	}
	launcher, err := managedCommand(binaryPath)
	if err != nil {
		return err
	}
	launcherDigest := digestBytes(launcher)
	if receipt.PendingLauncherDigest != "" {
		if err = reconcileManagedLauncher(root, &receipt); err != nil {
			return err
		}
	}
	_, exists, err := currentManagedLauncher(root, receipt)
	if err != nil {
		return err
	}

	receipt.PendingRevision = revision
	receipt.PendingBinaryPath = binaryPath
	receipt.PendingBinaryDigest = binaryDigest
	receipt.PendingLauncherDigest = launcherDigest
	receipt.PendingLauncherBytes = int64(len(launcher))
	if err = publishManagedPathReceipt(root, receipt, found); err != nil {
		return err
	}
	if exists {
		if err = replacePrivateFile(root, managedPathLauncher, launcher, 0600); err != nil {
			return err
		}
	} else if err = fileguard.Publish(root, managedPathLauncher, launcher); err != nil {
		return err
	}
	applyPendingManagedLauncher(&receipt)
	if err = publishManagedPathReceipt(root, receipt, true); err != nil {
		return err
	}
	if requested {
		added, addErr := addUserPath(directory)
		if addErr != nil {
			return addErr
		}
		if added && !receipt.PathAdded {
			receipt.PathAdded = true
			if err = publishManagedPathReceipt(root, receipt, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeManagedPath(prefix string) error {
	root, directory, err := managedPathRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	receipt, found, err := readManagedPathReceipt(root, directory)
	if err != nil || !found {
		return err
	}
	abs, err := canonicalPrefix(prefix)
	if err != nil {
		return err
	}
	if receipt.Prefix != abs || receipt.Path != directory {
		return c.Fail(c.Conflict, "managed PATH launcher belongs to a different install prefix")
	}
	if receipt.PendingLauncherDigest != "" {
		if err = reconcileManagedLauncher(root, &receipt); err != nil {
			return err
		}
	}
	entries, err := pathRootEntries(root)
	if err != nil {
		return err
	}
	launcherEntry, exists := containsEntry(entries, managedPathLauncher)
	if exists {
		if !launcherEntry.Type().IsRegular() {
			return c.Fail(c.Conflict, "PATH launcher changed; preserving the launcher and PATH entry")
		}
		launcherRaw, readErr := fileguard.ReadRegular(root, managedPathLauncher, 16<<10)
		current := int64(len(launcherRaw)) == receipt.LauncherBytes && digestBytes(launcherRaw) == receipt.LauncherDigest
		pending := receipt.PendingLauncherDigest != "" && int64(len(launcherRaw)) == receipt.PendingLauncherBytes && digestBytes(launcherRaw) == receipt.PendingLauncherDigest
		if readErr != nil || !current && !pending {
			return c.Fail(c.Conflict, "PATH launcher changed; preserving the launcher and PATH entry")
		}
	}
	if receipt.PathAdded && onlyManagedPathFiles(entries) {
		if err = removeUserPath(directory); err != nil {
			return err
		}
	}
	if exists {
		launcherRaw, readErr := fileguard.ReadRegular(root, managedPathLauncher, 16<<10)
		if readErr != nil {
			return readErr
		}
		if _, err = fileguard.RemoveBound(root, managedPathLauncher, digestBytes(launcherRaw), int64(len(launcherRaw)), 16<<10); err != nil {
			return err
		}
	}
	receiptRaw, err := fileguard.ReadRegular(root, managedPathReceiptFile, 16<<10)
	if err != nil {
		return err
	}
	if _, err = fileguard.RemoveBound(root, managedPathReceiptFile, digestBytes(receiptRaw), int64(len(receiptRaw)), 16<<10); err != nil {
		return err
	}
	entries, err = pathRootEntries(root)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		_ = root.Close()
		if removeErr := os.Remove(directory); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
	}
	return nil
}

func verifyActiveBinary(name, prefix string) (string, string, string, error) {
	binaryPath, err := filepath.Abs(name)
	if err != nil {
		return "", "", "", err
	}
	installPrefix, err := canonicalPrefix(prefix)
	if err != nil {
		return "", "", "", err
	}
	relative, err := filepath.Rel(installPrefix, binaryPath)
	if err != nil {
		return "", "", "", err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 3 || parts[0] != "versions" || !distribution.Revision(parts[1]) || !strings.EqualFold(parts[2], "viber.exe") {
		return "", "", "", c.Fail(c.PolicyDenied, "active binary is outside the managed version inventory")
	}
	if err = validateCommandPath(binaryPath); err != nil {
		return "", "", "", err
	}
	root, err := os.OpenRoot(filepath.Dir(binaryPath))
	if err != nil {
		return "", "", "", err
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, filepath.Base(binaryPath), managedBinaryLimit)
	if err != nil {
		return "", "", "", err
	}
	return binaryPath, parts[1], digestBytes(raw), nil
}

func managedCommand(binaryPath string) ([]byte, error) {
	if err := validateCommandPath(binaryPath); err != nil {
		return nil, err
	}
	return []byte("@echo off\r\nsetlocal DisableDelayedExpansion\r\n\"" + binaryPath + "\" %*\r\nexit /b %ERRORLEVEL%\r\n"), nil
}

func validateCommandPath(name string) error {
	for _, r := range name {
		if r > 0x7f || strings.ContainsRune("%!^&|<>()\"\r\n\t", r) {
			return c.Fail(c.UnsupportedCapability, "PATH launchers require an ASCII prefix without CMD expansion characters (% ! ^ & | < > parentheses, quotes, or line breaks)")
		}
	}
	return nil
}

func isManagedVersionBinary(executable, prefix string) (bool, error) {
	// GetModuleFileName may retain the junction used to launch the process.
	// Resolve both paths before deciding whether uninstall would retire it.
	executableAbs, err := resolvedManagedPath(executable)
	if err != nil {
		return false, err
	}
	prefixAbs, err := resolvedManagedPath(prefix)
	if err != nil {
		return false, err
	}
	versions := filepath.Join(prefixAbs, "versions")
	relative, err := filepath.Rel(versions, executableAbs)
	if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return false, err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	return len(parts) == 2 && distribution.Revision(parts[0]) && strings.EqualFold(parts[1], "viber.exe"), nil
}

func resolvedManagedPath(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buffer := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(f.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if n == 0 || n >= uint32(len(buffer)) {
		return "", c.Fail(c.UnsupportedCapability, "resolved managed path exceeds the Windows path profile")
	}
	resolved := windows.UTF16ToString(buffer[:n])
	if strings.HasPrefix(resolved, `\\?\UNC\`) {
		resolved = `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`)
	} else {
		resolved = strings.TrimPrefix(resolved, `\\?\`)
	}
	return filepath.Clean(resolved), nil
}

func readManagedPathReceipt(root *os.Root, directory string) (managedPathReceipt, bool, error) {
	var receipt managedPathReceipt
	info, err := root.Lstat(managedPathReceiptFile)
	if errors.Is(err, os.ErrNotExist) {
		return receipt, false, nil
	}
	if err != nil {
		return receipt, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<10 {
		return receipt, false, c.Fail(c.PolicyDenied, "managed PATH ownership receipt is not a regular file")
	}
	raw, err := fileguard.ReadRegular(root, managedPathReceiptFile, 16<<10)
	if err != nil || c.DecodeStrict(raw, &receipt) != nil || receipt.SchemaVersion != 1 || !filepath.IsAbs(receipt.Prefix) || receipt.Path != directory || receipt.BinaryDigest != "" && !c.ValidDigest(receipt.BinaryDigest) || receipt.LauncherDigest != "" && !c.ValidDigest(receipt.LauncherDigest) || receipt.LauncherBytes < 0 || receipt.LauncherBytes > 16<<10 || receipt.PendingBinaryDigest != "" && !c.ValidDigest(receipt.PendingBinaryDigest) || receipt.PendingLauncherDigest != "" && !c.ValidDigest(receipt.PendingLauncherDigest) || receipt.PendingLauncherBytes < 0 || receipt.PendingLauncherBytes > 16<<10 {
		return managedPathReceipt{}, false, c.Fail(c.StoreIntegrityError, "managed PATH ownership receipt invalid")
	}
	if receipt.Revision != "" && !distribution.Revision(receipt.Revision) || receipt.PendingRevision != "" && !distribution.Revision(receipt.PendingRevision) {
		return managedPathReceipt{}, false, c.Fail(c.StoreIntegrityError, "managed PATH revision receipt invalid")
	}
	canonical, err := c.CanonicalV1(receipt)
	if err != nil || string(canonical) != string(raw) {
		return managedPathReceipt{}, false, c.Fail(c.StoreIntegrityError, "managed PATH ownership receipt is not canonical")
	}
	return receipt, true, nil
}

func currentManagedLauncher(root *os.Root, receipt managedPathReceipt) ([]byte, bool, error) {
	entries, err := pathRootEntries(root)
	if err != nil {
		return nil, false, err
	}
	entry, found := containsEntry(entries, managedPathLauncher)
	if !found {
		return nil, false, nil
	}
	if !entry.Type().IsRegular() {
		return nil, false, c.Fail(c.PolicyDenied, "PATH launcher is not a regular file")
	}
	raw, err := fileguard.ReadRegular(root, managedPathLauncher, 16<<10)
	if err != nil {
		return nil, false, err
	}
	digest := digestBytes(raw)
	if receipt.PendingLauncherDigest != "" {
		if digest == receipt.PendingLauncherDigest && int64(len(raw)) == receipt.PendingLauncherBytes || digest == receipt.LauncherDigest && int64(len(raw)) == receipt.LauncherBytes {
			return raw, true, nil
		}
		return nil, false, c.Fail(c.Conflict, "PATH launcher changed during managed update")
	}
	if digest != receipt.LauncherDigest || int64(len(raw)) != receipt.LauncherBytes {
		return nil, false, c.Fail(c.Conflict, "PATH launcher changed; preserving the launcher and PATH entry")
	}
	return raw, true, nil
}

func reconcileManagedLauncher(root *os.Root, receipt *managedPathReceipt) error {
	_, exists, err := currentManagedLauncher(root, *receipt)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	raw, err := fileguard.ReadRegular(root, managedPathLauncher, 16<<10)
	if err != nil {
		return err
	}
	if digestBytes(raw) == receipt.PendingLauncherDigest && int64(len(raw)) == receipt.PendingLauncherBytes {
		applyPendingManagedLauncher(receipt)
		return publishManagedPathReceipt(root, *receipt, true)
	}
	return nil
}

func applyPendingManagedLauncher(receipt *managedPathReceipt) {
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

func publishManagedPathReceipt(root *os.Root, receipt managedPathReceipt, exists bool) error {
	raw, err := c.CanonicalV1(receipt)
	if err != nil {
		return err
	}
	if !exists {
		return fileguard.Publish(root, managedPathReceiptFile, raw)
	}
	return replacePrivateFile(root, managedPathReceiptFile, raw, 0600)
}

func replacePrivateFile(root *os.Root, name string, raw []byte, mode os.FileMode) error {
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
	from, err := windows.UTF16PtrFromString(filepath.Join(root.Name(), temporary))
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(filepath.Join(root.Name(), name))
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func pathRootEntries(root *os.Root) ([]os.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(128)
	if errors.Is(err, io.EOF) {
		return entries, nil
	}
	return entries, err
}

func containsEntry(entries []os.DirEntry, name string) (os.DirEntry, bool) {
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), name) {
			return entry, true
		}
	}
	return nil, false
}

func hasForeignViberCommand(entries []os.DirEntry) bool {
	known := map[string]bool{strings.ToLower(managedPathLauncher): true, strings.ToLower(managedPathReceiptFile): true}
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if known[name] || strings.HasPrefix(name, ".publish-") {
			continue
		}
		if strings.HasPrefix(name, "viber.") {
			return true
		}
	}
	return false
}

func onlyManagedPathFiles(entries []os.DirEntry) bool {
	if len(entries) > 2 {
		return false
	}
	for _, entry := range entries {
		if !strings.EqualFold(entry.Name(), managedPathLauncher) && !strings.EqualFold(entry.Name(), managedPathReceiptFile) {
			return false
		}
	}
	return true
}

func digestBytes(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func ensureNoExistingViberOnPath(managedDirectory string) error {
	managed := normalizeUserPathEntry(managedDirectory)
	userPath, _, exists, err := readUserPath()
	if err != nil {
		return err
	}
	entries := strings.Split(os.Getenv("PATH"), ";")
	if exists {
		entries = append(entries, strings.Split(userPath, ";")...)
	}
	extensions := strings.Split(os.Getenv("PATHEXT"), ";")
	if len(extensions) == 1 && extensions[0] == "" {
		extensions = []string{".COM", ".EXE", ".BAT", ".CMD", ".PS1"}
	}
	for _, entry := range entries {
		path := normalizeUserPathEntry(entry)
		if path == "" || path == managed {
			continue
		}
		for _, extension := range extensions {
			candidate := filepath.Join(path, "viber"+strings.TrimSpace(extension))
			if info, err := os.Lstat(candidate); err == nil && !info.IsDir() {
				return c.Fail(c.Conflict, "an existing Viber command is already on PATH; it was preserved")
			}
		}
	}
	return nil
}

func userPathContains(directory string) (bool, error) {
	path, _, exists, err := readUserPath()
	if err != nil || !exists {
		return false, err
	}
	target := normalizeUserPathEntry(directory)
	for _, entry := range strings.Split(path, ";") {
		if normalizeUserPathEntry(entry) == target {
			return true, nil
		}
	}
	return false, nil
}

func addUserPath(directory string) (bool, error) {
	key, err := openUserEnvironment(true)
	if err != nil {
		return false, err
	}
	defer key.Close()
	old, kind, exists, err := readUserPathFromKey(key)
	if err != nil {
		return false, err
	}
	if !exists {
		kind = registry.SZ
	}
	target := normalizeUserPathEntry(directory)
	for _, entry := range strings.Split(old, ";") {
		if normalizeUserPathEntry(entry) == target {
			return false, nil
		}
	}
	updated := old
	if updated != "" && !strings.HasSuffix(updated, ";") {
		updated += ";"
	}
	updated += directory
	if chars, _ := syscall.UTF16FromString(updated); len(chars) > 32767 {
		return false, c.Fail(c.BudgetLimitReached, "user PATH is too long to append the Viber directory")
	}
	if err = setUserPath(key, kind, updated); err != nil {
		return false, err
	}
	notifyUserEnvironment()
	return true, nil
}

func removeUserPath(directory string) error {
	key, err := openUserEnvironment(false)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer key.Close()
	old, kind, exists, err := readUserPathFromKey(key)
	if err != nil || !exists {
		return err
	}
	target := normalizeUserPathEntry(directory)
	entries := strings.Split(old, ";")
	for i, entry := range entries {
		if normalizeUserPathEntry(entry) == target {
			entries = append(entries[:i], entries[i+1:]...)
			if err = setUserPath(key, kind, strings.Join(entries, ";")); err != nil {
				return err
			}
			notifyUserEnvironment()
			return nil
		}
	}
	return nil
}

func readUserPath() (string, uint32, bool, error) {
	key, err := openUserEnvironment(false)
	if errors.Is(err, registry.ErrNotExist) {
		return "", registry.SZ, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	defer key.Close()
	return readUserPathFromKey(key)
}

func readUserPathFromKey(key registry.Key) (string, uint32, bool, error) {
	path, kind, err := key.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		return "", registry.SZ, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	if kind != registry.SZ && kind != registry.EXPAND_SZ {
		return "", 0, false, c.Fail(c.PolicyDenied, "user PATH has an unsupported registry type")
	}
	return path, kind, true, nil
}

func openUserEnvironment(create bool) (registry.Key, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err == nil || !create || !errors.Is(err, registry.ErrNotExist) {
		return key, err
	}
	key, _, err = registry.CreateKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	return key, err
}

func setUserPath(key registry.Key, kind uint32, value string) error {
	switch kind {
	case registry.SZ:
		return key.SetStringValue("Path", value)
	case registry.EXPAND_SZ:
		return key.SetExpandStringValue("Path", value)
	default:
		return c.Fail(c.PolicyDenied, "user PATH has an unsupported registry type")
	}
}

func normalizeUserPathEntry(entry string) string {
	entry = strings.TrimSpace(entry)
	if len(entry) >= 2 && entry[0] == '"' && entry[len(entry)-1] == '"' {
		entry = entry[1 : len(entry)-1]
	}
	if entry == "" {
		return ""
	}
	if strings.Contains(entry, "%") {
		if expanded, err := registry.ExpandString(entry); err == nil {
			entry = expanded
		}
	}
	absolute, err := filepath.Abs(entry)
	if err != nil {
		return strings.ToLower(filepath.Clean(entry))
	}
	return strings.TrimRight(strings.ToLower(filepath.Clean(absolute)), `\`)
}

func notifyUserEnvironment() {
	user32 := syscall.NewLazyDLL("user32.dll")
	procedure := user32.NewProc("SendMessageTimeoutW")
	setting, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	_, _, _ = procedure.Call(0xffff, 0x001a, 0, uintptr(unsafe.Pointer(setting)), 0x0002, 5000, 0)
}

func sameWindowsPath(first, second string) bool {
	first, err := filepath.Abs(first)
	if err != nil {
		return false
	}
	second, err = filepath.Abs(second)
	if err != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(first), filepath.Clean(second))
}
