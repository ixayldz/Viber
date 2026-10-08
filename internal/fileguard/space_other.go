//go:build !linux && !darwin && !windows

package fileguard

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
)

func AvailableBytes(root *os.Root) (uint64, error) {
	return 0, c.Fail(c.UnsupportedCapability, "filesystem space check unavailable")
}
