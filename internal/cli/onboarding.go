package cli

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ixayldz/Viber/internal/agent"
	"github.com/ixayldz/Viber/internal/auth"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/model"
	"github.com/ixayldz/Viber/internal/tui"
)

const (
	onboardingMaxLine  = 8192
	onboardingMaxLines = 12
	onboardingMaxTask  = 4096
)

const onboardingPRDDefaultTask = "Read the PRD, propose a typed plan, implement it, and use registered checks."

// onboardingInput is replaceable in CLI tests; ordinary invocations read the
// process stdin directly.
var onboardingInput io.Reader = os.Stdin

type onboardingPrompter struct {
	scanner *bufio.Scanner
	out     io.Writer
	lines   int
}

func newOnboardingPrompter(input io.Reader, out io.Writer) *onboardingPrompter {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 1024), onboardingMaxLine)
	return &onboardingPrompter{scanner: scanner, out: out}
}

func (p *onboardingPrompter) ask(label, defaultValue string, maxBytes int) (string, error) {
	if p.lines >= onboardingMaxLines {
		return "", c.Fail(c.InvalidArgument, "onboarding input limit reached")
	}
	if _, err := fmt.Fprint(p.out, label); err != nil {
		return "", err
	}
	if !p.scanner.Scan() {
		if err := p.scanner.Err(); err != nil {
			return "", c.Fail(c.InvalidArgument, "input line exceeded the onboarding limit")
		}
		return "", c.Fail(c.Conflict, "onboarding stopped because input ended")
	}
	p.lines++
	value := strings.TrimSpace(p.scanner.Text())
	if value == "" && defaultValue != "" {
		value = defaultValue
	}
	if strings.EqualFold(value, "cancel") || strings.EqualFold(value, "quit") || strings.EqualFold(value, "q") {
		return "", c.Fail(c.Conflict, "onboarding cancelled; no task was started")
	}
	if !utf8.ValidString(value) || len(value) > maxBytes {
		return "", c.Fail(c.InvalidArgument, "input is not valid UTF-8 or exceeds the field limit")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", c.Fail(c.InvalidArgument, "control characters are not allowed in onboarding input")
		}
	}
	return value, nil
}

func (p *onboardingPrompter) askWithDefault(label, defaultValue string, maxBytes int) (string, bool, error) {
	if p.lines >= onboardingMaxLines {
		return "", false, c.Fail(c.InvalidArgument, "onboarding input limit reached")
	}
	if _, err := fmt.Fprint(p.out, label); err != nil {
		return "", false, err
	}
	if !p.scanner.Scan() {
		if err := p.scanner.Err(); err != nil {
			return "", false, c.Fail(c.InvalidArgument, "input line exceeded the onboarding limit")
		}
		return "", false, c.Fail(c.Conflict, "onboarding stopped because input ended")
	}
	p.lines++
	value := strings.TrimSpace(p.scanner.Text())
	usedDefault := value == "" && defaultValue != ""
	if usedDefault {
		value = defaultValue
	}
	if strings.EqualFold(value, "cancel") || strings.EqualFold(value, "quit") || strings.EqualFold(value, "q") {
		return "", false, c.Fail(c.Conflict, "onboarding cancelled; no task was started")
	}
	if !utf8.ValidString(value) || len(value) > maxBytes {
		return "", false, c.Fail(c.InvalidArgument, "input is not valid UTF-8 or exceeds the field limit")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", false, c.Fail(c.InvalidArgument, "control characters are not allowed in onboarding input")
		}
	}
	return value, usedDefault, nil
}

// discoverOnboardingPRD examines only bounded top-level metadata. It never
// opens a candidate's body; runTask performs the authoritative captured-source
// and current-policy check before dispatch.
func discoverOnboardingPRD(source string) string {
	root, err := os.OpenRoot(source)
	if err != nil {
		return ""
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return ""
	}
	entries, readErr := directory.ReadDir(257)
	_ = directory.Close()
	if len(entries) > 256 || readErr != nil && !errors.Is(readErr, io.EOF) {
		return ""
	}
	name := ""
	for _, entry := range entries {
		if entry.Name() != "PRD.md" && entry.Name() != "prd.md" {
			continue
		}
		if name != "" {
			return ""
		}
		info, statErr := root.Lstat(entry.Name())
		if statErr != nil || !info.Mode().IsRegular() {
			return ""
		}
		name = entry.Name()
	}
	return name
}

func onboardingTaskPrompt(p *onboardingPrompter, source string) (string, string, error) {
	prdPath := discoverOnboardingPRD(source)
	if prdPath == "" {
		task, err := p.ask("Describe the task in your own words: ", "", onboardingMaxTask)
		return task, "", err
	}
	label := fmt.Sprintf("Describe the task in your own words, or press Enter to use the PRD workflow for %q [%s]: ", prdPath, onboardingPRDDefaultTask)
	task, usedDefault, err := p.askWithDefault(label, onboardingPRDDefaultTask, onboardingMaxTask)
	if err != nil || !usedDefault {
		return task, "", err
	}
	return task, prdPath, nil
}

func appendOnboardingPRD(args []string, path string) []string {
	if path == "" {
		return args
	}
	return append(args, "--prd", path)
}

func interactiveOnboarding(out io.Writer) bool {
	stdout, ok := out.(*os.File)
	if !ok || stdout != os.Stdout || onboardingInput != os.Stdin {
		return false
	}
	return tui.IsTerminal(os.Stdin) && tui.IsTerminal(os.Stdout)
}

func finishOnboardingTask(taskID, store string, code int, out, errout io.Writer) int {
	if code != 0 && code != 2 {
		return code
	}
	if interactiveOnboarding(out) {
		fmt.Fprintln(out, "Opening the Viber task interface. Type /help for commands; /quit detaches the interface.")
		uiCode := runUI([]string{taskID, "--store", store}, out, errout)
		if uiCode != 0 {
			return uiCode
		}
		return code
	}
	fmt.Fprintf(out, "Next: viber ui %s --store <private task path> (quote the path if it contains spaces)\n", taskID)
	return code
}

func onboardingID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", c.Fail(c.PolicyDenied, "operating-system random source unavailable")
	}
	return "task-" + hex.EncodeToString(raw[:]), nil
}

func onboardingUserDataRoot() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", c.Fail(c.PolicyDenied, "private user data directory is unavailable")
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return "", c.Fail(c.PolicyDenied, "private user data directory is unavailable")
	}
	return filepath.Join(base, "viber"), nil
}

func onboardingTaskStore(source, userDataRoot, taskID string) (string, error) {
	store, err := filepath.Abs(filepath.Join(userDataRoot, "tasks", taskID))
	if err != nil {
		return "", c.Fail(c.PolicyDenied, "private task directory is unavailable")
	}
	if source != "" && !fileguard.Disjoint(source, store) {
		return "", c.Fail(c.PolicyDenied, "the default private task folder overlaps the selected project; choose a narrower project folder")
	}
	return store, nil
}

func resolveOnboardingSource(value string) (string, error) {
	if value == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", c.Fail(c.InvalidArgument, "current project folder is unavailable")
		}
		value = cwd
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", c.Fail(c.InvalidArgument, "project folder path is invalid")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", c.Fail(c.InvalidArgument, "project folder does not exist")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", c.Fail(c.InvalidArgument, "project path must be an existing folder")
	}
	return resolved, nil
}

func onboardingAuthStatus() (auth.View, error) {
	var stdout, stderr strings.Builder
	if code := runAuth([]string{"status", "--json"}, &stdout, &stderr); code != 0 {
		return auth.View{}, c.Fail(c.PolicyDenied, "ChatGPT account status could not be checked; no task was started")
	}
	var view auth.View
	if err := json.Unmarshal([]byte(stdout.String()), &view); err != nil {
		return auth.View{}, c.Fail(c.PolicyDenied, "ChatGPT account status was not readable; no task was started")
	}
	return view, nil
}

func onboardingEligibleProfiles(view auth.View) []auth.ProfileView {
	profiles := make([]auth.ProfileView, 0, len(view.Profiles))
	for _, profile := range view.Profiles {
		if profile.Connected && profile.PlanUsage {
			profiles = append(profiles, profile)
		}
	}
	return profiles
}

func onboardingSelectChatGPTProfile(p *onboardingPrompter, out io.Writer) (string, error) {
	view, err := onboardingAuthStatus()
	if err != nil {
		return "", err
	}
	eligible := onboardingEligibleProfiles(view)
	for _, profile := range eligible {
		if profile.Active {
			return profile.ID, nil
		}
	}

	if len(eligible) > 0 {
		fmt.Fprintln(out, "A connected profile with ChatGPT plan access is available. Profile IDs only are shown; credential data is never displayed.")
		for _, profile := range eligible {
			fmt.Fprintf(out, "  %s\n", tui.Safe(profile.ID))
		}
		fmt.Fprintln(out, "Choose one of these IDs, type LOGIN to add an account through official ChatGPT sign-in, or type CANCEL.")
	} else {
		fmt.Fprintln(out, "No connected ChatGPT profile with plan access is available.")
		fmt.Fprintln(out, "Type LOGIN to open official ChatGPT sign-in, or type CANCEL.")
	}
	choice, err := p.ask("> ", "", 256)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(choice, "login") {
		fmt.Fprintln(out, "Opening the official ChatGPT sign-in flow. Choose your account and review its plan-usage permission in the browser.")
		var stdout, stderr strings.Builder
		if code := runAuth([]string{"login", "--json"}, &stdout, &stderr); code != 0 {
			return "", c.Fail(c.PolicyDenied, "ChatGPT sign-in did not complete; no task was started")
		}
		updated, statusErr := onboardingAuthStatus()
		if statusErr != nil {
			return "", statusErr
		}
		for _, profile := range onboardingEligibleProfiles(updated) {
			if profile.Active {
				return profile.ID, nil
			}
		}
		return "", c.Fail(c.PolicyDenied, "sign-in completed without an active profile that has plan access; review viber auth status")
	}
	for _, profile := range eligible {
		if choice == profile.ID {
			var stdout, stderr strings.Builder
			if code := runAuth([]string{"select", "--profile", profile.ID, "--json"}, &stdout, &stderr); code != 0 {
				return "", c.Fail(c.PolicyDenied, "the selected ChatGPT profile could not be activated; no task was started")
			}
			updated, statusErr := onboardingAuthStatus()
			if statusErr != nil {
				return "", statusErr
			}
			for _, selected := range onboardingEligibleProfiles(updated) {
				if selected.Active && selected.ID == profile.ID {
					return selected.ID, nil
				}
			}
			return "", c.Fail(c.PolicyDenied, "the selected ChatGPT profile is no longer active; no task was started")
		}
	}
	return "", c.Fail(c.InvalidArgument, "choose a listed profile ID, LOGIN, or CANCEL; no task was started")
}

func offlineGreetingFixture() ([]byte, error) {
	fixture := agent.Fixture{
		SchemaVersion: 1,
		Turns: []agent.Turn{
			{Calls: []model.Call{{ID: "read-greeting", Name: "fs_read", Arguments: json.RawMessage(`{"path":"hello.txt"}`)}}, UsageKnown: true, InputTokens: 128, OutputTokens: 24},
			{Calls: []model.Call{{ID: "update-greeting", Name: "candidate_propose", Arguments: json.RawMessage(`{"read_set":[{"path":"hello.txt","kind":"FILE","digest":"2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}],"changes":[{"path":"hello.txt","before_digest":"2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824","after_bytes":"aGVsbG8sIFZpYmVyCg==","delete":false}]}`)}}, UsageKnown: true, InputTokens: 192, OutputTokens: 64},
			{Text: "The private sample greeting was updated in an isolated candidate. This fixed demo is UNVERIFIED and does not perform a user task.", UsageKnown: true, InputTokens: 128, OutputTokens: 32},
		},
	}
	return json.Marshal(fixture)
}

func createOfflineDemoSource(userDataRoot, taskID string) (string, error) {
	root := filepath.Join(userDataRoot, "demo-source", taskID)
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return "", c.Fail(c.PolicyDenied, "private demo folder could not be created")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return "", c.Fail(c.Conflict, "private demo folder already exists; start again to choose a fresh task ID")
	}
	file, err := os.OpenFile(filepath.Join(root, "hello.txt"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", c.Fail(c.PolicyDenied, "private demo sample could not be created")
	}
	_, writeErr := file.Write([]byte("hello"))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return "", c.Fail(c.PolicyDenied, "private demo sample could not be written")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", c.Fail(c.PolicyDenied, "private demo folder path is unavailable")
	}
	return absolute, nil
}

func writePrivateFixture(userDataRoot, taskID string, raw []byte) (string, error) {
	if err := os.MkdirAll(userDataRoot, 0700); err != nil {
		return "", c.Fail(c.PolicyDenied, "private user data folder could not be created")
	}
	path := filepath.Join(userDataRoot, "onboarding-fixture-"+taskID+".json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", c.Fail(c.Conflict, "private onboarding fixture path already exists; start again")
	}
	_, writeErr := file.Write(raw)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return "", c.Fail(c.PolicyDenied, "private onboarding fixture could not be written")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		_ = os.Remove(path)
		return "", c.Fail(c.PolicyDenied, "private onboarding fixture path is unavailable")
	}
	return absolute, nil
}

func runOnboarding(args []string, out, errout io.Writer) int {
	if len(args) != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "viber start takes no arguments"), false)
	}
	p := newOnboardingPrompter(onboardingInput, out)
	fmt.Fprintln(out, "Welcome to Viber. Choose how to start; no project scripts or browser pages are opened automatically.")
	fmt.Fprintln(out, "  1) ChatGPT — online; asks before sending your task and project context.")
	fmt.Fprintln(out, "  2) Ollama — local model; you choose a loopback endpoint and model.")
	fmt.Fprintln(out, "  3) Offline greeting demo — fixed private sample; it does not inspect your project or run your requested task.")
	mode, err := p.ask("Choose 1, 2, or 3 (or type CANCEL): ", "", 32)
	if err != nil {
		return report(out, errout, err, false)
	}
	if strings.EqualFold(mode, "chatgpt") {
		mode = "1"
	} else if strings.EqualFold(mode, "ollama") {
		mode = "2"
	} else if strings.EqualFold(mode, "offline") || strings.EqualFold(mode, "offline-demo") {
		mode = "3"
	}
	if mode != "1" && mode != "2" && mode != "3" {
		return report(out, errout, c.Fail(c.InvalidArgument, "choose ChatGPT, Ollama, or the offline demo"), false)
	}

	if mode == "3" {
		fmt.Fprintln(out, "The demo creates a private sample containing hello.txt, then proposes a greeting change in an isolated candidate.")
		fmt.Fprintln(out, "It does not read or change your project, does not use a model, and always remains UNVERIFIED (exit code 2).")
		confirm, promptErr := p.ask("Type YES to run the fixed demo: ", "", 16)
		if promptErr != nil {
			return report(out, errout, promptErr, false)
		}
		if !strings.EqualFold(confirm, "YES") {
			return report(out, errout, c.Fail(c.PolicyDenied, "offline demo was not started; your project was not read"), false)
		}
		userDataRoot, rootErr := onboardingUserDataRoot()
		if rootErr != nil {
			return report(out, errout, rootErr, false)
		}
		taskID, idErr := onboardingID()
		if idErr != nil {
			return report(out, errout, idErr, false)
		}
		store, storeErr := onboardingTaskStore("", userDataRoot, taskID)
		if storeErr != nil {
			return report(out, errout, storeErr, false)
		}
		demoSource, sourceErr := createOfflineDemoSource(userDataRoot, taskID)
		if sourceErr != nil {
			return report(out, errout, sourceErr, false)
		}
		if !fileguard.Disjoint(demoSource, store) {
			return report(out, errout, c.Fail(c.PolicyDenied, "private demo and task data paths overlap"), false)
		}
		fixtureRaw, fixtureErr := offlineGreetingFixture()
		if fixtureErr != nil {
			return report(out, errout, c.Fail(c.PolicyDenied, "offline greeting fixture could not be prepared"), false)
		}
		fixturePath, fixtureErr := writePrivateFixture(userDataRoot, taskID, fixtureRaw)
		if fixtureErr != nil {
			return report(out, errout, fixtureErr, false)
		}
		defer os.Remove(fixturePath)
		if !fileguard.Disjoint(demoSource, fixturePath) || !fileguard.Disjoint(store, fixturePath) {
			return report(out, errout, c.Fail(c.PolicyDenied, "private demo, fixture and task data paths must be separate"), false)
		}
		fmt.Fprintf(out, "Private demo source: %s\nPrivate task data: %s\nTask ID: %s\n", tui.Safe(demoSource), tui.Safe(store), taskID)
		code := runTask([]string{"Update the greeting to say hello, Viber.", "--root", demoSource, "--store", store, "--task", taskID, "--offline", "--fixture", fixturePath, "--allow-unverified", "--autonomy", "auto"}, out, errout)
		if code == 0 || code == 2 {
			fmt.Fprintln(out, "Offline demo finished; its unverified result is ready for review.")
		}
		return finishOnboardingTask(taskID, store, code, out, errout)
	}

	endpoint, modelID, profileID := "", "", ""
	if mode == "2" {
		endpoint, err = p.ask("Ollama endpoint [http://127.0.0.1:11434]: ", "http://127.0.0.1:11434", 512)
		if err != nil {
			return report(out, errout, err, false)
		}
		modelID, err = p.ask("Ollama model ID: ", "", 128)
		if err != nil {
			return report(out, errout, err, false)
		}
		if modelID == "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "an Ollama model ID is required; no task was started"), false)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return report(out, errout, c.Fail(c.InvalidArgument, "current project folder is unavailable"), false)
	}
	projectInput, err := p.ask("Project folder [current folder]: ", cwd, 4096)
	if err != nil {
		return report(out, errout, err, false)
	}
	source, err := resolveOnboardingSource(projectInput)
	if err != nil {
		return report(out, errout, err, false)
	}
	taskPrompt, prdPath, err := onboardingTaskPrompt(p, source)
	if err != nil {
		return report(out, errout, err, false)
	}
	if taskPrompt == "" {
		return report(out, errout, c.Fail(c.InvalidArgument, "a task description is required; no task was started"), false)
	}
	if mode == "1" {
		fmt.Fprintf(out, "ChatGPT will receive your task and project context captured from %s. Credentials use Viber's official ChatGPT sign-in; Viber does not reuse Codex login.\n", tui.Safe(source))
		consent, consentErr := p.ask("Type YES to allow this transmission, or CANCEL: ", "", 16)
		if consentErr != nil {
			return report(out, errout, consentErr, false)
		}
		if !strings.EqualFold(consent, "YES") {
			return report(out, errout, c.Fail(c.PolicyDenied, "ChatGPT transmission was declined; no task or project context was sent"), false)
		}
	}

	userDataRoot, err := onboardingUserDataRoot()
	if err != nil {
		return report(out, errout, err, false)
	}
	taskID, err := onboardingID()
	if err != nil {
		return report(out, errout, err, false)
	}
	store, err := onboardingTaskStore(source, userDataRoot, taskID)
	if err != nil {
		return report(out, errout, err, false)
	}
	if mode == "1" {
		authDir, authErr := defaultAuthDirectory()
		if authErr != nil || !fileguard.Disjoint(source, authDir) || !fileguard.Disjoint(store, authDir) {
			return report(out, errout, c.Fail(c.PolicyDenied, "ChatGPT credentials must remain outside the project and task folder; no task was started"), false)
		}
		profileID, err = onboardingSelectChatGPTProfile(p, out)
		if err != nil {
			return report(out, errout, err, false)
		}
	}

	fmt.Fprintln(out, "Changes are prepared in a separate candidate and require your review. In the interface use /requests, /diff and /resume. An unverified final result also requires approval; it is never reported as verified.")
	args = []string{taskPrompt, "--root", source, "--store", store, "--task", taskID, "--autonomy", "review"}
	args = appendOnboardingPRD(args, prdPath)
	if mode == "1" {
		args = append(args, "--provider", "chatgpt", "--allow-remote", "--auth-profile", profileID)
	} else {
		fmt.Fprintln(out, "Ollama is declared local; Viber limits the endpoint to a loopback address. The model receives your task and selected project context.")
		args = append(args, "--provider", "ollama", "--model", modelID, "--endpoint", endpoint, "--local-model")
	}
	if interactiveOnboarding(out) {
		// Attach the interface after durable task creation/admission instead
		// of waiting for a potentially long model run to finish. The owner
		// continues running when the interface detaches.
		args = append(args, "--detach")
	}
	fmt.Fprintf(out, "Starting task %s\nPrivate task data: %s\n", taskID, tui.Safe(store))
	code := runTask(args, out, errout)
	return finishOnboardingTask(taskID, store, code, out, errout)
}
