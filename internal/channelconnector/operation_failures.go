package channelconnector

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
)

type OperationFailureCode string

const (
	FailureInvalidAddress                OperationFailureCode = "invalid_address"
	FailureAddressUnavailable            OperationFailureCode = "address_unavailable"
	FailureUnsupportedAddress            OperationFailureCode = "unsupported_address"
	FailureReviewNotOwned                OperationFailureCode = "review_not_owned"
	FailureReviewCreationInProgress      OperationFailureCode = "review_creation_in_progress"
	FailureProviderPendingReviewConflict OperationFailureCode = "provider_pending_review_conflict"
	FailureReviewStateUnavailable        OperationFailureCode = "review_state_unavailable"
	FailurePendingReviewExists           OperationFailureCode = "pending_review_exists"
	FailureReviewCommitMismatch          OperationFailureCode = "review_commit_mismatch"
	FailureReviewCreationAlreadyRecorded OperationFailureCode = "review_creation_already_recorded"
	FailureReviewFindingFailed           OperationFailureCode = "review_finding_failed"
	FailureReviewOperationUnknown        OperationFailureCode = "review_operation_unknown"
)

// OperationFailure admits only fixed diagnostics and bounded recovery facts.
// Arbitrary provider error messages, response bodies and credentials are dropped.
type OperationFailure struct {
	Code     OperationFailureCode `json:"code"`
	Metadata map[string]string    `json:"metadata,omitempty"`
}

// MatchesOutcome keeps fixed diagnostics consistent with the transport's facts.
// A malformed diagnostic is discarded without changing the observed outcome.
func (f OperationFailure) MatchesOutcome(outcome OperationOutcome) bool {
	if f.Code == FailureReviewOperationUnknown {
		return outcome == OperationUnknown
	}
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
	case FailureReviewNotOwned:
		return "This agent does not own that draft review."
	case FailureReviewCreationInProgress:
		return "Another review creation by this agent is still running. Wait for its result."
	case FailureProviderPendingReviewConflict:
		return "The bot account has another pending review that this agent cannot change."
	case FailureReviewStateUnavailable:
		return "GitHub's current review state could not be established. No further review action was dispatched."
	case FailurePendingReviewExists:
		return "This agent has a pending review. Continue or publish it using the returned review_id."
	case FailureReviewCommitMismatch:
		return "The finding must use the commit_id pinned to this review. Publishing the existing review remains possible."
	case FailureReviewCreationAlreadyRecorded:
		return "This call already created a review. Use its review_id for an explicit follow-up."
	case FailureReviewFindingFailed:
		return "A review was created, but this finding was not confirmed. Inspect that review before retrying."
	case FailureReviewOperationUnknown:
		return "The review action's outcome is unknown. Inspect the returned review before retrying."
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
		if key != "code" && key != "metadata" {
			return failure, invalid
		}
	}
	if json.Unmarshal(raw, &failure) != nil {
		return OperationFailure{}, invalid
	}
	allowReference := false
	switch failure.Code {
	case FailureInvalidAddress, FailureAddressUnavailable, FailureUnsupportedAddress,
		FailureReviewNotOwned, FailureReviewCreationInProgress,
		FailureProviderPendingReviewConflict, FailureReviewStateUnavailable:
	case FailurePendingReviewExists, FailureReviewCommitMismatch,
		FailureReviewCreationAlreadyRecorded, FailureReviewFindingFailed, FailureReviewOperationUnknown:
		allowReference = true
	default:
		return OperationFailure{}, invalid
	}
	if metadata, supplied := object["metadata"]; supplied {
		if _, ok := metadata.(map[string]any); !ok {
			return OperationFailure{}, invalid
		}
	}
	for key, value := range failure.Metadata {
		if !allowReference || value == "" || dbsafe.Text(value) != nil {
			return OperationFailure{}, invalid
		}
		switch key {
		case "review_id":
			if strings.TrimSpace(value) != value || len(value) > 512 {
				return OperationFailure{}, invalid
			}
		case "commit_id":
			commit, err := hex.DecodeString(value)
			if err != nil || len(commit) != 20 {
				return OperationFailure{}, invalid
			}
		default:
			return OperationFailure{}, invalid
		}
	}
	return failure, nil
}
