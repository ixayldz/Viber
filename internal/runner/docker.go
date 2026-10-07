// Package runner implements a narrow offline, read-only Docker computation
// profile. Engine access stays in the trusted host broker. This is a developer
// backend pending the full PRD escape/conformance suite, never a host fallback.
package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type Profile struct {
	SchemaVersion  int    `json:"schema_version"`
	Image          string `json:"image"`
	MemoryBytes    int64  `json:"memory_bytes"`
	Pids           int64  `json:"pids"`
	CPUs           int64  `json:"cpus"`
	ScratchBytes   int64  `json:"scratch_bytes"`
	TimeoutSeconds int64  `json:"timeout_seconds"`
	MaxOutputBytes int64  `json:"max_output_bytes"`
}

var pinnedImage = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:\-]*@sha256:[a-f0-9]{64}$`)

func (p Profile) Validate() error {
	if p.SchemaVersion != c.SchemaVersion || !pinnedImage.MatchString(p.Image) || p.MemoryBytes < 64<<20 || p.MemoryBytes > 8<<30 || p.Pids < 8 || p.Pids > 512 || p.CPUs < 1 || p.CPUs > 8 || p.ScratchBytes < 1<<20 || p.ScratchBytes > 1<<30 || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 900 || p.MaxOutputBytes < 1024 || p.MaxOutputBytes > 8<<20 {
		return c.Fail(c.InvalidArgument, "invalid pinned offline sandbox profile")
	}
	return nil
}
func DefaultProfile(image string) Profile {
	return Profile{SchemaVersion: 1, Image: image, MemoryBytes: 512 << 20, Pids: 64, CPUs: 1, ScratchBytes: 128 << 20, TimeoutSeconds: 60, MaxOutputBytes: 1 << 20}
}

type Invocation struct {
	CandidateDigest string   `json:"candidate_digest"`
	Argv            []string `json:"argv"`
}
type Result struct {
	SchemaVersion          int    `json:"schema_version"`
	CandidateDigest        string `json:"candidate_digest"`
	ProfileDigest          string `json:"profile_digest"`
	InvocationDigest       string `json:"invocation_digest"`
	ContainerID            string `json:"container_id"`
	ExitCode               int    `json:"exit_code"`
	TimedOut               bool   `json:"timed_out"`
	Cancelled              bool   `json:"cancelled"`
	OOMKilled              bool   `json:"oom_killed"`
	OutputTruncated        bool   `json:"output_truncated"`
	Stdout                 []byte `json:"stdout"`
	Stderr                 []byte `json:"stderr"`
	SourceReadOnly         bool   `json:"source_read_only"`
	ProtectedResultChannel bool   `json:"protected_result_channel"`
	ProcessTreeQuiescent   bool   `json:"process_tree_quiescent"`
	DurationMillis         int64  `json:"duration_millis"`
}
type Docker struct {
	binary string
	config string
	mu     sync.Mutex
	closed bool
}

func OpenDocker() (result *Docker, err error) {
	binary, err := exec.LookPath("docker")
	if err != nil {
		return nil, c.Fail(c.UnsupportedCapability, "Docker broker unavailable")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, err
	}
	config, err := os.MkdirTemp("", "viber-docker-config-")
	if err != nil {
		return nil, err
	}
	return &Docker{binary: binary, config: config}, nil
}
func (d *Docker) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	// Only an empty, privately created config directory is removed.
	return os.Remove(d.config)
}
func (d *Docker) command(ctx context.Context, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, d.binary, append([]string{"--config", d.config}, args...)...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "SystemRoot=" + os.Getenv("SystemRoot"), "HOME=" + d.config, "USERPROFILE=" + d.config, "DOCKER_CLI_HINTS=false"}
	command.WaitDelay = 2 * time.Second
	return command
}
func (d *Docker) control(ctx context.Context, args ...string) ([]byte, error) {
	var out boundedOutput
	out.limit = 1 << 20
	command := d.command(ctx, args...)
	command.Stdout = &out
	command.Stderr = &out
	err := command.Run()
	if err != nil {
		return nil, c.Fail(c.UnsupportedCapability, "Docker control operation failed: "+string(out.Bytes()))
	}
	if out.Truncated() {
		return nil, c.Fail(c.UnsupportedCapability, "Docker control response exceeded quota")
	}
	return out.Bytes(), nil
}
func (d *Docker) Check(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return os.ErrClosed
	}
	return d.check(ctx)
}
func (d *Docker) check(ctx context.Context) error {
	raw, err := d.control(ctx, "info", "--format", "{{json .}}")
	if err != nil {
		return err
	}
	var info struct {
		OSType          string
		SecurityOptions []string
	}
	if err = json.Unmarshal(raw, &info); err != nil {
		return c.Fail(c.UnsupportedCapability, "unreadable Docker capability response")
	}
	if info.OSType != "linux" {
		return c.Fail(c.UnsupportedCapability, "Linux container engine required")
	}
	seccomp := false
	for _, option := range info.SecurityOptions {
		if strings.Contains(option, "name=seccomp") && strings.Contains(option, "profile=builtin") {
			seccomp = true
		}
	}
	if !seccomp {
		return c.Fail(c.UnsupportedCapability, "tested built-in seccomp unavailable")
	}
	return nil
}

type inspected struct {
	ID    string `json:"Id"`
	State struct {
		Running   bool
		ExitCode  int
		OOMKilled bool
		Error     string
	}
	Config     struct{ User string }
	HostConfig struct {
		Privileged     bool
		ReadonlyRootfs bool
		NetworkMode    string
		CapDrop        []string
		CapAdd         []string
		SecurityOpt    []string
		Memory         int64
		MemorySwap     int64
		PidsLimit      int64
		NanoCpus       int64
	}
	Mounts []struct {
		Destination string
		RW          bool
		Type        string
	}
}

func (d *Docker) inspect(ctx context.Context, id string) (inspected, error) {
	raw, err := d.control(ctx, "inspect", "--type", "container", id)
	if err != nil {
		return inspected{}, err
	}
	var values []inspected
	if err = json.Unmarshal(raw, &values); err != nil || len(values) != 1 || values[0].ID != id {
		return inspected{}, c.Fail(c.UnsupportedCapability, "protected inspect binding mismatch")
	}
	return values[0], nil
}
func validateInspect(info inspected, p Profile) error {
	h := info.HostConfig
	if info.Config.User != "65532:65532" || h.Privileged || !h.ReadonlyRootfs || h.NetworkMode != "none" || len(h.CapAdd) != 0 || len(h.CapDrop) != 1 || !strings.EqualFold(h.CapDrop[0], "ALL") || h.Memory != p.MemoryBytes || h.MemorySwap != p.MemoryBytes || h.PidsLimit != p.Pids || h.NanoCpus != p.CPUs*1e9 {
		return c.Fail(c.UnsupportedCapability, "engine did not enforce requested sandbox profile")
	}
	nnp, seccomp := false, false
	for _, option := range h.SecurityOpt {
		if option == "no-new-privileges=true" || option == "no-new-privileges" {
			nnp = true
		}
		if option == "seccomp=builtin" {
			seccomp = true
		}
	}
	if !nnp || !seccomp {
		return c.Fail(c.UnsupportedCapability, "sandbox security option mismatch")
	}
	source := false
	for _, mount := range info.Mounts {
		switch mount.Destination {
		case "/workspace":
			if mount.Type != "bind" || mount.RW {
				return c.Fail(c.PolicyDenied, "source mount is writable")
			}
			source = true
		case "/tmp":
			if mount.Type != "tmpfs" {
				return c.Fail(c.PolicyDenied, "unexpected scratch mount")
			}
		default:
			return c.Fail(c.PolicyDenied, "unexpected container mount")
		}
	}
	if !source {
		return c.Fail(c.PolicyDenied, "source mount missing")
	}
	return nil
}
func (d *Docker) Run(ctx context.Context, source string, p Profile, inv Invocation) (result Result, resultErr error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return result, os.ErrClosed
	}
	if err := p.Validate(); err != nil {
		return result, err
	}
	if !c.ValidDigest(inv.CandidateDigest) || len(inv.Argv) == 0 || len(inv.Argv) > 128 {
		return result, c.Fail(c.InvalidArgument, "invalid computation invocation")
	}
	for _, arg := range inv.Argv {
		if len(arg) > 16384 || strings.IndexByte(arg, 0) >= 0 {
			return result, c.Fail(c.InvalidArgument, "invalid argv")
		}
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return result, err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return result, err
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return result, c.Fail(c.InvalidArgument, "candidate source directory required")
	}
	if strings.ContainsAny(absolute, ",\r\n") {
		return result, c.Fail(c.UnsupportedCapability, "candidate path not representable in mount protocol")
	}
	if err = d.check(ctx); err != nil {
		return result, err
	}
	// Never pull an image implicitly. Profile/environment publication is a
	// separate, authorized network effect; this broker is always offline.
	if _, err = d.control(ctx, "image", "inspect", p.Image); err != nil {
		return result, c.Fail(c.UnsupportedCapability, "pinned environment image not installed")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return result, err
	}
	name := "viber-" + hex.EncodeToString(nonce[:])
	args := []string{"create", "--pull=never", "--name", name, "--label", "io.viber.runtime=offline-v1", "--user", "65532:65532", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=builtin", "--pids-limit", fmt.Sprint(p.Pids), "--memory", fmt.Sprint(p.MemoryBytes), "--memory-swap", fmt.Sprint(p.MemoryBytes), "--cpus", fmt.Sprint(p.CPUs), "--ipc", "private", "--init", "--workdir", "/workspace", "--env", "HOME=/tmp", "--env", "TMPDIR=/tmp", "--env", "GOCACHE=/tmp/go-build", "--env", "GOMODCACHE=/tmp/go-mod", "--env", "GOTOOLCHAIN=local", "--env", "GOPROXY=off", "--env", "GOSUMDB=off", "--mount", "type=bind,src=" + absolute + ",dst=/workspace,readonly,bind-propagation=rprivate", "--tmpfs", fmt.Sprintf("/tmp:rw,exec,nosuid,nodev,size=%d,mode=1777", p.ScratchBytes), "--entrypoint", inv.Argv[0], p.Image}
	args = append(args, inv.Argv[1:]...)
	raw, err := d.control(ctx, args...)
	// A lost create response has an unknown effect: reconcile by our unique name,
	// never retry create. Cleanup cannot depend on a cancelled invocation context.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cleanupCancel()
	id := strings.TrimSpace(string(raw))
	if err != nil {
		reconciled, reconcileErr := d.control(cleanupCtx, "inspect", "--type", "container", "--format", "{{.Id}}", name)
		if reconcileErr == nil {
			id = strings.TrimSpace(string(reconciled))
		}
		if validContainerID(id) {
			_, cleanupErr := d.control(cleanupCtx, "rm", "--force", id)
			return result, errors.Join(err, cleanupErr)
		}
		return result, err
	}
	if !validContainerID(id) {
		return result, c.Fail(c.UnsupportedCapability, "unbound container identity")
	}
	result = Result{SchemaVersion: c.SchemaVersion, CandidateDigest: inv.CandidateDigest, ContainerID: id, ExitCode: -1}
	result.ProfileDigest, err = c.Digest(p)
	if err != nil {
		return result, err
	}
	result.InvocationDigest, err = c.Digest(inv)
	if err != nil {
		return result, err
	}
	defer func() {
		removeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, removeErr := d.control(removeCtx, "rm", "--force", id)
		if removeErr != nil {
			resultErr = errors.Join(resultErr, removeErr)
			return
		}
		result.ProcessTreeQuiescent = true
	}()
	initial, err := d.inspect(ctx, id)
	if err != nil {
		return result, err
	}
	if err = validateInspect(initial, p); err != nil {
		return result, err
	}
	result.SourceReadOnly = true
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()
	stdout, stderr := &boundedOutput{limit: p.MaxOutputBytes}, &boundedOutput{limit: p.MaxOutputBytes}
	stdout.onLimit = cancel
	stderr.onLimit = cancel
	command := d.command(runCtx, "start", "--attach", id)
	command.Stdout = stdout
	command.Stderr = stderr
	started := time.Now()
	runErr := command.Run()
	result.DurationMillis = time.Since(started).Milliseconds()
	result.Stdout = stdout.Bytes()
	result.Stderr = stderr.Bytes()
	result.OutputTruncated = stdout.Truncated() || stderr.Truncated()
	result.TimedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
	result.Cancelled = ctx.Err() != nil
	if runCtx.Err() != nil || runErr != nil {
		killCtx, killCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = d.control(killCtx, "kill", id)
		killCancel()
	}
	finalCtx, finalCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer finalCancel()
	final, err := d.inspect(finalCtx, id)
	if err != nil {
		return result, err
	}
	if final.State.Running {
		return result, c.Fail(c.UnsupportedCapability, "container did not quiesce")
	}
	if err = validateInspect(final, p); err != nil {
		return result, err
	}
	if final.State.Error != "" {
		return result, c.Fail(c.UnsupportedCapability, "engine could not execute sandbox computation")
	}
	result.ExitCode = final.State.ExitCode
	result.OOMKilled = final.State.OOMKilled
	result.ProtectedResultChannel = true
	// Nonzero candidate exit is a recorded computation outcome, not a broker
	// error and never a verification verdict inferred from stdout.
	return result, nil
}
func validContainerID(id string) bool {
	if len(id) != 64 {
		return false
	}
	raw, err := hex.DecodeString(id)
	return err == nil && hex.EncodeToString(raw) == id
}

type boundedOutput struct {
	mu        sync.Mutex
	limit     int64
	data      []byte
	truncated bool
	onLimit   func()
}

func (b *boundedOutput) Write(raw []byte) (int, error) {
	b.mu.Lock()
	left := b.limit - int64(len(b.data))
	take := int64(len(raw))
	if take > left {
		take = left
	}
	if take > 0 {
		b.data = append(b.data, raw[:take]...)
	}
	hit := int64(len(raw)) > left && !b.truncated
	b.truncated = b.truncated || int64(len(raw)) > left
	callback := b.onLimit
	b.mu.Unlock()
	if hit && callback != nil {
		callback()
	}
	return len(raw), nil
}
func (b *boundedOutput) Bytes() []byte   { b.mu.Lock(); defer b.mu.Unlock(); return bytes.Clone(b.data) }
func (b *boundedOutput) Truncated() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.truncated }

var _ io.Writer = (*boundedOutput)(nil)
