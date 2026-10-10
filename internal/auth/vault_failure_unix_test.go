//go:build !windows

package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestVaultResponseFailureMatrixNeverAdoptsNoisyOrNoncanonicalKey(t *testing.T) {
	original := runVaultHelper
	defer func() { runVaultHelper = original }()
	key := bytes.Repeat([]byte{9}, 32)
	encoded := base64.StdEncoding.EncodeToString(key)
	missingExit := 1
	if runtime.GOOS == "darwin" {
		missingExit = 44
	}
	cases := []struct {
		name             string
		response         vaultResponse
		missing, success bool
	}{
		{"missing", vaultResponse{Exit: missingExit}, true, false},
		{"native-missing-diagnostic", vaultResponse{Exit: missingExit, Stderr: true}, runtime.GOOS == "darwin", false},
		{"locked", vaultResponse{Exit: 2, Stderr: true}, false, false},
		{"helper-error", vaultResponse{Err: errors.New("fixture secret must not be exposed"), Output: []byte(encoded)}, false, false},
		{"truncated-success", vaultResponse{Exit: 0, Completed: true, OutputTruncated: true, Output: []byte(encoded)}, false, false},
		{"successful-exit-with-error", vaultResponse{Exit: 0, Stderr: true, Output: []byte(encoded)}, false, false},
		{"missing-with-output", vaultResponse{Exit: missingExit, Output: []byte(encoded)}, false, false},
		{"empty-success", vaultResponse{Exit: 0}, false, false},
		{"short-key", vaultResponse{Exit: 0, Output: []byte(base64.StdEncoding.EncodeToString(key[:31]))}, false, false},
		{"long-key", vaultResponse{Exit: 0, Output: []byte(base64.StdEncoding.EncodeToString(append(key, 1)))}, false, false},
		{"embedded-newline", vaultResponse{Exit: 0, Output: []byte(encoded[:8] + "\n" + encoded[8:])}, false, false},
		{"carriage-return", vaultResponse{Exit: 0, Output: []byte(encoded + "\r\n")}, false, false},
		{"multiple-newlines", vaultResponse{Exit: 0, Output: []byte(encoded + "\n\n")}, false, false},
		{"canonical", vaultResponse{Exit: 0, Output: []byte(encoded)}, false, true},
		{"canonical-single-newline", vaultResponse{Exit: 0, Output: []byte(encoded + "\n")}, false, true},
	}
	for _, fixture := range cases {
		t.Run(fixture.name, func(t *testing.T) {
			result := fixture.response
			result.Output = append([]byte{}, result.Output...)
			runVaultHelper = func(string, []string, []byte) vaultResponse { return result }
			actual, err := (commandVault{}).Get(c.HashBytes([]byte("bounded-vault-fixture")))
			defer clear(actual)
			if (err == nil) != fixture.success || errors.Is(err, errVaultKeyMissing) != fixture.missing || fixture.success && !bytes.Equal(actual, key) {
				t.Fatal("wrong vault failure classification", err)
			}
			if err != nil && strings.Contains(err.Error(), "fixture secret") {
				t.Fatal("helper diagnostic leaked")
			}
			if !bytes.Equal(result.Output, make([]byte, len(result.Output))) {
				t.Fatal("retained helper key buffer was not cleared on return")
			}
		})
	}
}

type vaultFixtureInfo struct {
	mode os.FileMode
	uid  uint32
}

func (i vaultFixtureInfo) Name() string       { return "fixture" }
func (i vaultFixtureInfo) Size() int64        { return 1 }
func (i vaultFixtureInfo) Mode() os.FileMode  { return i.mode }
func (i vaultFixtureInfo) ModTime() time.Time { return time.Time{} }
func (i vaultFixtureInfo) IsDir() bool        { return i.mode.IsDir() }
func (i vaultFixtureInfo) Sys() any           { return &syscall.Stat_t{Uid: i.uid} }

func TestVaultExecutableTrustIncludesEveryResolvedAncestor(t *testing.T) {
	const selected = "/usr/bin/vault-fixture"
	for _, attack := range []string{"none", "writable-file", "nonexecutable", "owned-file", "writable-bin", "owned-usr", "symlink-bin", "missing-parent", "writable-root"} {
		t.Run(attack, func(t *testing.T) {
			inspect := func(path string) (os.FileInfo, error) {
				info := vaultFixtureInfo{mode: os.ModeDir | 0755}
				if path == selected {
					info.mode = 0755
				}
				switch {
				case attack == "writable-file" && path == selected:
					info.mode = 0775
				case attack == "nonexecutable" && path == selected:
					info.mode = 0644
				case attack == "owned-file" && path == selected:
					info.uid = 1000
				case attack == "writable-bin" && path == "/usr/bin":
					info.mode |= 0020
				case attack == "owned-usr" && path == "/usr":
					info.uid = 1000
				case attack == "symlink-bin" && path == "/usr/bin":
					info.mode = os.ModeSymlink | 0755
				case attack == "missing-parent" && path == "/usr":
					return nil, os.ErrNotExist
				case attack == "writable-root" && path == "/":
					info.mode |= 0002
				}
				return info, nil
			}
			if err := trustedVaultPath(selected, inspect); (err == nil) != (attack == "none") {
				t.Fatal("unsafe executable namespace admitted", err)
			}
		})
	}
}

func TestVaultProcessHelper(t *testing.T) {
	marker := -1
	for i, arg := range os.Args {
		if arg == "vault-process-fixture" {
			marker = i
			break
		}
	}
	if marker < 0 || marker+1 >= len(os.Args) {
		t.Skip("real subprocess helper")
	}
	switch os.Args[marker+1] {
	case "overflow":
		os.Stdout.Write(bytes.Repeat([]byte{'x'}, 64<<10))
		os.Stderr.Write(bytes.Repeat([]byte{'y'}, 64<<10))
		os.Exit(0)
	case "timeout", "holder":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	case "inherited-pipe":
		exe, err := os.Executable()
		if err != nil {
			os.Exit(90)
		}
		child := exec.Command(exe, "-test.run=^TestVaultProcessHelper$", "--", "vault-process-fixture", "holder")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err = child.Start(); err != nil {
			os.Exit(91)
		}
		fmt.Fprintf(os.Stdout, "holder=%d\n", child.Process.Pid)
		os.Exit(0)
	}
	os.Exit(92)
}

func TestVaultProcessBoundsTimeoutOverflowAndInheritedPipeDrain(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"overflow", "timeout", "inherited-pipe"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if mode == "timeout" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			}
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestVaultProcessHelper$", "--", "vault-process-fixture", mode)
			start := time.Now()
			result := runVaultProcess(ctx, cmd, nil)
			defer clear(result.Output)
			if mode == "overflow" && (len(result.Output) != 512 || !result.Stderr) {
				t.Fatal("real helper did not exercise both bounded output pipes")
			}
			if mode == "overflow" && (!result.Completed || !result.OutputTruncated || result.Exit != 0) {
				t.Fatal("truncated output lost natural command completion facts")
			}
			if mode == "timeout" && ctx.Err() == nil {
				t.Fatal("real helper did not exercise deadline cancellation")
			}
			if mode == "timeout" && result.Completed || mode == "inherited-pipe" && result.Completed {
				t.Fatal("interrupted helper claimed natural completion")
			}
			if mode == "inherited-pipe" {
				var pid int
				if _, err := fmt.Sscanf(string(result.Output), "holder=%d", &pid); err != nil || pid <= 1 {
					t.Fatal("fixture holder identity missing")
				}
				process, err := os.FindProcess(pid)
				if err != nil {
					t.Fatal(err)
				}
				// This fixture's child is explicitly cleaned up. WaitDelay itself does
				// not claim that arbitrary descendants have been fenced or settled.
				defer process.Release()
				if err = process.Kill(); err != nil {
					t.Fatal("fixture child cleanup", err)
				}
			}
			if result.Err == nil || len(result.Output) > 512 || time.Since(start) > 3*time.Second {
				t.Fatal("helper protocol was not bounded", result.Exit, result.Err)
			}
		})
	}
}

func TestVaultRotationFailureKeepsExistingCiphertextAndAccountIdentity(t *testing.T) {
	original := osCredentialVault
	defer func() { osCredentialVault = original }()
	for _, failure := range []string{"locked", "missing", "invalid-key"} {
		t.Run(failure, func(t *testing.T) {
			vault := &fixtureVault{keys: map[string][]byte{}}
			osCredentialVault = vault
			directory := t.TempDir()
			store, err := Open(directory, true)
			if err != nil {
				t.Fatal(err)
			}
			identity := store.data.HostID
			profile := Profile{Issuer: "https://auth.openai.com", ClientID: "viber-client-one", Subject: "offline-vault-subject", AccessToken: "fixture-retained-access", RefreshToken: "fixture-retained-refresh", Scopes: []string{"resource.invoke", "chatgpt.tokens.use.direct"}, ExpiresAt: time.Now().Unix() + 3600}
			profile.ID, _ = c.Digest(struct{ Issuer, ClientID, Subject string }{profile.Issuer, profile.ClientID, profile.Subject})
			if err = store.put(profile); err != nil {
				store.Close()
				t.Fatal(err)
			}
			store.data.PendingClient = "viber-client-one"
			if err = store.save(); err != nil {
				store.Close()
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(directory, "credentials.bin"))
			if err != nil {
				store.Close()
				t.Fatal(err)
			}
			handle := vaultHandle(identity)
			retained := append([]byte{}, vault.keys[handle]...)
			defer clear(retained)
			switch failure {
			case "locked":
				vault.unavailable = true
			case "missing":
				delete(vault.keys, handle)
			case "invalid-key":
				vault.keys[handle] = []byte{1}
			}
			store.data.PendingClient = "viber-client-two"
			store.data.Profiles[0].AccessToken = "fixture-rotated-access"
			store.data.Profiles[0].RefreshToken = "fixture-rotated-refresh"
			if err = store.save(); err == nil {
				store.Close()
				t.Fatal("failed vault allowed rotation")
			}
			if failure == "missing" && len(vault.keys) != 0 {
				store.Close()
				t.Fatal("missing key was regenerated for an existing account")
			}
			store.Close()
			after, err := os.ReadFile(filepath.Join(directory, "credentials.bin"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed rotation changed retained ciphertext")
			}
			vault.unavailable = false
			vault.keys[handle] = retained
			reopened, err := Open(directory, false)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if reopened.data.HostID != identity || reopened.data.PendingClient != "viber-client-one" {
				t.Fatal("failed rotation changed durable identity")
			}
			retainedProfile, err := reopened.profile(profile.ID)
			if err != nil || retainedProfile == nil {
				t.Fatal("retained account missing", err)
			}
			if retainedProfile.AccessToken != profile.AccessToken || retainedProfile.RefreshToken != profile.RefreshToken || reopened.data.Active != profile.ID {
				t.Fatal("failed rotation changed retained tokens or selected account")
			}
		})
	}
}
