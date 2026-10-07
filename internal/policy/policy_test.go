package policy

import (
	c "github.com/ixayldz/Viber/internal/contracts"
	"testing"
)

func p() Policy {
	return Policy{SchemaVersion: c.SchemaVersion, Epoch: 2, Generation: 3, Effects: []string{"snapshot.read", "candidate.write"}, Paths: []string{"src/**"}, RemoteInference: false}
}
func TestRestrictionIntersection(t *testing.T) {
	user := p()
	repo := p()
	repo.Paths = []string{"**"}
	repo.Effects = append(repo.Effects, "process.start")
	repo.RemoteInference = true
	repo.Providers = []string{"remote"}
	ok := Action{Epoch: 2, Generation: 3, Effect: "candidate.write", Path: "src/a.go"}
	if err := Admit([]Policy{user, repo}, ok); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Action{
		{Epoch: 2, Generation: 3, Effect: "candidate.write", Path: "outside"},
		{Epoch: 2, Generation: 3, Effect: "process.start"},
		{Epoch: 2, Generation: 3, Effect: "snapshot.read", Remote: true, Provider: "remote"},
		{Epoch: 1, Generation: 3, Effect: "snapshot.read"},
		{Epoch: 2, Generation: 2, Effect: "snapshot.read"},
		{Epoch: 2, Generation: 3, Effect: "snapshot.read", InputBarrier: true},
	} {
		if Admit([]Policy{user, repo}, a) == nil {
			t.Fatalf("policy bypass: %#v", a)
		}
	}
}
func TestNonportableAndTraversalPaths(t *testing.T) {
	for _, p := range []string{"../secret", "a/../b", "C:/secret", "a\\b", "/root", "a:ads", "NUL", "con.txt", "COM1", "a.", "a ", "a\nspoof", "a\x1bOSC", "a//b", "a/."} {
		if SafePath(p) {
			t.Errorf("unsafe path %q accepted", p)
		}
	}
	for _, p := range []string{"src/app.go", "Türkçe/α.py", "COM10", "a/b-c.txt"} {
		if !SafePath(p) {
			t.Errorf("safe path %q rejected", p)
		}
	}
}
