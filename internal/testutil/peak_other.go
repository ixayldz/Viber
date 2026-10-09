//go:build !windows && !linux && !darwin

package testutil

import "errors"

func PeakResidentBytes() (int64, error) {
	return 0, errors.New("process peak resident counter unsupported")
}
