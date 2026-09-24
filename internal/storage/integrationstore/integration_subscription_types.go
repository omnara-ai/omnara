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
	Type          string          `json:"type"`
	Conversation  json.RawMessage `json:"conversation"`
	Events        []string        `json:"events,omitempty"`
}

type IntegrationSubscriptionRecord struct {
	ID, ProjectID, AgentID, IntegrationID uuid.UUID
	Type                                  string
	Address                               ConversationAddress
	Events                                []string
	CreatedAt                             time.Time
	AgentName                             string
	Conversation                          json.RawMessage
}

type RegisterIntegrationSubscriptionInput struct {
	OrgID, ProjectID, AgentID, IntegrationID uuid.UUID
	Type                                     string
	Address                                  ConversationAddress
	Events                                   []string
}

type CreateIntegrationSubscriptionInput struct {
	OrgID, ProjectID, IntegrationID, AgentID uuid.UUID
	Type                                     string
	Conversation                             json.RawMessage
	Events                                   []string
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
