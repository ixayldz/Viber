package contracts

// ResourceVector uses integral units. Money is millionths of the policy currency;
// CPU is a conservative allocation charge, disk is retained write work, wall
// time is admitted operation time, children are concurrent owned processes.
type ResourceVector struct {
	MoneyMicros int64 `json:"money_micros"`
	CPUMillis   int64 `json:"cpu_millis"`
	DiskBytes   int64 `json:"disk_bytes"`
	WallMillis  int64 `json:"wall_millis"`
	Children    int64 `json:"children"`
}

func (v ResourceVector) values() []int64 {
	return []int64{v.MoneyMicros, v.CPUMillis, v.DiskBytes, v.WallMillis, v.Children}
}
func (v ResourceVector) Valid(bound int64) bool {
	for _, n := range v.values() {
		if n < 0 || n > bound {
			return false
		}
	}
	return true
}
func (v ResourceVector) Add(w ResourceVector) ResourceVector {
	return ResourceVector{v.MoneyMicros + w.MoneyMicros, v.CPUMillis + w.CPUMillis, v.DiskBytes + w.DiskBytes, v.WallMillis + w.WallMillis, v.Children + w.Children}
}
func (v ResourceVector) Sub(w ResourceVector) ResourceVector {
	return ResourceVector{v.MoneyMicros - w.MoneyMicros, v.CPUMillis - w.CPUMillis, v.DiskBytes - w.DiskBytes, v.WallMillis - w.WallMillis, v.Children - w.Children}
}
func (v ResourceVector) Fits(limits ResourceVector) bool {
	a, b := v.values(), limits.values()
	for i := range a {
		if a[i] > b[i] {
			return false
		}
	}
	return v.Valid(1 << 52)
}

type ModelPrice struct {
	Version                string `json:"version"`
	Provider               string `json:"provider"`
	Model                  string `json:"model"`
	InputMicrosPerMillion  int64  `json:"input_micros_per_million_tokens"`
	OutputMicrosPerMillion int64  `json:"output_micros_per_million_tokens"`
}

func (p ModelPrice) Validate() error {
	if p.Version == "" || len(p.Version) > 128 || len(p.Model) > 256 || p.Model == "" || (p.Provider != "openai" && p.Provider != "anthropic") || p.InputMicrosPerMillion < 0 || p.InputMicrosPerMillion > 1<<30 || p.OutputMicrosPerMillion < 0 || p.OutputMicrosPerMillion > 1<<30 {
		return Fail(InvalidArgument, "invalid operator price catalog entry")
	}
	return nil
}
func priceQuantity(tokens, rate int64) (int64, error) {
	if tokens < 0 || tokens > 1<<40 || rate < 0 || rate > 1<<30 {
		return 0, Fail(InvalidArgument, "price quantity outside bounds")
	}
	// Split the product before multiplying. Round each charge up to a micro-unit.
	return tokens/1000000*rate + (tokens%1000000*rate+999999)/1000000, nil
}
func (p ModelPrice) Cost(input, output int64) (int64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	a, err := priceQuantity(input, p.InputMicrosPerMillion)
	if err != nil {
		return 0, err
	}
	b, err := priceQuantity(output, p.OutputMicrosPerMillion)
	return a + b, err
}

type ResourcePolicy struct {
	SchemaVersion int            `json:"schema_version"`
	Currency      string         `json:"currency"`
	Limits        ResourceVector `json:"limits"`
	Prices        []ModelPrice   `json:"operator_prices"`
}

func DefaultResourcePolicy() ResourcePolicy {
	return ResourcePolicy{1, "USD", ResourceVector{0, 1 << 36, 8 << 30, 1 << 32, 512}, []ModelPrice{}}
}
func (p ResourcePolicy) Validate() error {
	if p.SchemaVersion != 1 || len(p.Currency) != 3 || !p.Limits.Valid(1<<48) || p.Limits.CPUMillis < 1000 || p.Limits.DiskBytes < 1<<20 || p.Limits.WallMillis < 1000 || p.Limits.Children < 1 || p.Limits.Children > 4096 || len(p.Prices) > 64 {
		return Fail(InvalidArgument, "invalid immutable resource policy")
	}
	for _, r := range p.Currency {
		if r < 'A' || r > 'Z' {
			return Fail(InvalidArgument, "currency must be an explicit uppercase three-letter unit")
		}
	}
	seen := map[string]bool{}
	for _, price := range p.Prices {
		key := price.Provider + "\x00" + price.Model
		if err := price.Validate(); err != nil {
			return err
		}
		if seen[key] {
			return Fail(InvalidArgument, "duplicate model price")
		}
		seen[key] = true
	}
	return nil
}
func (p ResourcePolicy) Price(provider, model string) (*ModelPrice, error) {
	if provider != "openai" && provider != "anthropic" {
		return nil, nil
	}
	for _, price := range p.Prices {
		if price.Provider == provider && price.Model == model {
			return &price, nil
		}
	}
	return nil, Fail(PolicyDenied, "paid inference requires an operator versioned price catalog; no invented zero price")
}

type ResourceReservation struct {
	CallDigest          string         `json:"source_call_digest,omitempty"`
	Provider            string         `json:"provider,omitempty"`
	Model               string         `json:"model,omitempty"`
	StartedActiveMillis int64          `json:"started_active_millis"`
	TokenUpper          TokenLimits    `json:"token_upper_bound"`
	TokenUsed           TokenLimits    `json:"token_charge"`
	ID                  string         `json:"reservation_id"`
	Kind                string         `json:"operation_kind"`
	RequestDigest       string         `json:"request_digest"`
	ProfileDigest       string         `json:"profile_digest"`
	ReceiptDigest       string         `json:"receipt_digest,omitempty"`
	SpecVersion         int64          `json:"spec_version"`
	PolicyEpoch         int64          `json:"policy_epoch"`
	Generation          int64          `json:"generation"`
	Upper               ResourceVector `json:"upper_bound"`
	Used                ResourceVector `json:"charged"`
	Status              string         `json:"status"`
	Meter               string         `json:"meter,omitempty"`
	PriceVersion        string         `json:"price_version,omitempty"`
}
type ResourceAccount struct {
	SchemaVersion int                   `json:"schema_version"`
	Policy        ResourcePolicy        `json:"immutable_store_policy"`
	Used          ResourceVector        `json:"charged"`
	Reserved      ResourceVector        `json:"reserved"`
	Reservations  []ResourceReservation `json:"reservations"`
}
type ResourceMutation struct {
	Action      string              `json:"action"`
	Policy      *ResourcePolicy     `json:"policy,omitempty"`
	Reservation ResourceReservation `json:"reservation"`
}

func ApplyResources(account *ResourceAccount, change ResourceMutation, task TaskState) (*ResourceAccount, error) {
	if change.Action == "INIT" {
		if account != nil || change.Policy == nil || change.Policy.Validate() != nil || change.Reservation != (ResourceReservation{}) {
			return nil, Fail(InvalidArgument, "resource init requires a fresh immutable policy")
		}
		policy := *change.Policy
		policy.Prices = append([]ModelPrice{}, policy.Prices...)
		return &ResourceAccount{SchemaVersion: 1, Policy: policy, Reservations: []ResourceReservation{}}, nil
	}
	if account == nil || account.SchemaVersion != 1 || account.Policy.Validate() != nil || change.Policy != nil {
		return nil, Fail(StoreIntegrityError, "resource account unavailable or policy changed")
	}
	next := *account
	next.Reservations = append([]ResourceReservation{}, account.Reservations...)
	r := change.Reservation
	switch change.Action {
	case "RESERVE":
		if task.Execution != Running || task.InputBarrier || r.ID == "" || len(r.ID) > 128 || (r.Kind != "MODEL" && r.Kind != "NATIVE_TOOL") || !ValidDigest(r.RequestDigest) || !ValidDigest(r.ProfileDigest) || r.SpecVersion != task.SpecVersion || r.PolicyEpoch != task.PolicyEpoch || r.Generation != task.KernelGeneration || !r.Upper.Valid(1<<40) || r.Upper.WallMillis < 1 || r.Upper.CPUMillis < 1 || r.StartedActiveMillis < 0 || r.StartedActiveMillis > 1<<40 || r.TokenUsed != (TokenLimits{}) || r.Status != "RESERVED" || r.ReceiptDigest != "" || r.Used != (ResourceVector{}) || r.Meter != "" || len(next.Reservations) >= 2048 {
			return nil, Fail(PolicyDenied, "invalid resource reservation authority")
		}
		for _, old := range next.Reservations {
			if old.ID == r.ID || old.Status != "SETTLED" {
				return nil, Fail(PolicyDenied, "duplicate or unresolved resource reservation")
			}
		}
		if err := validateResourcePrice(account.Policy, r, false); err != nil {
			return nil, err
		}
		next.Reservations = append(next.Reservations, r)
		next.Reserved = next.Reserved.Add(r.Upper)
	case "SETTLE", "UNKNOWN":
		i := -1
		for n, old := range next.Reservations {
			if old.ID == r.ID {
				i = n
				break
			}
		}
		if i < 0 {
			return nil, Fail(InvalidArgument, "resource reservation missing")
		}
		old := next.Reservations[i]
		if old.Status != "RESERVED" && old.Status != "UNKNOWN" {
			return nil, Fail(PolicyDenied, "resource reservation already settled")
		}
		expected := old
		expected.Status, expected.ReceiptDigest, expected.Used, expected.Meter, expected.TokenUsed = r.Status, r.ReceiptDigest, r.Used, r.Meter, r.TokenUsed
		if expected != r {
			return nil, Fail(StaleAuthority, "resource settlement lineage changed")
		}
		if change.Action == "UNKNOWN" {
			if r.Status != "UNKNOWN" || r.TokenUsed != (TokenLimits{}) || r.Used != (ResourceVector{}) || r.ReceiptDigest != "" || r.Meter != "" {
				return nil, Fail(InvalidArgument, "unknown resource risk cannot be released")
			}
		} else {
			if r.Status != "SETTLED" || !ValidDigest(r.ReceiptDigest) || !r.Used.Valid(1<<52) || r.Used.Children != 0 || (r.Meter != "CONSERVATIVE_KERNEL_RECEIPT_V1" && r.Meter != "KERNEL_NO_DISPATCH" && r.Meter != "OPERATOR_ASSUMED_UPPER_BOUND") {
				return nil, Fail(InvalidArgument, "resource settlement requires receipt and quiescent children")
			}
			if r.Meter == "KERNEL_NO_DISPATCH" && r.Used != (ResourceVector{}) {
				return nil, Fail(StoreIntegrityError, "no dispatch cannot have a charge")
			}
			if err := validateResourcePrice(account.Policy, r, true); err != nil {
				return nil, err
			}
			if r.Meter == "OPERATOR_ASSUMED_UPPER_BOUND" {
				expected := old.Upper
				expected.Children = 0
				if old.Status != "UNKNOWN" || r.Kind != "MODEL" || r.Used != expected {
					return nil, Fail(PolicyDenied, "operator model risk must retain full resource exposure")
				}
			}
			next.Reserved = next.Reserved.Sub(old.Upper)
			next.Used = next.Used.Add(r.Used)
		}
		next.Reservations[i] = r
	default:
		return nil, Fail(InvalidArgument, "unsupported resource mutation; no blind release of unknown risk")
	}
	if !next.Used.Valid(1<<52) || !next.Reserved.Valid(1<<52) {
		return nil, Fail(StoreIntegrityError, "resource accounting overflow")
	}
	return &next, nil
}
func ValidateResourceAdmission(states map[string]TaskState, replacement TaskState, change *ResourceMutation) error {
	if change == nil {
		if replacement.Resources == nil {
			for _, state := range states {
				if state.Resources != nil {
					return Fail(UnsupportedCapability, "untracked task cannot join enrolled resource ledger")
				}
			}
		}
		return nil
	}
	if replacement.Resources == nil {
		return Fail(StoreIntegrityError, "resource mutation missing account")
	}
	policy := replacement.Resources.Policy
	total := replacement.Resources.Used.Add(replacement.Resources.Reserved)
	for id, state := range states {
		if id == replacement.TaskID {
			continue
		}
		if state.Resources == nil {
			return Fail(UnsupportedCapability, "resource ledger requires fresh accounted store; legacy usage cannot be omitted")
		}
		if !EqualResourcePolicy(policy, state.Resources.Policy) {
			return Fail(PolicyDenied, "store resource policy is immutable")
		}
		if change.Action == "RESERVE" {
			for _, r := range state.Resources.Reservations {
				if r.ID == change.Reservation.ID {
					return Fail(PolicyDenied, "operation ID belongs to another task")
				}
			}
		}
		total = total.Add(state.Resources.Used).Add(state.Resources.Reserved)
		if !total.Valid(1 << 52) {
			return Fail(StoreIntegrityError, "global resource accounting overflow")
		}
	}
	if (change.Action == "INIT" || change.Action == "RESERVE") && !total.Fits(policy.Limits) {
		return Fail(BudgetLimitReached, "global resource work limit reached; unresolved reservations retained")
	}
	return nil
}

func validateResourcePrice(policy ResourcePolicy, r ResourceReservation, settled bool) error {
	if r.Kind == "NATIVE_TOOL" {
		if !ValidDigest(r.CallDigest) || r.Provider != "" || r.Model != "" || r.PriceVersion != "" || r.TokenUpper != (TokenLimits{}) || r.TokenUsed != (TokenLimits{}) || r.Upper.MoneyMicros != 0 || r.Used.MoneyMicros != 0 {
			return Fail(StoreIntegrityError, "native tool cannot manufacture a model price")
		}
		return nil
	}
	if r.CallDigest != "" || r.Model == "" || (r.Provider != "fixture" && r.Provider != "ollama" && r.Provider != "chatgpt" && r.Provider != "openai" && r.Provider != "anthropic") || r.TokenUpper.Input < 1 || r.TokenUpper.Output < 1 {
		return Fail(StoreIntegrityError, "model resource profile invalid")
	}
	price, err := policy.Price(r.Provider, r.Model)
	if err != nil {
		return err
	}
	upper, used := int64(0), int64(0)
	if price != nil {
		if r.PriceVersion != price.Version {
			return Fail(StoreIntegrityError, "price version changed")
		}
		upper, err = price.Cost(r.TokenUpper.Input, r.TokenUpper.Output)
		if err != nil {
			return err
		}
		if settled {
			used, err = price.Cost(r.TokenUsed.Input, r.TokenUsed.Output)
			if err != nil {
				return err
			}
		}
	} else if r.PriceVersion != "" {
		return Fail(StoreIntegrityError, "unpriced plan/local runtime cannot claim API price")
	}
	if r.Upper.MoneyMicros != upper || settled && r.Used.MoneyMicros != used {
		return Fail(StoreIntegrityError, "money differs from versioned token price")
	}
	return nil
}
func ValidateResourceTokenPair(resources *ResourceAccount, change *ResourceMutation, tokens *TokenMutation) error {
	if resources == nil {
		return nil
	}
	if tokens != nil && tokens.Action == "INIT" {
		if change == nil || change.Action != "INIT" {
			return Fail(StoreIntegrityError, "new account requires atomic resource init")
		}
		return nil
	}
	if tokens != nil {
		if change == nil || change.Action != tokens.Action || change.Reservation.Kind != "MODEL" || change.Reservation.ID != tokens.Reservation.ID || change.Reservation.RequestDigest != tokens.Reservation.RequestDigest || change.Reservation.ProfileDigest != tokens.Reservation.ProfileDigest || change.Reservation.TokenUpper != tokens.Reservation.Upper || change.Reservation.TokenUsed != tokens.Reservation.Used || change.Reservation.Status != tokens.Reservation.Status {
			return Fail(StoreIntegrityError, "model token and resource mutations must be atomic and identical")
		}
	} else if change != nil && change.Reservation.Kind == "MODEL" {
		return Fail(StoreIntegrityError, "model resources cannot escape token ledger")
	}
	return nil
}

func EqualResourcePolicy(a, b ResourcePolicy) bool {
	if a.SchemaVersion != b.SchemaVersion || a.Currency != b.Currency || a.Limits != b.Limits || len(a.Prices) != len(b.Prices) {
		return false
	}
	for i := range a.Prices {
		if a.Prices[i] != b.Prices[i] {
			return false
		}
	}
	return true
}
