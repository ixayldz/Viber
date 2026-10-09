package agent

import (
	"errors"
	"os"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/runner"
)

func preflightRuntimeInstance(directory string) error {
	root, err := os.OpenRoot(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, runtimeInstanceFile, 1024)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var value RuntimeInstance
	if c.DecodeStrict(raw, &value) != nil || value.SchemaVersion != 1 || !c.ValidDigest(value.PhysicalRoot) {
		return c.Fail(c.StoreIntegrityError, "invalid physical runtime metadata")
	}
	probe := runner.Lease{SchemaVersion: 1, InstanceID: value.ID, ID: value.ID, ClockDomain: value.ID, TaskDigest: c.HashBytes(nil), OperationDigest: c.HashBytes(nil), Generation: 1, PolicyEpoch: 1, SpecVersion: 1, DeadlineMillis: 1}
	if probe.Validate() != nil {
		return c.Fail(c.StoreIntegrityError, "invalid physical runtime identity")
	}
	identity, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return err
	}
	if identity != value.PhysicalRoot {
		return c.Fail(c.StoreIntegrityError, "runtime metadata belongs to a different physical store")
	}
	return nil
}
