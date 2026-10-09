//go:build !windows

package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type commandVault struct{}

func (commandVault) Backend() string {
	switch runtime.GOOS {
	case "linux":
		return "LINUX_SECRET_SERVICE"
	case "darwin":
		return "MACOS_KEYCHAIN"
	default:
		return "UNSUPPORTED_OS_VAULT"
	}
}

type vaultOutput struct {
	bytes.Buffer
	overflow bool
	limit    int
}

func (w *vaultOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := max(0, w.limit-w.Len())
	if n > remaining {
		w.overflow = true
	}
	w.Buffer.Write(p[:min(n, remaining)])
	return n, nil
}

type vaultResponse struct {
	Output []byte
	Stderr bool
	Exit   int
	Err    error
}

var runVaultHelper = execVaultHelper

func execVaultHelper(executable string, args []string, input []byte) vaultResponse {
	response := vaultResponse{Exit: -1}
	// Fixed system executable only. Repo/PATH/environment cannot select a vault.
	expected := "/usr/bin/secret-tool"
	if runtime.GOOS == "darwin" {
		expected = "/usr/bin/security"
	}
	if executable != expected || (runtime.GOOS != "linux" && runtime.GOOS != "darwin") {
		response.Err = c.Fail(c.UnsupportedCapability, "supported OS vault helper unavailable")
		return response
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		response.Err = err
		return response
	}
	info, err := os.Stat(resolved)
	if err != nil {
		response.Err = err
		return response
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		response.Err = c.Fail(c.PolicyDenied, "OS vault helper must be a root-owned nonwritable system executable")
		return response
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, resolved, args...)
	command.Stdin = bytes.NewReader(input)
	stdout, stderr := &vaultOutput{limit: 512}, &vaultOutput{limit: 512}
	command.Stdout = stdout
	command.Stderr = stderr
	// Locale stabilizes the tool protocol. No secret is placed in argv or env.
	command.Env = vaultEnvironment(os.Environ())
	err = command.Run()
	response.Output = append([]byte{}, stdout.Bytes()...)
	response.Stderr = stderr.Len() > 0 || stderr.overflow
	if stdout.overflow || stderr.overflow || ctx.Err() != nil {
		response.Err = c.Fail(c.UnsupportedCapability, "OS vault helper unavailable or exceeded bounded protocol")
		return response
	}
	if err == nil {
		response.Exit = 0
		return response
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		response.Exit = exit.ExitCode()
		return response
	}
	response.Err = c.Fail(c.UnsupportedCapability, "OS vault helper could not start")
	return response
}

// Desktop IPC identity is operator input. Loader, proxy, language, shell and
// executable selection variables never reach a credential helper.
func vaultEnvironment(source []string) []string {
	env := []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	for _, key := range []string{"HOME", "USER", "LOGNAME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		for i := len(source) - 1; i >= 0; i-- {
			prefix := key + "="
			if strings.HasPrefix(source[i], prefix) {
				value := strings.TrimPrefix(source[i], prefix)
				if value != "" && len(value) <= 4096 && !strings.ContainsAny(value, "\x00\r\n") {
					env = append(env, prefix+value)
				}
				break
			}
		}
	}
	return env
}
func (v commandVault) Get(handle string) ([]byte, error) {
	if !c.ValidDigest(handle) {
		return nil, c.Fail(c.InvalidArgument, "invalid OS vault handle")
	}
	executable := "/usr/bin/secret-tool"
	args := []string{"lookup", "service", "com.viber.auth.v2", "account", handle}
	if runtime.GOOS == "darwin" {
		executable = "/usr/bin/security"
		args = []string{"find-generic-password", "-s", "com.viber.auth.v2", "-a", handle, "-w"}
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, c.Fail(c.UnsupportedCapability, "OS credential vault not supported")
	}
	result := runVaultHelper(executable, args, nil)
	if result.Err != nil {
		return nil, c.Fail(c.UnsupportedCapability, "OS credential vault unavailable")
	}
	defer clear(result.Output)
	if runtime.GOOS == "linux" && result.Exit == 1 && !result.Stderr && len(result.Output) == 0 || runtime.GOOS == "darwin" && result.Exit == 44 && len(result.Output) == 0 {
		return nil, errVaultKeyMissing
	}
	if result.Exit != 0 {
		return nil, c.Fail(c.UnsupportedCapability, "OS credential vault locked or unavailable")
	}
	encoded := bytes.TrimSuffix(result.Output, []byte("\n"))
	key, err := base64.StdEncoding.Strict().DecodeString(string(encoded))
	if err != nil || len(key) != 32 {
		clear(key)
		return nil, c.Fail(c.StoreIntegrityError, "invalid OS vault key material")
	}
	return key, nil
}
func (v commandVault) Put(handle string, key []byte) error {
	if !c.ValidDigest(handle) || len(key) != 32 {
		return c.Fail(c.InvalidArgument, "invalid OS vault key")
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(key))
	defer clear(encoded)
	executable := "/usr/bin/secret-tool"
	args := []string{"store", "--label", "Viber credential encryption key", "service", "com.viber.auth.v2", "account", handle}
	input := encoded
	if runtime.GOOS == "darwin" {
		executable = "/usr/bin/security"
		args = []string{"-q", "-i"}
		// Apple documents interactive stdin commands. All operands are static or
		// restricted digest/base64 alphabets; there is no shell or arbitrary input.
		input = append([]byte("add-generic-password -s com.viber.auth.v2 -a "+handle+" -w "), encoded...)
		input = append(input, '\n')
		defer clear(input)
	}
	result := runVaultHelper(executable, args, input)
	defer clear(result.Output)
	if result.Err != nil || result.Exit != 0 {
		return c.Fail(c.UnsupportedCapability, "OS vault did not accept the key")
	}
	// Interactive security may finish at EOF despite a failed subcommand.
	// protect always reads back and compares the key before publishing ciphertext.
	return nil
}

var _ io.Writer = (*vaultOutput)(nil)
