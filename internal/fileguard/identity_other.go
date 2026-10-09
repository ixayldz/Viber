//go:build !linux && !darwin && !windows

package fileguard

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
)

func DirectoryIdentity(root *os.Root) (string, error) {
	return "", c.Fail(c.UnsupportedCapability, "physical directory identity unavailable")
}
