package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"github.com/ixayldz/Viber/internal/workspace"
)

func TestDoctorJSONAndHonestUnsupportedRun(t *testing.T) {
	var out, stderr bytes.Buffer
	if exit := Execute([]string{"doctor", "--json"}, &out, &stderr); exit != 0 {
		t.Fatal(exit, stderr.String())
	}
	var d doctor
	if err := json.Unmarshal(out.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.ReleaseReady || d.Telemetry || d.TrainingExport || d.ProcessSandbox != "DEVELOPER_OFFLINE_V1_CONFORMANCE_PENDING" {
		t.Fatal("false assurance", d)
	}
	if stderr.Len() != 0 {
		t.Fatal("human logs leaked")
	}
	out.Reset()
	stderr.Reset()
	if exit := Execute([]string{"run", "fix bug", "--json"}, &out, &stderr); exit != 3 {
		t.Fatal("unsupported invocation not waiting", exit)
	}
	if !json.Valid(out.Bytes()) || bytes.Contains(out.Bytes(), []byte("\x1b")) {
		t.Fatal("bad JSONL")
	}
}
func TestSnapshotAndEndToEndPreviewLeaveSourceUntouched(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	if err := os.WriteFile(file, []byte("base\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if exit := Execute([]string{"snapshot", "--root", root, "--json"}, &out, &stderr); exit != 0 {
		t.Fatal(exit, stderr.String())
	}
	var snapshot c.Snapshot
	if err := json.Unmarshal(out.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	captured, err := workspace.CaptureDirectory(root, workspace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Digest != captured.Snapshot.Digest {
		t.Fatal("CLI snapshot differs")
	}
	p := c.Proposal{SchemaVersion: 1, ID: "p", TaskID: "t", SpecVersion: 1, BaseSnapshot: snapshot.Digest, PolicyEpoch: 1, KernelGeneration: 1, ReadSet: []c.ReadCondition{{Path: "a.txt", Kind: "FILE", Digest: snapshot.Entries[0].Hash}}, Changes: []c.Change{{Path: "a.txt", BeforeDigest: snapshot.Entries[0].Hash, After: []byte("candidate\r\n")}}}
	layers := []policy.Policy{{SchemaVersion: 1, Epoch: 1, Generation: 1, Effects: []string{"snapshot.read", "candidate.write"}, Paths: []string{"**"}}}
	state := c.TaskState{SchemaVersion: 1, TaskID: "t", SpecVersion: 1, Execution: c.Running, PolicyEpoch: 1, KernelGeneration: 1}
	artifacts := t.TempDir()
	for name, value := range map[string]any{"proposal.json": p, "policy.json": layers, "authority.json": state} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(artifacts, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"proposal-check", "--root", root, "--proposal", filepath.Join(artifacts, "proposal.json"), "--policy", filepath.Join(artifacts, "policy.json"), "--authority", filepath.Join(artifacts, "authority.json"), "--json"}
	out.Reset()
	if exit := Execute(args, &out, &stderr); exit != 0 {
		t.Fatal(exit, out.String(), stderr.String())
	}
	var candidate c.Snapshot
	if err := json.Unmarshal(out.Bytes(), &candidate); err != nil {
		t.Fatal(err)
	}
	if candidate.Digest == snapshot.Digest {
		t.Fatal("candidate unchanged")
	}
	raw, _ := os.ReadFile(file)
	if string(raw) != "base\r\n" {
		t.Fatal("live source overwritten")
	}
	if err := os.WriteFile(file, []byte("user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if exit := Execute(args, &out, &stderr); exit != 4 {
		t.Fatal("stale proposal accepted")
	}
	if !bytes.Contains(out.Bytes(), []byte("STALE_BASE")) {
		t.Fatal(out.String())
	}
}
