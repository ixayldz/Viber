//go:build windows

package tui

import (
	"golang.org/x/sys/windows"
	"os"
)

func Terminal(file *os.File) (func(), bool, error) {
	input := windows.Handle(file.Fd())
	output := windows.Handle(os.Stdout.Fd())
	var oldIn, oldOut uint32
	if windows.GetConsoleMode(input, &oldIn) != nil || windows.GetConsoleMode(output, &oldOut) != nil {
		return func() {}, false, nil
	}
	if err := windows.SetConsoleMode(input, (oldIn|windows.ENABLE_VIRTUAL_TERMINAL_INPUT)&^(windows.ENABLE_LINE_INPUT|windows.ENABLE_ECHO_INPUT|windows.ENABLE_PROCESSED_INPUT)); err != nil {
		return nil, false, err
	}
	if err := windows.SetConsoleMode(output, oldOut|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		windows.SetConsoleMode(input, oldIn)
		return nil, false, err
	}
	return func() { windows.SetConsoleMode(input, oldIn); windows.SetConsoleMode(output, oldOut) }, true, nil
}
func Size() (int, int) {
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(windows.Handle(os.Stdout.Fd()), &info) != nil {
		return 80, 24
	}
	return int(info.Window.Right - info.Window.Left + 1), int(info.Window.Bottom - info.Window.Top + 1)
}
