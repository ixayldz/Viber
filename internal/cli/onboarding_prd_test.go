package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOnboardingPRDDiscoveryIsCaseInsensitiveAndRootOnly(t *testing.T) {
	project := t.TempDir()
	const body = "PRD_BODY_MUST_NOT_ENTER_THE_ONBOARDING_PROMPT"
	if err := os.WriteFile(filepath.Join(project, "pRd.Md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(project, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "docs", "PRD.md"), []byte("nested PRD"), 0600); err != nil {
		t.Fatal(err)
	}

	paths := discoverOnboardingPRDs(project)
	if !reflect.DeepEqual(paths, []string{"pRd.Md"}) {
		t.Fatalf("unexpected root PRD discovery: %#v", paths)
	}
	var output bytes.Buffer
	prompter := newOnboardingPrompter(strings.NewReader("1\n"), &output)
	selected, err := onboardingSelectPRD(prompter, project)
	if err != nil {
		t.Fatal(err)
	}
	if selected != "pRd.Md" || prompter.lines != 1 {
		t.Fatalf("selection mismatch: path=%q lines=%d", selected, prompter.lines)
	}
	if !strings.Contains(output.String(), "1) pRd.Md") || strings.Contains(output.String(), body) {
		t.Fatalf("selection did not stay metadata-only: %q", output.String())
	}
}

func TestOnboardingPRDSelectionCanChooseAmongCaseVariants(t *testing.T) {
	project := t.TempDir()
	for _, name := range []string{"PRD.md", "prd.md"} {
		if err := os.WriteFile(filepath.Join(project, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(project)
	if err != nil {
		t.Fatal(err)
	}
	caseVariants := 0
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), "prd.md") {
			caseVariants++
		}
	}
	if caseVariants < 2 {
		t.Skip("filesystem does not preserve case-only filename variants")
	}
	var output bytes.Buffer
	prompter := newOnboardingPrompter(strings.NewReader("2\n"), &output)
	selected, err := onboardingSelectPRD(prompter, project)
	if err != nil {
		t.Fatal(err)
	}
	if selected != "prd.md" || !strings.Contains(output.String(), "1) PRD.md") || !strings.Contains(output.String(), "2) prd.md") {
		t.Fatalf("multiple PRD choice mismatch: selected=%q output=%q", selected, output.String())
	}
}

func TestOnboardingSelectedPRDRemainsBoundToCustomTask(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "prd.md"), []byte("private PRD body"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	prompter := newOnboardingPrompter(strings.NewReader("1\nKeep the existing project behavior.\n"), &output)
	prdPath, err := onboardingSelectPRD(prompter, project)
	if err != nil {
		t.Fatal(err)
	}
	task, boundPRD, err := onboardingTaskPrompt(prompter, prdPath)
	if err != nil {
		t.Fatal(err)
	}
	if task != "Keep the existing project behavior." || boundPRD != "prd.md" || prompter.lines != 2 {
		t.Fatalf("custom task lost its selected PRD binding: task=%q PRD=%q lines=%d", task, boundPRD, prompter.lines)
	}
	args := appendOnboardingPRD([]string{task, "--root", project}, boundPRD)
	want := []string{task, "--root", project, "--prd", "prd.md"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("explicit PRD selection was not bound to the task: %#v", args)
	}
	if strings.Contains(output.String(), "private PRD body") {
		t.Fatal("onboarding read the PRD body before the admitted task workflow")
	}
}

func TestOnboardingManualPRDPathAndExplicitOptOut(t *testing.T) {
	t.Run("manual project-relative path", func(t *testing.T) {
		project := t.TempDir()
		if err := os.Mkdir(filepath.Join(project, "docs"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(project, "docs", "product-spec.md"), []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		prompter := newOnboardingPrompter(strings.NewReader("docs/product-spec.md\nSummarize the migration plan.\n"), &output)
		prdPath, err := onboardingSelectPRD(prompter, project)
		if err != nil {
			t.Fatal(err)
		}
		task, boundPRD, err := onboardingTaskPrompt(prompter, prdPath)
		if err != nil {
			t.Fatal(err)
		}
		if task != "Summarize the migration plan." || boundPRD != "docs/product-spec.md" {
			t.Fatalf("manual PRD selection mismatch: task=%q path=%q", task, boundPRD)
		}
	})

	t.Run("none selected", func(t *testing.T) {
		project := t.TempDir()
		if err := os.WriteFile(filepath.Join(project, "PRD.md"), []byte("must not be inferred"), 0600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		prompter := newOnboardingPrompter(strings.NewReader("NONE\nSummarize one source file.\n"), &output)
		prdPath, err := onboardingSelectPRD(prompter, project)
		if err != nil {
			t.Fatal(err)
		}
		task, boundPRD, err := onboardingTaskPrompt(prompter, prdPath)
		if err != nil {
			t.Fatal(err)
		}
		if task != "Summarize one source file." || boundPRD != "" {
			t.Fatalf("explicit opt-out was not preserved: task=%q path=%q", task, boundPRD)
		}
		if got := appendOnboardingPRD([]string{task}, boundPRD); !reflect.DeepEqual(got, []string{task}) {
			t.Fatalf("task gained an unselected PRD: %#v", got)
		}
	})
}
