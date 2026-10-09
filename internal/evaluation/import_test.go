package evaluation

import (
	"encoding/json"
	c "github.com/ixayldz/Viber/internal/contracts"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeInputs(t *testing.T, p Protocol, rows []Observation) (string, string) {
	t.Helper()
	dir := t.TempDir()
	protocol := filepath.Join(dir, "protocol.json")
	raw, _ := c.CanonicalV1(p)
	if err := os.WriteFile(protocol, raw, 0600); err != nil {
		t.Fatal(err)
	}
	observations := filepath.Join(dir, "observations.jsonl")
	data := []byte{}
	for _, row := range rows {
		raw, _ := c.CanonicalV1(row)
		data = append(data, raw...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(observations, data, 0600); err != nil {
		t.Fatal(err)
	}
	return protocol, observations
}
func TestImportedBundleRecomputesAllRowsAndRejectsForgedStatistics(t *testing.T) {
	p := protocolFixture(2, 3)
	rows := []Observation{observed(p, "variant", "a", 1, c.Pass), observed(p, "variant", "a", 2, c.FailVerdict)}
	protocol, observations := writeInputs(t, p, rows)
	input, err := ReadImportedInputs(protocol, observations)
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "bundle")
	manifest, err := PublishImportedReport(input, bundle)
	if err != nil {
		t.Fatal(err)
	}
	original, err := ImportedReport(p, rows)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadImportedReport(bundle)
	if err != nil || !reflect.DeepEqual(got, original) {
		t.Fatal(got, err)
	}
	if _, err = PublishImportedReport(input, bundle); err == nil {
		t.Fatal("existing report overwritten")
	}
	if len(manifest.Files) != 3 || manifest.Files[1].Path != "observations.json" {
		t.Fatal(manifest)
	}
	// Updating a file hash cannot make a changed presentation statistic valid.
	original.ReleaseEvidence = true
	raw, _ := json.Marshal(original)
	if err = os.WriteFile(filepath.Join(bundle, "report.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	manifest.Files[2] = OutputFile{"report.json", c.HashBytes(raw), int64(len(raw))}
	raw, _ = c.CanonicalV1(manifest)
	if err = os.WriteFile(filepath.Join(bundle, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadImportedReport(bundle); err == nil {
		t.Fatal("forged release flag accepted after rehash")
	}
}
func TestImportedInputBoundsAndNoPartialAdmission(t *testing.T) {
	p := protocolFixture(1, 1)
	for _, mode := range []string{"empty", "blank-row", "unknown-field", "duplicate", "wrong-assignment", "float", "truncated", "empty-object"} {
		t.Run(mode, func(t *testing.T) {
			rows := []Observation{observed(p, "variant", "a", 1, c.Pass)}
			protocol, observations := writeInputs(t, p, rows)
			raw, _ := os.ReadFile(observations)
			switch mode {
			case "empty":
				raw = nil
			case "blank-row":
				raw = append(raw, '\n', '\n')
			case "unknown-field":
				raw = append([]byte("{\"untrusted_override\":true,"), raw[1:]...)
			case "duplicate":
				raw = append(raw, raw...)
			case "wrong-assignment":
				rows[0].AssignmentID = c.HashBytes([]byte("other"))
				raw, _ = c.CanonicalV1(rows[0])
			case "float":
				raw = []byte("{\"schema_version\":1.5}")
			case "truncated":
				raw = raw[:len(raw)/2]
			case "empty-object":
				raw = []byte("{}")
			}
			if err := os.WriteFile(observations, raw, 0600); err != nil {
				t.Fatal(err)
			}
			input, err := ReadImportedInputs(protocol, observations)
			if err == nil {
				_, err = PublishImportedReport(input, filepath.Join(t.TempDir(), "result"))
			}
			if mode == "empty" {
				if err != nil {
					t.Fatal(err)
				}
				report, err := ImportedReport(input.Protocol, input.Observations)
				if err != nil || findArm(t, report, "variant").Primary.Missing != 1 {
					t.Fatal(report, err)
				}
			} else if err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}
func TestImportedBundleMissingFilesAndTraversalAreRejected(t *testing.T) {
	p := protocolFixture(1, 1)
	protocol, observations := writeInputs(t, p, nil)
	input, err := ReadImportedInputs(protocol, observations)
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "bundle")
	manifest, err := PublishImportedReport(input, bundle)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Files[0].Path = "../protocol.json"
	raw, _ := c.CanonicalV1(manifest)
	if err = os.WriteFile(filepath.Join(bundle, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadImportedReport(bundle); err == nil {
		t.Fatal("manifest traversal accepted")
	}
}
