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
	ID, ProjectID, AppID uuid.UUID
	// OwnerReceiptID is immutable publication provenance; only that receipt may
	// present the menu, using its current inbox lease and ordinary recovery.
	OwnerReceiptID              uuid.UUID
	Address                     ConversationAddress
	SourceKey                   string
	Event                       json.RawMessage
	Payload                     []byte
	Options                     []AppProfileChoiceOption
	SelectedKey, SelectedBy     string
	MessageChannelID, MessageID string
	ExpiresAt                   time.Time
	Revision                    int64
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
// source. SourceChoiceRevision fences attachment siblings that replace it.
// SourceSetupRevision pins the app authenticated by the callback.
type ChooseAppProfileInput struct {
	ProjectID, AppID, ID        uuid.UUID
	Key, ActorID                string
	MessageChannelID, MessageID string
	SourceChoiceRevision        int64
	Events                      json.RawMessage
	SourceSetupRevision         int64
}
