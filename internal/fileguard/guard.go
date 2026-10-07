// Package fileguard supplies host-side ownership and publication primitives.
// They are for trusted private stores, never an untrusted process sandbox.
package fileguard

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func Disjoint(a, b string) bool {
	a, err := ResolveProspective(a)
	if err != nil {
		return false
	}
	b, err = ResolveProspective(b)
	if err != nil {
		return false
	}
	inside := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return !inside(a, b) && !inside(b, a)
}

func ReadRegular(root *os.Root, name string, limit int64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, c.Fail(c.UnsupportedCapability, "nonregular or oversized artifact")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, c.Fail(c.Conflict, "artifact replaced during read")
	}
	if err := SingleLink(f); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, c.Fail(c.UnsupportedCapability, "artifact quota exceeded")
	}
	return raw, nil
}

// Publish writes and syncs private temporary bytes, then publishes without
// replacing an existing artifact. An interrupted temp is an unreferenced orphan.
func Publish(root *os.Root, name string, raw []byte) error {
	if err := root.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := ".publish-" + hex.EncodeToString(nonce[:])
	f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = publishName(root, temp, name); err != nil {
		return err
	}
	return SyncParents(root, filepath.Dir(name))
}

func SyncParents(root *os.Root, name string) error {
	for {
		if err := SyncDirectory(root, name); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		name = filepath.Dir(name)
	}
}
