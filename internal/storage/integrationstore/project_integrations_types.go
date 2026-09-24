package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

type ProjectIntegrationSettings struct {
	Launcher *IntegrationLauncher `json:"launcher,omitempty"`
}

type IntegrationLauncher struct {
	Trigger   string                  `json:"trigger"`
	ScopeKind string                  `json:"scope_kind,omitempty"`
	ScopeRef  string                  `json:"scope_ref,omitempty"`
	Slots     []IntegrationLaunchSlot `json:"slots"`
}

type IntegrationLaunchSlot struct {
	Key            string     `json:"key"`
	AgentProfileID *uuid.UUID `json:"agent_profile_id,omitempty"`
	AgentID        *uuid.UUID `json:"agent_id,omitempty"`
}

type ProjectIntegrationRecord struct {
	ID                       uuid.UUID
	OrgID                    uuid.UUID
	InstalledByUserID        uuid.UUID
	Provider                 string
	State                    ProjectIntegrationState
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
	IntegrationType          integrationdefinition.Type
	Settings                 ProjectIntegrationSettings
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

type SaveProjectIntegrationInput struct {
	OrgID           uuid.UUID
	ProjectID       uuid.UUID
	Name            string
	IntegrationType integrationdefinition.Type
	Settings        ProjectIntegrationSettings
}

type ListProjectIntegrationsInput struct {
	ProjectID   uuid.UUID
	NamePattern string
	After       listing.KeysetCursor
	Limit       int
}

type ListProjectIntegrationsResult struct {
	Integrations []ProjectIntegrationRecord
	HasMore      bool
	Next         listing.KeysetCursor
}
