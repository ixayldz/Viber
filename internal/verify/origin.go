package verify

import (
	"sort"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

// CheckPlan is trusted operator input, never a model tool argument or repo
// preference. Explicit scopes are a protection boundary, not proof that every
// runtime dependency was discovered or that the check is an independent oracle.
type CheckPlan struct {
	SchemaVersion int               `json:"schema_version"`
	Checks        []CheckDefinition `json:"checks"`
}
type CheckDefinition struct {
	ObserverDigest string       `json:"observer_digest,omitempty"`
	ID             string       `json:"id"`
	Kind           string       `json:"kind"`
	RequirementIDs []string     `json:"requirement_ids"`
	Argv           []string     `json:"argv"`
	RunnerDigest   string       `json:"runner_digest"`
	Selection      string       `json:"selection"`
	ExpectedTests  []string     `json:"expected_tests"`
	Closure        []CheckScope `json:"closure"`
}
type CheckScope struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}
type ProtectedScope struct {
	Scope  CheckScope `json:"scope"`
	Kind   string     `json:"kind"`
	Digest string     `json:"digest"`
}
type CheckOrigin struct {
	SourceRoot         string           `json:"source_root"`
	SchemaVersion      int              `json:"schema_version"`
	TaskID             string           `json:"task_id"`
	InitialSpecVersion int64            `json:"initial_spec_version"`
	BaselineDigest     string           `json:"baseline_digest"`
	InputDigest        string           `json:"input_digest"`
	Provenance         string           `json:"provenance"`
	ClosureCoverage    string           `json:"closure_coverage"`
	Plan               CheckPlan        `json:"plan"`
	Protected          []ProtectedScope `json:"protected"`
}

func validCheckID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.", r)) {
			return false
		}
	}
	return true
}
func uniqueStrings(values []string, maximum int, ids bool) bool {
	if len(values) > maximum {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || len(value) > 1024 || seen[value] || strings.ContainsAny(value, "\x00\r\n") || ids && !validCheckID(value) {
			return false
		}
		seen[value] = true
	}
	return true
}
func (p CheckPlan) Validate() error {
	if p.SchemaVersion != 1 || len(p.Checks) > 128 {
		return c.Fail(c.InvalidArgument, "invalid check plan version or quota")
	}
	ids := map[string]bool{}
	for _, check := range p.Checks {
		if !validCheckID(check.ID) || ids[check.ID] || !c.ValidDigest(check.RunnerDigest) || len(check.Selection) == 0 || len(check.Selection) > 4096 || strings.ContainsRune(check.Selection, 0) || len(check.Argv) == 0 || len(check.Argv) > 128 || len(check.Closure) == 0 || len(check.Closure) > 256 || len(check.RequirementIDs) == 0 || !uniqueStrings(check.RequirementIDs, 128, true) || !uniqueStrings(check.ExpectedTests, 4096, false) {
			return c.Fail(c.InvalidArgument, "invalid check definition or bounded discovery contract")
		}
		ids[check.ID] = true
		if check.ObserverDigest != "" && (check.Kind != "TEST" || !c.ValidDigest(check.ObserverDigest)) {
			return c.Fail(c.InvalidArgument, "invalid protected observer identity")
		}
		if check.Kind != "TEST" && check.Kind != "STATIC" && check.Kind != "BEHAVIOR" {
			return c.Fail(c.InvalidArgument, "unsupported check kind")
		}
		if check.Kind == "TEST" && len(check.ExpectedTests) == 0 || check.Kind != "TEST" && len(check.ExpectedTests) != 0 {
			return c.Fail(c.InvalidArgument, "test checks require explicit nonzero expected discovery")
		}
		total := 0
		for _, arg := range check.Argv {
			total += len(arg)
			if strings.ContainsRune(arg, 0) || len(arg) > 8192 {
				return c.Fail(c.InvalidArgument, "invalid check argv")
			}
		}
		if check.Argv[0] == "" || total > 32768 {
			return c.Fail(c.InvalidArgument, "check argv exceeds quota")
		}
		scopes := map[string]bool{}
		for _, scope := range check.Closure {
			if scope.Path != "." && !policy.SafePath(scope.Path) || scope.Path == "." && !scope.Recursive || scopes[strings.ToLower(scope.Path)] {
				return c.Fail(c.InvalidArgument, "unsafe, duplicate or aliased check scope")
			}
			scopes[strings.ToLower(scope.Path)] = true
		}
	}
	raw, err := c.CanonicalV1(p)
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return c.Fail(c.InvalidArgument, "check plan exceeds 1 MiB")
	}
	return nil
}

func scopeContains(scope CheckScope, name string) bool {
	return scope.Path == "." || name == scope.Path || scope.Recursive && strings.HasPrefix(name, scope.Path+"/")
}
func foldedContains(scope CheckScope, name string) bool {
	scope.Path = strings.ToLower(scope.Path)
	return scopeContains(scope, strings.ToLower(name))
}

// scopeRecord binds exact bytes, modes, directories and absence. A subtree with
// an excluded descendant has incomplete coverage and cannot be protected here.
func scopeRecord(capture workspace.Capture, scope CheckScope) (ProtectedScope, error) {
	record := ProtectedScope{Scope: scope, Kind: "ABSENT"}
	files := []c.Entry{}
	dirs := []string{}
	for _, exclusion := range capture.Snapshot.Exclusions {
		name := strings.TrimSuffix(exclusion, "/")
		if foldedContains(scope, name) || strings.EqualFold(scope.Path, name) || strings.HasPrefix(strings.ToLower(scope.Path), strings.ToLower(name)+"/") {
			return record, c.Fail(c.UnsupportedCapability, "protected check scope intersects excluded source")
		}
	}
	for _, entry := range capture.Snapshot.Entries {
		if foldedContains(scope, entry.Path) && !scopeContains(scope, entry.Path) || strings.HasPrefix(strings.ToLower(scope.Path), strings.ToLower(entry.Path)+"/") {
			return record, c.Fail(c.PolicyDenied, "check scope aliases a file or crosses a file parent")
		}
		if entry.Path == scope.Path {
			if scope.Recursive {
				return record, c.Fail(c.InvalidArgument, "recursive check scope must be a directory or absent tree")
			}
			record.Kind = "FILE"
		}
		if scopeContains(scope, entry.Path) {
			files = append(files, entry)
		}
	}
	if scope.Path == "." {
		record.Kind = "TREE"
	}
	for _, dir := range capture.Snapshot.Directories {
		if foldedContains(scope, dir) && !scopeContains(scope, dir) || strings.HasPrefix(strings.ToLower(scope.Path), strings.ToLower(dir)+"/") && !strings.HasPrefix(scope.Path, dir+"/") {
			return record, c.Fail(c.PolicyDenied, "check scope aliases a directory")
		}
		if dir == scope.Path {
			if !scope.Recursive {
				return record, c.Fail(c.InvalidArgument, "directory check scope requires recursive coverage")
			}
			record.Kind = "TREE"
		}
		if scopeContains(scope, dir) {
			dirs = append(dirs, dir)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Strings(dirs)
	digest, err := c.Digest(struct {
		Scope       CheckScope `json:"scope"`
		Kind        string     `json:"kind"`
		Entries     []c.Entry  `json:"entries"`
		Directories []string   `json:"directories"`
	}{scope, record.Kind, files, dirs})
	record.Digest = digest
	return record, err
}

// BuildCheckOrigin must run before the first candidate mutation. The caller
// durably binds this object in the task document before admitting any write.
func BuildCheckOrigin(task string, baseline workspace.Capture, inputDigest string, plan CheckPlan) (CheckOrigin, error) {
	origin := CheckOrigin{SourceRoot: baseline.Snapshot.Root, SchemaVersion: 1, TaskID: task, InitialSpecVersion: 1, BaselineDigest: baseline.Snapshot.Digest, InputDigest: inputDigest, Provenance: "OPERATOR_DECLARED_REPOSITORY_BASELINE", ClosureCoverage: "EXPLICIT_UNREVIEWED", Plan: plan, Protected: []ProtectedScope{}}
	if !validCheckID(task) || !c.ValidDigest(inputDigest) {
		return origin, c.Fail(c.InvalidArgument, "invalid check origin task or input binding")
	}
	if err := workspace.VerifyCapture(baseline); err != nil {
		return origin, err
	}
	if err := plan.Validate(); err != nil {
		return origin, err
	}
	// Own the plan arrays so a caller cannot mutate an admitted origin by alias.
	raw, err := c.CanonicalV1(plan)
	if err != nil {
		return origin, err
	}
	origin.Plan = CheckPlan{}
	if err = c.DecodeStrict(raw, &origin.Plan); err != nil {
		return origin, err
	}
	sort.Slice(origin.Plan.Checks, func(i, j int) bool { return origin.Plan.Checks[i].ID < origin.Plan.Checks[j].ID })
	scopes := map[string]CheckScope{}
	for i := range origin.Plan.Checks {
		check := &origin.Plan.Checks[i]
		sort.Strings(check.RequirementIDs)
		sort.Strings(check.ExpectedTests)
		sort.Slice(check.Closure, func(i, j int) bool { return check.Closure[i].Path < check.Closure[j].Path })
		for _, scope := range check.Closure {
			key := strings.ToLower(scope.Path)
			if old, ok := scopes[key]; ok && old != scope {
				return origin, c.Fail(c.InvalidArgument, "check scopes disagree on path spelling or recursion")
			}
			scopes[key] = scope
		}
	}
	if len(scopes) > 1024 {
		return origin, c.Fail(c.InvalidArgument, "protected closure scope quota exceeded")
	}
	for _, scope := range scopes {
		record, err := scopeRecord(baseline, scope)
		if err != nil {
			return origin, err
		}
		origin.Protected = append(origin.Protected, record)
	}
	sort.Slice(origin.Protected, func(i, j int) bool { return origin.Protected[i].Scope.Path < origin.Protected[j].Scope.Path })
	if len(origin.Plan.Checks) == 0 {
		origin.ClosureCoverage = "UNRESOLVED"
	}
	return origin, nil
}

// ValidateCheckOrigin rederives the entire protected origin from immutable
// baseline bytes, raw intent and the authoritative spec digest. Repository
// edits, model messages and a later check-plan file cannot revise this origin.
func ValidateCheckOrigin(spec c.TaskSpec, origin CheckOrigin, baseline workspace.Capture) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if origin.TaskID != spec.TaskID || origin.SchemaVersion != 1 || origin.InitialSpecVersion != 1 || spec.Version < origin.InitialSpecVersion || origin.BaselineDigest != baseline.Snapshot.Digest {
		return c.Fail(c.StoreIntegrityError, "protected check origin binding mismatch")
	}
	found := false
	for _, input := range spec.Inputs {
		if input.ID == "initial" && input.Digest == origin.InputDigest {
			found = true
		}
	}
	if !found {
		return c.Fail(c.StoreIntegrityError, "protected origin raw intent binding missing")
	}
	rebuilt, err := BuildCheckOrigin(spec.TaskID, baseline, origin.InputDigest, origin.Plan)
	if err != nil {
		return err
	}
	expected, err := c.Digest(rebuilt)
	if err != nil {
		return err
	}
	actual, err := c.Digest(origin)
	if err != nil {
		return err
	}
	if expected != actual || actual != spec.ProtectedOrigin {
		return c.Fail(c.StoreIntegrityError, "protected check origin or closure changed")
	}
	requirements := map[string]bool{}
	for _, req := range spec.Requirements {
		requirements[req.ID] = true
	}
	for _, check := range origin.Plan.Checks {
		for _, id := range check.RequirementIDs {
			if !requirements[id] {
				return c.Fail(c.StoreIntegrityError, "protected check criterion missing from current spec")
			}
		}
	}
	return nil
}

func GuardProtectedCandidate(origin CheckOrigin, candidate workspace.Capture) error {
	if origin.SchemaVersion != 1 || origin.SourceRoot != candidate.Snapshot.Root {
		return c.Fail(c.StoreIntegrityError, "protected candidate source identity mismatch")
	}
	if err := workspace.VerifyCapture(candidate); err != nil {
		return err
	}
	for _, protected := range origin.Protected {
		actual, err := scopeRecord(candidate, protected.Scope)
		if err != nil {
			return err
		}
		if actual != protected {
			return c.Fail(c.PolicyDenied, "candidate changes protected check closure; authorized check revision is required")
		}
	}
	return nil
}

// FrozenChecks only records immutable inputs. It does not contain a PASS,
// discovery result, readonly assurance, independent-observer assertion or receipt.
// A trusted runner and a closure review are still required for admissible evidence.
type FrozenChecks struct {
	SchemaVersion   int       `json:"schema_version"`
	OriginDigest    string    `json:"origin_digest"`
	ClosureCoverage string    `json:"closure_coverage"`
	Binding         c.Binding `json:"binding"`
}

func FreezeChecks(spec c.TaskSpec, origin CheckOrigin, baseline, candidate workspace.Capture, environmentDigest, policyDigest string) (FrozenChecks, error) {
	result := FrozenChecks{}
	if err := ValidateCheckOrigin(spec, origin, baseline); err != nil {
		return result, err
	}
	if !c.ValidDigest(environmentDigest) || !c.ValidDigest(policyDigest) {
		return result, c.Fail(c.InvalidArgument, "frozen checks need environment and policy bindings")
	}
	if err := GuardProtectedCandidate(origin, candidate); err != nil {
		return result, err
	}
	coverage := map[string]bool{}
	for _, check := range origin.Plan.Checks {
		for _, id := range check.RequirementIDs {
			coverage[id] = true
		}
	}
	for _, req := range spec.Requirements {
		if req.Required && !coverage[req.ID] {
			return result, c.Fail(c.UnsupportedCapability, "required criterion has no protected check definition")
		}
	}
	checkDigest, err := c.Digest(struct {
		Origin  string           `json:"origin"`
		Spec    c.TaskSpec       `json:"spec"`
		Plan    CheckPlan        `json:"plan"`
		Closure []ProtectedScope `json:"closure"`
	}{spec.ProtectedOrigin, spec, origin.Plan, origin.Protected})
	if err != nil {
		return result, err
	}
	result = FrozenChecks{SchemaVersion: 1, OriginDigest: spec.ProtectedOrigin, ClosureCoverage: origin.ClosureCoverage, Binding: c.Binding{TaskID: spec.TaskID, SpecVersion: spec.Version, CandidateDigest: candidate.Snapshot.Digest, CheckSetDigest: checkDigest, EnvironmentDigest: environmentDigest, PolicyDigest: policyDigest}}
	return result, nil
}
