package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const IntegrationProviderSlack = "slack"

type IntegrationInstallState string

const (
	IntegrationInstallStateActive   IntegrationInstallState = "active"
	IntegrationInstallStateDisabled IntegrationInstallState = "disabled"
)

type UpsertIntegrationInstallInput struct {
	OrgID                    uuid.UUID
	ProjectID                uuid.UUID
	AgentProfileID           uuid.UUID
	AgentID                  uuid.UUID
	InstalledByUserID        uuid.UUID
	Provider                 string
	IntegrationKind          string
	ConnectionMode           string
	State                    IntegrationInstallState
	ProviderTenantID         string
	ProviderAccountRef       string
	ProviderAgentDisplayName string
	CredentialSecretID       uuid.UUID
	ProviderConfig           json.RawMessage
	ProviderIdentity         json.RawMessage
	ProviderMetadata         json.RawMessage
	OAuthFlowID              uuid.UUID
}

type IntegrationInstallRecord struct {
	ID                       uuid.UUID               `json:"id"`
	OrgID                    uuid.UUID               `json:"org_id"`
	ProjectID                uuid.UUID               `json:"project_id"`
	AgentProfileID           uuid.UUID               `json:"agent_profile_id,omitempty"`
	AgentID                  uuid.UUID               `json:"agent_id,omitempty"`
	InstalledByUserID        uuid.UUID               `json:"installed_by_user_id"`
	Provider                 string                  `json:"provider"`
	IntegrationKind          string                  `json:"integration_kind"`
	ConnectionMode           string                  `json:"connection_mode"`
	State                    IntegrationInstallState `json:"state"`
	ProviderTenantID         string                  `json:"provider_tenant_id,omitempty"`
	ProviderAccountRef       string                  `json:"provider_account_ref"`
	ProviderAgentDisplayName string                  `json:"provider_agent_display_name"`
	CredentialSecretID       uuid.UUID               `json:"credential_secret_id,omitempty"`
	ProviderConfig           json.RawMessage         `json:"provider_config"`
	ProviderIdentity         json.RawMessage         `json:"provider_identity"`
	ProviderMetadata         json.RawMessage         `json:"provider_metadata"`
	LastOAuthFlowID          uuid.UUID               `json:"last_oauth_flow_id,omitempty"`
	CreatedAt                time.Time               `json:"created_at"`
	UpdatedAt                time.Time               `json:"updated_at"`
	Created                  bool                    `json:"-"`
}

type CreateIntegrationTargetInput struct {
	ProjectID            uuid.UUID
	AgentID              uuid.UUID
	IntegrationInstallID uuid.UUID
	ProviderRef          string
	ProviderRefKind      string
	DisplayName          string
}

type IntegrationTargetRecord struct {
	ID                   uuid.UUID       `json:"id"`
	OrgID                uuid.UUID       `json:"org_id"`
	ProjectID            uuid.UUID       `json:"project_id"`
	AgentID              uuid.UUID       `json:"agent_id"`
	IntegrationInstallID uuid.UUID       `json:"integration_install_id"`
	TargetRef            string          `json:"target_ref"`
	ProviderRef          string          `json:"provider_ref"`
	ProviderRefKind      string          `json:"provider_ref_kind"`
	DisplayName          string          `json:"display_name"`
	ProviderMetadata     json.RawMessage `json:"provider_metadata"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	Created              bool            `json:"-"`
}

type IntegrationTargetSummary struct {
	ID                   uuid.UUID               `json:"id"`
	IntegrationInstallID uuid.UUID               `json:"integration_install_id"`
	TargetRef            string                  `json:"target_ref"`
	Provider             string                  `json:"provider"`
	InstallState         IntegrationInstallState `json:"install_state"`
	ProviderRef          string                  `json:"provider_ref"`
	ProviderRefKind      string                  `json:"provider_ref_kind"`
	DisplayName          string                  `json:"display_name"`
	IsCurrent            bool                    `json:"is_current"`
}
