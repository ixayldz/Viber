//go:build linux

package tui

import (
	"golang.org/x/sys/unix"
	"os"
)

func Terminal(file *os.File) (func(), bool, error) {
	fd := int(file.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return func() {}, false, nil
	}
	next := *old
	next.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
	next.Iflag &^= unix.IXON | unix.ICRNL
	next.Cc[unix.VMIN] = 1
	next.Cc[unix.VTIME] = 0
	if err = unix.IoctlSetTermios(fd, unix.TCSETS, &next); err != nil {
		return nil, false, err
	}
	return func() { unix.IoctlSetTermios(fd, unix.TCSETS, old) }, true, nil
}
func Size() (int, int) {
	w, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 80, 24
	}
	return int(w.Col), int(w.Row)
}
