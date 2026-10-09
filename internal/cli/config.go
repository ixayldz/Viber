package cli

import (
	"github.com/ixayldz/Viber/internal/config"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"os"
	"path/filepath"
)

// Global preferences are outside source. Project preferences are untrusted
// and restrictions are intersected with user/CLI intent, never authority grants.
func loadRunConfig(source, userPath string) (*config.Resolved, error) {
	explicitUser := userPath != ""
	if userPath == "" {
		directory, err := os.UserConfigDir()
		if err != nil {
			return nil, c.Fail(c.InvalidArgument, "user config directory unavailable")
		}
		userPath = filepath.Join(directory, "viber", "config.json")
	}
	if !fileguard.Disjoint(source, userPath) {
		return nil, c.Fail(c.PolicyDenied, "user config must stay outside source")
	}
	readOptional := func(path string) (*config.File, error) {
		_, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, c.Fail(c.InvalidArgument, "config file unavailable")
		}
		f, err := config.Read(path)
		return &f, err
	}
	user, err := readOptional(userPath)
	if err != nil {
		return nil, err
	}
	if explicitUser && user == nil {
		return nil, c.Fail(c.InvalidArgument, "explicit user config file unavailable")
	}
	project, err := readOptional(filepath.Join(source, "viber.config.json"))
	if err != nil {
		return nil, err
	}
	if user == nil && project == nil {
		return nil, nil
	}
	result, err := config.Resolve(user, project)
	return &result, err
}
