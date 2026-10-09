package cli

import (
	"bytes"
	"github.com/ixayldz/Viber/internal/evaluation"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIEvaluationImportRoundtripAndInvalidInputs(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "report")
	args := []string{"eval-report", "--protocol", filepath.Join("..", "..", "examples", "evaluation", "protocol.json"), "--observations", filepath.Join("..", "..", "examples", "evaluation", "measurements.jsonl"), "--output", bundle, "--json"}
	var out, errout bytes.Buffer
	if code := Execute(args, &out, &errout); code != 0 {
		t.Fatal(code, out.String(), errout.String())
	}
	report, err := evaluation.ReadImportedReport(bundle)
	if err != nil || report.ReleaseEvidence || report.EvidenceMode != "IMPORTED_METADATA_UNATTESTED" {
		t.Fatal(report, err)
	}
	out.Reset()
	errout.Reset()
	if code := Execute([]string{"eval-report-inspect", "--bundle", bundle, "--json"}, &out, &errout); code != 0 || !strings.Contains(out.String(), `"release_evidence":false`) {
		t.Fatal(code, out.String(), errout.String())
	}
	out.Reset()
	errout.Reset()
	if code := Execute(args, &out, &errout); code != 4 {
		t.Fatal("existing report overwritten", code, out.String(), errout.String())
	}
	for _, command := range []string{"eval-report", "eval-report-inspect", "eval-candidate", "eval-inspect"} {
		out.Reset()
		errout.Reset()
		if code := Execute([]string{command, "--json"}, &out, &errout); code != 4 {
			t.Fatal("missing required args admitted", command, code)
		}
	}
	out.Reset()
	errout.Reset()
	if code := Execute([]string{"eval-inspect", "unexpected-task", "--bundle", bundle, "--json"}, &out, &errout); code != 4 {
		t.Fatal("mixed command args admitted", code)
	}
}
func TestCLIIndependentRecipePreflightHasNoOriginalOrOutputSideEffects(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "typo")
	output := filepath.Join(dir, "evaluation")
	recipe := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(recipe, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	code := Execute([]string{"eval-candidate", "task", "--store", store, "--recipe", recipe, "--output", output, "--json"}, &out, &errout)
	if code == 0 {
		t.Fatal("missing original accepted")
	}
	for _, path := range []string{store, output} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("preflight created path", path, err)
		}
	}
}
