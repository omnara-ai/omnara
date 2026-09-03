package integrationstore

import (
	"encoding/json"
	"time"
)

const IntegrationProviderSlack = "slack"

type IntegrationInstallState string

const (
	IntegrationInstallStateActive   IntegrationInstallState = "active"
	IntegrationInstallStateDisabled IntegrationInstallState = "disabled"
)

type UpsertIntegrationInstallInput struct {
	OrgID                    ID
	ProjectID                ID
	IntegrationAppID         ID
	AgentProfileID           ID
	AgentID                  ID
	InstalledByUserID        ID
	Provider                 string
	IntegrationKind          string
	ConnectionMode           string
	State                    IntegrationInstallState
	ProviderTenantID         string
	ProviderAccountRef       string
	ProviderAgentDisplayName string
	CredentialSecretID       ID
	ProviderConfig           json.RawMessage
	ProviderIdentity         json.RawMessage
	ProviderMetadata         json.RawMessage
	OAuthFlowID              ID
}

type IntegrationInstallRecord struct {
	ID                       ID                      `json:"id"`
	OrgID                    ID                      `json:"org_id"`
	ProjectID                ID                      `json:"project_id"`
	IntegrationAppID         ID                      `json:"integration_app_id"`
	AgentProfileID           ID                      `json:"agent_profile_id,omitempty"`
	AgentID                  ID                      `json:"agent_id,omitempty"`
	InstalledByUserID        ID                      `json:"installed_by_user_id"`
	Provider                 string                  `json:"provider"`
	IntegrationKind          string                  `json:"integration_kind"`
	ConnectionMode           string                  `json:"connection_mode"`
	State                    IntegrationInstallState `json:"state"`
	ProviderTenantID         string                  `json:"provider_tenant_id,omitempty"`
	ProviderAccountRef       string                  `json:"provider_account_ref"`
	ProviderAgentDisplayName string                  `json:"provider_agent_display_name"`
	CredentialSecretID       ID                      `json:"credential_secret_id,omitempty"`
	ProviderConfig           json.RawMessage         `json:"provider_config"`
	ProviderIdentity         json.RawMessage         `json:"provider_identity"`
	ProviderMetadata         json.RawMessage         `json:"provider_metadata"`
	ConfigurationRevision    int64                   `json:"configuration_revision"`
	LastOAuthFlowID          ID                      `json:"last_oauth_flow_id,omitempty"`
	CreatedAt                time.Time               `json:"created_at"`
	UpdatedAt                time.Time               `json:"updated_at"`
	Created                  bool                    `json:"-"`
}

type CreateIntegrationTargetInput struct {
	ProjectID            ID
	AgentID              ID
	IntegrationInstallID ID
	ProviderRef          string
	ProviderRefKind      string
	DisplayName          string
	ProviderMetadata     json.RawMessage
}

type IntegrationTargetRecord struct {
	ID                   ID              `json:"id"`
	OrgID                ID              `json:"org_id"`
	ProjectID            ID              `json:"project_id"`
	AgentID              ID              `json:"agent_id"`
	IntegrationInstallID ID              `json:"integration_install_id"`
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
	ID                   ID                      `json:"id"`
	IntegrationInstallID ID                      `json:"integration_install_id"`
	TargetRef            string                  `json:"target_ref"`
	Provider             string                  `json:"provider"`
	InstallState         IntegrationInstallState `json:"install_state"`
	ProviderRef          string                  `json:"provider_ref"`
	ProviderRefKind      string                  `json:"provider_ref_kind"`
	DisplayName          string                  `json:"display_name"`
	IsCurrent            bool                    `json:"is_current"`
}
