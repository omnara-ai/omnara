package appstore

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	AppInboxMaxPayloadBytes    = 1024 * 1024
	AppInboxMaxPlanBytes       = 256 * 1024
	AppInboxMaxEventsBytes     = 256 * 1024
	AppInboxMaxErrorBytes      = 4096
	AppInboxMaxReceiptKeyBytes = 512
	AppInboxMaxAttempts        = 8
	AppInboxMaxBatch           = 100
	AppInboxMaxLease           = 5 * time.Minute
	AppInboxMaxRetryDelay      = 24 * time.Hour
)

var ErrAppInboxLeaseLost = errors.New("app inbox lease lost")

type AppInboxState string

type AppInboxSource string

const (
	AppInboxSourceProvider  AppInboxSource = "provider"
	AppInboxSourceScheduled AppInboxSource = "scheduled"
)

const (
	AppInboxPending    AppInboxState = "pending"
	AppInboxProcessing AppInboxState = "processing"
	AppInboxCompleted  AppInboxState = "completed"
	AppInboxFailed     AppInboxState = "failed"
)

type VerifiedAppReceipt struct {
	ProjectID  uuid.UUID
	AppID      uuid.UUID
	ReceiptKey string
	Payload    []byte
}

type AppInboxRecord struct {
	ID             uuid.UUID
	ProjectID      uuid.UUID
	AppID          uuid.UUID
	ReceiptKey     string
	State          AppInboxState
	AttemptCount   int
	AvailableAt    time.Time
	ClaimExpiresAt *time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time
	Source         AppInboxSource
	Payload        []byte
	Events         json.RawMessage
	Plan           json.RawMessage
	Progress       json.RawMessage // Outcomes only; admission identities come from Plan.
	ClaimToken     uuid.UUID
}

type AppInboxLease struct {
	ProjectID uuid.UUID
	ReceiptID uuid.UUID
	Token     uuid.UUID
}

func (r AppInboxRecord) Lease() AppInboxLease {
	return AppInboxLease{ProjectID: r.ProjectID, ReceiptID: r.ID, Token: r.ClaimToken}
}

type ClaimAppInboxInput struct {
	ProjectID     uuid.UUID
	AppID         uuid.UUID
	LeaseDuration time.Duration
}

type AppInboxApp struct {
	ProjectID uuid.UUID
	AppID     uuid.UUID
}

type AppInboxAppPage struct {
	Apps       []AppInboxApp
	NextCursor AppInboxApp
}
