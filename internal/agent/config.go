package agent

import (
	"github.com/ixayldz/Viber/internal/config"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/workspace"
)

func configuredCaptureLimits(resolved *config.Resolved) workspace.Limits {
	limits := workspace.DefaultLimits()
	if resolved != nil {
		limits.SensitivePaths = resolved.SensitivePaths()
	}
	return limits
}
func taskSensitivePaths(doc Document) []string {
	paths := []string{"viber.config.json"}
	if doc.Config != nil {
		paths = append(paths, doc.Config.SensitivePaths()...)
	}
	return paths
}
func validateTaskConfig(doc Document) error {
	if doc.Config == nil {
		return nil
	}
	if err := doc.Config.Validate(); err != nil {
		return err
	}
	if doc.Runtime != nil {
		if err := doc.Config.AdmitProvider(doc.Runtime.Provider, doc.Runtime.Provider != "ollama"); err != nil {
			return c.Fail(c.StoreIntegrityError, "runtime exceeds retained config restrictions")
		}
	}
	return nil
}
