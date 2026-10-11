package config

import (
	"os"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// Authority is an operator-managed restriction profile. Repository preferences
// cannot select or update this file, and it can only narrow a task's authority.
type Authority struct {
	SchemaVersion  int       `json:"schema_version"`
	Revision       int64     `json:"revision"`
	Restrictions   Privacy   `json:"restrictions"`
	AllowedEffects *[]string `json:"allowed_effects,omitempty"`
}
type AuthorityRef struct {
	Path    string    `json:"path"`
	Digest  string    `json:"digest"`
	Initial Authority `json:"initial"`
}

func (a Authority) Validate() error {
	if a.SchemaVersion != 1 || a.Revision < 1 || a.Revision > 1<<53 {
		return c.Fail(c.InvalidArgument, "invalid operator restriction revision")
	}
	if err := (File{SchemaVersion: 1, Restrictions: a.Restrictions}).Validate(); err != nil {
		return err
	}
	if a.AllowedEffects != nil {
		if len(*a.AllowedEffects) > 16 {
			return c.Fail(c.InvalidArgument, "operator effect bound exceeded")
		}
		seen := map[string]bool{}
		for _, effect := range *a.AllowedEffects {
			switch effect {
			case "snapshot.read", "model.infer", "plan.update", "candidate.write", "check.run", "export", "context.read":
			default:
				return c.Fail(c.InvalidArgument, "unknown operator effect")
			}
			if seen[effect] {
				return c.Fail(c.InvalidArgument, "duplicate operator effect")
			}
			seen[effect] = true
		}
	}
	return nil
}
func ReadAuthority(name string) (Authority, error) {
	var authority Authority
	if !filepath.IsAbs(name) || strings.ContainsAny(name, "\x00\r\n") {
		return authority, c.Fail(c.InvalidArgument, "absolute operator policy path required")
	}
	root, err := os.OpenRoot(filepath.Dir(name))
	if err != nil {
		return authority, c.Fail(c.PolicyDenied, "operator restriction file unavailable")
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, filepath.Base(name), 64<<10)
	if err != nil || c.DecodeStrict(raw, &authority) != nil {
		return authority, c.Fail(c.PolicyDenied, "operator restriction file invalid; raw contents omitted")
	}
	return authority, authority.Validate()
}
func BindAuthority(name, source string) (AuthorityRef, error) {
	var reference AuthorityRef
	if !fileguard.Disjoint(name, source) {
		return reference, c.Fail(c.PolicyDenied, "operator restrictions must stay outside project source")
	}
	abs, err := filepath.Abs(name)
	if err != nil {
		return reference, err
	}
	authority, err := ReadAuthority(abs)
	if err != nil {
		return reference, err
	}
	digest, err := c.Digest(authority)
	if err != nil {
		return reference, err
	}
	return AuthorityRef{Path: abs, Digest: digest, Initial: authority}, nil
}
func (r AuthorityRef) Validate() error {
	if !filepath.IsAbs(r.Path) || !c.ValidDigest(r.Digest) {
		return c.Fail(c.StoreIntegrityError, "invalid retained operator authority")
	}
	if err := r.Initial.Validate(); err != nil {
		return err
	}
	digest, err := c.Digest(r.Initial)
	if err != nil || digest != r.Digest {
		return c.Fail(c.StoreIntegrityError, "retained operator authority digest changed")
	}
	return nil
}

// Current rejects revision regression, same-revision mutation, removal and any
// unacknowledged update. A new task must explicitly bind a new revision.
func (r AuthorityRef) Current() (Authority, error) {
	if err := r.Validate(); err != nil {
		return Authority{}, err
	}
	current, err := ReadAuthority(r.Path)
	if err != nil {
		return current, err
	}
	digest, err := c.Digest(current)
	if err != nil {
		return current, err
	}
	if current.Revision != r.Initial.Revision || digest != r.Digest {
		return current, c.Fail(c.StaleAuthority, "operator restriction revision changed; pause and create a new bound task")
	}
	return current, nil
}
func (r Resolved) CurrentAuthority() error {
	if r.Authority == nil {
		return nil
	}
	_, err := r.Authority.Current()
	return err
}

func (r Resolved) AdmitEffect(effect string) error {
	if r.Authority == nil {
		return nil
	}
	authority, err := r.Authority.Current()
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
