package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/runner"
	"github.com/ixayldz/Viber/internal/verify"
)

func TestNativeCrashHelper(t *testing.T) {
	if os.Getenv("VIBER_NATIVE_CRASH_HELPER") != "1" {
		return
	}
	source, directory, image := os.Getenv("VIBER_NATIVE_CRASH_SOURCE"), os.Getenv("VIBER_NATIVE_CRASH_STORE"), os.Getenv("VIBER_DOCKER_TEST_IMAGE")
	s, err := Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	profile := runner.DefaultProfile(image)
	profile.TimeoutSeconds = 300
	digest, _ := c.Digest(profile)
	check := verify.CheckPlan{SchemaVersion: 1, Checks: []verify.CheckDefinition{{ID: "orphan", Kind: "STATIC", RequirementIDs: []string{"user-goal"}, Argv: []string{"/bin/sh", "-c", "sleep 300 & wait"}, RunnerDigest: digest, Selection: "full", Closure: []verify.CheckScope{{Path: "checks", Recursive: true}}}}}
	plan, _ := c.CanonicalV1(check)
	fixture, _ := c.CanonicalV1(Fixture{SchemaVersion: 1, Turns: []Turn{{Calls: []model.Call{{ID: "crash-check", Name: "check_run", Arguments: json.RawMessage(`{"check_id":"orphan"}`)}}, UsageKnown: true, InputTokens: 2, OutputTokens: 2}}})
	budget := DefaultBudget()
	budget.MaxActiveMillis = 600000
	if _, err = s.Create(context.Background(), StartOptions{TaskID: "crash-task", Root: source, Prompt: []byte("Inspect without changing the captured source."), Fixture: fixture, Budget: budget, Autonomy: "guided", AllowUnverified: true, CheckPlan: plan, CheckRuntime: &CheckRuntime{SchemaVersion: 1, Profile: profile}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Run(context.Background(), "crash-task"); err != nil {
		t.Fatal(err)
	}
}
func actualCrashDocker(ctx context.Context, configuration string, args ...string) ([]byte, error) {
	binary, err := exec.LookPath("docker")
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, binary, append([]string{"--config", configuration}, args...)...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "SystemRoot=" + os.Getenv("SystemRoot"), "HOME=" + configuration, "USERPROFILE=" + configuration, "DOCKER_CLI_HINTS=false"}
	command.WaitDelay = 2 * time.Second
	return command.CombinedOutput()
}
func TestActualNativeOwnerCrashRecoveryFencesOrphanAndKeepsVerificationUnknown(t *testing.T) {
	if os.Getenv("VIBER_DOCKER_TEST_IMAGE") == "" {
		t.Skip("opt-in real pinned Docker engine required")
	}
	source, directory, configuration := t.TempDir(), filepath.Join(t.TempDir(), "store"), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "source.txt"), []byte("unchanged-source"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "checks"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "checks", "origin.txt"), []byte("operator"), 0644); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var childOutput bytes.Buffer
	command := exec.Command(executable, "-test.run=^TestNativeCrashHelper$", "-test.timeout=400s")
	command.Env = append(os.Environ(), "VIBER_NATIVE_CRASH_HELPER=1", "VIBER_NATIVE_CRASH_SOURCE="+source, "VIBER_NATIVE_CRASH_STORE="+directory)
	command.Stdout = &childOutput
	command.Stderr = &childOutput
	command.WaitDelay = 2 * time.Second
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var instance RuntimeInstance
	oldID := ""
	for oldID == "" {
		if ctx.Err() != nil {
			command.Process.Kill()
			command.Wait()
			t.Fatal("child did not dispatch owned subject", childOutput.String())
		}
		raw, readErr := os.ReadFile(filepath.Join(directory, runtimeInstanceFile))
		if readErr == nil && c.DecodeStrict(raw, &instance) == nil {
			raw, inspectErr := actualCrashDocker(ctx, configuration, "ps", "--no-trunc", "--filter", "label=io.viber.instance="+instance.ID, "--filter", "label=io.viber.runtime=owned-offline-v1", "--format", "{{.ID}}")
			if inspectErr == nil {
				ids := strings.Fields(string(raw))
				if len(ids) == 1 && c.ValidDigest(ids[0]) {
					oldID = ids[0]
				}
			}
		}
		if oldID == "" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	defer actualCrashDocker(context.Background(), configuration, "rm", "--force", oldID)
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait() // Intentional abrupt process death; never a PASS outcome.
	s, err := OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state, err := s.Run(ctx, "crash-task")
	if err != nil || state.Execution != c.Blocked {
		t.Fatal("interrupted intent not blocked", state, err)
	}
	view, err := s.NativeRiskInfo(ctx, "crash-task")
	if err != nil || !view.CanFence || view.Command == nil {
		t.Fatal(view, err)
	}
	action := *view.Command
	action.CommandID = "actual-crash-fence"
	state, err = s.ReconcileNativeRisk(ctx, action)
	if err != nil {
		t.Fatal(err)
	}
	state, doc, err := s.Load(ctx, "crash-task")
	if err != nil {
		t.Fatal(err)
	}
	if state.Quality != c.Unverified || doc.UnknownEffect || doc.Pending != nil || len(doc.CheckRuns) != 0 || !doc.PendingReplies[0].IsError || doc.Budget.ToolCalls != 1 {
		t.Fatal("lost computation accepted", state)
	}
	proof, err := s.readNativeRisk(doc, doc.NativeCleanupAttempts[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range proof.Cleanup.Items {
		defer actualCrashDocker(context.Background(), configuration, "rm", "--force", item.FenceContainerID)
	}
	if _, err = actualCrashDocker(ctx, configuration, "start", oldID); err == nil {
		t.Fatal("old subject restarted")
	}
	raw, err := actualCrashDocker(ctx, configuration, "ps", "--filter", "label=io.viber.instance="+instance.ID, "--format", "{{.ID}}")
	if err != nil || len(strings.Fields(string(raw))) != 0 {
		t.Fatal("owned process still running", string(raw), err)
	}
	current, err := os.ReadFile(filepath.Join(source, "source.txt"))
	if err != nil || string(current) != "unchanged-source" {
		t.Fatal("live source changed", err)
	}
	replay, err := s.ReconcileNativeRisk(ctx, action)
	if err != nil || replay.TaskSeq != state.TaskSeq {
		t.Fatal("cleanup re-dispatched", err)
	}
}
