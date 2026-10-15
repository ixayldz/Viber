package resource

import (
	"context"
	"errors"
	"os"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func TestMetadataCatalogFeatureUsageIncludesBoundState(t *testing.T) {
	authority, _ := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	defer ledger.Close()

	usage, err := ledger.FeatureUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !usage.PrivacyMetadata || !usage.AllocatorSelfOverhead {
		t.Fatalf("bound metadata state did not advertise its durable protocol: %+v", usage)
	}
}

func TestMetadataCatalogImportPublishAndFreeReceipt(t *testing.T) {
	authority, _ := testAuthority(t)
	ledger := openTestLedger(t, authority, smallLimits(), "")
	defer ledger.Close()
	root, err := os.OpenRoot(authority.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = root.MkdirAll("records", 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"schema_version":1,"type":"CREATE","task":"metadata"}`)
	if err = fileguard.Publish(root, "records/0000000001.json", raw); err != nil {
		t.Fatal(err)
	}
	refs := []MetadataFileRef{{Path: "records/0000000001.json", Class: Payload}}
	digest, err := ledger.ImportMetadataFiles(context.Background(), root, refs)
	if err != nil {
		t.Fatal(err)
	}
	retryDigest, err := ledger.ImportMetadataFiles(context.Background(), root, refs)
	if err != nil || retryDigest != digest {
		t.Fatalf("same inventory import was not idempotent: digest=%q retry=%q err=%v", digest, retryDigest, err)
	}
	if err = ledger.PublishMetadataFile(context.Background(), root, "records/0000000002.json", raw, Payload); err != nil {
		t.Fatal(err)
	}
	if err = ledger.PublishMetadataFile(context.Background(), root, "records/0000000002.json", raw, Payload); err != nil {
		t.Fatal(err)
	}
	if err = ledger.PublishMetadataFile(context.Background(), root, "records/0000000002.json", []byte("changed"), Payload); err == nil {
		t.Fatal("immutable metadata path accepted changed content")
	}
	_, err = root.Lstat("records/0000000002.json")
	if err != nil {
		t.Fatal(err)
	}
	wrongDigest := c.HashBytes([]byte("wrong"))
	if _, err = ledger.RemoveMetadataFile(context.Background(), root, "records/0000000002.json", wrongDigest, int64(len(raw)), 1<<20); err == nil {
		t.Fatal("wrong metadata digest was accepted")
	}
	fileDigest := c.HashBytes(raw)
	removed, err := ledger.RemoveMetadataFile(context.Background(), root, "records/0000000002.json", fileDigest, int64(len(raw)), 1<<20)
	if err != nil || !removed {
		t.Fatalf("exact metadata free receipt did not remove file: removed=%v err=%v", removed, err)
	}
	if _, err = root.Lstat("records/0000000002.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata file still exists after exact free receipt: %v", err)
	}
}
