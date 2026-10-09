package agent

import (
	"context"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/runner"
	"os"
	"strings"
	"testing"
)

func TestNativeEnginePinRejectsReplacementMissingAndForeignInstance(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	expected := NativeEngineBinding{1, s.instance.ID, c.HashBytes([]byte("engine")), c.HashBytes([]byte("endpoint"))}
	lease := runner.Lease{}
	if err = s.bindEnginePin(context.Background(), expected, lease, false); err == nil {
		t.Fatal("reconciliation initialized missing pin")
	}
	if err = s.bindEnginePin(context.Background(), expected, lease, true); err != nil {
		t.Fatal(err)
	}
	if err = s.bindEnginePin(context.Background(), expected, lease, false); err != nil {
		t.Fatal("same engine refused", err)
	}
	for _, field := range []string{"engine", "endpoint"} {
		changed := expected
		if field == "engine" {
			changed.EngineDigest = c.HashBytes([]byte("replacement"))
		} else {
			changed.EndpointDigest = c.HashBytes([]byte("replacement"))
		}
		err = s.bindEnginePin(context.Background(), changed, lease, true)
		var failure *c.Error
		if !errors.As(err, &failure) || failure.Code != c.StaleAuthority {
			t.Fatal("replacement accepted", err)
		}
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = root.Remove(nativeEngineFile); err != nil {
		t.Fatal(err)
	}
	foreign := expected
	foreign.InstanceID = strings.Repeat("a", 64)
	raw, _ := c.CanonicalV1(foreign)
	if err = fileguard.Publish(root, nativeEngineFile, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = s.engineBinding(); err == nil {
		t.Fatal("foreign physical pin accepted")
	}
}
