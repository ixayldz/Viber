//go:build linux

package ipc

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"golang.org/x/sys/unix"
	"net"
	"os"
)

func authenticate(conn net.Conn, info Info, server bool) error {
	socket, ok := conn.(*net.UnixConn)
	if !ok {
		return c.Fail(c.PolicyDenied, "Unix peer required")
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var credentialErr error
	if err = raw.Control(func(fd uintptr) {
		cred, credentialErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credentialErr != nil {
		return credentialErr
	}
	if cred.Uid != uint32(os.Geteuid()) || !server && int(cred.Pid) != info.PID {
		return c.Fail(c.PolicyDenied, "owner IPC peer identity mismatch")
	}
	return nil
}
