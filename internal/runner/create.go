package runner

import (
	"fmt"
	"sort"
)

func createArguments(name, source string, p Profile, inv Invocation, labels map[string]string) []string {
	args := []string{"create", "--pull=never", "--name", name, "--user", "65532:65532", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=builtin", "--pids-limit", fmt.Sprint(p.Pids), "--memory", fmt.Sprint(p.MemoryBytes), "--memory-swap", fmt.Sprint(p.MemoryBytes), "--cpus", fmt.Sprint(p.CPUs), "--ipc", "private", "--init", "--workdir", "/workspace", "--env", "HOME=/tmp", "--env", "TMPDIR=/tmp", "--env", "GOCACHE=/tmp/go-build", "--env", "GOMODCACHE=/tmp/go-mod", "--env", "GOTOOLCHAIN=local", "--env", "GOPROXY=off", "--env", "GOSUMDB=off", "--mount", "type=bind,src=" + source + ",dst=/workspace,readonly,bind-propagation=rprivate", "--tmpfs", "/tmp:" + scratchOptions(p), "--entrypoint", inv.Argv[0]}
	if inv.Stdin != nil {
		args = append(args, "--interactive")
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--label", key+"="+labels[key])
	}
	args = append(args, p.Image)
	return append(args, inv.Argv[1:]...)
}
