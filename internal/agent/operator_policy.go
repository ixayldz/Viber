package agent

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"time"
)

// An active inference must also stop when its operator restriction revision is
// revoked. Durable UNKNOWN accounting remains the work loop's responsibility.
func guardOperatorContext(parent context.Context, doc Document) (context.Context, func()) {
	if doc.Config == nil || doc.Config.Authority == nil {
		return parent, func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	if err := doc.Config.CurrentAuthority(); err != nil {
		cancel()
		return ctx, func() {}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(75 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if doc.Config.CurrentAuthority() != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-done }
}

func operatorLayer(state c.TaskState, doc Document) []policy.Policy {
	if doc.Config == nil || doc.Config.Authority == nil {
		return nil
	}
	authority, err := doc.Config.Authority.Current()
	if err != nil {
		return []policy.Policy{{SchemaVersion: 1, Epoch: 0, Generation: 0}}
	}
	effects := []string{"snapshot.read", "model.infer", "plan.update", "candidate.write", "check.run", "export", "context.read"}
	if authority.AllowedEffects != nil {
		effects = append([]string{}, (*authority.AllowedEffects)...)
	}
	remote := true
	if authority.Restrictions.RemoteInference != nil {
		remote = *authority.Restrictions.RemoteInference
	}
	providers := []string{"ollama", "openai", "anthropic", "chatgpt"}
	if authority.Restrictions.AllowedProviders != nil {
		providers = append([]string{}, (*authority.Restrictions.AllowedProviders)...)
	}
	return []policy.Policy{{SchemaVersion: 1, Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, Effects: effects, Paths: []string{"**"}, DeniedPaths: authority.Restrictions.SensitivePaths, RemoteInference: remote, Providers: providers}}
}

func currentOperatorEffect(doc Document, effect string) error {
	if doc.Config == nil || doc.Config.Authority == nil {
		return nil
	}
	authority, err := doc.Config.Authority.Current()
	if err != nil {
		return err
	}
	if authority.AllowedEffects == nil {
		return nil
	}
	for _, allowed := range *authority.AllowedEffects {
		if allowed == effect {
			return nil
		}
	}
	return c.Fail(c.PolicyDenied, "effect denied by current operator restrictions")
}
