package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
)

const (
	MaxIntegrationControlPayloadBytes = 64 * 1024
	MaxIntegrationControlErrorBytes   = 8 * 1024
	MaxIntegrationControlRetryAfter   = 24 * time.Hour
)

type IntegrationControlProgress struct {
	LastInstallID uuid.UUID
	EndInstallID  uuid.UUID
}

// IntegrationControlReceipt is app-owned reconciliation work. It is never an
// agent-admission receipt or proof of authority over a child installation.
type IntegrationControlReceipt struct {
	ID, OrgID, IntegrationAppID uuid.UUID
	ConnectorKey, Provider      string
	ProviderTenantID, EventID   string
	Payload                     json.RawMessage
	Progress                    IntegrationControlProgress
	State                       IntegrationEventState
	// Saturates at 30 for backoff; it is not a total attempt count or claim limit.
	AttemptsSinceProgress int64
	AvailableAt           time.Time
	LeaseToken            uuid.UUID
	LeaseGeneration       int64
	LeaseExpiresAt        *time.Time
	LastError             json.RawMessage
	CompletedAt           *time.Time
	CreatedAt             time.Time
}

type ReceiveIntegrationControlInput struct {
	IntegrationAppID uuid.UUID
	ProviderTenantID string
	EventID          string
	Payload          json.RawMessage
	Capabilities     []channelconnector.Capability
}

type ClaimNextIntegrationControlInput struct {
	Capability    channelconnector.Capability
	LeaseDuration time.Duration
}

type IntegrationControlOutcome string

const (
	IntegrationControlCompleted IntegrationControlOutcome = "completed"
	IntegrationControlYield     IntegrationControlOutcome = "yield"
	IntegrationControlRetry     IntegrationControlOutcome = "retry"
	IntegrationControlFailed    IntegrationControlOutcome = "failed"
)

type FinishIntegrationControlInput struct {
	IntegrationAppID, ID uuid.UUID
	LeaseToken           uuid.UUID
	LeaseGeneration      int64
	Outcome              IntegrationControlOutcome
	LastInstallID        *uuid.UUID    // nil preserves confirmed progress
	RetryAfter           time.Duration // retry only; capped at 24 hours
	LastError            json.RawMessage
	Capabilities         []channelconnector.Capability
}

type DeleteRetainedIntegrationControlsInput struct {
	Retention time.Duration
	Limit     int
}
