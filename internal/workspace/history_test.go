package workspace

import (
	"strings"
	"testing"
)

func TestHistoryCommitParserKeepsOnlyTreeParentAndCommitterTime(t *testing.T) {
	secretMessage := "credential-looking commit message must not survive"
	obj := historyObject{kind: "commit", data: []byte(strings.Join([]string{
		"tree 0123456789012345678901234567890123456789",
		"parent abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		"author Alice Example <alice@example.test> 1700000000 +0300",
		"committer Alice Example <alice@example.test> 1700000001 +0300",
		"", secretMessage,
	}, "\n"))}
	got, err := parseCommitHistory(obj, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got.tree != "0123456789012345678901234567890123456789" ||
		len(got.parents) != 1 || got.parents[0] != "abcdefabcdefabcdefabcdefabcdefabcdefabcd" ||
		got.when != 1700000001 {
		t.Fatalf("unexpected parsed fields: %#v", got)
	}
	if strings.Contains(strings.Join(got.parents, ""), secretMessage) {
		t.Fatal("commit message leaked into metadata")
	}
}

func TestHistoryTreeDiffSortsAndClassifiesChanges(t *testing.T) {
	old := map[string]historyFile{
		"modify.go": {oid: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", mode: 0100644},
		"delete.go": {oid: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", mode: 0100644},
		"mode.go":   {oid: "cccccccccccccccccccccccccccccccccccccccc", mode: 0100644},
	}
	next := map[string]historyFile{
		"modify.go": {oid: "dddddddddddddddddddddddddddddddddddddddd", mode: 0100644},
		"add.go":    {oid: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", mode: 0100644},
		"mode.go":   {oid: "cccccccccccccccccccccccccccccccccccccccc", mode: 0100755},
	}
	got := diffHistoryTrees(old, next)
	if len(got) != 4 {
		t.Fatalf("got %d changes: %#v", len(got), got)
	}
	want := []struct{ path, kind string }{{"add.go", "ADDED"}, {"delete.go", "DELETED"}, {"mode.go", "MODE_CHANGED"}, {"modify.go", "MODIFIED"}}
	for i, item := range want {
		if got[i].Path != item.path || got[i].Kind != item.kind {
			t.Fatalf("change %d: %#v", i, got[i])
		}
	}
}
