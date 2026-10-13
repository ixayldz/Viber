package cli

import "path/filepath"

func canonicalPrefix(prefix string) (string, error) {
	absolute, err := filepath.Abs(prefix)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}
