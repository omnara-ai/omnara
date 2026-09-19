package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const AppProfileChoiceMinRetention = 7 * 24 * time.Hour

type AppProfileChoiceOption struct {
	Key       string    `json:"key"`
	ProfileID uuid.UUID `json:"profile_id"`
	Name      string    `json:"name"`
}

type AppProfileChoiceRecord struct {
	ID, ProjectID, ConnectionID, AppID uuid.UUID
	// OwnerReceiptID is immutable publication provenance; only that receipt may
	// present the menu, using its current inbox lease and ordinary recovery.
	OwnerReceiptID                  uuid.UUID
	Address                         ConversationAddress
	SourceKey                       string
	Event                           json.RawMessage
	Payload                         []byte
	Options                         []AppProfileChoiceOption
	SelectedKey, SelectedBy         string
	MessageChannelID, MessageID     string
	ExpiresAt, UpdatedAt, CreatedAt time.Time
}

type EnsureAppProfileChoiceInput struct {
	AppID          uuid.UUID
	Address        ConversationAddress
	SourceKey      string
	Event          json.RawMessage
	Payload        []byte
	Options        []AppProfileChoiceOption
	HasAttachments bool
}

// ChooseAppProfileInput carries events built by trusted app code from the stored
// source. SourceChoiceUpdatedAt fences attachment siblings that replace it.
// SourceConnectionUpdatedAt pins the connection authenticated by the callback.
type ChooseAppProfileInput struct {
	ProjectID, ConnectionID, ID uuid.UUID
	Key, ActorID                string
	MessageChannelID, MessageID string
	SourceChoiceUpdatedAt       time.Time
	Events                      json.RawMessage
	SourceConnectionUpdatedAt   time.Time
}
