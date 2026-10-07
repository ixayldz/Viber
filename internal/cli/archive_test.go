package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ixayldz/Viber/internal/artifact"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/runner"
)

func TestPersistedProposalCLIUsesOldBaselineAndPreservesUserEdits(t *testing.T) {
	source := t.TempDir()
	directory := t.TempDir()
	inputs := t.TempDir()
	for name, raw := range map[string]string{"a.txt": "base\r\n", "b.txt": "independent"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out, stderr bytes.Buffer
	if exit := Execute([]string{"snapshot-save", "--root", source, "--store", directory, "--task", "t", "--json"}, &out, &stderr); exit != 0 {
		t.Fatal(exit, out.String(), stderr.String())
	}
	var ref artifact.Ref
	if err := json.Unmarshal(out.Bytes(), &ref); err != nil {
		t.Fatal(err)
	}
	p := c.Proposal{SchemaVersion: 1, ID: "p", TaskID: "t", SpecVersion: 1, BaseSnapshot: ref.SnapshotDigest, PolicyEpoch: 1, KernelGeneration: 1, ReadSet: []c.ReadCondition{{Path: "a.txt", Kind: "FILE", Digest: c.HashBytes([]byte("base\r\n"))}}, Changes: []c.Change{{Path: "a.txt", BeforeDigest: c.HashBytes([]byte("base\r\n")), After: []byte("candidate\r\n")}}}
	layers := []policy.Policy{{SchemaVersion: 1, Epoch: 1, Generation: 1, Effects: []string{"snapshot.read", "candidate.write", "process.run"}, Paths: []string{"**"}}}
	state := c.TaskState{SchemaVersion: 1, TaskID: "t", SpecVersion: 1, Execution: c.Running, PolicyEpoch: 1, KernelGeneration: 1}
	for name, value := range map[string]any{"proposal.json": p, "policy.json": layers, "authority.json": state} {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(inputs, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "b.txt"), []byte("new user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"proposal-materialize", "--store", directory, "--base", ref.SnapshotDigest, "--proposal", filepath.Join(inputs, "proposal.json"), "--policy", filepath.Join(inputs, "policy.json"), "--authority", filepath.Join(inputs, "authority.json"), "--json"}
	out.Reset()
	if exit := Execute(args, &out, &stderr); exit != 0 {
		t.Fatal(exit, out.String(), stderr.String())
	}
	var materialized artifact.Materialized
	if err := json.Unmarshal(out.Bytes(), &materialized); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(materialized.SourceDirectory, "b.txt"))
	if string(raw) != "new user edit" {
		t.Fatal("user edit lost")
	}
	raw, _ = os.ReadFile(filepath.Join(source, "a.txt"))
	if string(raw) != "base\r\n" {
		t.Fatal("live source changed")
	}
	if image := os.Getenv("VIBER_DOCKER_TEST_IMAGE"); image != "" {
		profile := runner.DefaultProfile(image)
		raw, _ := json.Marshal(profile)
		if err := os.WriteFile(filepath.Join(inputs, "profile.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		sandbox := []string{"sandbox-run", "--store", directory, "--task", "t", "--snapshot", materialized.Ref.SnapshotDigest, "--profile", filepath.Join(inputs, "profile.json"), "--policy", filepath.Join(inputs, "policy.json"), "--authority", filepath.Join(inputs, "authority.json"), "--json", "--", "/bin/sh", "-c", "cat /workspace/a.txt; test ! -e /workspace/.git"}
		if exit := Execute(sandbox, &out, &stderr); exit != 0 {
			t.Fatal(exit, out.String(), stderr.String())
		}
		var result runner.Result
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(result.Stdout, []byte("candidate\r\n")) || !result.ProcessTreeQuiescent {
			t.Fatal("sandbox source binding", result)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("late user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if exit := Execute(args, &out, &stderr); exit != 4 || !bytes.Contains(out.Bytes(), []byte("STALE_BASE")) {
		t.Fatal("stale overwrite accepted", exit, out.String())
	}
}
