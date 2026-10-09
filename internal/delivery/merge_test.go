package delivery

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"strings"
	"testing"
)

func TestThreeWayPreservesDisjointEditsAndExactNewlines(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		base := strings.Join([]string{"one", "two", "three", "four", "final"}, newline)
		candidate := strings.Replace(base, "two", "candidate", 1)
		current := strings.Replace(base, "four", "user", 1)
		merged, rebased, err := MergeBytes(context.Background(), []byte(base), []byte(candidate), []byte(current))
		want := strings.Replace(candidate, "four", "user", 1)
		if err != nil || !rebased || string(merged) != want {
			t.Fatal("independent user change lost", err, string(merged))
		}
		if strings.HasSuffix(string(merged), newline) {
			t.Fatal("invented final newline")
		}
	}
	base := "a\nb\nc\nd\n"
	candidate := "new\na\nb\nc\nd\n"
	current := "a\nb\nc\nd\nuser\n"
	merged, _, err := MergeBytes(context.Background(), []byte(base), []byte(candidate), []byte(current))
	if err != nil || string(merged) != "new\na\nb\nc\nd\nuser\n" {
		t.Fatal("independent insertion lost", err)
	}
}
func TestThreeWayOverlapsAndBinaryDivergenceNeverPublish(t *testing.T) {
	cases := []struct{ base, candidate, current string }{
		{"a\nb\nc\n", "a\ncandidate\nc\n", "a\nuser\nc\n"},
		{"a\nb\n", "a\ninsert\nb\n", "a\nother\nb\n"},
		{"a\nb\nc\n", "a\nc\n", "a\nuser\nc\n"},
		{"a\x00b", "c\x00d", "e\x00f"},
	}
	for _, item := range cases {
		merged, _, err := MergeBytes(context.Background(), []byte(item.base), []byte(item.candidate), []byte(item.current))
		var failure *c.Error
		if !errors.As(err, &failure) || failure.Code != c.Conflict || merged != nil {
			t.Fatal("conflicting preview published", err)
		}
	}
	base := []byte("a\nb\nc\n")
	same := []byte("a\nshared\nc\n")
	merged, _, err := MergeBytes(context.Background(), base, same, same)
	if err != nil || !bytes.Equal(merged, same) {
		t.Fatal("identical edit not idempotent", err)
	}
}
func TestThreeWayBoundedCostCancellationAndOwnership(t *testing.T) {
	source := []byte{0, 1, 2}
	merged, _, err := MergeBytes(context.Background(), source, source, source)
	if err != nil {
		t.Fatal(err)
	}
	merged[0] = 9
	if source[0] != 0 {
		t.Fatal("preview aliases live source")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = MergeBytes(canceled, nil, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	large := strings.Repeat("a\n", 3000)
	_, _, err = MergeBytes(context.Background(), []byte(large), []byte(strings.Repeat("b\n", 3000)), []byte(strings.Repeat("c\n", 3000)))
	var failure *c.Error
	if !errors.As(err, &failure) || failure.Code != c.UnsupportedCapability {
		t.Fatal("unbounded line matrix", err)
	}
}
func FuzzThreeWayUnchangedCandidatePreservesUserBytes(f *testing.F) {
	f.Add([]byte("a\r\nb"), []byte("user\r\nb"))
	f.Add([]byte{0, 1, 255}, []byte{0, 2, 254})
	f.Fuzz(func(t *testing.T, base, current []byte) {
		if len(base) > 8192 || len(current) > 8192 {
			t.Skip()
		}
		result, _, err := MergeBytes(context.Background(), base, base, current)
		if err != nil || !bytes.Equal(result, current) {
			t.Fatal("unchanged candidate altered user bytes", err)
		}
		// Independently edited unique lines exercise the actual diff/merge
		// path, including arbitrary binary input represented as exact text.
		first := "candidate-" + base64.StdEncoding.EncodeToString(base) + "\r\n"
		last := "user-" + base64.StdEncoding.EncodeToString(current)
		source := []byte("original-first\r\nfixed-middle\r\noriginal-last")
		proposed := []byte(first + "fixed-middle\r\noriginal-last")
		outside := []byte("original-first\r\nfixed-middle\r\n" + last)
		combined, _, err := MergeBytes(context.Background(), source, proposed, outside)
		if err != nil || string(combined) != first+"fixed-middle\r\n"+last {
			t.Fatal("disjoint user/candidate edit lost", err)
		}
	})
}
