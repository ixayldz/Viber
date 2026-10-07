package context

import (
	"math"
	"testing"
)

func bytesEstimator(raw []byte) (int64, error) { return int64(len(raw)), nil }
func TestMandatoryInputCannotBeTruncated(t *testing.T) {
	required := []Block{{ID: "policy", Text: "never deploy; preserve user edits"}}
	_, _, err := Pack(required, nil, Limits{Context: 10, Output: 5, SafetyMargin: 1}, bytesEstimator)
	if err == nil {
		t.Fatal("mandatory content silently truncated")
	}
}
func TestWholeOptionalBlockOmissions(t *testing.T) {
	required := []Block{{ID: "policy", Text: "keep"}}
	optional := []Block{{ID: "large", Text: string(make([]byte, 1000))}, {ID: "small", Text: "ok"}}
	selected, m, err := Pack(required, optional, Limits{Context: 150, Output: 10, SafetyMargin: 10}, bytesEstimator)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || len(m.Omitted) != 1 || m.Omitted[0] != "large" || selected[1].ID != "small" {
		t.Fatal("whole block packing broken", m)
	}
	if m.InputTokens+m.OutputReserve+10 > 150 {
		t.Fatal("context overflow")
	}
}
func TestReserveOverflowAndBadCount(t *testing.T) {
	_, _, err := Pack([]Block{{ID: "r"}}, nil, Limits{Context: math.MaxInt64, Output: math.MaxInt64, SafetyMargin: 1}, bytesEstimator)
	if err == nil {
		t.Fatal("integer overflow bypass")
	}
	_, _, err = Pack([]Block{{ID: "r"}}, nil, Limits{Context: 100}, func([]byte) (int64, error) { return -1, nil })
	if err == nil {
		t.Fatal("negative estimator accepted")
	}

}
