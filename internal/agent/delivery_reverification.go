package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/delivery"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/workspace"
)

type DeliveryReverificationCommand struct {
	CommandID        string `json:"command_id"`
	ParentTask       string `json:"parent_task"`
	NewTask          string `json:"new_task"`
	ParentSequence   int64  `json:"expected_parent_sequence"`
	PreviewDirectory string `json:"preview_directory"`
	ManifestDigest   string `json:"manifest_digest"`
	AllowUnverified  bool   `json:"allow_unverified"`
}

type MergedDeliveryOrigin struct {
	Manifest string `json:"manifest_digest"`
	Preview  string `json:"preview_digest"`
	Target   string `json:"captured_target"`
	Result   string `json:"merged_candidate"`
}

func (origin MergedDeliveryOrigin) Validate() error {
	for _, digest := range []string{origin.Manifest, origin.Preview, origin.Target, origin.Result} {
		if !c.ValidDigest(digest) {
			return c.Fail(c.StoreIntegrityError, "invalid merged candidate provenance")
		}
	}
	return nil
}

func (s *Session) readRegisteredDeliveryPreview(ctx context.Context, command DeliveryReverificationCommand) (DeliveryPreviewManifest, error) {
	var result DeliveryPreviewManifest
	path, err := fileguard.ResolveProspective(command.PreviewDirectory)
	if err != nil {
		return result, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return result, err
	}
	defer root.Close()
	physical, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return result, err
	}
	raw, err := fileguard.ReadRegular(root, "manifest.json", 8<<20)
	if err != nil {
		return result, err
	}
	registry, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		return result, err
	}
	defer registry.Close()
	lock, err := privacyLock(ctx, registry)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	view, err := readPrivacy(registry)
	if err != nil {
		return result, err
	}
	found := false
	for _, copy := range view.Copies {
		if copy.Directory != path || copy.Kind != "DELIVERY_PREVIEW" || copy.PhysicalRoot != physical || !sameManagedTasks(copy.Tasks, []string{command.ParentTask}) {
			continue
		}
		for _, file := range copy.Files {
			if file.Path == "manifest.json" && file.Digest == c.HashBytes(raw) && file.Size == int64(len(raw)) {
				if err = checkManagedCopy(ctx, copy, false); err != nil {
					return result, err
				}
				found = true
			}
		}
	}
	if !found {
		return result, c.Fail(c.PolicyDenied, "exact preview manifest/root must be registered with its owner")
	}
	if err = c.DecodeStrict(raw, &result); err != nil {
		return result, err
	}
	expected := result.Digest
	result.Digest = ""
	actual, err := c.Digest(result)
	result.Digest = expected
	if err != nil || expected != actual || expected != command.ManifestDigest || result.SchemaVersion != 1 || result.TaskID != command.ParentTask || result.Preview.Status != "READY" || result.Changeset == nil {
		return result, c.Fail(c.StaleBase, "reviewed ready preview manifest does not match the command")
	}
	return result, nil
}

// CreateDeliveryReverification creates a new read-only task with a fixed local
// scheduler fixture. Ordinary resume executes the registered native checks.
// No model key, old PASS, goal review, request approval or live write is carried.
func (s *Session) CreateDeliveryReverification(ctx context.Context, command DeliveryReverificationCommand) (c.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if command.CommandID == "" || len(command.CommandID) > 128 || !utf8.ValidString(command.CommandID) || strings.ContainsAny(command.CommandID, "\x00\r\n") || command.ParentTask == command.NewTask || command.ParentSequence < 1 || !filepath.IsAbs(command.PreviewDirectory) || !c.ValidDigest(command.ManifestDigest) {
		return c.TaskState{}, c.Fail(c.InvalidArgument, "stable command, distinct task, parent cursor and exact absolute preview binding required")
	}
	for _, task := range []string{command.ParentTask, command.NewTask} {
		if _, err := artifact.ScopedBlobPath(task, c.HashBytes(nil)); err != nil {
			return c.TaskState{}, err
		}
	}
	if err := s.contentAvailable(command.NewTask); err != nil {
		return c.TaskState{}, err
	}
	digest, err := c.Digest(command)
	if err != nil {
		return c.TaskState{}, err
	}
	states, err := s.Journal.Replay(ctx, "")
	if err != nil {
		return c.TaskState{}, err
	}
	if state, found := states[command.NewTask]; found {
		_, doc, err := s.Load(ctx, command.NewTask)
		if err != nil {
			return state, err
		}
		if doc.AttemptOrigin == nil || doc.AttemptOrigin.RequestDigest != digest || doc.MergedDelivery == nil || doc.MergedDelivery.Manifest != command.ManifestDigest {
			return state, c.Fail(c.CommandIDConflict, "new task already belongs to a different delivery request")
		}
		if state.Execution == c.Created || state.Execution == c.Scoping {
			state, err = s.recover(ctx, state)
			if err != nil {
				return state, err
			}
			if state.Execution == c.Created || state.Execution == c.Recovering {
				state, err = s.transition(ctx, state, c.Scoping, "", "reconcile merged candidate task creation")
				if err != nil {
					return state, err
				}
			}
			return s.transition(ctx, state, c.Ready, "", "immutable merged candidate requires fresh checks")
		}
		return state, nil
	}
	parent, doc, err := s.Load(ctx, command.ParentTask)
	if err != nil {
		return parent, err
	}
	if parent.Execution != c.Terminated || parent.TaskSeq != command.ParentSequence || parent.InputBarrier || doc.Pending != nil || doc.UnknownEffect || unresolvedResource(parent) != nil || !tokensQuiescent(parent) {
		return parent, c.Fail(c.StaleRequest, "current terminal quiescent parent cursor required")
	}
	checks := registeredChecks(doc)
	if doc.CheckRuntime == nil || len(checks) == 0 || len(checks) > 16 {
		return parent, c.Fail(c.UnsupportedCapability, "merged reverification requires a bounded registered check runtime")
	}
	manifest, err := s.readRegisteredDeliveryPreview(ctx, command)
	if err != nil {
		return parent, err
	}
	if manifest.TaskSeq != parent.TaskSeq || manifest.Document != parent.DocumentDigest || manifest.SpecVersion != parent.SpecVersion || manifest.PolicyEpoch != parent.PolicyEpoch || manifest.HistoricalCandidateQuality != parent.Quality {
		return parent, c.Fail(c.StaleRequest, "preview no longer binds the current parent document")
	}
	base, err := s.Archive.Get(doc.Baseline)
	if err != nil {
		return parent, err
	}
	candidate, err := s.Archive.Get(doc.Candidate)
	if err != nil {
		return parent, err
	}
	var current workspace.Capture
	if base.Snapshot.Git != nil {
		current, err = workspace.CaptureRepository(base.Snapshot.Root, configuredCaptureLimits(doc.Config))
	} else {
		current, err = workspace.CaptureDirectory(base.Snapshot.Root, configuredCaptureLimits(doc.Config))
	}
	if err != nil {
		return parent, err
	}
	preview, merged, err := delivery.Preview(ctx, base, candidate, current)
	if err != nil {
		return parent, err
	}
	if preview.Status != "READY" || preview.Digest != manifest.Preview.Digest {
		return parent, c.Fail(c.StaleBase, "live target changed after reviewed preview; refresh instead of importing stale bytes")
	}
	if err = guardProtected(doc, merged); err != nil {
		return parent, err
	}
	options := AttemptOptions{ParentTask: parent.TaskID, NewTask: command.NewTask, ExpectedParentSequence: parent.TaskSeq}
	if err = s.registerAttemptLineage(ctx, options, parent, digest); err != nil {
		return parent, err
	}
	inherited := doc.Spec
	inherited.TaskID, inherited.Version, inherited.ProtectedOrigin = command.NewTask, 1, ""
	inherited.Inputs = append([]c.InputSource{}, doc.Spec.Inputs...)
	inherited.Requirements = append([]c.Requirement{}, doc.Spec.Requirements...)
	var prompt []byte
	for n, input := range inherited.Inputs {
		raw, readErr := s.Archive.GetBytes(parent.TaskID, input.Digest)
		if readErr != nil {
			return parent, readErr
		}
		if _, err = s.Archive.PutBytes(command.NewTask, raw); err != nil {
			return parent, err
		}
		inherited.Inputs[n].PayloadRef = "blob://" + command.NewTask + "/" + input.Digest
		if n == 0 {
			prompt = raw
		}
		if s.attemptStageFault != nil {
			if err = s.attemptStageFault(n + 1); err != nil {
				return parent, err
			}
		}
	}
	turns := []Turn{}
	for n, check := range checks {
		args, _ := c.CanonicalV1(struct {
			ID string `json:"check_id"`
		}{check.ID})
		turns = append(turns, Turn{Calls: []model.Call{{ID: fmt.Sprintf("merged-check-%02d", n), Name: "check_run", Arguments: args}}, UsageKnown: true})
	}
	turns = append(turns, Turn{Text: "Fixed registered checks completed against the immutable merged candidate; no live apply.", UsageKnown: true})
	fixture, err := c.CanonicalV1(Fixture{SchemaVersion: 1, Turns: turns})
	if err != nil {
		return parent, err
	}
	plan, err := c.CanonicalV1(doc.Protection.Plan)
	if err != nil {
		return parent, err
	}
	budget := DefaultBudget()
	budget.MaxSteps = int64(len(turns))
	origin := &AttemptOrigin{ParentTask: parent.TaskID, ParentSequence: parent.TaskSeq, ParentDocument: parent.DocumentDigest, ParentOutcome: parent.Outcome, RequestDigest: digest}
	mergedOrigin := &MergedDeliveryOrigin{Manifest: manifest.Digest, Preview: preview.Digest, Target: preview.Target, Result: preview.Result}
	return s.createLocked(ctx, StartOptions{Root: merged.Snapshot.Root, Prompt: prompt, TaskID: command.NewTask, TaskKind: "ANALYSIS", Budget: budget, Autonomy: "guided", AllowUnverified: command.AllowUnverified, CheckRuntime: doc.CheckRuntime, CheckPlan: plan, Fixture: fixture, Config: doc.Config, ResourcePolicy: doc.ResourcePolicy, inheritedSpec: &inherited, AttemptOrigin: origin, evaluationCapture: &merged, mergedDelivery: mergedOrigin})
}
