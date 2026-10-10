package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/ipc"
	"github.com/ixayldz/Viber/internal/owner"
)

func TestPrivacyCapacityCLIAndUIRouteStoreScopeWithoutCreatingTasks(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	s, err := agent.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	host, err := owner.Open(ctx, directory, s)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	before, err := s.Journal.SnapshotInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	for _, flags := range [][]string{{"--json"}, {"--replenish-control-reserve", "--json"}, {"--retire-operation-leases", "--json"}, {}} {
		out.Reset()
		args := append([]string{"privacy-capacity", "--store", directory}, flags...)
		if code := Execute(args, &out, &errs); code != 0 {
			t.Fatal(code, out.String(), errs.String())
		}
		if strings.Contains(out.String(), directory) || strings.Contains(out.String(), "physical_root") || strings.Contains(out.String(), "task_id") {
			t.Fatal("capacity output leaked metadata", out.String())
		}
		if len(flags) != 0 {
			var capacity agent.PrivacyMetadataStatus
			if err = c.DecodeStrict(out.Bytes(), &capacity); err != nil || !capacity.ObservationOnly || !capacity.WorkReady || capacity.Records != 0 {
				t.Fatal(capacity, err)
			}
		} else if !strings.Contains(out.String(), "instant observation") {
			t.Fatal("human output lacks observation boundary", out.String())
		}
	}
	u := uiSession{task: "absent-task", directory: directory}
	for _, command := range []string{"/capacity", "/capacity replenish", "/capacity retire-operations"} {
		text, detached, err := u.command(ctx, command)
		var capacity agent.PrivacyMetadataStatus
		if err != nil || detached || c.DecodeStrict([]byte(text), &capacity) != nil || !capacity.WorkReady {
			t.Fatal("UI capacity should have store scope", command, text, err)
		}
	}
	if _, _, err = u.command(ctx, "/capacity prune"); err == nil {
		t.Fatal("UI enabled unsupported pruning")
	}
	for _, action := range []string{"privacy-capacity", "privacy-reserve-replenish", "privacy-operation-retire"} {
		for _, request := range []ipc.Request{
			{ID: "scope-denied", TaskID: "absent-task", Command: action, Payload: []byte("null")},
			{ID: "payload-denied", Command: action, Payload: []byte(`{"prune":true}`)},
		} {
			if _, err = host.Handle(ctx, request); err == nil {
				t.Fatal("capacity accepted invalid scope/payload", request)
			}
		}
	}
	after, err := s.Journal.SnapshotInfo(ctx)
	if err != nil || after != before {
		t.Fatal("capacity changed kernel snapshot", err)
	}
	states, err := s.Journal.Replay(ctx, "")
	if err != nil || len(states) != 0 {
		t.Fatal("capacity created task", err)
	}
	expected, err := s.PrivacyMetadataCapacity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Execute([]string{"privacy-capacity", "--store", directory, "--json"}, &out, &errs); code != 0 {
		t.Fatal("standalone capacity fallback", code, out.String(), errs.String())
	}
	var actual agent.PrivacyMetadataStatus
	if err = c.DecodeStrict(out.Bytes(), &actual); err != nil || actual.Records != expected.Records || actual.CatalogEntries != expected.CatalogEntries || !reflect.DeepEqual(actual.WorkBlockers, expected.WorkBlockers) {
		t.Fatal("reopen changed metadata capacity", actual, err)
	}
}

func TestPrivacyCapacityMissingStoreAndInvalidFlagsHaveNoEffects(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "missing")
	for _, extra := range [][]string{{}, {"--replenish-control-reserve"}, {"--retire-operation-leases"}, {"--replenish-control-reserve", "--retire-operation-leases"}, {"task"}, {"--prune"}} {
		var out, errs bytes.Buffer
		args := append([]string{"privacy-capacity", "--store", directory, "--json"}, extra...)
		if code := Execute(args, &out, &errs); code != 4 {
			t.Fatal(code, out.String(), errs.String())
		}
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("capacity created a missing store", err)
		}
	}
}
