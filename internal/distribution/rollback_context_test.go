package distribution

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRollbackContextHonorsCancellationWithoutPublishing(t *testing.T) {
	platform := nativePlatform(t)
	manager, err := Open(filepath.Join(t.TempDir(), "program"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	first := strings.Repeat("d", 40)
	second := strings.Repeat("e", 40)
	for _, revision := range []string{first, second} {
		bundle, parseErr := ParseArchive(fixtureArchive(t, revision, nil), "zip", revision, platform)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if _, err = manager.Install(context.Background(), bundle); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = manager.RollbackContext(ctx, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled rollback should stop before publish: %v", err)
	}
	state, err := manager.Status()
	if err != nil || state.Active.Revision != second {
		t.Fatalf("cancelled rollback changed selection: %+v %v", state, err)
	}
}
