package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
)

// MaxEngineCapacitySlots bounds admitted owned identities across every broker
// connected to the exact same Docker engine. Each logical slot is represented
// by one permanent marker and can have up to two additional process identities
// over its lifetime: a workload and its cleanup fence.
const MaxEngineCapacitySlots = 4096

// MaxInstanceSubjects remains as a source-compatible name for callers which
// used the old per-instance quota. The bound now applies to the whole engine.
const MaxInstanceSubjects = MaxEngineCapacitySlots

const (
	capacityMarkerLabel       = "io.viber.capacity"
	capacityEngineLabel       = "io.viber.capacity.engine"
	capacityEngineIDLabel     = "io.viber.capacity.engine_id"
	capacitySlotLabel         = "io.viber.capacity.slot"
	capacityIdentityLabel     = "io.viber.capacity.identity"
	capacityCapsuleLabel      = "io.viber.capacity.capsule"
	capacityImageLabel        = "io.viber.capacity.image"
	capacityMarkerVersion     = "engine-slot-v1"
	capacityMarkerEntrypoint  = "/__viber_capacity_marker_never_start__"
	capacityMarkerCommand     = "__viber_capacity_marker_never_start__"
	capacityMarkerMemoryBytes = 64 << 20
	capacityMarkerPids        = 8
)

// Capacity keeps the legacy per-instance Subjects/Available view and also
// reports the engine-wide logical reservation authority. CapacityMarkerSlots
// is not a physical-storage quota or a count of every container identity ever
// created for those slots.
type Capacity struct {
	SchemaVersion       int    `json:"schema_version"`
	InstanceID          string `json:"instance_id"`
	EngineID            string `json:"engine_id"`
	Subjects            int    `json:"subjects"` // This instance's logical owner slots.
	CapacityMarkerSlots int    `json:"capacity_marker_slots"`
	Limit               int    `json:"limit"`     // Legacy per-instance view; use EngineLimit for admission.
	Available           int    `json:"available"` // Legacy per-instance view; use EngineAvailable for admission.
	EngineLimit         int    `json:"engine_limit"`
	EngineAvailable     int    `json:"engine_available"`
	PhysicalBytesKnown  bool   `json:"physical_bytes_known"`
	Retention           string `json:"retention"`
}

type capacityMarker struct {
	Slot        int
	Identity    string
	Capsule     Capsule
	CapsuleJSON string
	Image       string
}

func engineCapacityNamespace(engineID string) string {
	return c.HashBytes([]byte("viber:docker-engine-capacity:engine-slot-v1\x00" + engineID))
}

func capacityCapsuleIdentity(capsule Capsule) (identity, capsuleJSON string, err error) {
	if capsule.SchemaVersion != 1 || capsule.Owner.Validate() != nil || !c.ValidDigest(capsule.CandidateDigest) || !c.ValidDigest(capsule.ProfileDigest) || !c.ValidDigest(capsule.InvocationDigest) || !c.ValidDigest(capsule.SourceDigest) {
		return "", "", c.Fail(c.InvalidArgument, "invalid capacity capsule identity")
	}
	raw, err := c.CanonicalV1(capsule)
	if err != nil {
		return "", "", err
	}
	identity, err = c.Digest(capsule)
	return identity, string(raw), err
}

func capacityMarkerName(engineHash string, slot int) string {
	return fmt.Sprintf("viber-cap-v1-%s-%04d", engineHash, slot)
}

func capacityMarkerLabels(engineID string, slot int, capsule Capsule, image string) (map[string]string, error) {
	if engineID == "" || slot < 0 || slot >= MaxEngineCapacitySlots || !pinnedImage.MatchString(image) {
		return nil, c.Fail(c.InvalidArgument, "invalid engine capacity marker binding")
	}
	identity, capsuleJSON, err := capacityCapsuleIdentity(capsule)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		capacityMarkerLabel:   capacityMarkerVersion,
		capacityEngineLabel:   engineCapacityNamespace(engineID),
		capacityEngineIDLabel: engineID,
		capacitySlotLabel:     strconv.Itoa(slot),
		capacityIdentityLabel: identity,
		capacityCapsuleLabel:  capsuleJSON,
		capacityImageLabel:    image,
	}, nil
}

func capacityMarkerArguments(name, image string, labels map[string]string) []string {
	args := []string{
		"create", "--pull=never", "--name", name,
		"--user", "65532:65532", "--network", "none", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=builtin",
		"--pids-limit", strconv.Itoa(capacityMarkerPids),
		"--memory", strconv.Itoa(capacityMarkerMemoryBytes),
		"--memory-swap", strconv.Itoa(capacityMarkerMemoryBytes),
		"--cpus", "1", "--ipc", "private", "--init", "--workdir", "/",
		"--entrypoint", capacityMarkerEntrypoint,
	}
	keys := []string{capacityEngineIDLabel, capacityEngineLabel, capacityIdentityLabel, capacityImageLabel, capacityMarkerLabel, capacityCapsuleLabel, capacitySlotLabel}
	for _, key := range keys {
		args = append(args, "--label", key+"="+labels[key])
	}
	args = append(args, image, capacityMarkerCommand)
	return args
}

func decodeCapacityMarker(info inspected, engineID string) (capacityMarker, error) {
	marker := capacityMarker{}
	labels := info.Config.Labels
	if info.ID == "" || !validContainerID(info.ID) || engineID == "" {
		return marker, c.Fail(c.StoreIntegrityError, "unbound engine capacity marker")
	}
	if labels[capacityMarkerLabel] != capacityMarkerVersion || labels[capacityEngineLabel] != engineCapacityNamespace(engineID) || labels[capacityEngineIDLabel] != engineID {
		return marker, c.Fail(c.StoreIntegrityError, "capacity marker engine namespace mismatch")
	}
	slot, err := strconv.Atoi(labels[capacitySlotLabel])
	if err != nil || slot < 0 || slot >= MaxEngineCapacitySlots || info.Name != "/"+capacityMarkerName(engineCapacityNamespace(engineID), slot) {
		return marker, c.Fail(c.StoreIntegrityError, "capacity marker slot identity mismatch")
	}
	var capsule Capsule
	if err = c.DecodeStrict([]byte(labels[capacityCapsuleLabel]), &capsule); err != nil || capsule.SchemaVersion != 1 || capsule.Owner.Validate() != nil || !c.ValidDigest(capsule.CandidateDigest) || !c.ValidDigest(capsule.ProfileDigest) || !c.ValidDigest(capsule.InvocationDigest) || !c.ValidDigest(capsule.SourceDigest) {
		return marker, c.Fail(c.StoreIntegrityError, "capacity marker capsule binding is invalid")
	}
	canonical, err := c.CanonicalV1(capsule)
	if err != nil || string(canonical) != labels[capacityCapsuleLabel] {
		return marker, c.Fail(c.StoreIntegrityError, "capacity marker capsule encoding changed")
	}
	identity, _, err := capacityCapsuleIdentity(capsule)
	if err != nil || labels[capacityIdentityLabel] != identity {
		return marker, c.Fail(c.StoreIntegrityError, "capacity marker capsule digest mismatch")
	}
	image := labels[capacityImageLabel]
	if !pinnedImage.MatchString(image) || info.Config.Image != image || info.Config.User != "65532:65532" || info.Config.OpenStdin || info.Config.WorkingDir != "/" || len(info.Config.Entrypoint) != 1 || info.Config.Entrypoint[0] != capacityMarkerEntrypoint || len(info.Config.Cmd) != 1 || info.Config.Cmd[0] != capacityMarkerCommand {
		return marker, c.Fail(c.StoreIntegrityError, "capacity marker invocation changed")
	}
	if len(labels) != 7 || info.State.Status != "created" || info.State.Running || info.State.Error != "" || len(info.Mounts) != 0 {
		return marker, c.Fail(c.StoreIntegrityError, "capacity marker was altered or started")
	}
	h := info.HostConfig
	if h.Privileged || h.PidMode != "" || h.IpcMode != "private" || h.Init == nil || !*h.Init || len(h.Tmpfs) != 0 || len(h.Binds) != 0 || len(h.VolumesFrom) != 0 || h.ReadonlyRootfs != true || h.NetworkMode != "none" || len(h.CapAdd) != 0 || len(h.CapDrop) != 1 || !strings.EqualFold(h.CapDrop[0], "ALL") || h.Memory != capacityMarkerMemoryBytes || h.MemorySwap != capacityMarkerMemoryBytes || h.PidsLimit != capacityMarkerPids || h.NanoCpus != 1e9 {
		return marker, c.Fail(c.StoreIntegrityError, "capacity marker sandbox profile changed")
	}
	if len(h.SecurityOpt) != 2 || !containsSecurityOpt(h.SecurityOpt, "no-new-privileges=true") || !containsSecurityOpt(h.SecurityOpt, "seccomp=builtin") {
		return marker, c.Fail(c.StoreIntegrityError, "capacity marker security options changed")
	}
	marker.Slot, marker.Identity, marker.Capsule, marker.CapsuleJSON, marker.Image = slot, identity, capsule, string(canonical), image
	return marker, nil
}

func containsSecurityOpt(options []string, expected string) bool {
	for _, option := range options {
		if option == expected || expected == "no-new-privileges=true" && option == "no-new-privileges" {
			return true
		}
	}
	return false
}

func (d *Docker) capacityMarkers(ctx context.Context) ([]capacityMarker, error) {
	if d.engineID == "" {
		return nil, c.Fail(c.StaleAuthority, "verified Docker engine identity required for capacity")
	}
	engineHash := engineCapacityNamespace(d.engineID)
	raw, err := d.control(ctx, "ps", "--all", "--no-trunc", "--filter", "label="+capacityEngineLabel+"="+engineHash, "--format", "{{.ID}}")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(raw))
	if len(ids) > MaxEngineCapacitySlots {
		return nil, c.Fail(c.StoreIntegrityError, "engine capacity marker registry exceeds its fixed slot pool")
	}
	inspectedMarkers, err := d.inspectMany(ctx, ids)
	if err != nil {
		return nil, err
	}
	markers := make([]capacityMarker, 0, len(ids))
	seenIDs := map[string]bool{}
	seenSlots := map[int]bool{}
	seenOwners := map[string]bool{}
	for index, id := range ids {
		if !validContainerID(id) || seenIDs[id] {
			return nil, c.Fail(c.StoreIntegrityError, "invalid engine capacity marker registry")
		}
		seenIDs[id] = true
		info := inspectedMarkers[index]
		if info.ID != id {
			return nil, c.Fail(c.StoreIntegrityError, "capacity marker inspect ID binding mismatch")
		}
		marker, markerErr := decodeCapacityMarker(info, d.engineID)
		if markerErr != nil {
			return nil, markerErr
		}
		if seenSlots[marker.Slot] || seenOwners[marker.Identity] {
			return nil, c.Fail(c.StoreIntegrityError, "duplicate engine capacity slot or owner")
		}
		seenSlots[marker.Slot] = true
		seenOwners[marker.Identity] = true
		markers = append(markers, marker)
	}
	return markers, nil
}

// The engine's deterministic container-name namespace is the shared atomic
// reservation authority. Markers have no mounts or sockets, are never started,
// and are never removed by the broker. Docker daemon reset or operator pruning
// is outside the retention guarantee.
func (d *Docker) reserveCapacity(ctx context.Context, capsule Capsule, image string) (int, error) {
	identity, capsuleJSON, err := capacityCapsuleIdentity(capsule)
	if err != nil {
		return -1, err
	}
	if d.engineID == "" {
		return -1, c.Fail(c.StaleAuthority, "verified Docker engine identity required for capacity")
	}
	engineHash := engineCapacityNamespace(d.engineID)
	markers, err := d.capacityMarkers(ctx)
	if err != nil {
		return -1, err
	}
	occupied := make(map[int]capacityMarker, len(markers))
	for _, marker := range markers {
		occupied[marker.Slot] = marker
		if marker.Identity == identity {
			if marker.CapsuleJSON != capsuleJSON || marker.Image != image {
				return -1, c.Fail(c.StoreIntegrityError, "capacity capsule binding collision")
			}
			return marker.Slot, nil
		}
	}
	if len(markers) >= MaxEngineCapacitySlots {
		return -1, c.Fail(c.BudgetLimitReached, "engine process identity capacity exhausted; retained markers cannot be pruned to admit new work")
	}

	for slot := 0; slot < MaxEngineCapacitySlots; slot++ {
		if _, exists := occupied[slot]; exists {
			continue
		}
		labels, labelErr := capacityMarkerLabels(d.engineID, slot, capsule, image)
		if labelErr != nil {
			return -1, labelErr
		}
		name := capacityMarkerName(engineHash, slot)
		raw, createErr := d.control(ctx, capacityMarkerArguments(name, image, labels)...)
		if createErr == nil {
			id := strings.TrimSpace(string(raw))
			if !validContainerID(id) {
				return -1, c.Fail(c.UnknownOutcome, "capacity marker creation identity lost; workload dispatch stopped")
			}
			info, inspectErr := d.inspect(ctx, id)
			if inspectErr != nil {
				return -1, errors.Join(c.Fail(c.UnknownOutcome, "capacity marker identity could not be confirmed; workload dispatch stopped"), inspectErr)
			}
			marker, markerErr := decodeCapacityMarker(info, d.engineID)
			if markerErr != nil || marker.Slot != slot || marker.Identity != identity || marker.CapsuleJSON != capsuleJSON || marker.Image != image {
				return -1, errors.Join(c.Fail(c.StoreIntegrityError, "capacity marker does not match the reserved owner"), markerErr)
			}
			return slot, nil
		}

		// A name conflict is resolved only through the exact fixed slot name.
		// If the create acknowledgement was lost, this also proves whether our
		// exact owner owns the marker. Any unbound response stops admission.
		info, inspectErr := d.inspectName(ctx, name)
		if inspectErr != nil {
			return -1, errors.Join(c.Fail(c.UnknownOutcome, "capacity marker creation outcome is unbound; workload dispatch stopped"), createErr, inspectErr)
		}
		marker, markerErr := decodeCapacityMarker(info, d.engineID)
		if markerErr != nil || marker.Slot != slot {
			return -1, errors.Join(c.Fail(c.StoreIntegrityError, "capacity slot name is occupied by an invalid marker"), createErr, markerErr)
		}
		if marker.Identity == identity {
			if marker.CapsuleJSON != capsuleJSON || marker.Image != image {
				return -1, c.Fail(c.StoreIntegrityError, "capacity capsule binding collision")
			}
			return slot, nil
		}
		occupied[slot] = marker
	}
	return -1, c.Fail(c.BudgetLimitReached, "engine process identity capacity exhausted; retained markers cannot be pruned to admit new work")
}

func (d *Docker) capacity(ctx context.Context, instance string) (Capacity, error) {
	result := Capacity{SchemaVersion: 1, InstanceID: instance, EngineID: d.engineID, Limit: MaxInstanceSubjects, EngineLimit: MaxEngineCapacitySlots, Retention: "RETAIN_UNTIL_ENGINE_RESET_OR_OPERATOR_PRUNE"}
	if !opaqueID(instance) {
		return result, c.Fail(c.InvalidArgument, "physical instance ID required")
	}
	markers, err := d.capacityMarkers(ctx)
	if err != nil {
		return result, err
	}
	for _, marker := range markers {
		if marker.Capsule.Owner.Lease.InstanceID == instance {
			result.Subjects++
		}
	}
	result.CapacityMarkerSlots = len(markers)
	result.Available = max(0, result.Limit-result.Subjects)
	result.EngineAvailable = max(0, result.EngineLimit-result.CapacityMarkerSlots)
	// Docker engine storage and image-layer consumption are not bounded by this
	// logical admission counter and remain unknown.
	result.PhysicalBytesKnown = false
	return result, nil
}

// Capacity reports the caller's instance-local counters alongside the shared
// engine-wide marker count and available admission slots.
func (d *Docker) Capacity(ctx context.Context, instance string) (Capacity, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return Capacity{}, os.ErrClosed
	}
	if err := d.check(ctx); err != nil {
		return Capacity{}, err
	}
	return d.capacity(ctx, instance)
}

func (d *Docker) admitCapacity(ctx context.Context, capsule Capsule, profile Profile) error {
	return d.admitCapacities(ctx, []capacityRequest{{Capsule: capsule, Profile: profile}})
}

type capacityRequest struct {
	Capsule Capsule
	Profile Profile
}

func (d *Docker) admitCapacities(ctx context.Context, requests []capacityRequest) error {
	markers, err := d.capacityMarkers(ctx)
	if err != nil {
		return err
	}
	byIdentity := make(map[string]capacityMarker, len(markers))
	for _, marker := range markers {
		byIdentity[marker.Identity] = marker
	}
	missing := make([]capacityRequest, 0, len(requests))
	requested := make(map[string]string, len(requests))
	for _, request := range requests {
		if err = request.Profile.Validate(); err != nil {
			return err
		}
		identity, capsuleJSON, identityErr := capacityCapsuleIdentity(request.Capsule)
		if identityErr != nil {
			return identityErr
		}
		if image, exists := requested[identity]; exists {
			if image != request.Profile.Image {
				return c.Fail(c.StoreIntegrityError, "duplicate capacity capsule has conflicting image")
			}
			continue
		}
		requested[identity] = request.Profile.Image
		if marker, ok := byIdentity[identity]; ok {
			if marker.CapsuleJSON != capsuleJSON || marker.Image != request.Profile.Image {
				return c.Fail(c.StoreIntegrityError, "capacity capsule binding collision")
			}
			continue
		}
		missing = append(missing, request)
	}
	if len(markers)+len(missing) > MaxEngineCapacitySlots {
		return c.Fail(c.BudgetLimitReached, "engine process identity capacity exhausted; retained markers cannot be pruned to admit new work")
	}
	for _, request := range missing {
		if _, err = d.reserveCapacity(ctx, request.Capsule, request.Profile.Image); err != nil {
			return err
		}
	}
	return nil
}
