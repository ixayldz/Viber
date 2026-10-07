//go:build darwin

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
	var cred *unix.Xucred
	var credentialErr error
	var pid int
	if err = raw.Control(func(fd uintptr) {
		cred, credentialErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if credentialErr == nil && !server {
			pid, credentialErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		}
	}); err != nil {
		return err
	}
	if credentialErr != nil {
		return credentialErr
	}
	if cred.Uid != uint32(os.Geteuid()) || !server && pid != info.PID {
		return c.Fail(c.PolicyDenied, "owner IPC peer identity mismatch")
	}
	return nil
}
