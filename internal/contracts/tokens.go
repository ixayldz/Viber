package contracts

// TokenLimits are immutable store-wide work limits. They are not a monetary
// price estimate, a disk quota, or the physical control reserve.
type TokenLimits struct {
	Input  int64 `json:"input_tokens"`
	Output int64 `json:"output_tokens"`
}

func DefaultTokenLimits() TokenLimits { return TokenLimits{Input: 64 << 20, Output: 4 << 20} }
func (l TokenLimits) Validate() error {
	if l.Input < 4096 || l.Output < 512 || l.Input > 1<<40 || l.Output > 1<<40 {
		return Fail(InvalidArgument, "invalid store token limits")
	}
	return nil
}

type TokenReservation struct {
	ID             string      `json:"reservation_id"`
	RequestDigest  string      `json:"request_digest"`
	ProfileDigest  string      `json:"profile_digest"`
	ResponseDigest string      `json:"response_digest,omitempty"`
	SpecVersion    int64       `json:"spec_version"`
	PolicyEpoch    int64       `json:"policy_epoch"`
	Generation     int64       `json:"generation"`
	Upper          TokenLimits `json:"upper_bound"`
	Used           TokenLimits `json:"charged"`
	Status         string      `json:"status"`
	UsageSource    string      `json:"usage_source,omitempty"`
}
type TokenAccount struct {
	SchemaVersion int                `json:"schema_version"`
	Limits        TokenLimits        `json:"store_limits"`
	Used          TokenLimits        `json:"charged"`
	Reserved      TokenLimits        `json:"reserved"`
	Reservations  []TokenReservation `json:"reservations"`
}
type TokenMutation struct {
	Action      string           `json:"action"`
	Limits      TokenLimits      `json:"store_limits,omitempty"`
	Reservation TokenReservation `json:"reservation"`
}

func ApplyTokens(account *TokenAccount, change TokenMutation, task TaskState) (*TokenAccount, error) {
	if change.Action == "INIT" {
		if account != nil || change.Limits.Validate() != nil || change.Reservation != (TokenReservation{}) {
			return nil, Fail(InvalidArgument, "token account init requires fresh immutable limits")
		}
		return &TokenAccount{SchemaVersion: 1, Limits: change.Limits, Reservations: []TokenReservation{}}, nil
	}
	if account == nil || account.SchemaVersion != 1 || account.Limits.Validate() != nil || change.Limits != (TokenLimits{}) {
		return nil, Fail(StoreIntegrityError, "token account is unavailable or changed")
	}
	next := *account
	next.Reservations = append([]TokenReservation{}, account.Reservations...)
	r := change.Reservation
	switch change.Action {
	case "RESERVE":
		if task.Execution != Running || task.InputBarrier || r.ID == "" || len(r.ID) > 128 || !ValidDigest(r.RequestDigest) || !ValidDigest(r.ProfileDigest) || r.SpecVersion != task.SpecVersion || r.PolicyEpoch != task.PolicyEpoch || r.Generation != task.KernelGeneration || r.Upper.Input <= 0 || r.Upper.Output <= 0 || r.Upper.Input > 1<<40 || r.Upper.Output > 1<<40 || r.Status != "RESERVED" || r.ResponseDigest != "" || r.Used != (TokenLimits{}) || r.UsageSource != "" || len(next.Reservations) >= 256 {
			return nil, Fail(PolicyDenied, "invalid token reservation authority or upper bound")
		}
		for _, old := range next.Reservations {
			if old.ID == r.ID || old.Status == "RESERVED" || old.Status == "UNKNOWN" {
				return nil, Fail(PolicyDenied, "duplicate or unresolved token reservation")
			}
		}
		next.Reservations = append(next.Reservations, r)
		next.Reserved.Input += r.Upper.Input
		next.Reserved.Output += r.Upper.Output
	case "UNKNOWN", "SETTLE":
		i := -1
		for n, old := range next.Reservations {
			if old.ID == r.ID {
				i = n
				break
			}
		}
		if i < 0 {
			return nil, Fail(InvalidArgument, "reservation not found")
		}
		old := next.Reservations[i]
		if old.Status != "RESERVED" && old.Status != "UNKNOWN" {
			return nil, Fail(PolicyDenied, "reservation already settled")
		}
		expected := old
		expected.Status, expected.ResponseDigest, expected.Used, expected.UsageSource = r.Status, r.ResponseDigest, r.Used, r.UsageSource
		if r != expected {
			return nil, Fail(StaleAuthority, "reservation lineage changed")
		}
		if change.Action == "UNKNOWN" {
			if r.Status != "UNKNOWN" || r.Used != (TokenLimits{}) || r.ResponseDigest != "" || r.UsageSource != "" {
				return nil, Fail(InvalidArgument, "unknown usage cannot erase risk or manufacture charges")
			}
		} else {
			// Actual observed usage is charged even when it exceeds a reservation.
			// Subsequent admissions stop; rejecting the receipt would lose accounting.
			if r.UsageSource == "KERNEL_NO_DISPATCH" && r.Used != (TokenLimits{}) {
				return nil, Fail(StoreIntegrityError, "preflight zero charge cannot contain usage")
			}
			if r.Status != "SETTLED" || !ValidDigest(r.ResponseDigest) || (r.UsageSource != "PROVIDER_REPORTED" && r.UsageSource != "FIXTURE_REPORTED" && r.UsageSource != "KERNEL_NO_DISPATCH") || r.Used.Input < 0 || r.Used.Output < 0 || r.Used.Input > 1<<40 || r.Used.Output > 1<<40 {
				return nil, Fail(InvalidArgument, "settlement requires bounded observed usage and raw receipt")
			}
			next.Reserved.Input -= old.Upper.Input
			next.Reserved.Output -= old.Upper.Output
			next.Used.Input += r.Used.Input
			next.Used.Output += r.Used.Output
		}
		next.Reservations[i] = r
	default:
		return nil, Fail(InvalidArgument, "unsupported token mutation; unknown risk cannot be released")
	}
	if next.Used.Input < 0 || next.Used.Output < 0 || next.Used.Input > 1<<52 || next.Used.Output > 1<<52 || next.Reserved.Input < 0 || next.Reserved.Output < 0 {
		return nil, Fail(StoreIntegrityError, "token accounting overflow")
	}
	return &next, nil
}

// ValidateTokenAdmission is used under the metadata transaction and during
// full journal replay. Legacy tasks cannot silently escape an enrolled ledger.
func ValidateTokenAdmission(states map[string]TaskState, replacement TaskState, mutation *TokenMutation) error {
	if mutation == nil {
		if replacement.Tokens == nil {
			for _, state := range states {
				if state.Tokens != nil {
					return Fail(UnsupportedCapability, "untracked task cannot join a global token ledger")
				}
			}
		}
		return nil
	}
	limits := replacement.Tokens.Limits
	used, reserved := TokenLimits{}, TokenLimits{}
	for id, state := range states {
		if id == replacement.TaskID {
			continue
		}
		if state.Tokens == nil {
			return Fail(UnsupportedCapability, "global token ledger requires a fresh store; legacy task accounting must not be omitted")
		}
		if mutation.Action == "RESERVE" {
			for _, r := range state.Tokens.Reservations {
				if r.ID == mutation.Reservation.ID {
					return Fail(PolicyDenied, "operation identity already reserved by another task")
				}
			}
		}
		if state.Tokens.Limits != limits {
			return Fail(PolicyDenied, "store token limits are immutable")
		}
		used.Input += state.Tokens.Used.Input
		used.Output += state.Tokens.Used.Output
		reserved.Input += state.Tokens.Reserved.Input
		reserved.Output += state.Tokens.Reserved.Output
		if used.Input > 1<<52 || used.Output > 1<<52 || reserved.Input > 1<<52 || reserved.Output > 1<<52 {
			return Fail(StoreIntegrityError, "global token accounting overflow")
		}
	}
	used.Input += replacement.Tokens.Used.Input
	used.Output += replacement.Tokens.Used.Output
	reserved.Input += replacement.Tokens.Reserved.Input
	reserved.Output += replacement.Tokens.Reserved.Output
	if (mutation.Action == "RESERVE" || mutation.Action == "INIT") && (used.Input > limits.Input-reserved.Input || used.Output > limits.Output-reserved.Output) {
		return Fail(BudgetLimitReached, "store token work budget exhausted; reservations and control access retained")
	}
	return nil
}
