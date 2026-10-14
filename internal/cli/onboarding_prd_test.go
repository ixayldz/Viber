package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOnboardingPRDDefaultUsesExactPathWithoutReadingBody(t *testing.T) {
	project := t.TempDir()
	const body = "PRD_BODY_MUST_NOT_ENTER_THE_ONBOARDING_PROMPT"
	if err := os.WriteFile(filepath.Join(project, "PRD.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(project, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "docs", "PRD.md"), []byte("nested PRD"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	prompter := newOnboardingPrompter(strings.NewReader("\n"), &output)
	task, prdPath, err := onboardingTaskPrompt(prompter, project)
	if err != nil {
		t.Fatal(err)
	}
	if task != onboardingPRDDefaultTask || prdPath != "PRD.md" || prompter.lines != 1 {
		t.Fatalf("default choice mismatch: task=%q path=%q lines=%d", task, prdPath, prompter.lines)
	}
	if !strings.Contains(output.String(), `PRD workflow for "PRD.md"`) || !strings.Contains(output.String(), onboardingPRDDefaultTask) || strings.Contains(output.String(), body) {
		t.Fatalf("onboarding did not show a metadata-only PRD default: %q", output.String())
	}
	args := appendOnboardingPRD([]string{"Read PRD", "--root", project}, prdPath)
	want := []string{"Read PRD", "--root", project, "--prd", "PRD.md"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("default PRD selection was not bound to the CLI: %#v", args)
	}
}

func TestOnboardingCustomTaskAndNoPRDKeepExistingPrompt(t *testing.T) {
	projectWithPRD := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectWithPRD, "prd.md"), []byte("private body"), 0600); err != nil {
		t.Fatal(err)
	}
	var customOutput bytes.Buffer
	customPrompter := newOnboardingPrompter(strings.NewReader("Keep the existing project behavior.\n"), &customOutput)
	customTask, customPRD, err := onboardingTaskPrompt(customPrompter, projectWithPRD)
	if err != nil {
		t.Fatal(err)
	}
	if customTask != "Keep the existing project behavior." || customPRD != "" || customPrompter.lines != 1 {
		t.Fatalf("custom task was replaced or given implicit PRD authority: %q %q lines=%d", customTask, customPRD, customPrompter.lines)
	}
	if strings.Contains(customOutput.String(), "private body") || len(appendOnboardingPRD([]string{"custom"}, customPRD)) != 1 {
		t.Fatal("custom task read PRD content or added the PRD flag")
	}

	projectWithoutPRD := t.TempDir()
	var plainOutput bytes.Buffer
	plainPrompter := newOnboardingPrompter(strings.NewReader("Summarize one source file.\n"), &plainOutput)
	plainTask, plainPRD, err := onboardingTaskPrompt(plainPrompter, projectWithoutPRD)
	if err != nil {
		t.Fatal(err)
	}
	if plainTask != "Summarize one source file." || plainPRD != "" || plainPrompter.lines != 1 || plainOutput.String() != "Describe the task in your own words: " {
		t.Fatalf("no-PRD onboarding behavior changed: task=%q path=%q lines=%d prompt=%q", plainTask, plainPRD, plainPrompter.lines, plainOutput.String())
	}
	if got := appendOnboardingPRD([]string{"custom"}, plainPRD); !reflect.DeepEqual(got, []string{"custom"}) {
		t.Fatalf("no-PRD task gained a CLI flag: %#v", got)
	}
}
