package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
)

const fenceRuntime = "fenced-offline-v1"
const fenceEntrypoint = "/__viber_fence_never_execute__"

type CleanupItem struct {
	CapsuleDigest    string   `json:"capsule_digest"`
	RemovedIDs       []string `json:"removed_ids"`
	FenceContainerID string   `json:"fence_container_id"`
}
type CleanupReceipt struct {
	SchemaVersion        int           `json:"schema_version"`
	Lease                Lease         `json:"lease"`
	ReconcilerGeneration int64         `json:"reconciler_generation"`
	Items                []CleanupItem `json:"items"`
	NoLiveSubjects       bool          `json:"no_live_subjects"`
	FencesHeld           bool          `json:"fences_held"`
}

func (d *Docker) ownedIDs(ctx context.Context, lease Lease) ([]string, error) {
	raw, err := d.control(ctx, "ps", "--all", "--no-trunc", "--filter", "label=io.viber.instance="+lease.InstanceID, "--filter", "label=io.viber.lease="+lease.ID, "--format", "{{.ID}}")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(raw))
	if len(ids) > 256 {
		return nil, c.Fail(c.UnsupportedCapability, "owned process registry exceeds bound")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validContainerID(id) || seen[id] {
			return nil, c.Fail(c.StoreIntegrityError, "invalid owned process registry")
		}
		seen[id] = true
	}
	return ids, nil
}
func fenceInvocation(candidate string) Invocation {
	return Invocation{CandidateDigest: candidate, Argv: []string{fenceEntrypoint}}
}
func validateFence(info inspected, target Target, capsule Capsule) error {
	original := info.Config.Labels["io.viber.runtime"]
	if original != fenceRuntime || info.Config.Labels["io.viber.fence"] != "1" || info.State.Status != "created" || info.State.Running {
		return c.Fail(c.PolicyDenied, "process name fence was altered or started")
	}
	// Never mutate the inspected map when checking its ordinary capsule binding.
	clone := info
	clone.Config.Labels = map[string]string{}
	for key, value := range info.Config.Labels {
		clone.Config.Labels[key] = value
	}
	clone.Config.Labels["io.viber.runtime"] = "owned-offline-v1"
	if err := validateCapsule(clone, capsule); err != nil {
		return err
	}
	return validateInspect(info, target.Profile, target.Source, fenceInvocation(target.Invocation.CandidateDigest))
}
func validateSubjectOrFence(info inspected, target Target, capsule Capsule) error {
	if info.Config.Labels["io.viber.runtime"] == fenceRuntime {
		return validateFence(info, target, capsule)
	}
	if err := validateCapsule(info, capsule); err != nil {
		return err
	}
	return validateInspect(info, target.Profile, target.Source, target.Invocation)
}
func (d *Docker) inspectName(ctx context.Context, name string) (inspected, error) {
	raw, err := d.control(ctx, "inspect", "--type", "container", "--format", "{{.Id}}", name)
	if err != nil {
		return inspected{}, err
	}
	id := strings.TrimSpace(string(raw))
	if !validContainerID(id) {
		return inspected{}, c.Fail(c.StoreIntegrityError, "unbound process name")
	}
	return d.inspect(ctx, id)
}

// Reconcile keeps an unstarted engine name fence for every admitted subject.
// This prevents even a delayed create from a dead owner from resurrecting code.
// Existing full container IDs are removed before a replacement fence is held.
func (d *Docker) Reconcile(ctx context.Context, lease Lease, generation int64, targets []Target) (receipt CleanupReceipt, resultErr error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	receipt = CleanupReceipt{SchemaVersion: 1, Lease: lease, ReconcilerGeneration: generation, Items: []CleanupItem{}}
	if d.closed {
		return receipt, errors.New("Docker broker closed")
	}
	if lease.Validate() != nil || generation <= lease.Generation || len(targets) < 1 || len(targets) > 256 {
		return receipt, c.Fail(c.StaleAuthority, "cleanup requires durable subjects from an older owner generation")
	}
	if err := d.check(ctx); err != nil {
		return receipt, err
	}
	type expected struct {
		target  Target
		capsule Capsule
		initial *inspected
	}
	ordered := make([]expected, 0, len(targets))
	byName := map[string]int{}
	for _, target := range targets {
		if target.Owner.Lease != lease {
			return receipt, c.Fail(c.PolicyDenied, "foreign process target")
		}
		target, capsule, err := PrepareTarget(target)
		if err != nil {
			return receipt, err
		}
		name := "/" + capsule.name()
		if _, ok := byName[name]; ok {
			return receipt, c.Fail(c.StoreIntegrityError, "duplicate expected process")
		}
		byName[name] = len(ordered)
		ordered = append(ordered, expected{target, capsule, nil})
	}
	ids, err := d.ownedIDs(ctx, lease)
	if err != nil {
		return receipt, err
	}
	// Validate every present subject before removing or fencing any of them.
	for _, id := range ids {
		info, err := d.inspect(ctx, id)
		if err != nil {
			return receipt, err
		}
		position, ok := byName[info.Name]
		if !ok || ordered[position].initial != nil {
			return receipt, c.Fail(c.PolicyDenied, "unexpected process in owned group")
		}
		item := &ordered[position]
		if err = validateSubjectOrFence(info, item.target, item.capsule); err != nil {
			return receipt, err
		}
		item.initial = &info
	}
	for _, item := range ordered {
		digest, _ := c.Digest(item.capsule)
		proof := CleanupItem{CapsuleDigest: digest, RemovedIDs: []string{}}
		current := item.initial
		held := false
		for attempt := 0; attempt < 3; attempt++ {
			if err = ctx.Err(); err != nil {
				receipt.Items = append(receipt.Items, proof)
				return receipt, err
			}
			if current != nil {
				if err = validateSubjectOrFence(*current, item.target, item.capsule); err != nil {
					receipt.Items = append(receipt.Items, proof)
					return receipt, err
				}
				if current.Config.Labels["io.viber.runtime"] == fenceRuntime {
					proof.FenceContainerID = current.ID
					held = true
					break
				}
				_, err = d.control(ctx, "rm", "--force", current.ID)
				if err != nil {
					receipt.Items = append(receipt.Items, proof)
					return receipt, err
				}
				proof.RemovedIDs = append(proof.RemovedIDs, current.ID)
			}
			labels, _ := item.capsule.labels()
			labels["io.viber.runtime"] = fenceRuntime
			labels["io.viber.fence"] = "1"
			// create never starts a process and has no network/image-pull fallback.
			raw, createErr := d.control(ctx, createArguments(item.capsule.name(), item.target.Source, item.target.Profile, fenceInvocation(item.target.Invocation.CandidateDigest), labels)...)
			if createErr == nil {
				id := strings.TrimSpace(string(raw))
				if !validContainerID(id) {
					receipt.Items = append(receipt.Items, proof)
					return receipt, c.Fail(c.UnknownOutcome, "fence creation identity lost")
				}
				info, err := d.inspect(ctx, id)
				if err != nil {
					receipt.Items = append(receipt.Items, proof)
					return receipt, err
				}
				if err = validateFence(info, item.target, item.capsule); err != nil {
					receipt.Items = append(receipt.Items, proof)
					return receipt, err
				}
				proof.FenceContainerID = id
				held = true
				break
			}
			// Lost create response or old-owner create racing removal. Reconcile the
			// exact unique name, inspect its full identity, and retry boundedly.
			info, err := d.inspectName(ctx, item.capsule.name())
			if err != nil {
				receipt.Items = append(receipt.Items, proof)
				return receipt, errors.Join(createErr, err)
			}
			current = &info
		}
		receipt.Items = append(receipt.Items, proof)
		if !held {
			return receipt, c.Fail(c.UnknownOutcome, "old process race did not reach a held name fence")
		}
	}
	ids, err = d.ownedIDs(ctx, lease)
	if err != nil {
		return receipt, err
	}
	if len(ids) != len(ordered) {
		return receipt, c.Fail(c.UnknownOutcome, "owned registry changed after fencing")
	}
	for _, id := range ids {
		info, err := d.inspect(ctx, id)
		if err != nil {
			return receipt, err
		}
		position, ok := byName[info.Name]
		if !ok || receipt.Items[position].FenceContainerID != id {
			return receipt, c.Fail(c.UnknownOutcome, "fenced process identity changed")
		}
		if err = validateFence(info, ordered[position].target, ordered[position].capsule); err != nil {
			return receipt, err
		}
	}
	receipt.NoLiveSubjects = true
	receipt.FencesHeld = true
	return receipt, nil
}
func ValidateCleanupCapsules(receipt CleanupReceipt, lease Lease, generation int64, capsules []Capsule) error {
	if receipt.SchemaVersion != 1 || lease.Validate() != nil || receipt.Lease != lease || receipt.ReconcilerGeneration != generation || generation <= lease.Generation || !receipt.NoLiveSubjects || !receipt.FencesHeld || len(receipt.Items) != len(capsules) || len(capsules) < 1 || len(capsules) > 256 {
		return c.Fail(c.StoreIntegrityError, "invalid process fence proof")
	}
	seen := map[string]bool{}
	ids := map[string]bool{}
	for i, capsule := range capsules {
		hash, _ := c.Digest(capsule)
		proof := receipt.Items[i]
		if capsule.SchemaVersion != 1 || capsule.Owner.Validate() != nil || capsule.Owner.Lease != lease || !c.ValidDigest(capsule.SourceDigest) || !c.ValidDigest(capsule.CandidateDigest) || !c.ValidDigest(capsule.ProfileDigest) || !c.ValidDigest(capsule.InvocationDigest) || proof.CapsuleDigest != hash || seen[hash] || !validContainerID(proof.FenceContainerID) || ids[proof.FenceContainerID] || len(proof.RemovedIDs) > 3 {
			return c.Fail(c.StoreIntegrityError, fmt.Sprintf("process fence subject %d invalid", i))
		}
		seen[hash] = true
		ids[proof.FenceContainerID] = true
		for _, id := range proof.RemovedIDs {
			if !validContainerID(id) || ids[id] {
				return c.Fail(c.StoreIntegrityError, "process fence reused an engine identity")
			}
			ids[id] = true
		}
	}
	return nil
}
