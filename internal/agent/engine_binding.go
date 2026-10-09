package agent

import (
	"context"
	"errors"
	"os"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/runner"
)

const nativeEngineFile = "native-engine.json"

type NativeEngineBinding struct {
	SchemaVersion  int    `json:"schema_version"`
	InstanceID     string `json:"instance_id"`
	EngineDigest   string `json:"engine_digest"`
	EndpointDigest string `json:"endpoint_digest"`
}

func (s *Session) engineBinding() (*NativeEngineBinding, error) {
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, nativeEngineFile, 1024)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var binding NativeEngineBinding
	if c.DecodeStrict(raw, &binding) != nil || binding.SchemaVersion != 1 || binding.InstanceID != s.instance.ID || !c.ValidDigest(binding.EngineDigest) || !c.ValidDigest(binding.EndpointDigest) {
		return nil, c.Fail(c.StoreIntegrityError, "native engine binding invalid or belongs to another physical instance")
	}
	return &binding, nil
}

// Pin before the first effect. Reconciliation never initializes a missing pin.
// A new physical restore has a new instance and cannot clean the original engine.
func (s *Session) bindEngine(ctx context.Context, broker *runner.Docker, lease runner.Lease, initialize bool) error {
	info, err := broker.BackendInfo(ctx)
	if err != nil {
		return err
	}
	expected := NativeEngineBinding{1, s.instance.ID, c.HashBytes([]byte(info.EngineID)), broker.EndpointDigest()}
	return s.bindEnginePin(ctx, expected, lease, initialize)
}

func (s *Session) bindEnginePin(ctx context.Context, expected NativeEngineBinding, lease runner.Lease, initialize bool) error {
	bound, err := s.engineBinding()
	if err != nil {
		return err
	}
	if bound != nil {
		if *bound != expected {
			return c.Fail(c.StaleAuthority, "native operation cannot move to a different engine or endpoint")
		}
		return nil
	}
	if !initialize {
		return c.Fail(c.UnknownOutcome, "native engine pin missing; original effect cannot be reconciled on an unbound engine")
	}
	refs, err := s.Journal.DocumentReferences(ctx)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		raw, err := s.Archive.GetBytes(ref.TaskID, ref.Digest)
		if err != nil {
			return err
		}
		var doc Document
		if err = c.DecodeStrict(raw, &doc); err != nil {
			return err
		}
		if doc.Pending != nil && doc.Pending.NativeLease != nil {
			prior := doc.Pending.NativeLease
			if prior.InstanceID == s.instance.ID && prior.ID != lease.ID {
				return c.Fail(c.StoreIntegrityError, "native engine pin missing from existing owned history")
			}
		}
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := c.CanonicalV1(expected)
	if err != nil {
		return err
	}
	return fileguard.Publish(root, nativeEngineFile, raw)
}
