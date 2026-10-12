package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func useOnboardingInput(t *testing.T, value string) {
	t.Helper()
	previous := onboardingInput
	onboardingInput = strings.NewReader(value)
	t.Cleanup(func() { onboardingInput = previous })
}

func usePrivateConfigRoot(t *testing.T) string {
	t.Helper()
	root := onboardingTempDir(t)
	t.Setenv("APPDATA", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	return root
}

func onboardingTempDir(t *testing.T) string {
	t.Helper()
	if err := os.MkdirAll(".cache", 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(".cache", "onboarding-test-")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		_ = os.RemoveAll(directory)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(absolute) })
	return absolute
}

func TestBareNoninteractiveCommandKeepsHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Execute(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("bare command exit = %d; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage:") || strings.Contains(stdout.String(), "Welcome to Viber") {
		t.Fatalf("bare noninteractive command did not preserve help behavior: %q", stdout.String())
	}
}

func TestOnboardingChatGPTConsentRefusalCreatesNothing(t *testing.T) {
	configRoot := usePrivateConfigRoot(t)
	project := onboardingTempDir(t)
	useOnboardingInput(t, "1\n"+project+"\nSummarize this project\nNO\n")
	var stdout, stderr bytes.Buffer
	if code := Execute([]string{"start"}, &stdout, &stderr); code != 4 {
		t.Fatalf("start exit = %d, want typed policy denial; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "POLICY_DENIED") || !strings.Contains(stderr.String(), "no task or project context was sent") {
		t.Fatalf("consent refusal was not clear: %s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(configRoot, "viber")); !os.IsNotExist(err) {
		t.Fatalf("refusal created private task or auth data: stat err=%v", err)
	}
}

func TestOnboardingOfflineGreetingUsesPrivateSample(t *testing.T) {
	configRoot := usePrivateConfigRoot(t)
	userProject := onboardingTempDir(t)
	sentinel := filepath.Join(userProject, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	useOnboardingInput(t, "3\nYES\n")
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"start"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("offline demo exit = %d, want explicit UNVERIFIED exit 2; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "UNVERIFIED") || !strings.Contains(stdout.String(), "Next: viber ui") {
		t.Fatalf("offline demo did not explain its result and next action: %s", stdout.String())
	}
	match := regexp.MustCompile(`Task ID: (task-[0-9a-f]{32})`).FindStringSubmatch(stdout.String())
	if len(match) != 2 {
		t.Fatalf("generated task ID missing from output: %s", stdout.String())
	}
	id := match[1]
	dataRoot := filepath.Join(configRoot, "viber")
	sample := filepath.Join(dataRoot, "demo-source", id)
	store := filepath.Join(dataRoot, "tasks", id)
	if data, err := os.ReadFile(filepath.Join(sample, "hello.txt")); err != nil || string(data) != "hello" {
		t.Fatalf("private sample was modified or missing: %q, %v", data, err)
	}
	if info, err := os.Stat(store); err != nil || !info.IsDir() {
		t.Fatalf("private task store missing: %v", err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "unchanged" {
		t.Fatalf("user project was changed: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "onboarding-fixture-"+id+".json")); !os.IsNotExist(err) {
		t.Fatalf("temporary fixture was not removed: %v", err)
	}
}
