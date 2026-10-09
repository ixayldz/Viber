//go:build !windows && !linux && !darwin

package tui

import "os"

func Terminal(*os.File) (func(), bool, error) { return func() {}, false, nil }
func Size() (int, int)                        { return 80, 24 }
