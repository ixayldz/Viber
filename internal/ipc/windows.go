//go:build windows

package ipc

import (
	"context"
	"net"
	"os"

	"github.com/Microsoft/go-winio"
	c "github.com/ixayldz/Viber/internal/contracts"
	"golang.org/x/sys/windows"
)

func listen(root *os.Root, info *Info) (net.Listener, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	info.Transport = "named_pipe"
	info.Address = `\\.\pipe\viber-` + info.ID
	// go-winio's first-instance listener rejects remote clients at the OS boundary.
	return winio.ListenPipe(info.Address, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;" + user.User.Sid.String() + ")", InputBufferSize: 65536, OutputBufferSize: 65536})
}
func dial(ctx context.Context, info Info) (net.Conn, error) {
	return winio.DialPipeContext(ctx, info.Address)
}
func validateAddress(info Info) error {
	if info.Transport != "named_pipe" || info.Address != `\\.\pipe\viber-`+info.ID {
		return c.Fail(c.PolicyDenied, "invalid local named-pipe endpoint")
	}
	return nil
}
func processUser(pid uint32) (*windows.SID, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err = windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}
func authenticate(conn net.Conn, info Info, server bool) error {
	file, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return c.Fail(c.PolicyDenied, "named-pipe peer required")
	}
	var pid uint32
	var err error
	if server {
		err = windows.GetNamedPipeClientProcessId(windows.Handle(file.Fd()), &pid)
	} else {
		err = windows.GetNamedPipeServerProcessId(windows.Handle(file.Fd()), &pid)
	}
	if err != nil {
		return err
	}
	peer, err := processUser(pid)
	if err != nil {
		return err
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if !peer.Equals(current.User.Sid) || !server && int(pid) != info.PID {
		return c.Fail(c.PolicyDenied, "owner IPC peer identity mismatch")
	}
	return nil
}
