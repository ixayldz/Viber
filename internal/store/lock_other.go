//go:build !windows && !linux && !darwin

package store

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
)

func acquireLock(path string) (*os.File, error) {
	return nil, c.Fail(c.UnsupportedCapability, "OS store lock unsupported")
}
