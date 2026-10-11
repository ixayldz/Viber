package config

import (
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func TestCurrentOperatorRevisionCannotBeChangedRemovedOrRegressed(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(directory, "operator.json")
	initial := Authority{SchemaVersion: 1, Revision: 2}
	write := func(value Authority) {
		raw, err := c.CanonicalV1(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(name, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(initial)
	ref, err := BindAuthority(name, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ref.Current(); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int64{1, 3} {
		changed := initial
		changed.Revision = revision
		write(changed)
		if _, err = ref.Current(); err == nil {
			t.Fatal("changed operator authority accepted")
		}
	}
	changed := initial
	off := false
	changed.Restrictions.RemoteInference = &off
	write(changed)
	if _, err = ref.Current(); err == nil {
		t.Fatal("same-revision policy mutation accepted")
	}
	if err = os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if _, err = ref.Current(); err == nil {
		t.Fatal("missing current authority fell back")
	}
	retained := Resolved{Authority: &ref}
	if err = retained.AdmitRetainedProvider("ollama", false); err != nil {
		t.Fatal("revocation made valid historical binding unreadable", err)
	}
	if err = retained.AdmitProvider("ollama", false); err == nil {
		t.Fatal("revoked authority admitted a new provider effect")
	}
	if _, err = BindAuthority(filepath.Join(source, "policy.json"), source); err == nil {
		t.Fatal("project can supply operator authority")
	}
}

func TestOperatorRestrictionsNeverBroadenRetainedProviderOrEffects(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	off := false
	allowed := []string{"ollama"}
	effects := []string{"snapshot.read"}
	authority := Authority{SchemaVersion: 1, Revision: 1, Restrictions: Privacy{RemoteInference: &off, AllowedProviders: &allowed}, AllowedEffects: &effects}
	raw, _ := c.CanonicalV1(authority)
	name := filepath.Join(directory, "operator.json")
	if err := os.WriteFile(name, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ref, err := BindAuthority(name, source)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved.Authority = &ref
	if err = resolved.Validate(); err != nil {
		t.Fatal(err)
	}
	if err = resolved.AdmitProvider("openai", true); err == nil {
		t.Fatal("operator deny broadened")
	}
	if err = resolved.AdmitProvider("ollama", false); err != nil {
		t.Fatal(err)
	}
	if err = resolved.AdmitEffect("model.infer"); err == nil {
		t.Fatal("operator effect deny ignored")
	}
	if err = resolved.AdmitEffect("snapshot.read"); err != nil {
		t.Fatal(err)
	}
	ref.Digest = c.HashBytes([]byte("changed"))
	if err = resolved.Validate(); err == nil {
		t.Fatal("forged retained authority admitted")
	}
}
