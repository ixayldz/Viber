package contracts

import (
	"strings"
	"testing"
	"time"
)

func TestRetentionConsentRejectsImplicitScopeActorClockAndNoncanonicalDeadline(t *testing.T) {
	valid := RetentionPolicy{SchemaVersion: 1, TaskID: "task", Revision: 1, Actor: "USER", Scope: "MANAGED_FAMILY_TASK_CONTENT", Mode: "EXPIRE", Clock: "SYSTEM_UTC", ChangedAt: "2026-10-10T08:00:00Z", Deadline: "2026-10-10T09:00:00Z", Acknowledgement: RetentionAcknowledgement, AuthorityDigest: HashBytes([]byte("authority")), SourcePhysicalRoot: HashBytes([]byte("root")), SourceTaskSequence: 1, SourceDocumentDigest: HashBytes([]byte("document"))}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RetentionPolicy){
		"model actor":              func(p *RetentionPolicy) { p.Actor = "MODEL" },
		"provider scope":           func(p *RetentionPolicy) { p.Scope = "ALL_PROVIDER_DATA" },
		"implicit acknowledgement": func(p *RetentionPolicy) { p.Acknowledgement = "" },
		"unspecified clock":        func(p *RetentionPolicy) { p.Clock = "" },
		"offset":                   func(p *RetentionPolicy) { p.Deadline = "2026-10-10T12:00:00+03:00" },
		"zero fraction":            func(p *RetentionPolicy) { p.Deadline = "2026-10-10T09:00:00.000Z" },
		"elapsed at consent":       func(p *RetentionPolicy) { p.Deadline = p.ChangedAt },
		"unbounded timestamp":      func(p *RetentionPolicy) { p.Deadline = strings.Repeat("0", 1024) },
		"missing source":           func(p *RetentionPolicy) { p.SourceDocumentDigest = "" },
		"path task":                func(p *RetentionPolicy) { p.TaskID = "../task" },
		"keep with expiry":         func(p *RetentionPolicy) { p.Mode = "KEEP" },
	} {
		t.Run(name, func(t *testing.T) {
			policy := valid
			mutate(&policy)
			if policy.Validate() == nil {
				t.Fatal("unsafe retention consent accepted")
			}
		})
	}
	if (RetentionExpiry{Policy: valid, ObservedAt: "2026-10-10T08:59:59Z"}).Validate() == nil {
		t.Fatal("pre-deadline expiry accepted")
	}
}

func FuzzRetentionTimestampNeverNormalizesAuthorization(f *testing.F) {
	for _, seed := range []string{"2026-10-10T09:00:00Z", "2026-10-10T09:00:00.123456789Z", "2026-10-10T12:00:00+03:00", "2026-10-10T09:00:00.0Z", "", "0001-01-01T00:00:00Z"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		stamp, err := RetentionTime(value)
		if err == nil && (stamp.IsZero() || len(value) > 30 || stamp.Location() != time.UTC || stamp.Format(time.RFC3339Nano) != value) {
			t.Fatal("retention normalized an ambiguous authorization timestamp")
		}
	})
}
