package workspace

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPackedGitHistoryReadsObjectsAndRejectsStalePackStore(t *testing.T) {
	root := gitFixture(t)
	for i := 0; i < 6; i++ {
		content := make([]byte, 0, 8192)
		for row := 0; row < 128; row++ {
			content = append(content, []byte("repeated stable source line for packed delta coverage\n")...)
		}
		content = append(content, []byte("revision ")...)
		content = append(content, byte('0'+i), '\n')
		write(t, root, "a.txt", content)
		write(t, root, "b.txt", content)
		fixtureGit(t, root, "add", "a.txt", "b.txt")
		if i == 5 {
			time.Sleep(1100 * time.Millisecond)
		}
		fixtureGit(t, root, "commit", "-m", "packed fixture")
	}
	fixtureGit(t, root, "gc", "--aggressive", "--prune=now")

	source, err := CaptureRepository(root, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	cutoffReader, err := newHistoryReader(source.Snapshot.Git.CommonDir, source.Snapshot.Git.ObjectFormat, DefaultHistoryLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer cutoffReader.root.Close()
	if _, unknown, err := cutoffReader.loadPacks(); err != nil || unknown != "" {
		t.Fatalf("could not read cutoff fixture pack: unknown=%q err=%v", unknown, err)
	}
	head, err := cutoffReader.read(source.Snapshot.Git.HeadObject)
	if err != nil {
		t.Fatal(err)
	}
	headCommit, err := parseCommitHistory(head, cutoffReader.width)
	if err != nil || len(headCommit.parents) == 0 {
		t.Fatalf("fixture HEAD is not a child commit: %v", err)
	}
	parent, err := cutoffReader.read(headCommit.parents[0])
	if err != nil {
		t.Fatal(err)
	}
	parentCommit, err := parseCommitHistory(parent, cutoffReader.width)
	if err != nil || parentCommit.when >= headCommit.when {
		t.Fatalf("fixture commit times do not straddle the cutoff: %v", err)
	}
	cutoff := time.Unix(parentCommit.when, 0).UTC()
	history, err := CaptureGitHistory(source, cutoff, DefaultHistoryLimits())
	if err != nil {
		t.Fatal(err)
	}
	if history.Coverage != "COMPLETE_FIRST_PARENT_WINDOW" || len(history.Commits) < 6 || history.ObjectStoreDigest == "" {
		t.Fatalf("packed history was not complete: coverage=%s commits=%d", history.Coverage, len(history.Commits))
	}
	if history.CutoffUTC != cutoff.Format(time.RFC3339) || history.Commits[0].ObjectID == history.HeadObject {
		t.Fatal("history did not preserve the exact cutoff before captured HEAD")
	}
	if err := ValidateGitHistory(history, source); err != nil {
		t.Fatal(err)
	}

	reader, err := newHistoryReader(source.Snapshot.Git.CommonDir, source.Snapshot.Git.ObjectFormat, DefaultHistoryLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.root.Close()
	if _, unknown, err := reader.loadPacks(); err != nil || unknown != "" {
		t.Fatalf("pack index could not be loaded: unknown=%q err=%v", unknown, err)
	}
	if len(reader.packed) == 0 {
		t.Fatal("git gc produced no indexed packed objects")
	}
	deltaObjects := 0
	for _, pack := range reader.packs {
		for offset := range pack.byOffset {
			objectType, _, _, _, _, _, err := decodeHistoryPackEntry(pack.data, int(offset), reader.width, reader.limits.MaxObjectBytes)
			if err != nil {
				t.Fatal(err)
			}
			if objectType == 6 || objectType == 7 {
				deltaObjects++
			}
		}
	}
	if deltaObjects == 0 {
		t.Fatal("fixture pack did not exercise delta reconstruction")
	}
	for oid := range reader.packed {
		if _, err := reader.read(oid); err != nil {
			t.Fatalf("packed object %s was rejected: %v", oid, err)
		}
	}

	write(t, source.Snapshot.Git.CommonDir, filepath.Join("objects", "pack", "pack-"+strings.Repeat("0", 40)+".idx"), []byte("changed inventory"))
	if err := ValidateGitHistory(history, source); err == nil {
		t.Fatal("history report remained valid after its packed object store changed")
	}
}
