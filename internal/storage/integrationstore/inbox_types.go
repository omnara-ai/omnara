package integrationstore

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	IntegrationInboxMaxPayloadBytes    = 1024 * 1024
	IntegrationInboxMaxPlanBytes       = 256 * 1024
	IntegrationInboxMaxEventsBytes     = 256 * 1024
	IntegrationInboxMaxErrorBytes      = 4096
	IntegrationInboxMaxReceiptKeyBytes = 512
	IntegrationInboxMaxAttempts        = 8
	IntegrationInboxMaxBatch           = 100
	IntegrationInboxMaxLease           = 5 * time.Minute
	IntegrationInboxMaxRetryDelay      = 24 * time.Hour
)

var ErrIntegrationInboxLeaseLost = errors.New("integration inbox lease lost")

type IntegrationInboxState string

type IntegrationInboxSource string

const (
	IntegrationInboxSourceProvider  IntegrationInboxSource = "provider"
	IntegrationInboxSourceScheduled IntegrationInboxSource = "scheduled"
)

const (
	IntegrationInboxPending    IntegrationInboxState = "pending"
	IntegrationInboxProcessing IntegrationInboxState = "processing"
	IntegrationInboxCompleted  IntegrationInboxState = "completed"
	IntegrationInboxFailed     IntegrationInboxState = "failed"
)

type VerifiedIntegrationReceipt struct {
	ProjectID     uuid.UUID
	IntegrationID uuid.UUID
	ReceiptKey    string
	Payload       []byte
}

type IntegrationInboxRecord struct {
	ID             uuid.UUID
	ProjectID      uuid.UUID
	IntegrationID  uuid.UUID
	ReceiptKey     string
	State          IntegrationInboxState
	AttemptCount   int
	AvailableAt    time.Time
	ClaimExpiresAt *time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time
	Source         IntegrationInboxSource
	Payload        []byte
	Events         json.RawMessage
	Plan           json.RawMessage
	Progress       json.RawMessage // Outcomes only; admission identities come from Plan.
	ClaimToken     uuid.UUID
}

type IntegrationInboxLease struct {
	ProjectID uuid.UUID
	ReceiptID uuid.UUID
	Token     uuid.UUID
}

func (r IntegrationInboxRecord) Lease() IntegrationInboxLease {
	return IntegrationInboxLease{ProjectID: r.ProjectID, ReceiptID: r.ID, Token: r.ClaimToken}
}

type ClaimIntegrationInboxInput struct {
	ProjectID     uuid.UUID
	IntegrationID uuid.UUID
	LeaseDuration time.Duration
}

type IntegrationInboxIntegration struct {
	ProjectID     uuid.UUID
	IntegrationID uuid.UUID
}

type IntegrationInboxIntegrationPage struct {
	Integrations []IntegrationInboxIntegration
	NextCursor   IntegrationInboxIntegration
}
