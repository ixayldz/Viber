package agent

import (
	"context"
	"testing"

	"github.com/ixayldz/Viber/internal/resource"
)

// NTFS resident records can consume fewer than 4096 bytes. Measure the one
// actual new receipt and validate its bound catalog instead of assuming a
// platform allocation quantum.
func addedPrivacyMetadataCharge(t *testing.T, s *Session, before []resource.MetadataFileRef) uint64 {
	t.Helper()
	ctx := context.Background()
	after, _, err := s.PhysicalQuota.MetadataCatalogSnapshot(ctx)
	if err != nil || len(after) != len(before)+1 {
		t.Fatal("retry did not add exactly one privacy metadata receipt", before, after, err)
	}
	old := make(map[string]resource.Class, len(before))
	for _, ref := range before {
		old[ref.Path] = ref.Class
	}
	var added resource.MetadataFileRef
	for _, ref := range after {
		if class, found := old[ref.Path]; found {
			if class != ref.Class {
				t.Fatal("retry changed an existing privacy metadata class", ref)
			}
			delete(old, ref.Path)
		} else {
			if added.Path != "" {
				t.Fatal("retry replaced existing metadata receipts", before, after)
			}
			added = ref
		}
	}
	if len(old) != 0 || added.Path == "" || added.Class != resource.Payload {
		t.Fatal("retry lost prior metadata or used the control reserve", before, after)
	}
	root, err := openPrivacyRoot(*s.privacy)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = s.PhysicalQuota.ValidateMetadataFiles(ctx, root, after); err != nil {
		t.Fatal("privacy metadata no longer matches its exact physical catalog", err)
	}
	file, err := resource.MeasureFile(root, added.Path)
	if err != nil || file.ChargedBytes == 0 {
		t.Fatal("new metadata receipt has no exact measured charge", file, err)
	}
	return file.ChargedBytes
}
