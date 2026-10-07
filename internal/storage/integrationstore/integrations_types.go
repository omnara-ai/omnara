package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

type IntegrationSettings = json.RawMessage

type IntegrationRecord struct {
	ID                       uuid.UUID
	OrgID                    uuid.UUID
	InstalledByUserID        uuid.UUID
	Provider                 integrationdefinition.Provider
	State                    IntegrationState
	ProviderTenantID         string
	ProviderAccountRef       string
	ProviderAgentDisplayName string
	CredentialSecretID       uuid.UUID
	ProviderConfig           json.RawMessage
	ProviderIdentity         json.RawMessage
	ProviderMetadata         json.RawMessage
	LastOAuthFlowID          uuid.UUID
	SetupRevision            int64
	DeletedAt                *time.Time
	ProjectID                uuid.UUID
	Name                     string
	IntegrationKind          integrationdefinition.Kind
	Settings                 IntegrationSettings
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

type SaveIntegrationInput struct {
	OrgID           uuid.UUID
	ProjectID       uuid.UUID
	Name            string
	IntegrationKind integrationdefinition.Kind
	Settings        IntegrationSettings
}

type ListIntegrationsInput struct {
	ProjectID   uuid.UUID
	NamePattern string
	After       listing.KeysetCursor
	Limit       int
}

type ListIntegrationsResult struct {
	Integrations []IntegrationRecord
	HasMore      bool
	Next         listing.KeysetCursor
}
