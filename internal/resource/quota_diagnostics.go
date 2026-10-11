package resource

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

const quotaDiagnosticLimit = 64 << 10

type quotaTreeDiagnostic struct {
	Event             string   `json:"event"`
	RootIdentity      string   `json:"root_identity"`
	MeasuredBytes     uint64   `json:"measured_bytes"`
	AdmittedBytes     uint64   `json:"admitted_bytes"`
	MeasuredEntries   uint64   `json:"measured_entries"`
	AdmittedEntries   uint64   `json:"admitted_entries"`
	UnknownAllocation bool     `json:"unknown_allocation"`
	UnadmittedFiles   []string `json:"unadmitted_files"`
	MeasuredFiles     []string `json:"measured_files"`
	AdmittedFiles     []string `json:"admitted_files"`
	Truncated         bool     `json:"truncated"`
}

// writeQuotaTreeDiagnostic is opt-in operator telemetry for investigating a
// receipt mismatch. It never logs file contents or absolute paths, and caps a
// single JSON record at 64 KiB. Normal callers receive the generic integrity
// error and no path details.
func writeQuotaTreeDiagnostic(measured Inventory, admitted map[string]FileReceipt, admittedBytes, admittedEntries uint64, unknown bool, unadmitted []string) {
	if os.Getenv("VIBER_QUOTA_DIAGNOSTICS") != "1" {
		return
	}
	item := quotaTreeDiagnostic{
		Event:             "physical_quota_unadmitted_tree_file",
		RootIdentity:      measured.RootIdentity,
		MeasuredBytes:     measured.ChargedBytes,
		AdmittedBytes:     admittedBytes,
		MeasuredEntries:   measured.Entries,
		AdmittedEntries:   admittedEntries,
		UnknownAllocation: unknown,
		UnadmittedFiles:   []string{},
		MeasuredFiles:     []string{},
		AdmittedFiles:     []string{},
	}
	used := 1024 // Conservative room for fixed JSON fields and punctuation.
	appendNames := func(destination *[]string, names []string) {
		for _, name := range names {
			if used >= quotaDiagnosticLimit-1 {
				item.Truncated = true
				return
			}
			name = quotaDiagnosticName(name)
			encoded, err := json.Marshal(name)
			if err != nil || used+len(encoded)+1 > quotaDiagnosticLimit-1024 {
				item.Truncated = true
				return
			}
			*destination = append(*destination, name)
			used += len(encoded) + 1
		}
	}

	unadmitted = append([]string(nil), unadmitted...)
	sort.Strings(unadmitted)
	appendNames(&item.UnadmittedFiles, unadmitted)
	measuredNames := make([]string, 0, len(measured.Files))
	for _, file := range measured.Files {
		measuredNames = append(measuredNames, file.Path)
	}
	appendNames(&item.MeasuredFiles, measuredNames)
	admittedNames := make([]string, 0, len(admitted))
	for name := range admitted {
		admittedNames = append(admittedNames, name)
	}
	sort.Strings(admittedNames)
	appendNames(&item.AdmittedFiles, admittedNames)

	raw, err := json.Marshal(item)
	if err != nil || len(raw) > quotaDiagnosticLimit {
		return
	}
	_, _ = os.Stderr.Write(append(raw, '\n'))
}

func quotaDiagnosticName(name string) string {
	if len(name) > 4096 || !validRelativeReceiptPath(name) {
		return "<invalid-relative-path>"
	}
	return filepath.ToSlash(name)
}
