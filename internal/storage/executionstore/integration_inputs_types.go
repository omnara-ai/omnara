package executionstore

import (
	"encoding/json"

	"github.com/google/uuid"
)

type CreateIntegrationTargetContentInput struct {
	IntegrationInstallID   uuid.UUID
	IntegrationTargetID    uuid.UUID
	ProviderTenantID       string
	ProviderUserID         string
	ActorDisplayName       string
	ContentBlocks          json.RawMessage
	Metadata               json.RawMessage
	DeliveryMode           AgentInputDeliveryMode
	IdempotencyKey         string
	CancelOpenInteractions bool
}

type GetIntegrationTargetInputByIdempotencyInput struct {
	IntegrationInstallID uuid.UUID
	IntegrationTargetID  uuid.UUID
	IdempotencyKey       string
}
