package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	IntegrationProviderSlack   = "slack"
	IntegrationProviderGitHub  = "github"
	IntegrationProviderDiscord = "discord"
)

type IntegrationState string

const (
	IntegrationStateActive       IntegrationState = "active"
	IntegrationStateDisconnected IntegrationState = "disconnected"
)

type ConfigureIntegrationInput struct {
	OrgID                    uuid.UUID
	ProjectID                uuid.UUID
	IntegrationID            uuid.UUID
	InstalledByUserID        uuid.UUID
	Provider                 string
	ProviderTenantID         string
	ProviderAccountRef       string
	ProviderAgentDisplayName string
	CredentialSecretID       uuid.UUID
	CredentialVersionID      uuid.UUID
	CredentialAppID          int64
	ProviderConfig           json.RawMessage
	ProviderIdentity         json.RawMessage
	ProviderMetadata         json.RawMessage
	OAuthFlowID              uuid.UUID
	ExpectedSetupRevision    int64
}

type IntegrationTargetRecord struct {
	IntegrationID    uuid.UUID       `json:"integration_id,omitempty"`
	LaunchKey    string          `json:"launch_key,omitempty"`
	DeletedAt        *time.Time      `json:"deleted_at,omitempty"`
	ID               uuid.UUID       `json:"id"`
	OrgID            uuid.UUID       `json:"org_id"`
	ProjectID        uuid.UUID       `json:"project_id"`
	AgentID          uuid.UUID       `json:"agent_id"`
	ScopeRef      string          `json:"scope_ref"`
	ScopeKind  string          `json:"scope_kind"`
	DisplayName      string          `json:"display_name"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	Created          bool            `json:"-"`
}
