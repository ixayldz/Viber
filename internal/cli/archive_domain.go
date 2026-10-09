package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// Developer-supplied authority files cannot bypass an actual owner/privacy
// domain. Standalone archives stay separate from session stores and their CAS.
func openStandaloneArchive(directory string) (*artifact.Archive, error) {
	absolute, err := fileguard.ResolveProspective(directory)
	if err != nil {
		return nil, err
	}
	probes := []string{filepath.Join(absolute, "state.sqlite")}
	if strings.EqualFold(filepath.Base(absolute), "artifacts") {
		probes = append(probes, filepath.Join(filepath.Dir(absolute), "state.sqlite"))
	}
	for _, probe := range probes {
		if _, err := os.Lstat(probe); err == nil {
			return nil, c.Fail(c.PolicyDenied, "standalone archive command cannot bypass session owner or deletion authority")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return artifact.Open(absolute)
}
