package cli

import (
	"bytes"
	"context"
	"github.com/ixayldz/Viber/internal/agent"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectConfigCannotEnableRemoteAndInvalidConfigCreatesNoStore(t *testing.T) {
	for _, raw := range []string{`{"schema_version":1,"preferences":{"provider":"openai","model":"operator-model"},"restrictions":{"remote_inference":true}}`, `{"schema_version":1,"api_key":"CANARY_SECRET_456"}`} {
		source := t.TempDir()
		user := filepath.Join(t.TempDir(), "user.json")
		store := filepath.Join(t.TempDir(), "absent")
		os.WriteFile(filepath.Join(source, "viber.config.json"), []byte(raw), 0600)
		os.WriteFile(user, []byte(`{"schema_version":1,"preferences":{},"restrictions":{"remote_inference":false}}`), 0600)
		var out, errs bytes.Buffer
		code := Execute([]string{"run", "Review", "--root", source, "--store", store, "--user-config", user, "--allow-remote", "--json"}, &out, &errs)
		if code != 4 {
			t.Fatal("config broadened authority", code, out.String(), errs.String())
		}
		if bytes.Contains(out.Bytes(), []byte("CANARY_SECRET_456")) {
			t.Fatal("config secret echoed")
		}
		if _, err := os.Stat(store); !os.IsNotExist(err) {
			t.Fatal("rejected config created store", err)
		}
	}
}
func TestSensitiveConfigIsCapturedBeforeEgressAndRetainedOnRestore(t *testing.T) {
	source := t.TempDir()
	user := filepath.Join(t.TempDir(), "user.json")
	directory := t.TempDir()
	fixture := filepath.Join(t.TempDir(), "fixture.json")
	os.WriteFile(filepath.Join(source, "public.txt"), []byte("public"), 0600)
	os.WriteFile(filepath.Join(source, "private.txt"), []byte("CANARY_PRIVATE_SOURCE_973"), 0600)
	os.WriteFile(filepath.Join(source, "viber.config.json"), []byte(`{"schema_version":1,"preferences":{},"restrictions":{"sensitive_paths":["private.txt"]}}`), 0600)
	os.WriteFile(user, []byte(`{"schema_version":1,"preferences":{},"restrictions":{}}`), 0600)
	os.WriteFile(fixture, []byte(`{"schema_version":1,"turns":[{"text":"review","usage_known":true,"input_tokens":1,"output_tokens":1}]}`), 0600)
	var out, errs bytes.Buffer
	code := Execute([]string{"run", "Review public file", "--root", source, "--store", directory, "--user-config", user, "--offline", "--fixture", fixture, "--task", "privacy", "--json"}, &out, &errs)
	if code != 3 {
		t.Fatal(code, out.String(), errs.String())
	}
	s, err := agent.OpenExisting(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state, doc, err := s.Load(context.Background(), "privacy")
	if err != nil {
		t.Fatal(err)
	}
	capture, err := s.Archive.Get(doc.Baseline)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := capture.Contents["private.txt"]; ok {
		t.Fatal("sensitive payload entered CAS")
	}
	if _, ok := capture.Contents["viber.config.json"]; ok {
		t.Fatal("project config contents entered CAS")
	}
	value, err := s.Observe(context.Background(), "privacy", agent.Observation{Kind: "source-list", Limit: 32})
	if err != nil {
		t.Fatal(err)
	}
	if value.(agent.SourceMatches).Total != 1 {
		t.Fatal("private source metadata in listing", value)
	}
	history, err := s.Observe(context.Background(), "privacy", agent.Observation{Kind: "context-page", Limit: 16384})
	if err != nil {
		t.Fatal(err)
	}
	_ = history
	raw, err := s.Archive.GetBytes("privacy", doc.Context.RequestDigest)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("CANARY_PRIVATE_SOURCE_973")) || bytes.Contains(raw, []byte("private.txt")) {
		t.Fatal("private data entered model context")
	}
	if state.InputBarrier {
		t.Fatal("config introduced input barrier")
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err = s.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if _, err = agent.RestoreBackup(context.Background(), backup, target); err != nil {
		t.Fatal(err)
	}
	restored, err := agent.OpenExisting(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	_, restoredDoc, err := restored.Load(context.Background(), "privacy")
	if err != nil || restoredDoc.Config == nil || restoredDoc.Config.Validate() != nil {
		t.Fatal("privacy config lost on restore", err)
	}
}
