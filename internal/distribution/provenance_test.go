package distribution

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/store"
)

func TestRunActiveWithAllRuntimeCapabilitiesSkipsStoreOwnerLock(t *testing.T) {
	platform := nativePlatform(t)
	parent := t.TempDir()
	manager, err := Open(filepath.Join(parent, "program"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	storeDir := filepath.Join(parent, "task-store")
	createRuntimeCapabilityStore(t, storeDir, false, false)
	if err = manager.RegisterStore(storeDir); err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("c", 40)
	bundle, err := ParseArchive(fixtureArchiveCapabilities(t, revision, supportedRuntimeCapabilities, nil), "zip", revision, platform)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Install(context.Background(), bundle); err != nil {
		t.Fatal(err)
	}

	// Keep the real store owner held. A cancelled context prevents the portable
	// fixture executable from starting while proving RunActive reaches the child
	// command path instead of trying to acquire this store's exclusive lock.
	owner, err := store.Open(context.Background(), storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, runErr := manager.RunActive(ctx, nil, strings.NewReader(""), io.Discard, io.Discard)
	if code != 4 || !errors.Is(runErr, context.Canceled) {
		t.Fatalf("full-capability package should reach the cancelled child path while store is owned; code=%d err=%v", code, runErr)
	}
}
