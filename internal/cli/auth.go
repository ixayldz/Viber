package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/ixayldz/Viber/internal/auth"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func defaultAuthDirectory() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "viber", "auth"), nil
}
func openAuthBrowser(ctx context.Context, url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.CommandContext(ctx, filepath.Join(os.Getenv("SystemRoot"), "System32", "rundll32.exe"), filepath.Join(os.Getenv("SystemRoot"), "System32", "url.dll")+",FileProtocolHandler", url)
	case "darwin":
		command = exec.CommandContext(ctx, "/usr/bin/open", url)
	case "linux":
		command = exec.CommandContext(ctx, "/usr/bin/xdg-open", url)
	default:
		return c.Fail(c.UnsupportedCapability, "system browser unavailable")
	}
	// The trusted OS browser receives the auth URL; it is never printed, recorded
	// in task data, or passed through a shell.
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}
func runAuth(args []string, out, errout io.Writer) int {
	if len(args) == 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "auth login|status|profiles|select|models|logout|migrate-storage required"), false)
	}
	action := args[0]
	f := flags("auth "+action, errout)
	directory := f.String("auth-dir", "", "private credential directory outside source; default user config")
	provider := f.String("provider", "chatgpt", "ChatGPT plan via official Sign in with ChatGPT")
	profile := f.String("profile", "", "saved profile ID; login without it adds a registration")
	jsonMode := f.Bool("json", false, "redacted structured account status")
	timeout := f.Int("timeout-seconds", 600, "login timeout, 30..600")
	if err := f.Parse(args[1:]); err != nil {
		return 4
	}
	if f.NArg() != 0 || *provider != "chatgpt" || *timeout < 30 || *timeout > 600 {
		return report(out, errout, c.Fail(c.InvalidArgument, "supported provider is chatgpt; invalid auth flags"), *jsonMode)
	}
	switch action {
	case "login", "status", "profiles", "select", "models", "logout", "migrate-storage":
	default:
		return report(out, errout, c.Fail(c.InvalidArgument, "unknown auth action"), *jsonMode)
	}
	if *directory == "" {
		var err error
		*directory, err = defaultAuthDirectory()
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if !fileguard.Disjoint(cwd, *directory) {
		return report(out, errout, c.Fail(c.PolicyDenied, "credential directory must be outside current source workspace"), *jsonMode)
	}
	if action == "migrate-storage" {
		view, err := auth.MigrateLegacy(*directory)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		return statusAuth(out, view)
	}
	store, err := auth.Open(*directory, action == "login")
	if errors.Is(err, os.ErrNotExist) && (action == "status" || action == "profiles") {
		return printAuthView(out, auth.View{SchemaVersion: 1, Provider: "chatgpt", Profiles: []auth.ProfileView{}, ManageUsage: "https://chatgpt.com/settings/usage"}, *jsonMode)
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer store.Close()
	ctx, cancelSignal := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancelSignal()
	ctx, cancelTimeout := context.WithTimeout(ctx, time.Duration(*timeout)*time.Second)
	defer cancelTimeout()
	client := auth.NewClient()
	defer client.Close()
	switch action {
	case "login":
		if !*jsonMode {
			fmt.Fprintln(errout, "Continue with ChatGPT in your browser. Choose your account/workspace and review plan usage permission, then return here. Ctrl+C cancels sign-in.")
		}
		err = client.Login(ctx, store, *profile, func(url string) error { return openAuthBrowser(ctx, url) })
	case "select":
		if *profile == "" {
			err = c.Fail(c.InvalidArgument, "select requires --profile")
		} else {
			err = store.Select(*profile)
		}
	case "models":
		catalog, err := client.Models(ctx, store, *profile)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if _, err = out.Write(append(catalog, '\n')); err != nil {
			return 4
		}
		return 0
	case "logout":
		confirmed, err := client.Logout(ctx, store, *profile)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if !*jsonMode {
			fmt.Fprintf(out, "Local sign-out complete. Remote revocation confirmed: %t. Manage permissions at https://chatgpt.com/settings/usage\n", confirmed)
			return printAuthView(out, store.View(), false)
		}
		return statusAuth(out, struct {
			Status                    auth.View `json:"status"`
			RemoteRevocationConfirmed bool      `json:"remote_revocation_confirmed"`
		}{store.View(), confirmed})
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	return printAuthView(out, store.View(), *jsonMode)
}

func printAuthView(out io.Writer, view auth.View, jsonMode bool) int {
	if jsonMode {
		return statusAuth(out, view)
	}
	if len(view.Profiles) == 0 {
		_, err := fmt.Fprintln(out, "No ChatGPT account connected. Run: viber auth login")
		if err != nil {
			return 4
		}
		return 0
	}
	for _, profile := range view.Profiles {
		status := "sign-in required"
		if profile.Connected {
			status = "connected; plan permission required"
		}
		if profile.Connected && profile.PlanUsage {
			status = "connected; ChatGPT plan usage enabled"
		}
		active := ""
		if profile.Active {
			active = " · active"
		}
		if _, err := fmt.Fprintf(out, "%s · %s%s\n", strconv.QuoteToASCII(profile.ID), status, active); err != nil {
			return 4
		}
	}
	_, err := fmt.Fprintln(out, "Available models: viber auth models --json\nUsage settings: https://chatgpt.com/settings/usage")
	if err != nil {
		return 4
	}
	return 0
}
func statusAuth(out io.Writer, value any) int {
	if err := jsonWrite(out, value); err != nil {
		return 4
	}
	return 0
}
