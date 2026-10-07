//go:build !windows && !linux && !darwin

package fileguard

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
)

func unsupported() error {
	return c.Fail(c.UnsupportedCapability, "private artifact backend unavailable")
}
func Lock(*os.Root, string) (*os.File, error)    { return nil, unsupported() }
func Private(*os.Root) error                     { return unsupported() }
func SingleLink(*os.File) error                  { return unsupported() }
func publishName(*os.Root, string, string) error { return unsupported() }
func SyncDirectory(*os.Root, string) error       { return unsupported() }
