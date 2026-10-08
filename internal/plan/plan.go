// Package plan provides a deterministic single-worker dependency scheduler.
// IMPLEMENTED denotes a bound work product, never a verification verdict.
package plan

import (
	"sort"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
)

type Node struct {
	ID             string   `json:"id"`
	Goal           string   `json:"goal"`
	InputContract  string   `json:"input_contract"`
	OutputContract string   `json:"output_contract"`
	ReadScope      []string `json:"read_scope"`
	WriteScope     []string `json:"write_scope"`
	RequirementIDs []string `json:"requirement_ids"`
	RequiredChecks []string `json:"required_checks"`
	Mutex          []string `json:"mutex"`
}
type Relation struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}
type Definition struct {
	SchemaVersion int        `json:"schema_version"`
	ID            string     `json:"id"`
	SpecVersion   int64      `json:"spec_version"`
	PolicyEpoch   int64      `json:"policy_epoch"`
	BaseCandidate string     `json:"base_candidate"`
	Nodes         []Node     `json:"nodes"`
	Relations     []Relation `json:"relations"`
}
type Progress struct {
	NodeID       string `json:"node_id"`
	Status       string `json:"status"`
	OutputDigest string `json:"output_digest,omitempty"`
	Candidate    string `json:"candidate,omitempty"`
}
type State struct {
	Definition       Definition `json:"definition"`
	DefinitionDigest string     `json:"definition_digest"`
	CurrentCandidate string     `json:"current_candidate"`
	Progress         []Progress `json:"progress"`
}

func identifier(value string) bool {
	if len(value) < 1 || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 33 || r > 126 || strings.ContainsRune("/\\:\"<>|?*", r) {
			return false
		}
	}
	return true
}
func scopes(values []string) bool {
	if len(values) > 128 {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			return false
		}
		seen[value] = true
		if value == "**" {
			continue
		}
		if strings.HasSuffix(value, "/**") {
			value = strings.TrimSuffix(value, "/**")
		}
		if !policy.SafePath(value) {
			return false
		}
	}
	return true
}
func text(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 4096 && utf8.ValidString(value)
}
func (d Definition) Validate(spec c.TaskSpec, checks []string) error {
	invalid := func() error {
		return c.Fail(c.InvalidArgument, "invalid plan contract, binding, scope or dependency graph")
	}
	if d.SchemaVersion != 1 || !identifier(d.ID) || d.SpecVersion != spec.Version || d.PolicyEpoch < 1 || !c.ValidDigest(d.BaseCandidate) || len(d.Nodes) < 1 || len(d.Nodes) > 128 || len(d.Relations) > 1024 {
		return invalid()
	}
	requirements := map[string]bool{}
	covered := map[string]bool{}
	for _, r := range spec.Requirements {
		requirements[r.ID] = true
	}
	checkIDs := map[string]bool{}
	for _, id := range checks {
		checkIDs[id] = true
	}
	nodes := map[string]bool{}
	for _, n := range d.Nodes {
		if !identifier(n.ID) || nodes[n.ID] || !text(n.Goal) || !text(n.InputContract) || !text(n.OutputContract) || !scopes(n.ReadScope) || !scopes(n.WriteScope) || len(n.RequirementIDs) < 1 || len(n.RequirementIDs) > 128 || len(n.RequiredChecks) > 128 || len(n.Mutex) > 128 {
			return invalid()
		}
		nodes[n.ID] = true
		ids := map[string]bool{}
		for _, id := range n.RequirementIDs {
			if !requirements[id] || ids[id] {
				return invalid()
			}
			ids[id] = true
			covered[id] = true
		}
		ids = map[string]bool{}
		for _, id := range n.RequiredChecks {
			if !checkIDs[id] || ids[id] {
				return invalid()
			}
			ids[id] = true
		}
		ids = map[string]bool{}
		for _, id := range n.Mutex {
			if !identifier(id) || ids[id] {
				return invalid()
			}
			ids[id] = true
		}
	}
	for _, r := range spec.Requirements {
		if r.Required && !covered[r.ID] {
			return invalid()
		}
	}
	edges := map[string]bool{}
	dependencies := map[string][]string{}
	for _, edge := range d.Relations {
		key := edge.From + "\x00" + edge.Kind + "\x00" + edge.To
		if !nodes[edge.From] || !nodes[edge.To] || edge.From == edge.To || edges[key] {
			return invalid()
		}
		edges[key] = true
		switch edge.Kind {
		case "DEPENDS_ON":
			dependencies[edge.From] = append(dependencies[edge.From], edge.To)
		case "CAN_PARALLELIZE", "MUTEX", "PRODUCES_CONTRACT_FOR", "INVALIDATES", "VERIFIES":
		default:
			return invalid()
		}
	}
	colors := map[string]int{}
	var visit func(string) bool
	visit = func(id string) bool {
		if colors[id] == 1 {
			return false
		}
		if colors[id] == 2 {
			return true
		}
		colors[id] = 1
		for _, dependency := range dependencies[id] {
			if !visit(dependency) {
				return false
			}
		}
		colors[id] = 2
		return true
	}
	for id := range nodes {
		if !visit(id) {
			return invalid()
		}
	}
	return nil
}
func New(d Definition, spec c.TaskSpec, checks []string, candidate string, epoch int64) (*State, error) {
	if err := d.Validate(spec, checks); err != nil {
		return nil, err
	}
	if candidate != d.BaseCandidate || epoch != d.PolicyEpoch {
		return nil, c.Fail(c.StaleBase, "plan authority or candidate changed")
	}
	raw, err := c.CanonicalV1(d)
	if err != nil {
		return nil, err
	}
	var owned Definition
	if err = c.DecodeStrict(raw, &owned); err != nil {
		return nil, err
	}
	digest, err := c.Digest(owned)
	if err != nil {
		return nil, err
	}
	s := &State{Definition: owned, DefinitionDigest: digest, CurrentCandidate: candidate}
	for _, node := range owned.Nodes {
		s.Progress = append(s.Progress, Progress{NodeID: node.ID, Status: "PENDING"})
	}
	return s, nil
}
func (s *State) Validate(spec c.TaskSpec, checks []string, candidate string, epoch int64) error {
	if s == nil {
		return nil
	}
	if err := s.Definition.Validate(spec, checks); err != nil {
		return err
	}
	digest, err := c.Digest(s.Definition)
	if err != nil {
		return err
	}
	if digest != s.DefinitionDigest || candidate != s.CurrentCandidate || epoch != s.Definition.PolicyEpoch || len(s.Progress) != len(s.Definition.Nodes) {
		return c.Fail(c.StoreIntegrityError, "plan state binding mismatch")
	}
	statuses := map[string]string{}
	running := 0
	for i, p := range s.Progress {
		if p.NodeID != s.Definition.Nodes[i].ID {
			return c.Fail(c.StoreIntegrityError, "plan progress identity mismatch")
		}
		statuses[p.NodeID] = p.Status
		switch p.Status {
		case "PENDING", "RUNNING":
			if p.OutputDigest != "" || p.Candidate != "" {
				return c.Fail(c.StoreIntegrityError, "unfinished plan node has completion")
			}
			if p.Status == "RUNNING" {
				running++
			}
		case "IMPLEMENTED":
			if !c.ValidDigest(p.OutputDigest) || !c.ValidDigest(p.Candidate) {
				return c.Fail(c.StoreIntegrityError, "plan output binding missing")
			}
		default:
			return c.Fail(c.StoreIntegrityError, "invalid plan progress status")
		}
	}
	if running > 1 {
		return c.Fail(c.StoreIntegrityError, "multiple plan writers")
	}
	for _, edge := range s.Definition.Relations {
		if edge.Kind == "DEPENDS_ON" && statuses[edge.From] != "PENDING" && statuses[edge.To] != "IMPLEMENTED" {
			return c.Fail(c.StoreIntegrityError, "plan dependency bypass")
		}
	}
	return nil
}
func (s *State) Active() *Node {
	if s == nil {
		return nil
	}
	for i, p := range s.Progress {
		if p.Status == "RUNNING" {
			return &s.Definition.Nodes[i]
		}
	}
	return nil
}
func (s *State) Next() (*Node, error) {
	if s == nil {
		return nil, c.Fail(c.InvalidArgument, "no active plan")
	}
	if node := s.Active(); node != nil {
		return node, nil
	}
	statuses := map[string]string{}
	for _, p := range s.Progress {
		statuses[p.NodeID] = p.Status
	}
	ready := []int{}
	for i, p := range s.Progress {
		if p.Status != "PENDING" {
			continue
		}
		allowed := true
		for _, edge := range s.Definition.Relations {
			if edge.Kind == "DEPENDS_ON" && edge.From == p.NodeID && statuses[edge.To] != "IMPLEMENTED" {
				allowed = false
			}
		}
		if allowed {
			ready = append(ready, i)
		}
	}
	if len(ready) == 0 {
		if s.Complete() {
			return nil, nil
		}
		return nil, c.Fail(c.InvalidArgument, "plan has no schedulable node")
	}
	sort.Slice(ready, func(i, j int) bool { return s.Progress[ready[i]].NodeID < s.Progress[ready[j]].NodeID })
	index := ready[0]
	s.Progress[index].Status = "RUNNING"
	return &s.Definition.Nodes[index], nil
}
func (s *State) Finish(id, output, candidate string) error {
	node := s.Active()
	if node == nil || node.ID != id || !c.ValidDigest(output) || candidate != s.CurrentCandidate {
		return c.Fail(c.StaleBase, "plan node completion is not current")
	}
	for i := range s.Progress {
		if s.Progress[i].NodeID == id {
			s.Progress[i] = Progress{NodeID: id, Status: "IMPLEMENTED", OutputDigest: output, Candidate: candidate}
			return nil
		}
	}
	return c.Fail(c.StoreIntegrityError, "active node missing")
}
func (s *State) Complete() bool {
	if s == nil {
		return true
	}
	for _, p := range s.Progress {
		if p.Status != "IMPLEMENTED" {
			return false
		}
	}
	return true
}
func (s *State) AdmitWrites(changes []c.Change) error {
	if s == nil {
		return nil
	}
	node := s.Active()
	if node == nil {
		return c.Fail(c.PolicyDenied, "candidate mutation requires an active plan node")
	}
	for _, change := range changes {
		if !policy.PathAllowed(change.Path, node.WriteScope) {
			return c.Fail(c.PolicyDenied, "candidate write outside active plan contract")
		}
	}
	return nil
}
func (s *State) AdmitReadSet(conditions []c.ReadCondition) error {
	if s == nil {
		return nil
	}
	node := s.Active()
	if node == nil {
		return c.Fail(c.PolicyDenied, "read-set requires an active plan node")
	}
	scopes := append(append([]string{}, node.ReadScope...), node.WriteScope...)
	for _, condition := range conditions {
		allowed := policy.PathAllowed(condition.Path, scopes)
		if condition.Kind == "LISTING" {
			for _, scope := range scopes {
				if scope == "**" || strings.HasSuffix(scope, "/**") && condition.Path == strings.TrimSuffix(scope, "/**") {
					allowed = true
				}
			}
		}
		if !allowed {
			return c.Fail(c.PolicyDenied, "read-set outside active plan contract")
		}
	}
	return nil
}
