package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const IntegrationProfileChoiceMinRetention = 7 * 24 * time.Hour
const IntegrationProfileChoiceMaxEventBytes = 256 * 1024

type IntegrationProfileChoiceOption struct {
	Key       string    `json:"key"`
	ProfileID uuid.UUID `json:"profile_id"`
	Name      string    `json:"name"`
}

type IntegrationProfileChoiceRecord struct {
	ID, ProjectID, IntegrationID uuid.UUID
	OwnerReceiptID               uuid.UUID
	Address                      ConversationAddress
	SourceKey                    string
	Event                        json.RawMessage
	Payload                      []byte
	Options                      []IntegrationProfileChoiceOption
	SelectedKey, SelectedBy      string
	MessageChannelID, MessageID  string
	CreatedAt                    time.Time
	ExpiresAt                    time.Time
	Revision                     int64
}

type EnsureIntegrationProfileChoiceInput struct {
	IntegrationID  uuid.UUID
	Address        ConversationAddress
	SourceKey      string
	Event          json.RawMessage
	Payload        []byte
	Options        []IntegrationProfileChoiceOption
	HasAttachments bool
}

type ChooseIntegrationProfileInput struct {
	ProjectID, IntegrationID, ID uuid.UUID
	Key, ActorID                 string
	MessageChannelID, MessageID  string
	SourceChoiceRevision         int64
	SourceSetupRevision          int64
}
