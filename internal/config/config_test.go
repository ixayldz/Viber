package config

import (
	"encoding/json"
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreferencePrecedenceNeverBroadensRestriction(t *testing.T) {
	yes, no := true, false
	user := File{SchemaVersion: 1, Preferences: Preferences{Provider: "ollama", Model: "user-model"}, Restrictions: Privacy{RemoteInference: &no, TelemetryExport: &no, TrainingExport: &no, SensitivePaths: []string{"private/**"}}}
	project := File{SchemaVersion: 1, Preferences: Preferences{Provider: "openai", Model: "project-model"}, Restrictions: Privacy{RemoteInference: &yes, TelemetryExport: &yes, TrainingExport: &yes, SensitivePaths: []string{"notes.txt"}}}
	resolved, err := Resolve(&user, &project)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Preferences.Model != "project-model" || resolved.AdmitProvider("openai", true) == nil {
		t.Fatal("preference granted remote authority", resolved)
	}
	if resolved.AdmitProvider("ollama", false) != nil {
		t.Fatal("unrelated local axis was denied")
	}
	project.Restrictions.RemoteInference = &no
	if !*resolved.Layers[1].File.Restrictions.RemoteInference {
		t.Fatal("config resolution aliased caller data")
	}
	raw, _ := c.CanonicalV1(resolved)
	var restored Resolved
	if err = c.DecodeStrict(raw, &restored); err != nil || restored.Validate() != nil || restored.AdmitProvider("openai", true) == nil {
		t.Fatal("restore broadened restrictions", err)
	}
}
func TestEmptyProviderRestrictionSurvivesSerialization(t *testing.T) {
	empty := []string{}
	f := File{SchemaVersion: 1, Restrictions: Privacy{AllowedProviders: &empty}}
	resolved, err := Resolve(&f, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(resolved)
	var restored Resolved
	if err = json.Unmarshal(raw, &restored); err != nil || restored.Validate() != nil || restored.AdmitProvider("ollama", false) == nil {
		t.Fatal("empty provider deny disappeared", string(raw), err)
	}
}
func TestConfigErrorsNeverEchoUnknownSecretValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	for _, raw := range []string{`{"schema_version":1,"api_key":"CANARY_SECRET_999"}`, `{"schema_version":1,"preferences":{"model":"CANARY_SECRET_999"}`, `{"schema_version":2,"preferences":{},"restrictions":{}}`} {
		if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Read(p)
		if err == nil || strings.Contains(err.Error(), "CANARY_SECRET_999") || strings.Contains(err.Error(), p) {
			t.Fatal("unsafe config diagnostic", err)
		}
	}
}
