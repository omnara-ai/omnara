package integrationstore

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

// These limits match integration_inbox's durable bounds. Every claim consumes
// an attempt, including a worker crash before planning. Only explicit operator
// retry grants a new budget; it never clears the frozen plan or progress.
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
	IntegrationInboxSourceProvider        IntegrationInboxSource = "provider"
	IntegrationInboxSourceScheduledLaunch IntegrationInboxSource = "scheduled_launch"
)

const (
	IntegrationInboxPending    IntegrationInboxState = "pending"
	IntegrationInboxProcessing IntegrationInboxState = "processing"
	IntegrationInboxCompleted  IntegrationInboxState = "completed"
	IntegrationInboxFailed     IntegrationInboxState = "failed"
	IntegrationInboxDiscarded  IntegrationInboxState = "discarded"
)

// VerifiedIntegrationReceipt is admitted only after provider authentication and
// account identity validation by the caller. Payload is the exact verified body;
// the first receipt wins on (project, app, receipt key), even if a provider
// replay changes transport metadata. No receipt or payload update is exposed.
type VerifiedIntegrationReceipt struct {
	ProjectID  uuid.UUID
	AppID      uuid.UUID
	ReceiptKey string
	Payload    []byte
}

type IntegrationInboxSummary struct {
	ID             uuid.UUID
	ProjectID      uuid.UUID
	AppID          uuid.UUID
	ReceiptKey     string
	State          IntegrationInboxState
	AttemptCount   int
	AvailableAt    time.Time
	ClaimExpiresAt *time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time
}

type IntegrationInboxRecord struct {
	IntegrationInboxSummary
	Source  IntegrationInboxSource
	Payload []byte
	// Events is an optional array normalized and decided by trusted app code.
	// Raw provider receipts leave this nil and preserve Payload unchanged.
	Events json.RawMessage
	// Plan is an object keyed by stable recipient slot. Each value is a provider-
	// typed object containing all frozen recipient identities/config references.
	// An empty object represents a receipt deliberately having no recipients.
	Plan json.RawMessage
	// Progress maps slot keys to append-only prepared/committed result objects.
	// It carries outcomes only; admission identities MUST come from Plan.
	Progress   json.RawMessage
	ClaimToken uuid.UUID
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
	AppID         uuid.UUID
	LeaseDuration time.Duration
}

type ListIntegrationInboxInput struct {
	ProjectID uuid.UUID
	AppID     uuid.UUID
	State     IntegrationInboxState
	After     listing.KeysetCursor
	Limit     int
}

type ListIntegrationInboxResult struct {
	Receipts []IntegrationInboxSummary
	HasMore  bool
	Next     listing.KeysetCursor
}

type IntegrationInboxApp struct {
	ProjectID uuid.UUID
	AppID     uuid.UUID
}
