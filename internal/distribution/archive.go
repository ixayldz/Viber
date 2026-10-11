// Package distribution manages explicitly verified portable program versions.
// It never migrates task stores or grants source-workspace write authority.
package distribution

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"path"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
)

const MaxArchiveBytes = 128 << 20
const MaxFiles = 256

type File struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Digest string `json:"sha256"`
}
type Manifest struct {
	SchemaVersion  int             `json:"schema_version"`
	Channel        string          `json:"channel"`
	ReleaseReady   bool            `json:"release_ready"`
	Revision       string          `json:"source_revision"`
	SourceDigest   string          `json:"source_archive_sha256"`
	Platform       string          `json:"platform"`
	GoVersion      string          `json:"build_go_version"`
	NativeSmoke    json.RawMessage `json:"native_smoke"`
	StoreFormatMin int             `json:"store_format_min,omitempty"`
	StoreFormatMax int             `json:"store_format_max,omitempty"`
	ReducerVersion int             `json:"reducer_version,omitempty"`
	Files          []File          `json:"files"`
}
type Bundle struct {
	Manifest       Manifest
	ManifestDigest string
	ArchiveDigest  string
	Files          map[string][]byte
}

func Revision(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// ParseArchive inspects all bytes before any archive member is written. Members
// must match one exact platform prefix and a complete digest-bound inventory.
func ParseArchive(raw []byte, kind, revision, platform string) (Bundle, error) {
	var result Bundle
	if !Revision(revision) || len(raw) == 0 || len(raw) > MaxArchiveBytes || platform != "windows/amd64" && platform != "linux/amd64" && platform != "darwin/arm64" {
		return result, c.Fail(c.InvalidArgument, "bounded archive and exact supported revision/platform required")
	}
	prefix := "viber-" + revision[:12] + "-" + strings.ReplaceAll(platform, "/", "-") + "/"
	files := map[string][]byte{}
	folded := map[string]bool{}
	var total int64
	add := func(name string, size int64, mode uint32, reader io.Reader) error {
		if !strings.HasPrefix(name, prefix) {
			return c.Fail(c.InvalidArgument, "archive prefix mismatch")
		}
		name = strings.TrimPrefix(name, prefix)
		if !policy.SafePath(name) || len(name) > 512 || strings.Count(name, "/") > 12 || path.Clean(name) != name || strings.Contains(name, "\\") || folded[strings.ToLower(name)] || len(files) >= MaxFiles || size < 0 || size > MaxArchiveBytes-total || mode&06000 != 0 {
			return c.Fail(c.InvalidArgument, "archive traversal/alias/type/quota denied")
		}
		data, err := io.ReadAll(io.LimitReader(reader, size+1))
		if err != nil || int64(len(data)) != size {
			return c.Fail(c.InvalidArgument, "archive member length mismatch")
		}
		files[name] = data
		folded[strings.ToLower(name)] = true
		total += size
		return nil
	}
	switch kind {
	case "zip":
		z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			return result, c.Fail(c.InvalidArgument, "invalid ZIP")
		}
		for _, entry := range z.File {
			if !entry.Mode().IsRegular() || entry.UncompressedSize64 > MaxArchiveBytes {
				return result, c.Fail(c.InvalidArgument, "archive nonregular entry denied")
			}
			reader, err := entry.Open()
			if err != nil {
				return result, err
			}
			err = add(entry.Name, int64(entry.UncompressedSize64), uint32(entry.Mode().Perm()), reader)
			closeErr := reader.Close()
			if err != nil {
				return result, err
			}
			if closeErr != nil {
				return result, closeErr
			}
		}
	case "tar.gz":
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return result, c.Fail(c.InvalidArgument, "invalid gzip")
		}
		defer gz.Close()
		// The decompressed envelope is bounded too, including metadata/padding.
		tr := tar.NewReader(io.LimitReader(gz, MaxArchiveBytes+MaxFiles*4096+1))
		for {
			entry, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return result, c.Fail(c.InvalidArgument, "invalid TAR")
			}
			for key, value := range entry.PAXRecords {
				if key != "path" || value != entry.Name {
					return result, c.Fail(c.InvalidArgument, "unsupported archive extended metadata")
				}
			}
			if entry.Typeflag != tar.TypeReg || entry.Linkname != "" {
				return result, c.Fail(c.InvalidArgument, "archive links/extended entries denied")
			}
			if err = add(entry.Name, entry.Size, uint32(entry.Mode), tr); err != nil {
				return result, err
			}
		}
	default:
		return result, c.Fail(c.InvalidArgument, "supported archive is ZIP or tar.gz")
	}
	manifestRaw, ok := files["package.json"]
	if !ok || len(manifestRaw) > 1<<20 || c.DecodeStrict(manifestRaw, &result.Manifest) != nil {
		return Bundle{}, c.Fail(c.InvalidArgument, "package manifest invalid")
	}
	m := result.Manifest
	if m.SchemaVersion != 1 || m.Channel != "engineering" || m.ReleaseReady || m.Revision != revision || m.Platform != platform || !c.ValidDigest(m.SourceDigest) || len(m.Files) < 4 || len(m.Files)+1 != len(files) {
		return Bundle{}, c.Fail(c.InvalidArgument, "package identity/inventory mismatch")
	}
	seen := map[string]bool{}
	for _, entry := range m.Files {
		data, exists := files[entry.Path]
		if !exists || entry.Path == "package.json" || seen[entry.Path] || entry.Bytes != int64(len(data)) || !c.ValidDigest(entry.Digest) || c.HashBytes(data) != entry.Digest {
			return Bundle{}, c.Fail(c.InvalidArgument, "package bytes do not match inventory")
		}
		seen[entry.Path] = true
	}
	binary := "viber"
	if strings.HasPrefix(platform, "windows/") {
		binary += ".exe"
	}
	if !seen[binary] || !seen["LICENSE"] || !seen["sbom.cdx.json"] || !seen["README.md"] {
		return Bundle{}, c.Fail(c.InvalidArgument, "package binary/license/SBOM/readme required")
	}
	result.ManifestDigest = c.HashBytes(manifestRaw)
	result.ArchiveDigest = c.HashBytes(raw)
	result.Files = files
	return result, nil
}
