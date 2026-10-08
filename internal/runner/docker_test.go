package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestProfileRejectsUnpinnedOrUnboundedExecution(t *testing.T) {
	p := DefaultProfile("golang@sha256:" + strings.Repeat("a", 64))
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Profile){func(p *Profile) { p.Image = "golang:latest" }, func(p *Profile) { p.TimeoutSeconds = 0 }, func(p *Profile) { p.Pids = 0 }, func(p *Profile) { p.MemoryBytes = 1 }, func(p *Profile) { p.MaxOutputBytes = 1 << 62 }} {
		bad := p
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatal("invalid profile accepted", bad)
		}
	}
}
func TestBoundedOutputCancelsOnceAndDoesNotExposeInternalBytes(t *testing.T) {
	count := 0
	out := boundedOutput{limit: 3, onLimit: func() { count++ }}
	if n, err := out.Write([]byte("abcd")); n != 4 || err != nil {
		t.Fatal(n, err)
	}
	out.Write([]byte("more"))
	if count != 1 || string(out.Bytes()) != "abc" || !out.Truncated() {
		t.Fatal("output guard failed")
	}
	raw := out.Bytes()
	raw[0] = 'x'
	if string(out.Bytes()) != "abc" {
		t.Fatal("buffer aliased")
	}
}
func TestInspectGuardRejectsMountAndAuthorityDowngrade(t *testing.T) {
	p := DefaultProfile("golang@sha256:" + strings.Repeat("a", 64))
	source := "/owned/candidate"
	inv := Invocation{CandidateDigest: c.HashBytes([]byte("candidate")), Argv: []string{"/bin/sh", "-c", "echo computation"}}
	var valid inspected
	valid.Config.User = "65532:65532"
	valid.Config.Image = p.Image
	valid.Config.WorkingDir = "/workspace"
	valid.Config.Entrypoint = inv.Argv[:1]
	valid.Config.Cmd = inv.Argv[1:]
	valid.HostConfig.ReadonlyRootfs = true
	valid.HostConfig.NetworkMode = "none"
	valid.HostConfig.CapDrop = []string{"ALL"}
	valid.HostConfig.SecurityOpt = []string{"no-new-privileges=true", "seccomp=builtin"}
	valid.HostConfig.Memory = p.MemoryBytes
	valid.HostConfig.MemorySwap = p.MemoryBytes
	valid.HostConfig.PidsLimit = p.Pids
	valid.HostConfig.NanoCpus = p.CPUs * 1e9
	init := true
	valid.HostConfig.Init = &init
	valid.HostConfig.IpcMode = "private"
	valid.HostConfig.Tmpfs = map[string]string{"/tmp": scratchOptions(p)}
	valid.Mounts = []inspectedMount{{Destination: "/workspace", Source: source, Type: "bind", Propagation: "rprivate"}}
	if err := validateInspect(valid, p, source, inv); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*inspected){
		"privileged":          func(i *inspected) { i.HostConfig.Privileged = true },
		"writable source":     func(i *inspected) { i.Mounts[0].RW = true },
		"wrong mount source":  func(i *inspected) { i.Mounts[0].Source = "/other/candidate" },
		"shared propagation":  func(i *inspected) { i.Mounts[0].Propagation = "shared" },
		"duplicate workspace": func(i *inspected) { i.Mounts = append(i.Mounts, i.Mounts[0]) },
		"extra credentials mount": func(i *inspected) {
			i.Mounts = append(i.Mounts, inspectedMount{Destination: "/root/.ssh", Source: "/credentials", Type: "bind"})
		},
		"missing source":    func(i *inspected) { i.Mounts = nil },
		"wrong image":       func(i *inspected) { i.Config.Image = "golang:latest" },
		"wrong argv":        func(i *inspected) { i.Config.Cmd = []string{"-c", "echo fake-PASS"} },
		"wrong entrypoint":  func(i *inspected) { i.Config.Entrypoint = []string{"/bin/other"} },
		"wrong workdir":     func(i *inspected) { i.Config.WorkingDir = "/root" },
		"host PID":          func(i *inspected) { i.HostConfig.PidMode = "host" },
		"shared IPC":        func(i *inspected) { i.HostConfig.IpcMode = "host" },
		"no init":           func(i *inspected) { i.HostConfig.Init = nil },
		"unbounded scratch": func(i *inspected) { i.HostConfig.Tmpfs["/tmp"] = "rw" },
		"other scratch":     func(i *inspected) { i.HostConfig.Tmpfs["/home"] = "rw" },
		"extra binds":       func(i *inspected) { i.HostConfig.Binds = []string{"/host:/host"} },
		"inherited volumes": func(i *inspected) { i.HostConfig.VolumesFrom = []string{"other"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(valid)
			var bad inspected
			if err := json.Unmarshal(raw, &bad); err != nil {
				t.Fatal(err)
			}
			mutate(&bad)
			if err := validateInspect(bad, p, source, inv); err == nil {
				t.Fatal("downgraded execution admitted")
			}
		})
	}
}
func TestDockerOfflineReadOnlyQuiescenceAndTimeout(t *testing.T) {
	image := os.Getenv("VIBER_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("opt-in real Docker conformance image required")
	}
	d, err := OpenDocker()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	source := t.TempDir()
	if err = os.Chmod(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "source.txt"), []byte("source-bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p := DefaultProfile(image)
	inv := Invocation{CandidateDigest: c.HashBytes([]byte("fixture")), Argv: []string{"/bin/sh", "-c", `set -eu; test "$(id -u)" = 65532; test "$(cat /workspace/source.txt)" = source-bytes; if touch /workspace/forbidden 2>/dev/null; then exit 20; fi; if touch /root/forbidden 2>/dev/null; then exit 21; fi; test ! -S /var/run/docker.sock; test ! -e /root/.ssh; test "$(ls /sys/class/net)" = lo; echo fake-PASS; exit 7`}}
	result, err := d.Run(ctx, source, p, inv)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 || !result.ProcessTreeQuiescent || !result.SourceReadOnly || !result.ProtectedResultChannel || !strings.Contains(string(result.Stdout), "fake-PASS") {
		t.Fatal("bad protected outcome", result)
	}
	if _, err = os.Stat(filepath.Join(source, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("source was modified")
	}
	p.TimeoutSeconds = 1
	inv.Argv = []string{"/bin/sh", "-c", "sleep 30 & wait"}
	result, err = d.Run(ctx, source, p, inv)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || !result.ProcessTreeQuiescent || result.ExitCode == 0 {
		t.Fatal("timeout/child cleanup failed", result)
	}
}
