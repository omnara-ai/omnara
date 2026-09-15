package executionstore

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ExternalChannelRequestState string

const (
	ExternalChannelRequestPending   ExternalChannelRequestState = "pending"
	ExternalChannelRequestCompleted ExternalChannelRequestState = "completed"
	ExternalChannelRequestCanceled  ExternalChannelRequestState = "canceled"
	ExternalChannelRequestExpired   ExternalChannelRequestState = "expired"

	// ExternalChannelRequestTimeout bounds customer execution, including polling.
	// Expiration settles the owner; it never schedules another provider attempt.
	ExternalChannelRequestTimeout = 5 * time.Minute
)

type ExternalChannelRequestRecord struct {
	ID                         uuid.UUID
	ProjectID                  uuid.UUID
	AgentID                    uuid.UUID
	TurnID                     uuid.UUID
	ToolCallID                 uuid.UUID
	InteractionID              uuid.UUID
	NoticeKey                  string
	IntegrationInstallID       uuid.UUID
	IntegrationTargetID        uuid.UUID
	IntegrationTargetBindingID uuid.UUID
	Operation                  channelconnector.OperationKind
	CreatesReplyChannel        bool
	Payload                    json.RawMessage
	Deadline                   time.Time
	CreatedAt                  time.Time
	State                      ExternalChannelRequestState
	Result                     json.RawMessage
	StateReasonCode            string
	TerminalAt                 *time.Time
	Replayed                   bool
}

// CreateExternalChannelRequestInput contains accepted execution facts. Scope and
// the original tool input remain owned by the real tool call. The store chooses
// one live binding and preserves that binding's immutable grant identity.
type CreateExternalChannelRequestInput struct {
	TurnID              uuid.UUID
	ChannelID           uuid.UUID
	Operation           channelconnector.OperationKind
	CreatesReplyChannel bool
	Payload             json.RawMessage
	Timeout             time.Duration
}

type CreateExternalChannelPresentationInput struct {
	ProjectID     uuid.UUID
	AgentID       uuid.UUID
	InteractionID uuid.UUID
	Payload       json.RawMessage
	Timeout       time.Duration
}

type CreateExternalChannelNoticeInput struct {
	ProjectID     uuid.UUID
	AgentID       uuid.UUID
	TurnID        uuid.UUID
	RuntimeLockID uuid.UUID
	ChannelID     uuid.UUID
	NoticeKey     string
	Payload       json.RawMessage
	Timeout       time.Duration
}

type ExternalChannelRequestCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

type ListExternalChannelRequestsInput struct {
	ProjectID            uuid.UUID
	IntegrationInstallID uuid.UUID
	Limit                int32
	After                *ExternalChannelRequestCursor
}

type ListExternalChannelRequestsResult struct {
	Requests []ExternalChannelRequestRecord
	HasMore  bool
}

type CompleteExternalChannelRequestInput struct {
	ProjectID            uuid.UUID
	IntegrationInstallID uuid.UUID
	ID                   uuid.UUID
	Result               channelconnector.OperationResult
}

type CompleteExternalChannelRequestResult struct {
	Request  ExternalChannelRequestRecord
	ToolCall *ToolCallRecord
	Replayed bool
}

func normalizeExternalChannelPayload(raw json.RawMessage) (json.RawMessage, error) {
	object, err := jsoncanonical.ParseObject(raw, channelconnector.MaxOperationEnvelopeBytes)
	if err != nil {
		return nil, storeerr.InvalidRequest(err)
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, storeerr.InvalidRequest(err)
	}
	if err := dbsafe.JSONB(normalized, channelconnector.MaxOperationEnvelopeBytes); err != nil {
		return nil, storeerr.InvalidRequest(err)
	}
	return normalized, nil
}

func validateExternalChannelTimeout(timeout time.Duration) error {
	if timeout.Microseconds() < 1 || timeout > ExternalChannelRequestTimeout {
		return storeerr.InvalidRequest(errors.New("channel request timeout must be positive and at most five minutes"))
	}
	return nil
}
