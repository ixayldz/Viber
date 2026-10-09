package runner

import (
	"context"
	"os"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
)

const MaxInstanceSubjects = 4096

type Capacity struct {
	SchemaVersion      int    `json:"schema_version"`
	InstanceID         string `json:"instance_id"`
	EngineID           string `json:"engine_id"`
	Subjects           int    `json:"subjects"`
	Limit              int    `json:"limit"`
	Available          int    `json:"available"`
	PhysicalBytesKnown bool   `json:"physical_bytes_known"`
	Retention          string `json:"retention"`
}

// Every held name consumes a slot, including unstarted fences. Control cleanup
// bypasses this admission limit: it must still fence already admitted subjects.
// Engine metadata disk usage is unknown, not zero; this quota measures identities.
func (d *Docker) capacity(ctx context.Context, instance string) (Capacity, error) {
	result := Capacity{SchemaVersion: 1, InstanceID: instance, EngineID: d.engineID, Limit: MaxInstanceSubjects, Retention: "RETAIN_UNTIL_OLD_REQUESTS_TECHNICALLY_IMPOSSIBLE"}
	if !opaqueID(instance) {
		return result, c.Fail(c.InvalidArgument, "physical instance ID required")
	}
	raw, err := d.control(ctx, "ps", "--all", "--no-trunc", "--filter", "label=io.viber.instance="+instance, "--format", "{{.ID}}")
	if err != nil {
		return result, err
	}
	ids := strings.Fields(string(raw))
	seen := map[string]bool{}
	for _, id := range ids {
		if !validContainerID(id) || seen[id] {
			return result, c.Fail(c.StoreIntegrityError, "invalid instance process registry")
		}
		seen[id] = true
	}
	result.Subjects = len(ids)
	result.Available = max(0, result.Limit-result.Subjects)
	if len(ids) > MaxInstanceSubjects+256 {
		return result, c.Fail(c.UnsupportedCapability, "instance process registry exceeds control reserve")
	}
	return result, nil
}
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
func (d *Docker) admitCapacity(ctx context.Context, owner Ownership) error {
	view, err := d.capacity(ctx, owner.Lease.InstanceID)
	if err != nil {
		return err
	}
	if view.Available < 1 {
		return c.Fail(c.BudgetLimitReached, "persistent process name capacity exhausted; retained fences cannot be pruned to admit new work")
	}
	return nil
}
