package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
)

func TestDeliveryApplyPreservesOpenWriterAndReviewPackageSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	cacheRoot, err := filepath.Abs(filepath.Join("..", "..", ".cache"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(cacheRoot, 0700); err != nil {
		t.Fatal(err)
	}
	tempRoot, err := os.MkdirTemp(cacheRoot, "delivery-apply-test-")
	if err != nil {
		t.Fatal(err)
	}
	tempRoot, err = filepath.Abs(tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cacheRoot, tempRoot)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("test temporary directory escaped the repository cache: %s", tempRoot)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempRoot) })
	turns := []Turn{{Calls: []model.Call{patchCall()}, UsageKnown: true}, {Text: "done", UsageKnown: true}}
	source := filepath.Join(tempRoot, "source")
	storeDirectory := filepath.Join(tempRoot, "store")
	if err = os.Mkdir(source, 0700); err == nil {
		err = os.WriteFile(filepath.Join(source, "a.txt"), []byte("before\r\n"), 0600)
	}
	if err != nil {
		t.Fatal(err)
	}
	session, err := Open(ctx, storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := json.Marshal(Fixture{SchemaVersion: 1, Turns: turns})
	if err != nil {
		t.Fatal(err)
	}
	options := StartOptions{Root: source, Prompt: []byte("Change a.txt without altering the live workspace."), TaskID: "task-one", Budget: DefaultBudget(), Autonomy: "guided", AllowUnverified: true, Fixture: fixture}
	if _, err = session.Create(ctx, options); err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	defer func() {
		if session != nil {
			_ = session.Close()
		}
	}()
	state, err := session.Run(ctx, options.TaskID)
	if err != nil || state.Execution != c.Terminated {
		t.Fatal(state, err)
	}
	target := filepath.Join(source, "a.txt")
	writer, err := os.OpenFile(target, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	openedBefore, err := writer.Stat()
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(tempRoot, "apply-review")
	result, err := session.DeliveryApply(ctx, options.TaskID, output)
	if err != nil || result.Status != "UNSUPPORTED_STRONG_EXCLUSIVITY" || result.TargetWritten || result.Backend != "UNSUPPORTED_STRONG_EXCLUSIVITY; CANDIDATE_PATCH_ONLY" || !c.ValidDigest(result.ManifestDigest) || result.PackageDirectory != output {
		t.Fatal("unsafe live apply admission", result, err)
	}
	found := false
	for _, decision := range result.Decisions {
		if decision.Path == "a.txt" && decision.Action == "APPLY_CANDIDATE" {
			found = true
		}
	}
	if !found || result.Changeset == nil {
		t.Fatal("apply preflight omitted per-file target plan", result)
	}
	openedAfter, err := os.Stat(target)
	if err != nil || !os.SameFile(openedBefore, openedAfter) {
		t.Fatal("preflight replaced a file while a writable descriptor was held", err)
	}
	userEdit := []byte("user edit\r\n")
	if err = writer.Truncate(0); err == nil {
		_, err = writer.WriteAt(userEdit, 0)
	}
	if err == nil {
		err = writer.Sync()
	}
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(userEdit) {
		t.Fatal("live apply detached or overwrote the open user writer", string(got), err)
	}

	directory := session.directory
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	session = nil
	reopened, err := OpenExisting(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	session = reopened
	manifest, err := session.readRegisteredDeliveryPreview(ctx, DeliveryReverificationCommand{ParentTask: options.TaskID, PreviewDirectory: output, ManifestDigest: result.ManifestDigest})
	if err != nil || manifest.TaskSeq != state.TaskSeq || manifest.Digest != result.ManifestDigest || manifest.Preview.Status != "READY" {
		t.Fatal("review package could not be reconciled after session restart", manifest, err)
	}
	restored, err := session.DeliveryRestore(ctx, options.TaskID)
	if err != nil || restored.Status != "UNSUPPORTED_NO_APPLIED_RECEIPT" || restored.OwnedReceiptPresent || restored.TargetWritten {
		t.Fatal("restore claimed ownership without an applied receipt", restored, err)
	}
	got, err = os.ReadFile(target)
	if err != nil || string(got) != string(userEdit) {
		t.Fatal("restore changed the user's edit", string(got), err)
	}
}
