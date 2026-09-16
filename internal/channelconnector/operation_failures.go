package channelconnector

import (
	"encoding/json"
	"errors"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
)

type OperationFailureCode string

const (
	FailureInvalidAddress     OperationFailureCode = "invalid_address"
	FailureAddressUnavailable OperationFailureCode = "address_unavailable"
	FailureUnsupportedAddress OperationFailureCode = "unsupported_address"
)

// OperationFailure admits only fixed diagnostics.
// Arbitrary provider error messages, response bodies and credentials are dropped.
type OperationFailure struct {
	Code OperationFailureCode `json:"code"`
}

// MatchesOutcome keeps fixed diagnostics consistent with the transport's facts.
// A malformed diagnostic is discarded without changing the observed outcome.
func (f OperationFailure) MatchesOutcome(outcome OperationOutcome) bool {
	return outcome == OperationFailed
}

func (f OperationFailure) Detail() string {
	switch f.Code {
	case FailureInvalidAddress:
		return "The channel address is invalid."
	case FailureAddressUnavailable:
		return "The channel address is not available to this installation."
	case FailureUnsupportedAddress:
		return "This channel address type is not supported."
	default:
		return "The channel operation could not be completed."
	}
}

func DecodeOperationFailure(raw json.RawMessage) (OperationFailure, error) {
	var failure OperationFailure
	invalid := errors.New("invalid channel operation failure")
	object, err := jsoncanonical.ParseObject(raw, 2048)
	if err != nil {
		return failure, invalid
	}
	for key := range object {
		if key != "code" {
			return failure, invalid
		}
	}
	if json.Unmarshal(raw, &failure) != nil {
		return OperationFailure{}, invalid
	}
	switch failure.Code {
	case FailureInvalidAddress, FailureAddressUnavailable, FailureUnsupportedAddress:
	default:
		return OperationFailure{}, invalid
	}
	return failure, nil
}
