package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/skills"
)

type SkillRegistryRef struct {
	Directory    string `json:"directory"`
	PhysicalRoot string `json:"physical_root"`
	TrustFile    string `json:"trust_file"`
	TrustDigest  string `json:"trust_digest"`
}

func (ref SkillRegistryRef) Validate() error {
	if !filepath.IsAbs(ref.Directory) || !filepath.IsAbs(ref.TrustFile) || !c.ValidDigest(ref.PhysicalRoot) || !c.ValidDigest(ref.TrustDigest) || !fileguard.Disjoint(ref.Directory, ref.TrustFile) {
		return c.Fail(c.StoreIntegrityError, "invalid operator advisory registry binding")
	}
	return nil
}
func BindSkillRegistry(directory, trustFile string) (SkillRegistryRef, error) {
	var ref SkillRegistryRef
	var err error
	ref.Directory, err = fileguard.ResolveProspective(directory)
	if err != nil {
		return ref, err
	}
	ref.TrustFile, err = fileguard.ResolveProspective(trustFile)
	if err != nil {
		return ref, err
	}
	trust, err := skills.ReadTrustPolicy(ref.TrustFile)
	if err != nil {
		return ref, err
	}
	ref.TrustDigest, err = c.Digest(trust)
	if err != nil {
		return ref, err
	}
	root, err := os.OpenRoot(ref.Directory)
	if err != nil {
		return ref, err
	}
	defer root.Close()
	ref.PhysicalRoot, err = fileguard.DirectoryIdentity(root)
	if err != nil {
		return ref, err
	}
	return ref, ref.Validate()
}
func (s *Session) executeSkillAdvice(ctx context.Context, state c.TaskState, doc Document, call model.Call) (any, error) {
	if err := policy.Admit(s.layers(state, doc), policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier, Effect: "snapshot.read"}); err != nil {
		return nil, err
	}
	var args struct {
		ID      string `json:"id,omitempty"`
		Version uint64 `json:"version,omitempty"`
	}
	if err := c.DecodeStrict(call.Arguments, &args); err != nil {
		return nil, err
	}
	if (args.ID == "") != (args.Version == 0) {
		return nil, c.Fail(c.InvalidArgument, "skill identity and exact version required together")
	}
	if doc.SkillRegistry == nil {
		return struct {
			Status   string         `json:"status"`
			Bindings []SkillBinding `json:"bindings"`
		}{"DISABLED_BY_DEFAULT", []SkillBinding{}}, nil
	}
	ref := *doc.SkillRegistry
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(ref.Directory)
	if err != nil {
		return nil, c.Fail(c.StaleAuthority, "advisory skill registry unavailable")
	}
	physical, e := fileguard.DirectoryIdentity(root)
	root.Close()
	if e != nil || physical != ref.PhysicalRoot {
		return nil, c.Fail(c.StaleAuthority, "advisory skill registry was physically replaced")
	}
	trust, err := skills.ReadTrustPolicy(ref.TrustFile)
	if err != nil {
		return nil, err
	}
	digest, err := c.Digest(trust)
	if err != nil || digest != ref.TrustDigest {
		return nil, c.Fail(c.StaleAuthority, "advisory skill trust revision changed")
	}
	bindings, err := s.BindActiveSkillsFromPolicyFile(ctx, ref.Directory, ref.TrustFile, state.TaskID)
	if err != nil {
		return nil, err
	}
	if len(bindings) > 128 {
		return nil, c.Fail(c.BudgetLimitReached, "advisory skill listing bound reached")
	}
	if args.ID == "" {
		return struct {
			Trust    string         `json:"trust"`
			Bindings []SkillBinding `json:"bindings"`
		}{"EXPLICITLY_APPROVED_ADVISORY_ONLY; NO_TOOL_OR_VERIFICATION_AUTHORITY", bindings}, nil
	}
	for _, binding := range bindings {
		if binding.ID == args.ID && binding.Version == args.Version {
			active, e := s.LoadBoundSkillAdvice(ctx, ref.Directory, ref.TrustFile, state.TaskID, binding)
			if e != nil {
				return nil, e
			}
			raw, e := json.Marshal(active)
			if e != nil {
				return nil, e
			}
			if len(raw) > 128<<10 {
				return nil, c.Fail(c.BudgetLimitReached, "complete advisory procedure exceeds output bound")
			}
			return active, nil
		}
	}
	return nil, c.Fail(c.PolicyDenied, "skill is not active in the current task scope")
}
