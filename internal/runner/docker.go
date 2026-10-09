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
	Stdin           *[]byte  `json:"stdin,omitempty"`
	CandidateDigest string   `json:"candidate_digest"`
	Argv            []string `json:"argv"`
}
type Result struct {
	Ownership              *Capsule `json:"ownership,omitempty"`
	SchemaVersion          int      `json:"schema_version"`
	CandidateDigest        string   `json:"candidate_digest"`
	ProfileDigest          string   `json:"profile_digest"`
	InvocationDigest       string   `json:"invocation_digest"`
	ContainerID            string   `json:"container_id"`
	ExitCode               int      `json:"exit_code"`
	TimedOut               bool     `json:"timed_out"`
	Cancelled              bool     `json:"cancelled"`
	OOMKilled              bool     `json:"oom_killed"`
	OutputTruncated        bool     `json:"output_truncated"`
	Stdout                 []byte   `json:"stdout"`
	Stderr                 []byte   `json:"stderr"`
	SourceReadOnly         bool     `json:"source_read_only"`
	ProtectedResultChannel bool     `json:"protected_result_channel"`
	ProcessTreeQuiescent   bool     `json:"process_tree_quiescent"`
	DurationMillis         int64    `json:"duration_millis"`
}
type Docker struct {
	binary      string
	config      string
	endpoint    string
	engineID    string
	mu          sync.Mutex
	closed      bool
	controlHook func(context.Context, ...string) ([]byte, error)
}

func OpenDocker() (result *Docker, err error) {
	endpoint, err := localEndpoint(os.Getenv("VIBER_DOCKER_HOST"))
	if err != nil {
		return nil, err
	}
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
	return &Docker{binary: binary, config: config, endpoint: endpoint}, nil
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
	prefix := []string{"--config", d.config}
	if d.endpoint != "" {
		prefix = append(prefix, "--host", d.endpoint)
	}
	command := exec.CommandContext(ctx, d.binary, append(prefix, args...)...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "SystemRoot=" + os.Getenv("SystemRoot"), "HOME=" + d.config, "USERPROFILE=" + d.config, "DOCKER_CLI_HINTS=false"}
	command.WaitDelay = 2 * time.Second
	return command
}
func (d *Docker) control(ctx context.Context, args ...string) ([]byte, error) {
	if d.controlHook != nil {
		return d.controlHook(ctx, args...)
	}
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
func (d *Docker) check(ctx context.Context) error { _, err := d.backend(ctx); return err }

type inspectedMount struct {
	Destination string
	Source      string
	RW          bool
	Type        string
	Propagation string
}
type inspected struct {
	ID    string `json:"Id"`
	Name  string
	State struct {
		Status    string
		Running   bool
		ExitCode  int
		OOMKilled bool
		Error     string
	}
	Config struct {
		Labels     map[string]string
		OpenStdin  bool
		User       string
		Image      string
		WorkingDir string
		Entrypoint []string
		Cmd        []string
	}
	HostConfig struct {
		Privileged     bool
		PidMode        string
		IpcMode        string
		Init           *bool
		Tmpfs          map[string]string
		Binds          []string
		VolumesFrom    []string
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
	Mounts []inspectedMount
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
func validateInspect(info inspected, p Profile, source string, inv Invocation) error {
	if info.Config.OpenStdin != (inv.Stdin != nil) || info.Config.Image != p.Image || info.Config.WorkingDir != "/workspace" || len(info.Config.Entrypoint) != 1 || len(inv.Argv) == 0 || info.Config.Entrypoint[0] != inv.Argv[0] || len(info.Config.Cmd) != len(inv.Argv)-1 {
		return c.Fail(c.PolicyDenied, "container image or invocation binding mismatch")
	}
	for i, arg := range info.Config.Cmd {
		if arg != inv.Argv[i+1] {
			return c.Fail(c.PolicyDenied, "container argv binding mismatch")
		}
	}
	h := info.HostConfig
	if h.PidMode != "" || h.IpcMode != "private" || h.Init == nil || !*h.Init || len(h.Binds) != 0 || len(h.VolumesFrom) != 0 || len(h.Tmpfs) != 1 || h.Tmpfs["/tmp"] != scratchOptions(p) {
		return c.Fail(c.PolicyDenied, "unexpected process namespace, mount or scratch configuration")
	}
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
	seen := map[string]bool{}
	for _, mount := range info.Mounts {
		if seen[mount.Destination] {
			return c.Fail(c.PolicyDenied, "duplicate container mount")
		}
		seen[mount.Destination] = true
		switch mount.Destination {
		case "/workspace":
			if mount.Type != "bind" || mount.RW || mount.Source != source || mount.Propagation != "rprivate" {
				return c.Fail(c.PolicyDenied, "source mount identity or readonly enforcement mismatch")
			}
		case "/tmp":
			if mount.Type != "tmpfs" || !mount.RW || mount.Source != "" {
				return c.Fail(c.PolicyDenied, "unexpected scratch mount")
			}
		default:
			return c.Fail(c.PolicyDenied, "unexpected container mount")
		}
	}
	if !seen["/workspace"] {
		return c.Fail(c.PolicyDenied, "source mount missing")
	}
	return nil
}
func scratchOptions(p Profile) string {
	return fmt.Sprintf("rw,exec,nosuid,nodev,size=%d,mode=1777", p.ScratchBytes)
}
func (d *Docker) Run(ctx context.Context, source string, p Profile, inv Invocation) (result Result, resultErr error) {
	dispatched := false
	owner, ownerErr := activeOwnership(ctx)
	if ownerErr != nil {
		return result, &DispatchFailure{Cause: ownerErr, EffectPossible: false}
	}
	defer func() {
		if resultErr != nil {
			resultErr = &DispatchFailure{Cause: resultErr, EffectPossible: dispatched}
		}
	}()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return result, os.ErrClosed
	}
	if err := p.Validate(); err != nil {
		return result, err
	}
	if !c.ValidDigest(inv.CandidateDigest) || len(inv.Argv) == 0 || inv.Argv[0] == "" || len(inv.Argv) > 128 {
		return result, c.Fail(c.InvalidArgument, "invalid computation invocation")
	}
	argvBytes := 0
	for _, arg := range inv.Argv {
		argvBytes += len(arg)
		if len(arg) > 16384 || strings.IndexByte(arg, 0) >= 0 {
			return result, c.Fail(c.InvalidArgument, "invalid argv")
		}
	}
	if argvBytes > 65536 || inv.Stdin != nil && len(*inv.Stdin) > 64<<10 {
		return result, c.Fail(c.InvalidArgument, "invocation argv exceeds quota")
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
	var capsule *Capsule
	if owner != nil {
		prepared, prepareErr := makeCapsule(*owner, absolute, p, inv)
		if prepareErr != nil {
			return result, prepareErr
		}
		capsule = &prepared
		name = capsule.name()
	}
	labels := map[string]string{"io.viber.runtime": "offline-v1"}
	if capsule != nil {
		labels, err = capsule.labels()
		if err != nil {
			return result, err
		}
	}
	if owner != nil {
		if err = d.admitCapacity(ctx, *owner); err != nil {
			return result, err
		}
	}
	args := createArguments(name, absolute, p, inv, labels)
	if _, err = activeOwnership(ctx); err != nil {
		return result, err
	}
	dispatched = true
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
			if capsule != nil {
				bound, bindingErr := d.inspect(cleanupCtx, id)
				if bindingErr != nil {
					return result, errors.Join(err, bindingErr)
				}
				if bindingErr = validateCapsule(bound, *capsule); bindingErr != nil {
					return result, errors.Join(err, bindingErr)
				}
			}
			_, cleanupErr := d.control(cleanupCtx, "rm", "--force", id)
			return result, errors.Join(err, cleanupErr)
		}
		return result, err
	}
	if !validContainerID(id) {
		return result, c.Fail(c.UnsupportedCapability, "unbound container identity")
	}
	result = Result{Ownership: capsule, SchemaVersion: c.SchemaVersion, CandidateDigest: inv.CandidateDigest, ContainerID: id, ExitCode: -1}
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
		if capsule != nil {
			bound, bindingErr := d.inspect(removeCtx, id)
			if bindingErr != nil {
				resultErr = errors.Join(resultErr, bindingErr)
				return
			}
			if bindingErr = validateCapsule(bound, *capsule); bindingErr != nil {
				resultErr = errors.Join(resultErr, bindingErr)
				return
			}
		}
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
	if err = validateInspect(initial, p, absolute, inv); err != nil {
		return result, err
	}
	if capsule != nil {
		if err = validateCapsule(initial, *capsule); err != nil {
			return result, err
		}
	}
	result.SourceReadOnly = true
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()
	if value, ok := ctx.Value(ownerContextKey{}).(ownershipContext); ok {
		leased, leaseCancel := context.WithDeadline(runCtx, value.expires)
		defer leaseCancel()
		runCtx = leased
	}
	if _, err = activeOwnership(ctx); err != nil {
		return result, err
	}
	stdout, stderr := &boundedOutput{limit: p.MaxOutputBytes}, &boundedOutput{limit: p.MaxOutputBytes}
	stdout.onLimit = cancel
	stderr.onLimit = cancel
	startArgs := []string{"start", "--attach", id}
	if inv.Stdin != nil {
		startArgs = []string{"start", "--attach", "--interactive", id}
	}
	command := d.command(runCtx, startArgs...)
	if inv.Stdin != nil {
		command.Stdin = bytes.NewReader(*inv.Stdin)
	}
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
	if err = validateInspect(final, p, absolute, inv); err != nil {
		return result, err
	}
	if capsule != nil {
		if err = validateCapsule(final, *capsule); err != nil {
			return result, err
		}
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

// DispatchFailure records whether container creation was attempted. A proven
// preflight refusal can be reported as a tool error without an UNKNOWN effect.
type DispatchFailure struct {
	Cause          error
	EffectPossible bool
}

func (e *DispatchFailure) Error() string { return e.Cause.Error() }
func (e *DispatchFailure) Unwrap() error { return e.Cause }
