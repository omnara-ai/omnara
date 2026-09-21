package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

const MaxAppSubscriptionsPerLaunch = 100

// AppSubscriptionAttachment is a concrete receive route supplied by a launcher
// or public caller. Conversation is validated against the app's definition; only
// its canonical address and event set are persisted in the subscription table.
type AppSubscriptionAttachment struct {
	AppID        uuid.UUID       `json:"app_id"`
	Type         string          `json:"type"`
	Conversation json.RawMessage `json:"conversation"`
	Events       []string        `json:"events,omitempty"`
}

type AppSubscriptionRecord struct {
	ID, ProjectID, AgentID, AppID uuid.UUID
	Type                          string
	Address                       ConversationAddress
	Events                        []string
	ToolCallID                    uuid.UUID
	CreatedAt                     time.Time
	AgentName                     string
	Conversation                  json.RawMessage
}

type RegisterAppSubscriptionInput struct {
	OrgID, ProjectID, AgentID, AppID uuid.UUID
	Type                             string
	Address                          ConversationAddress
	Events                           []string
	ToolCallID                       *uuid.UUID
}

type CreateAppSubscriptionInput struct {
	OrgID, ProjectID, AppID, AgentID uuid.UUID
	Type                             string
	Conversation                     json.RawMessage
	Events                           []string
}

type ListAppSubscriptionsInput struct {
	ProjectID, AppID uuid.UUID
	After            listing.KeysetCursor
	Limit            int
}

type ListAppSubscriptionsResult struct {
	Subscriptions []AppSubscriptionRecord
	HasMore       bool
	Next          listing.KeysetCursor
}
