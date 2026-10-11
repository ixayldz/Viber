package delivery

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
)

// NativeFileIntent describes one existing regular file that may be changed by
// the restricted native apply profile. Additions, deletions and mode changes
// are intentionally outside that profile.
type NativeFileIntent struct {
	Path   string
	Before []byte
	After  []byte
	Mode   uint32
}

type NativeDirectorySeal struct {
	Path     string `json:"path"`
	Identity string `json:"identity"`
}

type NativeFileSeal struct {
	Path         string `json:"path"`
	Identity     string `json:"identity"`
	BeforeDigest string `json:"before_digest"`
	AfterDigest  string `json:"after_digest"`
	Size         int64  `json:"size"`
	AfterSize    int64  `json:"after_size"`
	Mode         uint32 `json:"mode"`
	Attributes   uint32 `json:"attributes"`
}

type NativeTargetSeal struct {
	SchemaVersion  int                   `json:"schema_version"`
	Profile        string                `json:"profile"`
	Root           string                `json:"root"`
	VolumeIdentity string                `json:"volume_identity"`
	Directories    []NativeDirectorySeal `json:"directories"`
	Files          []NativeFileSeal      `json:"files"`
	TargetDigest   string                `json:"target_digest"`
	ResultDigest   string                `json:"result_digest"`
}

type NativeTargetLease interface {
	WriteFile(ctx context.Context, path, expectedDigest string, contents []byte) error
	ReadFile(path string, limit int64) ([]byte, error)
	Close() error
}

func InspectNativeTarget(root string, files []NativeFileIntent) (NativeTargetSeal, error) {
	return inspectNativeTarget(root, files)
}

func AcquireNativeTarget(ctx context.Context, seal NativeTargetSeal, expected map[string]string) (NativeTargetLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return acquireNativeTarget(ctx, seal, expected)
}

var errNativeTargetUnsupported = errors.New("native target profile unsupported")

type nativeFileBinding struct {
	Path         string `json:"path"`
	Identity     string `json:"identity"`
	BeforeDigest string `json:"before_digest"`
	Mode         uint32 `json:"mode"`
	Size         int64  `json:"size"`
}

func nativeTargetDigest(seal NativeTargetSeal) (string, error) {
	files := make([]nativeFileBinding, 0, len(seal.Files))
	for _, file := range seal.Files {
		files = append(files, nativeFileBinding{file.Path, file.Identity, file.BeforeDigest, file.Mode, file.Size})
	}
	return c.Digest(struct {
		Root           string                `json:"root"`
		VolumeIdentity string                `json:"volume_identity"`
		Directories    []NativeDirectorySeal `json:"directories"`
		Files          []nativeFileBinding   `json:"files"`
	}{seal.Root, seal.VolumeIdentity, seal.Directories, files})
}

func nativeResultDigest(seal NativeTargetSeal) (string, error) {
	type fileResult struct {
		Path        string `json:"path"`
		AfterDigest string `json:"after_digest"`
		AfterSize   int64  `json:"after_size"`
	}
	results := make([]fileResult, 0, len(seal.Files))
	for _, file := range seal.Files {
		results = append(results, fileResult{file.Path, file.AfterDigest, file.AfterSize})
	}
	return c.Digest(results)
}

func validateNativeTargetSeal(seal NativeTargetSeal) error {
	if seal.SchemaVersion != 1 || seal.Profile != "WINDOWS_FIXED_NTFS_EXCLUSIVE_HANDLES_V1" || !filepath.IsAbs(seal.Root) || filepath.Clean(seal.Root) != seal.Root || !c.ValidDigest(seal.VolumeIdentity) || len(seal.Directories) == 0 || len(seal.Directories) > 4096 || len(seal.Files) == 0 || len(seal.Files) > 256 || !c.ValidDigest(seal.TargetDigest) || !c.ValidDigest(seal.ResultDigest) {
		return c.Fail(c.StoreIntegrityError, "invalid restricted native target seal")
	}
	seenDirectories := map[string]bool{}
	for _, directory := range seal.Directories {
		clean := filepath.Clean(directory.Path)
		key := strings.ToLower(clean)
		if !filepath.IsAbs(directory.Path) || filepath.Clean(directory.Path) != directory.Path || !c.ValidDigest(directory.Identity) || seenDirectories[key] {
			return c.Fail(c.StoreIntegrityError, "invalid native directory seal")
		}
		if !nativePathContains(seal.Root, clean) && !nativePathContains(clean, seal.Root) {
			return c.Fail(c.StoreIntegrityError, "native directory seal escapes the target ancestry")
		}
		seenDirectories[key] = true
	}
	rootKey := strings.ToLower(filepath.Clean(seal.Root))
	if !seenDirectories[rootKey] {
		return c.Fail(c.StoreIntegrityError, "native directory seal omits its target root")
	}
	volumeRoot := filepath.VolumeName(seal.Root) + string(filepath.Separator)
	for current := filepath.Clean(seal.Root); ; current = filepath.Dir(current) {
		if !seenDirectories[strings.ToLower(current)] {
			return c.Fail(c.StoreIntegrityError, "native directory seal omits a target-root ancestor")
		}
		if strings.EqualFold(current, volumeRoot) {
			break
		}
		parent := filepath.Dir(current)
		if parent == current || !nativePathContains(volumeRoot, parent) {
			return c.Fail(c.StoreIntegrityError, "native target root is outside its sealed local volume")
		}
	}
	seenFiles := map[string]bool{}
	lastFile := ""
	var totalBytes int64
	for _, file := range seal.Files {
		key := strings.ToLower(file.Path)
		if !policy.SafePath(file.Path) || strings.ContainsAny(file.Path, `\\:`) || !c.ValidDigest(file.Identity) || !c.ValidDigest(file.BeforeDigest) || !c.ValidDigest(file.AfterDigest) || file.BeforeDigest == file.AfterDigest || file.Size < 0 || file.AfterSize < 0 || file.Size > 64<<20 || file.AfterSize > 64<<20 || file.Mode == 0 || seenFiles[key] || key < lastFile {
			return c.Fail(c.StoreIntegrityError, "invalid native file seal")
		}
		parent := filepath.Dir(filepath.Join(seal.Root, filepath.FromSlash(file.Path)))
		for current := parent; ; current = filepath.Dir(current) {
			if !seenDirectories[strings.ToLower(filepath.Clean(current))] {
				return c.Fail(c.StoreIntegrityError, "native directory seal omits a changed file parent")
			}
			if strings.EqualFold(current, seal.Root) {
				break
			}
			if !nativePathContains(seal.Root, current) || filepath.Dir(current) == current {
				return c.Fail(c.StoreIntegrityError, "native changed file escapes its target root")
			}
		}
		totalBytes += file.Size + file.AfterSize
		if totalBytes > 256<<20 {
			return c.Fail(c.UnsupportedCapability, "native target seal exceeds aggregate byte bounds")
		}
		seenFiles[key] = true
		lastFile = key
	}
	target, err := nativeTargetDigest(seal)
	if err != nil || target != seal.TargetDigest {
		return c.Fail(c.StoreIntegrityError, "native target seal digest mismatch")
	}
	result, err := nativeResultDigest(seal)
	if err != nil || result != seal.ResultDigest {
		return c.Fail(c.StoreIntegrityError, "native result seal digest mismatch")
	}
	return nil
}

func nativePathContains(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
