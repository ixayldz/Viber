//go:build windows

package delivery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unsafe"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/policy"
	"golang.org/x/sys/windows"
)

const nativeProfileV1 = "WINDOWS_FIXED_NTFS_EXCLUSIVE_HANDLES_V1"
const nativeMaxFileBytes = 64 << 20

type nativeLeaseFile struct {
	handle *os.File
	seal   NativeFileSeal
}

type nativeLeaseDirectory struct {
	handle *os.File
	seal   NativeDirectorySeal
}

type windowsNativeLease struct {
	files          map[string]nativeLeaseFile
	directories    []nativeLeaseDirectory
	root           string
	volumeIdentity string
}

func inspectNativeTarget(root string, intents []NativeFileIntent) (NativeTargetSeal, error) {
	seal := NativeTargetSeal{SchemaVersion: 1, Profile: nativeProfileV1, Directories: []NativeDirectorySeal{}, Files: []NativeFileSeal{}}
	if !filepath.IsAbs(root) || len(intents) == 0 || len(intents) > 256 {
		return seal, c.Fail(c.UnsupportedCapability, "restricted NTFS apply requires an absolute root and 1..256 existing file changes")
	}
	root = filepath.Clean(root)
	if !isDrivePath(root) {
		return seal, c.Fail(c.UnsupportedCapability, "restricted NTFS apply supports only a local drive path")
	}
	volumeRoot, err := windowsVolumePath(root)
	if err != nil || windows.GetDriveType(windows.StringToUTF16Ptr(volumeRoot)) != windows.DRIVE_FIXED {
		return seal, c.Fail(c.UnsupportedCapability, "restricted NTFS apply requires a local fixed volume")
	}
	rootHandle, err := openNativeHandle(root, nativeDirectoryAccess(), windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_DIRECTORY_FILE)
	if err != nil {
		return seal, nativeProfileError(err)
	}
	rootInfo, err := fileInfo(rootHandle)
	if err != nil || rootInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || rootInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		rootHandle.Close()
		return seal, c.Fail(c.UnsupportedCapability, "target root must be a non-reparse directory")
	}
	var fileSystem [64]uint16
	if err = windows.GetVolumeInformationByHandle(windows.Handle(rootHandle.Fd()), nil, 0, nil, nil, nil, &fileSystem[0], uint32(len(fileSystem))); err != nil || !strings.EqualFold(windows.UTF16ToString(fileSystem[:]), "NTFS") {
		rootHandle.Close()
		return seal, c.Fail(c.UnsupportedCapability, "restricted live apply requires NTFS")
	}
	volumeID, err := volumeIdentity(rootHandle)
	rootHandle.Close()
	if err != nil {
		return seal, err
	}
	seal.Root = root
	seal.VolumeIdentity = volumeID
	directoryPaths := map[string]string{}
	ancestors, err := directoryAncestors(root, volumeRoot)
	if err != nil {
		return seal, c.Fail(c.UnsupportedCapability, "target root is not beneath its fixed volume root")
	}
	for _, path := range ancestors {
		directoryPaths[windowsPathKey(path)] = path
	}
	seenFiles := map[string]bool{}
	var totalBytes int64
	for _, intent := range intents {
		if !policy.SafePath(intent.Path) || filepath.IsAbs(intent.Path) || strings.ContainsAny(intent.Path, `\\:`) || intent.Mode == 0 || len(intent.Before) > nativeMaxFileBytes || len(intent.After) > nativeMaxFileBytes || !c.ValidDigest(c.HashBytes(intent.Before)) || !c.ValidDigest(c.HashBytes(intent.After)) || c.HashBytes(intent.Before) == c.HashBytes(intent.After) {
			return seal, c.Fail(c.UnsupportedCapability, "restricted NTFS apply accepts only exact changed regular files")
		}
		totalBytes += int64(len(intent.Before)) + int64(len(intent.After))
		if totalBytes > 256<<20 {
			return seal, c.Fail(c.UnsupportedCapability, "restricted NTFS apply exceeds its bounded aggregate byte profile")
		}
		key := strings.ToLower(intent.Path)
		if seenFiles[key] {
			return seal, c.Fail(c.UnsupportedCapability, "case-alias file changes are outside restricted NTFS apply")
		}
		seenFiles[key] = true
		fullPath := filepath.Join(root, filepath.FromSlash(intent.Path))
		for directory := filepath.Dir(fullPath); withinWindowsRoot(root, directory); directory = filepath.Dir(directory) {
			directoryPaths[windowsPathKey(directory)] = directory
			if windowsPathEqual(directory, root) {
				break
			}
		}
		f, openErr := openNativeHandle(fullPath, nativeFileReadAccess(), windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_NON_DIRECTORY_FILE)
		if openErr != nil {
			return seal, nativeProfileError(openErr)
		}
		info, infoErr := fileInfo(f)
		stat, statErr := f.Stat()
		identity, identityErr := fileguard.FileIdentity(f)
		linkErr := fileguard.SingleLink(f)
		contents, readErr := readHandle(f, int64(len(intent.Before))+1)
		closeErr := f.Close()
		if err = errors.Join(infoErr, statErr, identityErr, linkErr, readErr, closeErr); err != nil {
			return seal, err
		}
		if !stat.Mode().IsRegular() || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE|windows.FILE_ATTRIBUTE_READONLY) != 0 || stat.Size() != int64(len(intent.Before)) || uint32(stat.Mode().Perm()) != intent.Mode || c.HashBytes(contents) != c.HashBytes(intent.Before) {
			return seal, c.Fail(c.StaleBase, "changed file does not match its exact regular-file precondition")
		}
		seal.Files = append(seal.Files, NativeFileSeal{Path: intent.Path, Identity: identity, BeforeDigest: c.HashBytes(intent.Before), AfterDigest: c.HashBytes(intent.After), Size: stat.Size(), AfterSize: int64(len(intent.After)), Mode: intent.Mode, Attributes: info.FileAttributes})
	}
	paths := make([]string, 0, len(directoryPaths))
	for _, path := range directoryPaths {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool {
		depthI, depthJ := strings.Count(paths[i], `\`), strings.Count(paths[j], `\`)
		if depthI != depthJ {
			return depthI < depthJ
		}
		return windowsPathKey(paths[i]) < windowsPathKey(paths[j])
	})
	for _, path := range paths {
		directory, openErr := openNativeHandle(path, nativeDirectoryAccess(), windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_DIRECTORY_FILE)
		if openErr != nil {
			return seal, nativeProfileError(openErr)
		}
		info, infoErr := fileInfo(directory)
		identity, identityErr := fileguard.FileIdentity(directory)
		closeErr := directory.Close()
		if err = errors.Join(infoErr, identityErr, closeErr); err != nil {
			return seal, err
		}
		if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return seal, c.Fail(c.UnsupportedCapability, "workspace ancestor is a reparse point or not a directory")
		}
		seal.Directories = append(seal.Directories, NativeDirectorySeal{Path: path, Identity: identity})
	}
	sort.Slice(seal.Files, func(i, j int) bool { return strings.ToLower(seal.Files[i].Path) < strings.ToLower(seal.Files[j].Path) })
	seal.TargetDigest, err = nativeTargetDigest(seal)
	if err != nil {
		return seal, err
	}
	seal.ResultDigest, err = nativeResultDigest(seal)
	if err != nil {
		return seal, err
	}
	if err = validateNativeTargetSeal(seal); err != nil {
		return seal, err
	}
	return seal, nil
}

func acquireNativeTarget(ctx context.Context, seal NativeTargetSeal, expected map[string]string) (NativeTargetLease, error) {
	if err := validateNativeTargetSeal(seal); err != nil {
		return nil, err
	}
	if !isDrivePath(seal.Root) {
		return nil, c.Fail(c.UnsupportedCapability, "restricted NTFS apply requires a local drive path")
	}
	lease := &windowsNativeLease{
		files:          map[string]nativeLeaseFile{},
		directories:    []nativeLeaseDirectory{},
		root:           seal.Root,
		volumeIdentity: seal.VolumeIdentity,
	}
	fail := func(err error) (NativeTargetLease, error) { return nil, errors.Join(err, lease.Close()) }
	volumeRoot, err := windowsVolumePath(seal.Root)
	if err != nil || windows.GetDriveType(windows.StringToUTF16Ptr(volumeRoot)) != windows.DRIVE_FIXED {
		return fail(c.Fail(c.UnsupportedCapability, "restricted NTFS apply requires a local fixed volume"))
	}
	directories := append([]NativeDirectorySeal{}, seal.Directories...)
	sort.Slice(directories, func(i, j int) bool {
		depthI, depthJ := strings.Count(directories[i].Path, `\`), strings.Count(directories[j].Path, `\`)
		if depthI != depthJ {
			return depthI < depthJ
		}
		return windowsPathKey(directories[i].Path) < windowsPathKey(directories[j].Path)
	})
	rootFound := false
	for _, directory := range directories {
		if err = ctx.Err(); err != nil {
			return fail(err)
		}
		// Directory handles deny concurrent write and delete opens while the
		// lease is live. Read sharing still permits ordinary enumeration.
		handle, openErr := openNativeHandle(directory.Path, nativeDirectoryAccess(), windows.FILE_SHARE_READ, windows.FILE_DIRECTORY_FILE)
		if openErr != nil {
			return fail(nativeProfileError(fmt.Errorf("directory handle: %w", openErr)))
		}
		info, infoErr := fileInfo(handle)
		identity, identityErr := fileguard.FileIdentity(handle)
		if err = errors.Join(infoErr, identityErr); err != nil {
			handle.Close()
			return fail(err)
		}
		// A short 8.3 component may be returned by the filesystem even when the
		// caller sealed the same directory through its long name. The handle's
		// volume/file identity plus OBJ_DONT_REPARSE pins the exact object; a
		// textual final-path comparison would reject that valid alias.
		if identity != directory.Identity || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			handle.Close()
			return fail(c.Fail(c.StaleBase, "target directory identity, path or type changed"))
		}
		if windowsPathEqual(directory.Path, seal.Root) {
			rootFound = true
			volumeID, volumeErr := volumeIdentity(handle)
			if volumeErr != nil || volumeID != seal.VolumeIdentity {
				handle.Close()
				return fail(c.Fail(c.StaleBase, "target volume identity changed"))
			}
			var fsName [64]uint16
			if volumeErr = windows.GetVolumeInformationByHandle(windows.Handle(handle.Fd()), nil, 0, nil, nil, nil, &fsName[0], uint32(len(fsName))); volumeErr != nil || !strings.EqualFold(windows.UTF16ToString(fsName[:]), "NTFS") {
				handle.Close()
				return fail(c.Fail(c.StaleBase, "target volume is no longer NTFS"))
			}
		}
		lease.directories = append(lease.directories, nativeLeaseDirectory{handle: handle, seal: directory})
	}
	if !rootFound {
		return fail(c.Fail(c.StoreIntegrityError, "target root is missing from exclusive directory seal"))
	}
	if len(expected) != len(seal.Files) {
		return fail(c.Fail(c.InvalidArgument, "expected content set does not match sealed file set"))
	}
	for _, file := range seal.Files {
		if err = ctx.Err(); err != nil {
			return fail(err)
		}
		digest, ok := expected[file.Path]
		if !ok || digest != file.BeforeDigest && digest != file.AfterDigest {
			return fail(c.Fail(c.InvalidArgument, "expected file digest is outside the sealed before/after pair"))
		}
		fullPath := filepath.Join(seal.Root, filepath.FromSlash(file.Path))
		handle, openErr := openNativeHandle(fullPath, nativeFileAccess(), 0, windows.FILE_NON_DIRECTORY_FILE)
		if openErr != nil {
			return fail(nativeProfileError(openErr))
		}
		info, infoErr := fileInfo(handle)
		stat, statErr := handle.Stat()
		identity, identityErr := fileguard.FileIdentity(handle)
		linkErr := fileguard.SingleLink(handle)
		expectedSize := file.Size
		if digest == file.AfterDigest {
			expectedSize = file.AfterSize
		}
		contents, readErr := readHandle(handle, expectedSize)
		if err = errors.Join(infoErr, statErr, identityErr, linkErr, readErr); err != nil {
			handle.Close()
			return fail(err)
		}
		if !stat.Mode().IsRegular() || identity != file.Identity || stat.Size() != expectedSize || uint32(stat.Mode().Perm()) != file.Mode || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE|windows.FILE_ATTRIBUTE_READONLY) != 0 || c.HashBytes(contents) != digest {
			handle.Close()
			return fail(c.Fail(c.StaleBase, "target file identity, permissions or content changed"))
		}
		lease.files[file.Path] = nativeLeaseFile{handle: handle, seal: file}
	}
	return lease, nil
}

func (l *windowsNativeLease) WriteFile(ctx context.Context, path, expectedDigest string, contents []byte) error {
	file, ok := l.files[path]
	if !ok || expectedDigest != file.seal.BeforeDigest && expectedDigest != file.seal.AfterDigest {
		return c.Fail(c.PolicyDenied, "write is outside the sealed before/after file pair")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := l.validateDirectories(); err != nil {
		return err
	}
	if err := validateNativeFileHandle(file); err != nil {
		return err
	}
	if int64(len(contents)) > nativeMaxFileBytes {
		return c.Fail(c.UnsupportedCapability, "native target write exceeds the bounded profile")
	}
	current, err := readHandle(file.handle, nativeMaxFileBytes)
	if err != nil {
		return err
	}
	if c.HashBytes(current) != expectedDigest {
		return c.Fail(c.StaleBase, "exclusive target file no longer matches the expected digest")
	}
	newDigest := c.HashBytes(contents)
	if newDigest != file.seal.BeforeDigest && newDigest != file.seal.AfterDigest {
		return c.Fail(c.PolicyDenied, "write bytes are outside the sealed before/after pair")
	}
	if _, err = file.handle.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err = file.handle.Truncate(0); err != nil {
		return err
	}
	for offset := 0; offset < len(contents); {
		if err = ctx.Err(); err != nil {
			return err
		}
		n, writeErr := file.handle.Write(contents[offset:])
		if writeErr != nil {
			return writeErr
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		offset += n
	}
	if err = file.handle.Sync(); err != nil {
		return err
	}
	if err = l.validateDirectories(); err != nil {
		return err
	}
	readback, err := readHandle(file.handle, nativeMaxFileBytes)
	if err != nil {
		return err
	}
	if c.HashBytes(readback) != newDigest {
		return c.Fail(c.StoreIntegrityError, "synced target bytes do not match the requested sealed result")
	}
	if err = validateNativeFileHandle(file); err != nil {
		return err
	}
	return nil
}

func (l *windowsNativeLease) ReadFile(path string, limit int64) ([]byte, error) {
	file, ok := l.files[path]
	if !ok || limit < 0 {
		return nil, c.Fail(c.PolicyDenied, "read is outside the sealed target files")
	}
	if err := l.validateDirectories(); err != nil {
		return nil, err
	}
	if err := validateNativeFileHandle(file); err != nil {
		return nil, err
	}
	if limit > nativeMaxFileBytes {
		limit = nativeMaxFileBytes
	}
	return readHandle(file.handle, limit)
}

func (l *windowsNativeLease) Close() error {
	var err error
	for path, file := range l.files {
		err = errors.Join(err, file.handle.Close())
		delete(l.files, path)
	}
	for i, directory := range l.directories {
		err = errors.Join(err, directory.handle.Close())
		l.directories[i].handle = nil
	}
	l.directories = nil
	return err
}

func (l *windowsNativeLease) validateDirectories() error {
	for _, directory := range l.directories {
		info, err := fileInfo(directory.handle)
		if err != nil {
			return err
		}
		identity, err := fileguard.FileIdentity(directory.handle)
		if err != nil {
			return err
		}
		if identity != directory.seal.Identity || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return c.Fail(c.StaleBase, "target directory identity or type changed during the operation")
		}
		if windowsPathEqual(directory.seal.Path, l.root) {
			volumeID, volumeErr := volumeIdentity(directory.handle)
			if volumeErr != nil || volumeID != l.volumeIdentity {
				return c.Fail(c.StaleBase, "target volume identity changed during the operation")
			}
		}
	}
	return nil
}

func validateNativeFileHandle(file nativeLeaseFile) error {
	info, err := fileInfo(file.handle)
	if err != nil {
		return err
	}
	stat, err := file.handle.Stat()
	if err != nil {
		return err
	}
	identity, err := fileguard.FileIdentity(file.handle)
	if err != nil {
		return err
	}
	if err = fileguard.SingleLink(file.handle); err != nil {
		return err
	}
	if !stat.Mode().IsRegular() || identity != file.seal.Identity || uint32(stat.Mode().Perm()) != file.seal.Mode || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE|windows.FILE_ATTRIBUTE_READONLY) != 0 {
		return c.Fail(c.StaleBase, "target file identity, permissions or type changed during the operation")
	}
	return nil
}

func nativeDirectoryAccess() uint32 {
	return windows.FILE_LIST_DIRECTORY | windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE
}
func nativeFileReadAccess() uint32 {
	return windows.FILE_READ_DATA | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE
}
func nativeFileAccess() uint32 {
	return windows.FILE_READ_DATA | windows.FILE_WRITE_DATA | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE
}

func openNativeHandle(path string, access uint32, share uint32, options uint32) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, c.Fail(c.InvalidArgument, "absolute native path required")
	}
	// Resolve the DOS drive alias once for this open. OBJ_DONT_REPARSE also
	// rejects object-manager symlinks, including \??\C:, so using that alias
	// would reject every normal drive path before filesystem validation.
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return nil, errNativeTargetUnsupported
	}
	var deviceBuffer [4096]uint16
	if _, err := windows.QueryDosDevice(windows.StringToUTF16Ptr(volume), &deviceBuffer[0], uint32(len(deviceBuffer))); err != nil {
		return nil, err
	}
	device := windows.UTF16ToString(deviceBuffer[:])
	if !strings.HasPrefix(device, `\Device\HarddiskVolume`) || strings.ContainsAny(device, "/\x00") {
		return nil, errNativeTargetUnsupported
	}
	ntPath := device + filepath.Clean(path)[len(volume):]
	name, err := windows.NewNTUnicodeString(ntPath)
	if err != nil {
		return nil, err
	}
	attributes := uint32(windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE)
	object := windows.OBJECT_ATTRIBUTES{Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), ObjectName: name, Attributes: attributes}
	status := windows.IO_STATUS_BLOCK{}
	var handle windows.Handle
	if err = windows.NtCreateFile(&handle, access, &object, &status, nil, windows.FILE_ATTRIBUTE_NORMAL, share, windows.FILE_OPEN, options|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0); err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func fileInfo(file *os.File) (windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info)
	return info, err
}

func readHandle(file *os.File, limit int64) ([]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, c.Fail(c.UnsupportedCapability, "native target file exceeds the bounded profile")
	}
	return raw, nil
}

func windowsVolumePath(path string) (string, error) {
	path16 := windows.StringToUTF16Ptr(path)
	var buffer [windows.MAX_PATH]uint16
	if err := windows.GetVolumePathName(path16, &buffer[0], uint32(len(buffer))); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buffer[:]), nil
}

func volumeIdentity(file *os.File) (string, error) {
	info, err := fileInfo(file)
	if err != nil {
		return "", err
	}
	return c.HashBytes([]byte(fmt.Sprintf("WINDOWS_NTFS_VOLUME_V1:%d", info.VolumeSerialNumber))), nil
}

func directoryAncestors(root, volumeRoot string) ([]string, error) {
	relative, err := filepath.Rel(volumeRoot, root)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, errNativeTargetUnsupported
	}
	result := []string{filepath.Clean(volumeRoot)}
	if relative == "." {
		return result, nil
	}
	current := filepath.Clean(volumeRoot)
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return nil, errNativeTargetUnsupported
		}
		current = filepath.Join(current, part)
		result = append(result, current)
	}
	return result, nil
}

func finalNativePath(file *os.File) (string, error) {
	buffer := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if n >= uint32(len(buffer)) {
		return "", c.Fail(c.UnsupportedCapability, "final target path exceeds native path limit")
	}
	return windows.UTF16ToString(buffer[:n]), nil
}

func windowsPathKey(path string) string {
	path = strings.TrimPrefix(filepath.Clean(path), `\??\`)
	path = strings.TrimPrefix(path, `\\?\`)
	return strings.ToLower(strings.TrimRight(path, `\`))
}

func windowsPathEqual(a, b string) bool { return windowsPathKey(a) == windowsPathKey(b) }

func isDrivePath(path string) bool {
	volume := filepath.VolumeName(path)
	return len(volume) == 2 && volume[1] == ':' && path[0] != '\\'
}

func withinWindowsRoot(root, directory string) bool {
	relative, err := filepath.Rel(root, directory)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func nativeProfileError(err error) error {
	if err == nil {
		return nil
	}
	return c.Fail(c.UnsupportedCapability, "exclusive NTFS handle acquisition failed; no target write is safe: "+err.Error())
}
