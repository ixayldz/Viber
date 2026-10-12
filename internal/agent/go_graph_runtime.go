package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/intelligence"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/runner"
)

const (
	GoGraphHelperCommand      = "/usr/local/bin/viber-go-graph-helper"
	GoGraphHelperVersion      = "VIBER_GO_GRAPH_HELPER_V1"
	maxGoGraphRuntimeManifest = 16 << 10
)

// GoGraphRuntimeManifest opts a task into isolated Go type analysis. Image is
// immutable (repository digest or local content-addressed image ID); helper
// command and protocol are fixed by this Viber build.
type GoGraphRuntimeManifest struct {
	SchemaVersion int            `json:"schema_version"`
	Profile       runner.Profile `json:"profile"`
	HelperCommand string         `json:"helper_command"`
	HelperVersion string         `json:"helper_version"`
}

func (manifest GoGraphRuntimeManifest) Validate() error {
	if manifest.SchemaVersion != 1 || manifest.Profile.Validate() != nil || manifest.Profile.TimeoutSeconds > 120 || manifest.Profile.MemoryBytes > 2<<30 || manifest.Profile.CPUs > 4 || manifest.Profile.Pids > 128 || manifest.Profile.ScratchBytes > 256<<20 || manifest.Profile.MaxOutputBytes > 8<<20 || manifest.HelperCommand != GoGraphHelperCommand || manifest.HelperVersion != GoGraphHelperVersion {
		return c.Fail(c.InvalidArgument, "invalid pinned Go graph helper runtime manifest")
	}
	return nil
}

type GoGraphRuntimeBinding struct {
	ManifestPath   string                 `json:"manifest_path"`
	ManifestDigest string                 `json:"manifest_digest"`
	Manifest       GoGraphRuntimeManifest `json:"manifest"`
}

func (binding GoGraphRuntimeBinding) Validate() error {
	if !filepath.IsAbs(binding.ManifestPath) || strings.ContainsAny(binding.ManifestPath, "\x00\r\n") || !c.ValidDigest(binding.ManifestDigest) || binding.Manifest.Validate() != nil {
		return c.Fail(c.InvalidArgument, "invalid Go graph runtime binding")
	}
	canonical, err := c.CanonicalV1(binding.Manifest)
	if err != nil || c.HashBytes(canonical) != binding.ManifestDigest {
		return c.Fail(c.StoreIntegrityError, "Go graph runtime manifest digest mismatch")
	}
	return nil
}

func BindGoGraphRuntime(path string) (GoGraphRuntimeBinding, error) {
	var binding GoGraphRuntimeBinding
	absolute, err := filepath.Abs(path)
	if err != nil || path == "" || strings.ContainsAny(path, "\x00\r\n") {
		return binding, c.Fail(c.InvalidArgument, "Go graph runtime manifest path unavailable")
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return binding, c.Fail(c.PolicyDenied, "Go graph runtime manifest unavailable")
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, filepath.Base(absolute), maxGoGraphRuntimeManifest)
	if err != nil || c.DecodeStrict(raw, &binding.Manifest) != nil || binding.Manifest.Validate() != nil {
		return GoGraphRuntimeBinding{}, c.Fail(c.InvalidArgument, "Go graph runtime manifest is invalid or unavailable")
	}
	canonical, err := c.CanonicalV1(binding.Manifest)
	if err != nil || !bytes.Equal(canonical, raw) {
		return GoGraphRuntimeBinding{}, c.Fail(c.InvalidArgument, "Go graph runtime manifest must use canonical encoding")
	}
	binding.ManifestPath = absolute
	binding.ManifestDigest = c.HashBytes(raw)
	return binding, binding.Validate()
}

func (binding GoGraphRuntimeBinding) Read() (GoGraphRuntimeManifest, error) {
	if err := binding.Validate(); err != nil {
		return GoGraphRuntimeManifest{}, err
	}
	root, err := os.OpenRoot(filepath.Dir(binding.ManifestPath))
	if err != nil {
		return GoGraphRuntimeManifest{}, c.Fail(c.StaleAuthority, "Go graph runtime manifest unavailable")
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, filepath.Base(binding.ManifestPath), maxGoGraphRuntimeManifest)
	if err != nil || c.HashBytes(raw) != binding.ManifestDigest {
		return GoGraphRuntimeManifest{}, c.Fail(c.StaleAuthority, "Go graph runtime manifest changed or unavailable")
	}
	var manifest GoGraphRuntimeManifest
	if err = c.DecodeStrict(raw, &manifest); err != nil || manifest.Validate() != nil {
		return GoGraphRuntimeManifest{}, c.Fail(c.StoreIntegrityError, "Go graph runtime manifest is invalid")
	}
	canonical, err := c.CanonicalV1(manifest)
	if err != nil || !bytes.Equal(canonical, raw) {
		return GoGraphRuntimeManifest{}, c.Fail(c.StoreIntegrityError, "Go graph runtime manifest is not canonical")
	}
	return manifest, nil
}

type GoGraphIntent struct {
	Options         intelligence.GoSemanticOptions `json:"options"`
	RuntimeDigest   string                         `json:"runtime_digest"`
	CandidateDigest string                         `json:"candidate_digest"`
	ArgumentsDigest string                         `json:"arguments_digest"`
	OperationDigest string                         `json:"operation_digest"`
}

type goGraphQuery struct {
	Candidate    string   `json:"candidate_digest,omitempty"`
	Fingerprint  string   `json:"graph_fingerprint,omitempty"`
	GOOS         string   `json:"goos,omitempty"`
	GOARCH       string   `json:"goarch,omitempty"`
	Compiler     string   `json:"compiler,omitempty"`
	Version      string   `json:"go_version,omitempty"`
	Tags         []string `json:"build_tags,omitempty"`
	TagsComplete bool     `json:"tags_complete,omitempty"`
	CGO          string   `json:"cgo,omitempty"`
	NodeOffset   int      `json:"node_offset,omitempty"`
	EdgeOffset   int      `json:"edge_offset,omitempty"`
	Limit        int      `json:"limit,omitempty"`
}

func decodeGoGraphQuery(call model.Call) (goGraphQuery, error) {
	var args goGraphQuery
	if err := c.DecodeStrict(call.Arguments, &args); err != nil {
		return args, err
	}
	if args.Limit == 0 {
		args.Limit = 32
	}
	if args.NodeOffset < 0 || args.EdgeOffset < 0 || args.Limit < 1 || args.Limit > 64 || len(args.Tags) > 64 {
		return args, c.Fail(c.InvalidArgument, "bounded graph offsets/options required")
	}
	if (args.NodeOffset > 0 || args.EdgeOffset > 0) && (args.Candidate == "" || args.Fingerprint == "") {
		return args, c.Fail(c.StaleBase, "later graph pages require exact candidate and graph fingerprint")
	}
	return args, nil
}

func goGraphOptions(args goGraphQuery) (intelligence.GoSemanticOptions, error) {
	return intelligence.NormalizeGoSemanticOptions(intelligence.GoSemanticOptions{
		GOOS: args.GOOS, GOARCH: args.GOARCH, Compiler: args.Compiler, GoVersion: args.Version,
		BuildTags: args.Tags, BuildTagsComplete: args.TagsComplete, CGOEnabled: args.CGO,
	})
}

func newGoGraphIntent(state c.TaskState, doc Document, call model.Call, options intelligence.GoSemanticOptions) (*GoGraphIntent, error) {
	if doc.GoGraphRuntime == nil || doc.GoGraphRuntime.Validate() != nil {
		return nil, c.Fail(c.PolicyDenied, "pinned Go graph helper is not configured")
	}
	argumentsDigest := c.HashBytes(call.Arguments)
	intent := &GoGraphIntent{Options: options, RuntimeDigest: doc.GoGraphRuntime.ManifestDigest, CandidateDigest: doc.Candidate.SnapshotDigest, ArgumentsDigest: argumentsDigest}
	var err error
	intent.OperationDigest, err = c.Digest(struct {
		Protocol   string                         `json:"protocol"`
		Task       string                         `json:"task"`
		Spec       int64                          `json:"spec_version"`
		Epoch      int64                          `json:"policy_epoch"`
		Generation int64                          `json:"generation"`
		Runtime    string                         `json:"runtime_digest"`
		Candidate  string                         `json:"candidate_digest"`
		Arguments  string                         `json:"arguments_digest"`
		Options    intelligence.GoSemanticOptions `json:"options"`
	}{intelligence.GoSemanticWorkerProtocol, doc.TaskID, doc.Spec.Version, state.PolicyEpoch, state.KernelGeneration, intent.RuntimeDigest, intent.CandidateDigest, intent.ArgumentsDigest, options})
	if err != nil {
		return nil, err
	}
	return intent, nil
}

// admitGoGraphCall checks all graph authority before the operation is persisted
// or a native lease can create any runner-side state. isolatedGoGraph repeats
// the effect checks immediately before transport to close the admission race.
func (s *Session) admitGoGraphCall(state c.TaskState, doc Document, call model.Call) (*GoGraphIntent, error) {
	args, err := decodeGoGraphQuery(call)
	if err != nil {
		return nil, err
	}
	if args.Candidate != "" && args.Candidate != doc.Candidate.SnapshotDigest {
		return nil, c.Fail(c.StaleBase, "graph candidate changed")
	}
	options, err := goGraphOptions(args)
	if err != nil {
		return nil, err
	}
	layers := s.layers(state, doc)
	checkAction := policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier, Effect: "check.run"}
	if err = policy.Admit(layers, checkAction); err != nil {
		return nil, err
	}
	if err = currentOperatorEffect(doc, "check.run"); err != nil {
		return nil, err
	}
	if doc.Config != nil {
		if err = doc.Config.AdmitEffect("check.run"); err != nil {
			return nil, err
		}
	}
	if doc.GoGraphRuntime == nil {
		return nil, c.Fail(c.PolicyDenied, "pinned Go graph helper is not configured")
	}
	if _, err = doc.GoGraphRuntime.Read(); err != nil {
		return nil, err
	}
	readLayers := layers
	if node := doc.Plan.Active(); node != nil {
		readLayers = append(append([]policy.Policy(nil), layers...), policy.Policy{SchemaVersion: 1, Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, Effects: []string{"snapshot.read"}, Paths: node.ReadScope})
	}
	readAction := policy.Action{Epoch: state.PolicyEpoch, Generation: state.KernelGeneration, InputBarrier: state.InputBarrier, Effect: "snapshot.read"}
	if err = policy.Admit(readLayers, readAction); err != nil {
		return nil, err
	}
	candidate, err := s.Archive.Get(doc.Candidate)
	if err != nil {
		return nil, err
	}
	for _, entry := range candidate.Snapshot.Entries {
		readAction.Path = entry.Path
		if err = policy.Admit(readLayers, readAction); err != nil {
			return nil, c.Fail(c.PolicyDenied, "cross-file graph is outside the current source read scope")
		}
	}
	return newGoGraphIntent(state, doc, call, options)
}

func validateGoGraphIntent(state c.TaskState, doc Document, call model.Call, intent GoGraphIntent) error {
	if call.Name != "fs_graph" || doc.GoGraphRuntime == nil || doc.GoGraphRuntime.Validate() != nil || intent.RuntimeDigest != doc.GoGraphRuntime.ManifestDigest || intent.CandidateDigest != doc.Candidate.SnapshotDigest || intent.ArgumentsDigest != c.HashBytes(call.Arguments) || doc.Pending != nil && (doc.Pending.Candidate != doc.Candidate.SnapshotDigest || doc.Pending.Generation != state.KernelGeneration || doc.Pending.Epoch != state.PolicyEpoch) {
		return c.Fail(c.StaleAuthority, "Go graph operation binding changed")
	}
	args, err := decodeGoGraphQuery(call)
	if err != nil {
		return err
	}
	options, err := goGraphOptions(args)
	if err != nil {
		return err
	}
	got, _ := c.Digest(intent.Options)
	want, _ := c.Digest(options)
	if got != want {
		return c.Fail(c.StaleAuthority, "Go graph build context changed")
	}
	expected, err := newGoGraphIntent(state, doc, call, options)
	if err != nil || expected.OperationDigest != intent.OperationDigest {
		return c.Fail(c.StaleAuthority, "Go graph operation digest changed")
	}
	return nil
}

func goGraphWorkerRequest(intent GoGraphIntent) ([]byte, error) {
	raw, err := json.Marshal(intelligence.GoSemanticWorkerRequest{Protocol: intelligence.GoSemanticWorkerProtocol, CandidateDigest: intent.CandidateDigest, Options: intent.Options})
	if err != nil || len(raw) > intelligence.GoSemanticWorkerMaxInput {
		return nil, c.Fail(c.BudgetLimitReached, "Go graph worker request exceeds protocol bound")
	}
	return raw, nil
}

func goGraphInvocation(doc Document, intent GoGraphIntent) (runner.Invocation, error) {
	request, err := goGraphWorkerRequest(intent)
	if err != nil {
		return runner.Invocation{}, err
	}
	return runner.Invocation{CandidateDigest: doc.Candidate.SnapshotDigest, RequestDigest: intent.OperationDigest, Argv: []string{GoGraphHelperCommand}, Stdin: &request}, nil
}

func ownedGoGraphTarget(s *Session, doc Document, call model.Call, lease runner.Lease) ([]runner.Target, []runner.Capsule, error) {
	if doc.Pending == nil || doc.Pending.GoGraphIntent == nil || doc.GoGraphRuntime == nil || validateGoGraphIntent(c.TaskState{PolicyEpoch: doc.Pending.Epoch, KernelGeneration: doc.Pending.Generation}, doc, call, *doc.Pending.GoGraphIntent) != nil {
		return nil, nil, c.Fail(c.StoreIntegrityError, "owned Go graph intent is invalid")
	}
	manifest, err := doc.GoGraphRuntime.Read()
	if err != nil {
		return nil, nil, err
	}
	materialized, err := s.Archive.Materialize(doc.Candidate)
	if err != nil {
		return nil, nil, err
	}
	if !fileguard.Disjoint(doc.GoGraphRuntime.ManifestPath, materialized.SourceDirectory) || !fileguard.Disjoint(s.directory, doc.GoGraphRuntime.ManifestPath) {
		return nil, nil, c.Fail(c.PolicyDenied, "graph runtime manifest and materialized source must be separate")
	}
	invocation, err := goGraphInvocation(doc, *doc.Pending.GoGraphIntent)
	if err != nil {
		return nil, nil, err
	}
	target, capsule, err := runner.PrepareTarget(runner.Target{Owner: runner.Ownership{Lease: lease, Phase: "CHECK", Ordinal: 1}, Source: materialized.SourceDirectory, Profile: manifest.Profile, Invocation: invocation})
	if err != nil {
		return nil, nil, err
	}
	return []runner.Target{target}, []runner.Capsule{capsule}, nil
}
