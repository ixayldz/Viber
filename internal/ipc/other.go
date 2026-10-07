//go:build !linux && !darwin && !windows

package ipc

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"net"
	"os"
)

func listen(*os.Root, *Info) (net.Listener, error) {
	return nil, c.Fail(c.UnsupportedCapability, "local peer-auth IPC unsupported")
}
func dial(context.Context, Info) (net.Conn, error) {
	return nil, c.Fail(c.UnsupportedCapability, "local peer-auth IPC unsupported")
}
func validateAddress(Info) error {
	return c.Fail(c.UnsupportedCapability, "local peer-auth IPC unsupported")
}
func authenticate(net.Conn, Info, bool) error {
	return c.Fail(c.UnsupportedCapability, "local peer-auth IPC unsupported")
}
