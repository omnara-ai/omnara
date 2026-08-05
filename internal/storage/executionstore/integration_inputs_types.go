package executionstore

import "encoding/json"

type CreateIntegrationTargetContentInput struct {
	IntegrationInstallID   ID
	IntegrationTargetID    ID
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
	IntegrationInstallID ID
	IntegrationTargetID  ID
	IdempotencyKey       string
}
