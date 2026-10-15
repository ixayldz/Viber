package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

func TestRestoredBootstrapPinsExactlyOneHandoffPath(t *testing.T) {
	for _, scenario := range []string{"stage", "target", "both", "foreign-target"} {
		t.Run(scenario, func(t *testing.T) {
			p, stage, target, displaced := restoredBootstrapWitnessFixture(t)
			switch scenario {
			case "target":
				if err := os.Rename(stage, target); err != nil {
					t.Fatal(err)
				}
			case "both":
				if err := os.Rename(stage, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(stage, 0700); err != nil {
					t.Fatal(err)
				}
			case "foreign-target":
				if err := os.Rename(stage, displaced); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.validate(); err != nil {
				t.Fatal("test receipt invalid", err)
			}
			err := verifyBootstrapWitness(p)
			if scenario == "both" || scenario == "foreign-target" {
				if err == nil {
					t.Fatal("bootstrap accepted an ambiguous or foreign owner path")
				}
				return
			}
			if err != nil {
				t.Fatal("bootstrap rejected the exact stage/target handoff inode", err)
			}
		})
	}
}

func restoredBootstrapWitnessFixture(t *testing.T) (PrivacyBootstrapReservation, string, string, string) {
	t.Helper()
	base := t.TempDir()
	parentPath := filepath.Join(base, "parent")
	if err := os.Mkdir(parentPath, 0700); err != nil {
		t.Fatal(err)
	}
	parentPath, err := fileguard.ResolveProspective(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	sourceOwner := filepath.Join(base, "source-owner")
	stage := filepath.Join(parentPath, "restore-stage")
	target := filepath.Join(parentPath, "restore-target")
	displaced := filepath.Join(parentPath, "displaced-stage")
	privacyRoot := filepath.Join(parentPath, "privacy-root")
	for _, path := range []string{sourceOwner, stage, privacyRoot} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	openIdentity := func(path string) (string, string) {
		t.Helper()
		root, err := os.OpenRoot(path)
		if err != nil {
			t.Fatal(err)
		}
		identity, identityErr := fileguard.DirectoryIdentity(root)
		volume, volumeErr := fileguard.VolumeIdentity(root)
		if err = errors.Join(identityErr, volumeErr, root.Close()); err != nil {
			t.Fatal(err)
		}
		return identity, volume
	}
	parentID, volume := openIdentity(parentPath)
	ownerID, _ := openIdentity(sourceOwner)
	stageID, _ := openIdentity(stage)
	rootID, rootVolume := openIdentity(privacyRoot)
	if rootVolume != volume {
		t.Fatal("fixture crossed volumes")
	}
	p := PrivacyBootstrapReservation{
		SchemaVersion: 1, State: "AUTHORITY_BOUND", AuthorityID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Directory: privacyRoot,
		StagingDirectory: filepath.Join(parentPath, "unused-bootstrap-stage"), ParentPath: parentPath,
		ParentIdentity: parentID, VolumeIdentity: volume, OwnerDirectory: sourceOwner, OwnerIdentity: ownerID,
		OwnerInstanceID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", OwnerGeneration: 1, UpperBytes: 1 << 20, UpperEntries: 16,
		PhysicalRoot: rootID, AuthorityDigest: c.HashBytes([]byte("authority")), RestoreBinding: c.HashBytes([]byte("restore-binding")),
		RestoreOwnerDirectory: stage, RestoreOwnerTarget: target, RestoreOwnerIdentity: stageID,
		RestoreOwnerInstanceID: "cccccccccccccccccccccccccccccccc", RestoreOwnerGeneration: 1,
	}
	p.RequestDigest, err = PrivacyBootstrapRequestDigest(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, stage, target, displaced
}
