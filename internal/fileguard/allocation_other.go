//go:build !linux && !darwin && !windows

package fileguard

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
)

func AllocatedBytes(*os.File) (int64, error) {
	return 0, c.Fail(c.UnsupportedCapability, "physical allocation query unavailable")
}
