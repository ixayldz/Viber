package store

import (
	"context"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func checkpointPin(phase string, generation int64, previous string) *PrivacyCheckpointPin {
	return &PrivacyCheckpointPin{
		SchemaVersion: 1, Phase: phase,
		AuthorityDigest: c.HashBytes([]byte("authority")),
		AnchorDigest:    c.HashBytes([]byte("anchor")),
		PreviousAnchor:  previous, Generation: generation,
		Sequence: 9, Tail: c.HashBytes([]byte("tail")), Watermark: 3,
		InstanceID:   "0123456789abcdef0123456789abcdef",
		PhysicalRoot: c.HashBytes([]byte("physical root")),
	}
}

func TestPrivacyCheckpointPinCASAndReopen(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "owner")
	s, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	prepared := checkpointPin("PREPARED", 1, "")
	if err = s.CompareAndSwapPrivacyCheckpointPin(ctx, nil, prepared); err != nil {
		t.Fatal("initial prepare", err)
	}
	actual, err := s.PrivacyCheckpointPin(ctx)
	if err != nil || actual == nil || *actual != *prepared {
		t.Fatal("prepared pin did not replay", actual, err)
	}
	active := *prepared
	active.Phase = "ACTIVE"
	if err = s.CompareAndSwapPrivacyCheckpointPin(ctx, prepared, &active); err != nil {
		t.Fatal("activate", err)
	}
	if err = s.CompareAndSwapPrivacyCheckpointPin(ctx, prepared, &active); err == nil {
		t.Fatal("stale expected pin accepted")
	}
	next := checkpointPin("PREPARED", 2, active.AnchorDigest)
	if err = s.CompareAndSwapPrivacyCheckpointPin(ctx, &active, next); err != nil {
		t.Fatal("next generation", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	actual, err = s.PrivacyCheckpointPin(ctx)
	if err != nil || actual == nil || *actual != *next {
		t.Fatal("new prepared pin did not survive reopen", actual, err)
	}
}

func TestPrivacyCheckpointPinRejectsInvalidAndSkippedTransitions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	invalid := checkpointPin("ACTIVE", 1, "")
	if err = s.CompareAndSwapPrivacyCheckpointPin(ctx, nil, invalid); err == nil {
		t.Fatal("active genesis pin bypassed preparation")
	}
	prepared := checkpointPin("PREPARED", 1, "")
	if err = s.CompareAndSwapPrivacyCheckpointPin(ctx, nil, prepared); err != nil {
		t.Fatal(err)
	}
	skipped := checkpointPin("PREPARED", 3, prepared.AnchorDigest)
	if err = s.CompareAndSwapPrivacyCheckpointPin(ctx, prepared, skipped); err == nil {
		t.Fatal("skipped generation accepted")
	}
	wrongOwner := *prepared
	wrongOwner.InstanceID = "fedcba9876543210fedcba9876543210"
	if err = s.CompareAndSwapPrivacyCheckpointPin(ctx, prepared, &wrongOwner); err == nil {
		t.Fatal("pin moved to another runtime scope")
	}
}
