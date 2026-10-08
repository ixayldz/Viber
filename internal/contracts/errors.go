// Package contracts defines versioned data shared across kernel boundaries.
package contracts

import "fmt"

type Code string

const (
	InvalidArgument       Code = "INVALID_ARGUMENT"
	UnsupportedCapability Code = "UNSUPPORTED_CAPABILITY"
	PolicyDenied          Code = "POLICY_DENIED"
	StaleBase             Code = "STALE_BASE"
	StaleRequest          Code = "STALE_REQUEST"
	Conflict              Code = "CONFLICT"
	StaleAuthority        Code = "STALE_AUTHORITY"
	BudgetLimitReached    Code = "BUDGET_LIMIT_REACHED"
	ContextTooSmall       Code = "CONTEXT_TOO_SMALL"
	StoreIntegrityError   Code = "STORE_INTEGRITY_ERROR"
	StoreOwned            Code = "STORE_OWNED"
	CommandIDConflict     Code = "COMMAND_ID_CONFLICT"
	UnknownOutcome        Code = "UNKNOWN_OPERATION_OUTCOME"
)

type Error struct {
	Code      Code   `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string             { return fmt.Sprintf("%s: %s", e.Code, e.Message) }
func Fail(code Code, message string) error { return &Error{Code: code, Message: message} }

const SchemaVersion = 1
const ReducerVersion = 1
