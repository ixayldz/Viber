package contracts

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCanonicalIdentity(t *testing.T) {
	a, err := Digest(map[string]any{"b": int64(2), "a": "x"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Digest(map[string]any{"a": "x", "b": int64(2)})
	if err != nil || a != b {
		t.Fatal("field order changed identity", err)
	}
	null, _ := Digest(map[string]any{"a": nil})
	absent, _ := Digest(map[string]any{})
	if null == absent {
		t.Fatal("null collapsed with absent")
	}
	x, _ := Digest([]int{1, 2})
	y, _ := Digest([]int{2, 1})
	if x == y {
		t.Fatal("array order collapsed")
	}
	if HashBytes([]byte("a\r\n")) == HashBytes([]byte("a\n")) {
		t.Fatal("byte normalization")
	}
	if HashBytes([]byte("{}")) == absent {
		t.Fatal("digest domain not separated")
	}
}
func TestStrictJSON(t *testing.T) {
	cases := []string{`{"id":"a","id":"b"}`, `{"unknown":1}`, `{"id":"a"} {}`, `{"id":1.5}`, `{"id":1e0}`, `{"id":-0}`, `{"id":9223372036854775808}`, `{"id":"\ud800"}`}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			var target struct {
				ID string `json:"id"`
			}
			if DecodeStrict([]byte(raw), &target) == nil {
				t.Fatal("invalid JSON accepted")
			}
		})
	}
	var target struct {
		ID string `json:"id"`
	}
	if err := DecodeStrict([]byte(`{"id":"Türkçe 😀"}`), &target); err != nil {
		t.Fatal(err)
	}
	if err := DecodeStrict([]byte{0xff}, &target); err == nil {
		t.Fatal("invalid UTF8 accepted")
	}
}
func TestSpecNeverVacuouslyValid(t *testing.T) {
	s := validSpec()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Requirements = nil
	if s.Validate() == nil {
		t.Fatal("empty criteria accepted")
	}
	s = validSpec()
	s.Inputs[0].Integrity = Missing
	if s.Validate() == nil {
		t.Fatal("missing intent accepted")
	}
	s = validSpec()
	s.Requirements[0].Source.End = 999
	if s.Validate() == nil {
		t.Fatal("invalid source span accepted")
	}
}
func validSpec() TaskSpec {
	return TaskSpec{SchemaVersion: 1, TaskID: "task", Version: 1, Goal: "fix", DeliveryPolicy: "CANDIDATE_ONLY", ProtectedOrigin: HashBytes([]byte("checks")),
		Inputs:       []InputSource{{ID: "input", PayloadRef: "ctx://input/1", Digest: HashBytes([]byte("fix")), ByteLength: 3, Integrity: Intact}},
		Requirements: []Requirement{{ID: "req", Source: SourceSpan{InputID: "input", End: 3}, Required: true, Risk: "NORMAL", VerificationMethod: "protected-test"}}}
}
func FuzzStrictDecode(f *testing.F) {
	f.Add([]byte(`{"id":"ok"}`))
	f.Add([]byte(`{"id":"one","id":"two"}`))
	f.Add([]byte{0xff})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			return
		}
		var target struct {
			ID string `json:"id"`
		}
		if DecodeStrict(raw, &target) != nil {
			return
		}
		one, err := CanonicalV1(target)
		if err != nil {
			t.Fatal(err)
		}
		var next struct {
			ID string `json:"id"`
		}
		if err := DecodeStrict(one, &next); err != nil {
			t.Fatal(err)
		}
		two, err := CanonicalV1(next)
		if err != nil || !bytes.Equal(one, two) {
			t.Fatal("canonical instability", err)
		}
	})
}

func TestInvalidMetadataStringsCannotCollapseIdentity(t *testing.T) {
	if _, err := Digest(map[string]any{"key": string([]byte{0xff})}); err == nil {
		t.Fatal("invalid UTF8 became replacement character")
	}
	if _, err := Digest(map[string]any{string([]byte{0xff}): "value"}); err == nil {
		t.Fatal("invalid key normalized")
	}
}

func TestTypedFloatCannotBypassIntegerContract(t *testing.T) {
	if _, err := Digest(map[string]any{"n": float64(1)}); err == nil {
		t.Fatal("typed float accepted as integer")
	}
}

func TestStrictShapeRejectsCaseAliasesNullsAndMissingFields(t *testing.T) {
	type doc struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	for _, raw := range []string{`{"ID":"a","version":1}`, `{"id":"a","ID":"b","version":1}`, `{"id":null,"version":1}`, `{"id":"a","version":null}`, `{"id":"a"}`, "null"} {
		var target doc
		if DecodeStrict([]byte(raw), &target) == nil {
			t.Fatal("invalid authoritative shape accepted", raw)
		}
	}
	var target doc
	if err := DecodeStrict([]byte(`{"id":"a","version":1}`), &target); err != nil {
		t.Fatal(err)
	}
}

func TestRawMessageKeepsJSONSyntaxAndDoesNotWeakenCanonicalValidation(t *testing.T) {
	var value struct {
		Raw   json.RawMessage `json:"raw"`
		Bytes []byte          `json:"bytes"`
	}
	if err := DecodeStrict([]byte(`{"raw":{"path":"a.txt"},"bytes":"YWJj"}`), &value); err != nil {
		t.Fatal(err)
	}
	if string(value.Raw) != `{"path":"a.txt"}` || string(value.Bytes) != "abc" {
		t.Fatal("RawMessage treated as base64")
	}
	for _, raw := range []string{`{"raw":{"x":1.0},"bytes":"YWJj"}`, `{"raw":{"x":1,"x":2},"bytes":"YWJj"}`, `{"raw":{},"bytes":{"not":"base64"}}`} {
		if err := DecodeStrict([]byte(raw), &value); err == nil {
			t.Fatal("strict validation weakened", raw)
		}
	}
}
