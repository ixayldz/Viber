//go:build linux || darwin

package fileguard

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"golang.org/x/sys/unix"
	"os"
)

func AllocatedBytes(f *os.File) (int64, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return 0, err
	}
	if st.Blocks < 0 || st.Blocks > 1<<52 {
		return 0, c.Fail(c.UnsupportedCapability, "invalid physical allocation count")
	}
	return st.Blocks * 512, nil
}
