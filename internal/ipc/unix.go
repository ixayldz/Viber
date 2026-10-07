//go:build linux || darwin

package ipc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type privateListener struct {
	*net.UnixListener
	directory string
	once      sync.Once
	err       error
}

func (l *privateListener) Close() error {
	l.once.Do(func() { l.err = errors.Join(l.UnixListener.Close(), os.Remove(l.directory)) })
	return l.err
}
func listen(root *os.Root, info *Info) (net.Listener, error) {
	info.Transport = "unix"
	info.Address = socketPath(root, info.ID)
	temporary := ""
	if len(info.Address) > 100 {
		// macOS's standard test/user directory can exceed sun_path even when the
		// store is valid. A fresh mode-0700 local endpoint directory remains private;
		// the protected descriptor and kernel peer PID bind it to this owner.
		var err error
		temporary, err = os.MkdirTemp("", "vi-")
		if err != nil {
			return nil, err
		}
		info.Address = filepath.Join(temporary, "o-"+info.ID[:12]+".sock")
	}
	if len(info.Address) > 100 {
		if temporary != "" {
			_ = os.Remove(temporary)
		}
		return nil, c.Fail(c.UnsupportedCapability, "private temporary root exceeds portable Unix socket address limit")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: info.Address, Net: "unix"})
	if err != nil {
		if temporary != "" {
			_ = os.Remove(temporary)
		}
		return nil, err
	}
	listener.SetUnlinkOnClose(true)
	if err = os.Chmod(info.Address, 0600); err != nil {
		listener.Close()
		if temporary != "" {
			_ = os.Remove(temporary)
		}
		return nil, err
	}
	if temporary != "" {
		return &privateListener{UnixListener: listener, directory: temporary}, nil
	}
	return listener, nil
}
func dial(ctx context.Context, info Info) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", info.Address)
}
func validateAddress(info Info) error {
	if info.Transport != "unix" || !filepath.IsAbs(info.Address) || filepath.Base(info.Address) != "o-"+info.ID[:12]+".sock" || strings.ContainsAny(info.Address, "\r\n") {
		return c.Fail(c.PolicyDenied, "invalid local Unix owner endpoint")
	}
	return nil
}
