//go:build !windows && !linux && !darwin

package cli

import (
	"errors"
	"os"
	"path/filepath"
)

func defaultManagedPrefix() (string, error) {
	if data := os.Getenv("XDG_DATA_HOME"); data != "" && filepath.IsAbs(data) {
		return filepath.Join(data, "viber", "program"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("per-user install location unavailable; provide --prefix")
	}
	return filepath.Join(home, ".local", "share", "viber", "program"), nil
}

func checkManagedPathOperation(_ string, _ string, requested bool) error {
	if requested {
		return errors.New("--launcher is unsupported on this operating system")
	}
	return nil
}

func syncManagedPath(_ string, _ string, requested bool) error {
	if requested {
		return errors.New("--launcher is unsupported on this operating system")
	}
	return nil
}

func removeManagedPath(string) error { return nil }

func managedPathInstruction() string { return "" }
