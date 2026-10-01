package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

const MaxIntegrationSubscriptionsPerLaunch = 100

type IntegrationSubscriptionAttachment struct {
	IntegrationID uuid.UUID       `json:"integration_id"`
	Conversation  json.RawMessage `json:"conversation"`
}

type IntegrationSubscriptionRecord struct {
	ID, ProjectID, AgentID, IntegrationID uuid.UUID
	Address                               ConversationAddress
	CreatedAt                             time.Time
	AgentName                             string
	Conversation                          json.RawMessage
}

type RegisterIntegrationSubscriptionInput struct {
	OrgID, ProjectID, AgentID, IntegrationID uuid.UUID
	Address                                  ConversationAddress
}

type CreateIntegrationSubscriptionInput struct {
	OrgID, ProjectID, IntegrationID, AgentID uuid.UUID
	Conversation                             json.RawMessage
}

type ListIntegrationSubscriptionsInput struct {
	ProjectID, IntegrationID uuid.UUID
	After                    listing.KeysetCursor
	Limit                    int
}

type ListIntegrationSubscriptionsResult struct {
	Subscriptions []IntegrationSubscriptionRecord
	HasMore       bool
	Next          listing.KeysetCursor
}
