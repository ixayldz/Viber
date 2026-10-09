package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
)

// BackendInfo is an engine observation, not an escape-resistance certification.
type BackendInfo struct {
	EngineID       string `json:"engine_id"`
	Version        string `json:"version"`
	Rootless       bool   `json:"rootless"`
	CgroupVersion  string `json:"cgroup_version"`
	CgroupDriver   string `json:"cgroup_driver"`
	ResourceLimits bool   `json:"resource_limits"`
	SeccompBuiltin bool   `json:"seccomp_builtin"`
}

func decodeBackend(raw []byte) (BackendInfo, error) {
	var info struct {
		ID, OSType, ServerVersion, CgroupVersion, CgroupDriver       string
		SecurityOptions                                              []string
		MemoryLimit, SwapLimit, CpuCfsQuota, CpuCfsPeriod, PidsLimit *bool
	}
	var result BackendInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return result, c.Fail(c.UnsupportedCapability, "unreadable Docker capability response")
	}
	result = BackendInfo{EngineID: info.ID, Version: info.ServerVersion, CgroupVersion: info.CgroupVersion, CgroupDriver: info.CgroupDriver}
	for _, option := range info.SecurityOptions {
		if option == "name=rootless" {
			result.Rootless = true
		}
		if strings.Contains(option, "name=seccomp") && strings.Contains(option, "profile=builtin") {
			result.SeccompBuiltin = true
		}
	}
	result.ResourceLimits = true
	for _, absent := range []*bool{info.MemoryLimit, info.SwapLimit, info.CpuCfsQuota, info.CpuCfsPeriod, info.PidsLimit} {
		result.ResourceLimits = result.ResourceLimits && absent != nil && *absent
	}
	if info.OSType != "linux" || len(info.ID) < 1 || len(info.ID) > 128 || strings.ContainsAny(info.ID, "\x00\r\n") || !result.SeccompBuiltin || !result.ResourceLimits {
		return result, c.Fail(c.UnsupportedCapability, "Linux engine identity, built-in seccomp and CPU/memory/swap/PID enforcement required")
	}
	if result.Rootless && (result.CgroupVersion != "2" || result.CgroupDriver != "systemd") {
		return result, c.Fail(c.UnsupportedCapability, "rootless sandbox requires delegated cgroup v2 with systemd; resource flags cannot be ignored")
	}
	return result, nil
}

func (d *Docker) backend(ctx context.Context) (BackendInfo, error) {
	raw, err := d.control(ctx, "info", "--format", "{{json .}}")
	if err != nil {
		return BackendInfo{}, err
	}
	info, err := decodeBackend(raw)
	if err != nil {
		return info, err
	}
	if d.engineID != "" && d.engineID != info.EngineID {
		return info, c.Fail(c.StaleAuthority, "Docker engine identity changed; dispatch and cleanup stopped")
	}
	d.engineID = info.EngineID
	return info, nil
}
func (d *Docker) BackendInfo(ctx context.Context) (BackendInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return BackendInfo{}, os.ErrClosed
	}
	return d.backend(ctx)
}
func (d *Docker) EndpointDigest() string {
	return c.HashBytes([]byte("viber:local-engine-endpoint:v1\x00" + d.endpoint))
}

// The trusted operator may select a local rootless socket. Candidate environment,
// Docker contexts, credentials and remote TCP/SSH endpoints are never inherited.
func localEndpoint(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "unix://") {
		path := strings.TrimPrefix(value, "unix://")
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return "", c.Fail(c.InvalidArgument, "absolute local engine socket required")
		}
		return value, nil
	}
	if value == "npipe:////./pipe/docker_engine" || value == "npipe:////./pipe/dockerDesktopLinuxEngine" {
		return value, nil
	}
	return "", c.Fail(c.PolicyDenied, "only explicit local Unix socket or Docker named pipe endpoints supported")
}
