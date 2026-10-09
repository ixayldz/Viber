package runner

import (
	"context"
	"encoding/hex"

	"strconv"

	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
)

// Lease identifies one durable, admitted native operation. DeadlineMillis is
// relative to an in-process monotonic clock domain, never a persisted wall clock.
type Lease struct {
	SchemaVersion   int    `json:"schema_version"`
	InstanceID      string `json:"instance_id"`
	ID              string `json:"id"`
	TaskDigest      string `json:"task_digest"`
	OperationDigest string `json:"operation_digest"`
	Generation      int64  `json:"generation"`
	PolicyEpoch     int64  `json:"policy_epoch"`
	SpecVersion     int64  `json:"spec_version"`
	ClockDomain     string `json:"clock_domain"`
	DeadlineMillis  int64  `json:"deadline_millis"`
}

func opaqueID(s string) bool {
	raw, err := hex.DecodeString(s)
	return len(s) == 32 && err == nil && hex.EncodeToString(raw) == s
}
func (l Lease) Validate() error {
	if l.SchemaVersion != 1 || !opaqueID(l.InstanceID) || !opaqueID(l.ID) || !opaqueID(l.ClockDomain) || !c.ValidDigest(l.TaskDigest) || !c.ValidDigest(l.OperationDigest) || l.Generation < 1 || l.PolicyEpoch < 1 || l.SpecVersion < 1 || l.DeadlineMillis < 1 || l.DeadlineMillis > 1<<40 {
		return c.Fail(c.InvalidArgument, "invalid durable process lease")
	}
	return nil
}

type Ownership struct {
	Lease   Lease  `json:"lease"`
	Phase   string `json:"phase"`
	Ordinal int    `json:"ordinal"`
}

func (o Ownership) Validate() error {
	if err := o.Lease.Validate(); err != nil {
		return err
	}
	if o.Phase != "CHECK" && o.Phase != "BASELINE" && o.Phase != "CANDIDATE" || o.Ordinal < 1 || o.Ordinal > 128 {
		return c.Fail(c.InvalidArgument, "invalid owned subject position")
	}
	return nil
}

type ownershipContext struct {
	owner   Ownership
	expires time.Time
}
type ownerContextKey struct{}

func WithOwnership(ctx context.Context, owner Ownership, expires time.Time) (context.Context, error) {
	if err := owner.Validate(); err != nil {
		return nil, err
	}
	if expires.IsZero() || !expires.After(time.Now()) {
		return nil, c.Fail(c.StaleAuthority, "native monotonic lease expired")
	}
	return context.WithValue(ctx, ownerContextKey{}, ownershipContext{owner, expires}), nil
}
func activeOwnership(ctx context.Context) (*Ownership, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, ok := ctx.Value(ownerContextKey{}).(ownershipContext)
	if !ok {
		return nil, nil
	}
	if value.owner.Validate() != nil || !time.Now().Before(value.expires) {
		return nil, c.Fail(c.StaleAuthority, "native monotonic lease expired")
	}
	owner := value.owner
	return &owner, nil
}

type Capsule struct {
	SchemaVersion    int       `json:"schema_version"`
	Owner            Ownership `json:"owner"`
	CandidateDigest  string    `json:"candidate_digest"`
	ProfileDigest    string    `json:"profile_digest"`
	InvocationDigest string    `json:"invocation_digest"`
	SourceDigest     string    `json:"source_digest"`
}

func makeCapsule(owner Ownership, source string, p Profile, inv Invocation) (Capsule, error) {
	capsule := Capsule{SchemaVersion: 1, Owner: owner, CandidateDigest: inv.CandidateDigest, SourceDigest: c.HashBytes([]byte(source))}
	if owner.Validate() != nil || p.Validate() != nil || !c.ValidDigest(inv.CandidateDigest) {
		return capsule, c.Fail(c.InvalidArgument, "invalid owned dispatch")
	}
	var err error
	capsule.ProfileDigest, err = c.Digest(p)
	if err != nil {
		return capsule, err
	}
	capsule.InvocationDigest, err = c.Digest(inv)
	return capsule, err
}
func (capsule Capsule) name() string { hash, _ := c.Digest(capsule); return "viber-owned-" + hash }
func (capsule Capsule) labels() (map[string]string, error) {
	raw, err := c.CanonicalV1(capsule)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"io.viber.runtime":    "owned-offline-v1",
		"io.viber.instance":   capsule.Owner.Lease.InstanceID,
		"io.viber.lease":      capsule.Owner.Lease.ID,
		"io.viber.generation": strconv.FormatInt(capsule.Owner.Lease.Generation, 10),
		"io.viber.capsule":    string(raw),
	}, nil
}
func validateCapsule(info inspected, capsule Capsule) error {
	labels, err := capsule.labels()
	if err != nil {
		return err
	}
	if info.Name != "/"+capsule.name() {
		return c.Fail(c.PolicyDenied, "owned process name changed")
	}
	for name, value := range labels {
		if info.Config.Labels[name] != value {
			return c.Fail(c.PolicyDenied, "owned process label binding changed")
		}
	}
	return nil
}

type Target struct {
	Owner      Ownership
	Source     string
	Profile    Profile
	Invocation Invocation
}
