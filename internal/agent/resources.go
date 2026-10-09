package agent

import (
	"context"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/model"
	"runtime"
)

type ResourceReceipt struct {
	SchemaVersion       int          `json:"schema_version"`
	TaskID              string       `json:"task_id"`
	OperationID         string       `json:"operation_id"`
	RequestDigest       string       `json:"request_digest"`
	ProfileDigest       string       `json:"profile_digest"`
	StartedActiveMillis int64        `json:"started_active_millis"`
	EndedActiveMillis   int64        `json:"ended_active_millis"`
	SourceReceipt       string       `json:"model_receipt,omitempty"`
	ToolReply           *model.Reply `json:"tool_reply,omitempty"`
	Candidate           string       `json:"candidate"`
	Measurement         string       `json:"measurement"`
}

func (s *Session) creationResourcePolicy(ctx context.Context, requested *c.ResourcePolicy) (*c.ResourcePolicy, error) {
	view, err := s.Journal.ResourceLedger(ctx)
	if err != nil {
		return nil, err
	}
	if view.Coverage == "LEGACY_UNTRACKED" {
		return nil, c.Fail(c.UnsupportedCapability, "resource accounting requires a fresh store; legacy tasks remain inspectable")
	}
	policy := c.DefaultResourcePolicy()
	if view.Policy != nil {
		policy = *view.Policy
	}
	if requested != nil {
		if err = requested.Validate(); err != nil {
			return nil, err
		}
		if view.Coverage != "EMPTY" && !c.EqualResourcePolicy(policy, *requested) {
			return nil, c.Fail(c.PolicyDenied, "store resource policy is immutable")
		}
		policy = *requested
	}
	return &policy, nil
}
func unresolvedResource(state c.TaskState) *c.ResourceReservation {
	if state.Resources == nil {
		return nil
	}
	for i := range state.Resources.Reservations {
		r := state.Resources.Reservations[i]
		if r.Status != "SETTLED" {
			return &r
		}
	}
	return nil
}
func resourceProfile(doc Document) string {
	digest, _ := c.Digest(struct {
		Runtime  *Runtime      `json:"runtime,omitempty"`
		Checks   *CheckRuntime `json:"checks,omitempty"`
		Protocol string        `json:"protocol"`
	}{doc.Runtime, doc.CheckRuntime, "NATIVE_TOOL_RESOURCE_V1"})
	return digest
}
func resourceUpper(doc Document, modelOperation bool) c.ResourceVector {
	remaining := max(int64(1), doc.Budget.MaxActiveMillis-doc.Budget.ActiveMillis)
	if modelOperation && doc.Runtime != nil {
		remaining = min(remaining, doc.Runtime.TimeoutMillis)
	}
	// This is a charged allocation bound, not an assertion of measured OS CPU,
	// provider GPU, electricity, cloud invoice or physical bytes actually written.
	upper := c.ResourceVector{CPUMillis: remaining * int64(runtime.NumCPU()), WallMillis: remaining, DiskBytes: 8 << 20}
	if !modelOperation {
		upper.DiskBytes = 64 << 20
		if doc.CheckRuntime != nil {
			p := doc.CheckRuntime.Profile
			upper.Children = p.Pids
			upper.CPUMillis += min(remaining, p.TimeoutSeconds*1000) * p.CPUs
			upper.DiskBytes += p.ScratchBytes
		}
	}
	return upper
}
func (s *Session) recordResourceMutation(state c.TaskState, doc Document, kind string, extra *c.EventPayload) error {
	if kind == "TaskCreated" {
		return nil
	}
	if state.Resources == nil {
		if doc.ResourcePolicy != nil {
			return c.Fail(c.StoreIntegrityError, "document lost resource ledger")
		}
		return nil
	}
	if doc.ResourcePolicy == nil || !c.EqualResourcePolicy(state.Resources.Policy, *doc.ResourcePolicy) {
		return c.Fail(c.StoreIntegrityError, "resource policy changed")
	}
	if kind != "SessionRecorded" && kind != "CandidateRecorded" {
		return nil
	}
	old := unresolvedResource(state)
	if doc.Pending != nil && old == nil {
		pending := doc.Pending
		isModel := pending.Kind == "MODEL"
		upper := resourceUpper(doc, isModel)
		r := c.ResourceReservation{ID: pending.ID, Kind: pending.Kind, RequestDigest: pending.ArgumentsDigest, ProfileDigest: resourceProfile(doc), SpecVersion: doc.Spec.Version, PolicyEpoch: pending.Epoch, Generation: pending.Generation, StartedActiveMillis: doc.Budget.ActiveMillis, Upper: upper, Status: "RESERVED"}
		if !isModel {
			r.ID = nativeResourceID(doc)
			r.CallDigest = c.HashBytes([]byte(pending.ID))
		}
		if isModel {
			if extra.Tokens == nil || extra.Tokens.Action != "RESERVE" {
				return c.Fail(c.StoreIntegrityError, "model resources require token admission")
			}
			r.ProfileDigest = tokenProfile(doc)
			r.RequestDigest = doc.Context.RequestDigest
			r.Provider, _, _ = contextProfile(doc)
			r.Model = "offline-fixture-v1"
			if doc.Runtime != nil {
				r.Model = doc.Runtime.Model
			}
			r.TokenUpper = extra.Tokens.Reservation.Upper
			price, err := doc.ResourcePolicy.Price(r.Provider, r.Model)
			if err != nil {
				return err
			}
			if price != nil {
				r.PriceVersion = price.Version
				r.Upper.MoneyMicros, err = price.Cost(r.TokenUpper.Input, r.TokenUpper.Output)
				if err != nil {
					return err
				}
			}
		}
		extra.Resources = &c.ResourceMutation{Action: "RESERVE", Reservation: r}
		return nil
	}
	if old == nil {
		return nil
	}
	if doc.Pending != nil {
		if (old.Kind == "MODEL" && doc.Pending.ID != old.ID) || (old.Kind == "NATIVE_TOOL" && nativeResourceID(doc) != old.ID) {
			return c.Fail(c.StoreIntegrityError, "pending resource identity changed")
		}
		if doc.Pending.Status == "UNKNOWN" && old.Status == "RESERVED" {
			r := *old
			r.Status = "UNKNOWN"
			extra.Resources = &c.ResourceMutation{Action: "UNKNOWN", Reservation: r}
		}
		return nil
	}
	if doc.UnknownEffect {
		return c.Fail(c.StoreIntegrityError, "unknown resources cannot be cleared without receipt")
	}
	r := *old
	r.Status = "SETTLED"
	r.Meter = "CONSERVATIVE_KERNEL_RECEIPT_V1"
	r.Used = r.Upper
	r.Used.Children = 0
	r.Used.WallMillis = max(int64(0), doc.Budget.ActiveMillis-r.StartedActiveMillis)
	receipt := ResourceReceipt{SchemaVersion: 1, TaskID: doc.TaskID, OperationID: r.ID, RequestDigest: r.RequestDigest, ProfileDigest: r.ProfileDigest, StartedActiveMillis: r.StartedActiveMillis, EndedActiveMillis: doc.Budget.ActiveMillis, Candidate: doc.Candidate.SnapshotDigest, Measurement: r.Meter}
	if r.Kind == "MODEL" {
		if extra.Tokens == nil || extra.Tokens.Action != "SETTLE" {
			return c.Fail(c.StoreIntegrityError, "model resource settlement needs token receipt")
		}
		receipt.SourceReceipt = extra.Tokens.Reservation.ResponseDigest
		r.TokenUsed = extra.Tokens.Reservation.Used
		price, err := doc.ResourcePolicy.Price(r.Provider, r.Model)
		if err != nil {
			return err
		}
		r.Used.MoneyMicros = 0
		if price != nil {
			r.Used.MoneyMicros, err = price.Cost(r.TokenUsed.Input, r.TokenUsed.Output)
			if err != nil {
				return err
			}
		}
		if doc.LastNoDispatch {
			r.Used = c.ResourceVector{}
			r.Meter = "KERNEL_NO_DISPATCH"
			receipt.Measurement = r.Meter
		}
	} else {
		for _, reply := range doc.PendingReplies {
			if c.HashBytes([]byte(reply.CallID)) == r.CallDigest {
				owned := reply
				receipt.ToolReply = &owned
			}
		}
		if receipt.ToolReply == nil {
			return c.Fail(c.StoreIntegrityError, "native tool resource settlement needs retained reply")
		}
	}
	raw, err := c.CanonicalV1(receipt)
	if err != nil {
		return err
	}
	r.ReceiptDigest, err = s.Archive.PutControlBytes(doc.TaskID, raw, true)
	if err != nil {
		return err
	}
	extra.Resources = &c.ResourceMutation{Action: "SETTLE", Reservation: r}
	return nil
}
func (s *Session) validateResourceDocument(state c.TaskState, doc Document) error {
	if state.Resources == nil {
		if doc.ResourcePolicy != nil {
			return c.Fail(c.StoreIntegrityError, "resource policy without ledger")
		}
		return nil
	}
	if doc.ResourcePolicy == nil || !c.EqualResourcePolicy(state.Resources.Policy, *doc.ResourcePolicy) {
		return c.Fail(c.StoreIntegrityError, "resource policy differs from journal")
	}
	unresolved := 0
	for _, r := range state.Resources.Reservations {
		if r.Status != "SETTLED" {
			unresolved++
			if doc.Pending == nil || (r.Kind == "MODEL" && doc.Pending.ID != r.ID || r.Kind == "NATIVE_TOOL" && (nativeResourceID(doc) != r.ID || c.HashBytes([]byte(doc.Pending.ID)) != r.CallDigest)) || doc.Pending.Kind != r.Kind || doc.Pending.ArgumentsDigest != r.RequestDigest || doc.Pending.Epoch != r.PolicyEpoch || doc.Pending.Generation != r.Generation || doc.Spec.Version != r.SpecVersion || r.Status == "UNKNOWN" && (doc.Pending.Status != "UNKNOWN" || !doc.UnknownEffect) {
				return c.Fail(c.StoreIntegrityError, "pending resource lineage invalid")
			}
			if r.Kind == "MODEL" {
				if r.ProfileDigest != tokenProfile(doc) {
					return c.Fail(c.StoreIntegrityError, "pending model resource profile changed")
				}
			} else if r.ProfileDigest != resourceProfile(doc) {
				return c.Fail(c.StoreIntegrityError, "pending tool resource profile changed")
			}
			continue
		}
		raw, err := s.Archive.GetBytes(doc.TaskID, r.ReceiptDigest)
		if err != nil {
			return err
		}
		var proof ResourceReceipt
		if c.DecodeStrict(raw, &proof) != nil || proof.SchemaVersion != 1 || proof.TaskID != doc.TaskID || proof.OperationID != r.ID || proof.RequestDigest != r.RequestDigest || proof.ProfileDigest != r.ProfileDigest || proof.Measurement != r.Meter || proof.StartedActiveMillis != r.StartedActiveMillis || proof.EndedActiveMillis < proof.StartedActiveMillis || !c.ValidDigest(proof.Candidate) {
			return c.Fail(c.StoreIntegrityError, "resource receipt binding invalid")
		}
		if r.Meter == "OPERATOR_ASSUMED_UPPER_BOUND" {
			expected := r.Upper
			expected.Children = 0
			if r.Kind != "MODEL" || r.Used != expected {
				return c.Fail(c.StoreIntegrityError, "operator risk resource exposure was reduced")
			}
		} else if r.Meter == "KERNEL_NO_DISPATCH" {
			if r.Used != (c.ResourceVector{}) || r.Kind != "MODEL" {
				return c.Fail(c.StoreIntegrityError, "invalid no-dispatch resource proof")
			}
		} else {
			if r.Used.CPUMillis != r.Upper.CPUMillis || r.Used.DiskBytes != r.Upper.DiskBytes || r.Used.WallMillis != proof.EndedActiveMillis-proof.StartedActiveMillis {
				return c.Fail(c.StoreIntegrityError, "conservative resource meter differs from receipt")
			}
		}
		if r.Kind == "MODEL" {
			found := false
			if state.Tokens != nil {
				for _, charge := range state.Tokens.Reservations {
					if charge.ID == r.ID && charge.Status == "SETTLED" && charge.RequestDigest == r.RequestDigest && charge.ProfileDigest == r.ProfileDigest && charge.ResponseDigest == proof.SourceReceipt && charge.Used == r.TokenUsed && charge.Upper == r.TokenUpper {
						found = true
					}
				}
			}
			if r.Meter == "OPERATOR_ASSUMED_UPPER_BOUND" {
				rawRisk, err := s.Archive.GetBytes(doc.TaskID, proof.SourceReceipt)
				if err != nil {
					return err
				}
				var risk ModelRiskReceipt
				if c.DecodeStrict(rawRisk, &risk) != nil || risk.AssumedResources != r.Used {
					return c.Fail(c.StoreIntegrityError, "operator resource assumption differs from paired receipt")
				}
			}
			if !found || proof.ToolReply != nil {
				return c.Fail(c.StoreIntegrityError, "model resource receipt missing paired token charge")
			}
		} else if proof.ToolReply == nil || c.HashBytes([]byte(proof.ToolReply.CallID)) != r.CallDigest || proof.SourceReceipt != "" {
			return c.Fail(c.StoreIntegrityError, "native resource reply binding invalid")
		}
	}
	if unresolved == 0 && doc.Pending != nil {
		return c.Fail(c.StoreIntegrityError, "pending operation escaped resource ledger")
	}
	return nil
}

func nativeResourceID(doc Document) string {
	if doc.Pending == nil {
		return ""
	}
	digest, _ := c.Digest(struct {
		Task      string `json:"task"`
		Steps     int64  `json:"steps"`
		Tools     int64  `json:"tools"`
		Call      string `json:"call"`
		Arguments string `json:"arguments"`
	}{doc.TaskID, doc.Budget.Steps, doc.Budget.ToolCalls, doc.Pending.ID, doc.Pending.ArgumentsDigest})
	return "native-" + digest
}
