package resource

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func captureQuotaDiagnostic(t *testing.T, call func()) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "quota-diagnostic-*.log")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = file
	t.Cleanup(func() {
		os.Stderr = previous
		_ = file.Close()
	})
	call()
	if err = file.Sync(); err != nil {
		t.Fatal(err)
	}
	os.Stderr = previous
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestQuotaTreeDiagnosticIsOptInRelativeAndBounded(t *testing.T) {
	t.Setenv("VIBER_QUOTA_DIAGNOSTICS", "")
	measured := Inventory{RootIdentity: "root", ChargedBytes: 4096, Entries: 2, Files: []FileReceipt{{Path: "private/name.txt"}}}
	known := map[string]FileReceipt{"admitted.txt": {Path: "admitted.txt"}}
	unadmitted := []string{"private/name.txt"}
	if got := captureQuotaDiagnostic(t, func() { writeQuotaTreeDiagnostic(measured, known, 4096, 1, false, unadmitted) }); got != "" {
		t.Fatalf("default quota diagnostics wrote to stderr: %q", got)
	}

	t.Setenv("VIBER_QUOTA_DIAGNOSTICS", "1")
	paths := make([]string, 256)
	for i := range paths {
		paths[i] = strings.Repeat("x", 240) + string(rune('a'+i%26))
	}
	measured.Files = make([]FileReceipt, len(paths))
	for i, name := range paths {
		measured.Files[i] = FileReceipt{Path: name}
	}
	output := captureQuotaDiagnostic(t, func() { writeQuotaTreeDiagnostic(measured, known, 4096, 1, false, paths) })
	if len(output) == 0 || len(output) > quotaDiagnosticLimit+1 || !bytes.Contains([]byte(output), []byte(`"event":"physical_quota_unadmitted_tree_file"`)) {
		t.Fatalf("quota diagnostic missing or out of bounds: len=%d", len(output))
	}
	if strings.Contains(output, t.TempDir()) || strings.Contains(output, "C:\\Users\\") || !strings.Contains(output, `"truncated":true`) {
		t.Fatalf("quota diagnostic leaked an absolute path or omitted truncation marker: %s", output)
	}
}
