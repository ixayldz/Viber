package agent

import (
	"context"
	"errors"
	"os"

	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/runner"
)

type RuntimeInstance struct {
	SchemaVersion int    `json:"schema_version"`
	PhysicalRoot  string `json:"physical_root"`
	ID            string `json:"id"`
}

const runtimeInstanceFile = "runtime-instance.json"

// A restored store gets its own physical runtime namespace. This file is never
// part of a backup. Losing it in an existing owned history is an integrity error.
func createRuntimeInstance(directory string) (RuntimeInstance, error) {
	value := RuntimeInstance{SchemaVersion: 1, ID: newID("")}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return value, err
	}
	defer root.Close()
	value.PhysicalRoot, err = fileguard.DirectoryIdentity(root)
	if err != nil {
		return value, err
	}
	raw, err := c.CanonicalV1(value)
	if err != nil {
		return value, err
	}
	return value, fileguard.Publish(root, runtimeInstanceFile, raw)
}
func openRuntimeInstance(ctx context.Context, s *Session) (RuntimeInstance, error) {
	var value RuntimeInstance
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return value, err
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, runtimeInstanceFile, 1024)
	if err == nil {
		physicalRoot, identityErr := fileguard.DirectoryIdentity(root)
		if identityErr != nil {
			return value, identityErr
		}
		if c.DecodeStrict(raw, &value) != nil || value.SchemaVersion != 1 || len(value.ID) != 32 || value.PhysicalRoot != physicalRoot {
			return value, c.Fail(c.StoreIntegrityError, "invalid physical runtime instance")
		}
		probe := runner.Lease{SchemaVersion: 1, InstanceID: value.ID, ID: value.ID, ClockDomain: value.ID, TaskDigest: c.HashBytes(nil), OperationDigest: c.HashBytes(nil), Generation: 1, PolicyEpoch: 1, SpecVersion: 1, DeadlineMillis: 1}
		if probe.Validate() != nil {
			return value, c.Fail(c.StoreIntegrityError, "invalid physical runtime instance")
		}
		return value, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return value, err
	}
	refs, err := s.Journal.DocumentReferences(ctx)
	if err != nil {
		return value, err
	}
	for _, ref := range refs {
		raw, err := s.Archive.GetBytes(ref.TaskID, ref.Digest)
		if err != nil {
			return value, err
		}
		var doc Document
		if err = c.DecodeStrict(raw, &doc); err != nil {
			return value, err
		}
		if doc.Pending != nil && doc.Pending.NativeLease != nil {
			return value, c.Fail(c.StoreIntegrityError, "runtime instance missing from owned native history; restore must use a fresh explicit destination")
		}
	}
	return createRuntimeInstance(s.directory)
}
func pendingCheck(doc Document) (model.Call, error) {
	if doc.Pending == nil || doc.Pending.Kind != "NATIVE_TOOL" || len(doc.Messages) == 0 || doc.ToolCursor < 0 {
		return model.Call{}, c.Fail(c.StoreIntegrityError, "native intent has no retained assistant call")
	}
	message := doc.Messages[len(doc.Messages)-1]
	if message.Role != "assistant" || doc.ToolCursor >= int64(len(message.Calls)) {
		return model.Call{}, c.Fail(c.StoreIntegrityError, "native call cursor invalid")
	}
	call := message.Calls[doc.ToolCursor]
	if call.Name != "check_run" || call.ID != doc.Pending.ID || c.HashBytes(call.Arguments) != doc.Pending.ArgumentsDigest {
		return model.Call{}, c.Fail(c.StoreIntegrityError, "owned native call differs from durable intent")
	}
	return call, nil
}
func ownedCheckTargets(s *Session, doc Document, call model.Call, lease runner.Lease) ([]runner.Target, []runner.Capsule, error) {
	var query struct {
		CheckID string `json:"check_id"`
	}
	if c.DecodeStrict(call.Arguments, &query) != nil || doc.CheckRuntime == nil || doc.Protection == nil {
		return nil, nil, c.Fail(c.PolicyDenied, "owned process requires a registered check")
	}
	var argv []string
	for _, check := range doc.Protection.Plan.Checks {
		if check.ID == query.CheckID {
			argv = check.Argv
		}
	}
	if argv == nil {
		return nil, nil, c.Fail(c.PolicyDenied, "owned check is not registered")
	}
	current, err := s.Archive.Materialize(doc.Candidate)
	if err != nil {
		return nil, nil, err
	}
	targets := []runner.Target{}
	capsules := []runner.Capsule{}
	add := func(phase string, ordinal int, source string, inv runner.Invocation) error {
		target, capsule, err := runner.PrepareTarget(runner.Target{Owner: runner.Ownership{Lease: lease, Phase: phase, Ordinal: ordinal}, Source: source, Profile: doc.CheckRuntime.Profile, Invocation: inv})
		if err == nil {
			targets = append(targets, target)
			capsules = append(capsules, capsule)
		}
		return err
	}
	if suite, _, ok := observerSuite(doc, query.CheckID); ok {
		baseline, err := s.Archive.Materialize(doc.Baseline)
		if err != nil {
			return nil, nil, err
		}
		for _, subject := range []struct{ phase, source, candidate string }{{"BASELINE", baseline.SourceDirectory, doc.Baseline.SnapshotDigest}, {"CANDIDATE", current.SourceDirectory, doc.Candidate.SnapshotDigest}} {
			ordinal := 0
			for _, item := range suite.Cases {
				for repeat := 1; repeat <= suite.Repeats; repeat++ {
					ordinal++
					input := append([]byte{}, item.Input...)
					if err = add(subject.phase, ordinal, subject.source, runner.Invocation{CandidateDigest: subject.candidate, Argv: argv, Stdin: &input}); err != nil {
						return nil, nil, err
					}
				}
			}
		}
	} else {
		if err = add("CHECK", 1, current.SourceDirectory, runner.Invocation{CandidateDigest: doc.Candidate.SnapshotDigest, Argv: argv}); err != nil {
			return nil, nil, err
		}
	}
	return targets, capsules, nil
}
func (s *Session) prepareNativeLease(state c.TaskState, doc *Document, call model.Call) error {
	remaining := doc.Budget.MaxActiveMillis - doc.Budget.ActiveMillis
	if remaining < 1 {
		return c.Fail(c.BudgetLimitReached, "native task deadline exhausted")
	}
	lease := runner.Lease{SchemaVersion: 1, InstanceID: s.instance.ID, ID: newID(""), TaskDigest: c.HashBytes([]byte(doc.TaskID)), OperationDigest: c.HashBytes([]byte(nativeResourceID(*doc))), Generation: state.KernelGeneration, PolicyEpoch: state.PolicyEpoch, SpecVersion: doc.Spec.Version, ClockDomain: s.clockDomain, DeadlineMillis: time.Since(s.clockOrigin).Milliseconds() + remaining}
	_, capsules, err := ownedCheckTargets(s, *doc, call, lease)
	if err != nil {
		return err
	}
	doc.Pending.NativeLease = &lease
	doc.Pending.NativeSubjects = capsules
	return nil
}
func validateNativeLease(doc Document) error {
	if doc.Pending == nil {
		return nil
	}
	p := doc.Pending
	if p.NativeLease == nil {
		if len(p.NativeSubjects) != 0 {
			return c.Fail(c.StoreIntegrityError, "subject capsules without native lease")
		}
		return nil // Older retained intents stay UNKNOWN; they cannot be released by cleanup.
	}
	l := *p.NativeLease
	if p.Kind != "NATIVE_TOOL" || l.Validate() != nil || l.TaskDigest != c.HashBytes([]byte(doc.TaskID)) || l.OperationDigest != c.HashBytes([]byte(nativeResourceID(doc))) || l.Generation != p.Generation || l.PolicyEpoch != p.Epoch || l.SpecVersion != doc.Spec.Version || len(p.NativeSubjects) < 1 || len(p.NativeSubjects) > 256 {
		return c.Fail(c.StoreIntegrityError, "native lease intent binding invalid")
	}
	call, err := pendingCheck(doc)
	if err != nil {
		return err
	}
	var query struct {
		CheckID string `json:"check_id"`
	}
	if c.DecodeStrict(call.Arguments, &query) != nil {
		return c.Fail(c.StoreIntegrityError, "native check query invalid")
	}
	var argv []string
	if doc.Protection != nil {
		for _, check := range doc.Protection.Plan.Checks {
			if check.ID == query.CheckID {
				argv = check.Argv
			}
		}
	}
	if argv == nil || doc.CheckRuntime == nil {
		return c.Fail(c.StoreIntegrityError, "native check authority missing")
	}
	expected := []struct {
		phase     string
		candidate string
		input     *[]byte
	}{}
	if suite, _, ok := observerSuite(doc, query.CheckID); ok {
		for _, subject := range []struct{ phase, candidate string }{{"BASELINE", doc.Baseline.SnapshotDigest}, {"CANDIDATE", doc.Candidate.SnapshotDigest}} {
			for _, item := range suite.Cases {
				for i := 0; i < suite.Repeats; i++ {
					input := append([]byte{}, item.Input...)
					expected = append(expected, struct {
						phase     string
						candidate string
						input     *[]byte
					}{subject.phase, subject.candidate, &input})
				}
			}
		}
	} else {
		expected = append(expected, struct {
			phase     string
			candidate string
			input     *[]byte
		}{"CHECK", doc.Candidate.SnapshotDigest, nil})
	}
	if len(expected) != len(p.NativeSubjects) {
		return c.Fail(c.StoreIntegrityError, "owned subjects omitted or added")
	}
	profile, _ := c.Digest(doc.CheckRuntime.Profile)
	phase := ""
	ordinal := 0
	sourceDigests := map[string]string{}
	for i, item := range expected {
		if item.phase != phase {
			phase = item.phase
			ordinal = 0
		}
		ordinal++
		capsule := p.NativeSubjects[i]
		invocation, _ := c.Digest(runner.Invocation{CandidateDigest: item.candidate, Argv: argv, Stdin: item.input})
		if capsule.SchemaVersion != 1 || capsule.Owner != (runner.Ownership{Lease: l, Phase: phase, Ordinal: ordinal}) || capsule.CandidateDigest != item.candidate || capsule.ProfileDigest != profile || capsule.InvocationDigest != invocation || !c.ValidDigest(capsule.SourceDigest) {
			return c.Fail(c.StoreIntegrityError, "owned subject changed its registered invocation")
		}
		if prior := sourceDigests[phase]; prior != "" && prior != capsule.SourceDigest {
			return c.Fail(c.StoreIntegrityError, "owned phase changed physical mount")
		}
		sourceDigests[phase] = capsule.SourceDigest
	}
	return nil
}

type leasedSubjectRunner struct {
	broker  *runner.Docker
	session *Session
	pending Pending
	phase   string
	ordinal int
	expires time.Time
}

func (s *Session) leasedRunner(doc Document, broker *runner.Docker, phase string) (*leasedSubjectRunner, error) {
	if doc.Pending == nil || doc.Pending.NativeLease == nil || validateNativeLease(doc) != nil {
		return nil, c.Fail(c.StaleAuthority, "durable process lease required")
	}
	lease := *doc.Pending.NativeLease
	if lease.InstanceID != s.instance.ID || lease.ClockDomain != s.clockDomain || lease.Generation != s.Journal.Generation() {
		return nil, c.Fail(c.StaleAuthority, "native clock domain or owner generation changed")
	}
	expires := s.clockOrigin.Add(time.Duration(lease.DeadlineMillis) * time.Millisecond)
	return &leasedSubjectRunner{broker: broker, session: s, pending: *doc.Pending, phase: phase, expires: expires}, nil
}
func (r *leasedSubjectRunner) Run(ctx context.Context, source string, p runner.Profile, inv runner.Invocation) (runner.Result, error) {
	r.ordinal++
	owner := runner.Ownership{Lease: *r.pending.NativeLease, Phase: r.phase, Ordinal: r.ordinal}
	_, capsule, err := runner.PrepareTarget(runner.Target{Owner: owner, Source: source, Profile: p, Invocation: inv})
	if err != nil {
		return runner.Result{}, &runner.DispatchFailure{Cause: err, EffectPossible: false}
	}
	found := false
	for _, expected := range r.pending.NativeSubjects {
		found = found || expected == capsule
	}
	if !found || r.session.Journal.Generation() != owner.Lease.Generation {
		return runner.Result{}, &runner.DispatchFailure{Cause: c.Fail(c.StaleAuthority, "subject differs from durable process capsule"), EffectPossible: false}
	}
	owned, err := runner.WithOwnership(ctx, owner, r.expires)
	if err != nil {
		return runner.Result{}, &runner.DispatchFailure{Cause: err, EffectPossible: false}
	}
	return r.broker.Run(owned, source, p, inv)
}
func validateOwnedResult(result runner.Result, doc Document, record CheckRun, phase string, ordinal int) error {
	if record.NativeLease != nil {
		lease := *record.NativeLease
		if lease.Validate() != nil || lease.TaskDigest != c.HashBytes([]byte(doc.TaskID)) || lease.Generation != record.Generation || lease.SpecVersion != record.SpecVersion || lease.PolicyEpoch != record.PolicyEpoch {
			return c.Fail(c.StoreIntegrityError, "check receipt lease authority invalid")
		}
		if result.Ownership == nil && result.ContainerID != "" {
			return c.Fail(c.StoreIntegrityError, "owned subject receipt lost its lease")
		}
	}
	if result.Ownership == nil {
		return nil
	} // Historical unowned engineering receipt.
	if record.NativeLease == nil || result.Ownership.Owner.Lease != *record.NativeLease {
		return c.Fail(c.StoreIntegrityError, "computation used a different admitted lease")
	}
	capsule := *result.Ownership
	o := capsule.Owner
	l := o.Lease
	if capsule.SchemaVersion != 1 || o.Validate() != nil || l.TaskDigest != c.HashBytes([]byte(doc.TaskID)) || l.Generation != record.Generation || l.SpecVersion != record.SpecVersion || l.PolicyEpoch != record.PolicyEpoch || o.Phase != phase || o.Ordinal != ordinal || capsule.CandidateDigest != result.CandidateDigest || capsule.ProfileDigest != result.ProfileDigest || capsule.InvocationDigest != result.InvocationDigest || !c.ValidDigest(capsule.SourceDigest) {
		return c.Fail(c.StoreIntegrityError, "owned computation receipt mismatch")
	}
	return nil
}
