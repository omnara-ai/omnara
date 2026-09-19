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

type IntegrationConnectionState string

const (
	IntegrationConnectionStateActive   IntegrationConnectionState = "active"
	IntegrationConnectionStateDisabled IntegrationConnectionState = "disabled"
)

type SaveIntegrationConnectionInput struct {
	OrgID                    uuid.UUID
	ProjectID                uuid.UUID
	InstalledByUserID        uuid.UUID
	Provider                 string
	State                    IntegrationConnectionState
	ProviderTenantID         string
	ProviderAccountRef       string
	ProviderAgentDisplayName string
	CredentialSecretID       uuid.UUID
	ProviderConfig           json.RawMessage
	ProviderIdentity         json.RawMessage
	ProviderMetadata         json.RawMessage
	OAuthFlowID              uuid.UUID
	// Credential validation happens before the transaction; the version is
	// checked under the secret reference lock to fence concurrent rotation.
	CredentialVersionID uuid.UUID
	CredentialAppID     int64 // Verified GitHub credential App ID; required for GitHub.
	// SourceVerifiedIdentityRevision is the observed connection updated_at before
	// provider verification. The verified update checks it under the lifecycle gate.
	SourceVerifiedIdentityRevision time.Time
	// Set only by UpdateIntegrationConnectionWithVerifiedIdentity. Ordinary
	// account updates preserve the latest provider observations under the gate.
	verifiedProviderIdentity bool
}

type IntegrationConnectionRecord struct {
	ID                       uuid.UUID                  `json:"id"`
	OrgID                    uuid.UUID                  `json:"org_id"`
	ProjectID                uuid.UUID                  `json:"project_id"`
	InstalledByUserID        uuid.UUID                  `json:"installed_by_user_id"`
	Provider                 string                     `json:"provider"`
	State                    IntegrationConnectionState `json:"state"`
	ProviderTenantID         string                     `json:"provider_tenant_id,omitempty"`
	ProviderAccountRef       string                     `json:"provider_account_ref"`
	ProviderAgentDisplayName string                     `json:"provider_agent_display_name"`
	CredentialSecretID       uuid.UUID                  `json:"credential_secret_id,omitempty"`
	ProviderConfig           json.RawMessage            `json:"provider_config"`
	ProviderIdentity         json.RawMessage            `json:"provider_identity"`
	ProviderMetadata         json.RawMessage            `json:"provider_metadata"`
	LastOAuthFlowID          uuid.UUID                  `json:"last_oauth_flow_id,omitempty"`
	CreatedAt                time.Time                  `json:"created_at"`
	UpdatedAt                time.Time                  `json:"updated_at"`
	Created                  bool                       `json:"-"`
}

type IntegrationTargetRecord struct {
	RoutingRole             TargetRoutingRole `json:"routing_role"`
	AppID                   uuid.UUID         `json:"app_id,omitempty"`
	SelectionSlot           string            `json:"selection_slot,omitempty"`
	DeletedAt               *time.Time        `json:"deleted_at,omitempty"`
	ID                      uuid.UUID         `json:"id"`
	OrgID                   uuid.UUID         `json:"org_id"`
	ProjectID               uuid.UUID         `json:"project_id"`
	AgentID                 uuid.UUID         `json:"agent_id"`
	IntegrationConnectionID uuid.UUID         `json:"integration_connection_id"`
	TargetRef               string            `json:"target_ref"`
	ProviderRef             string            `json:"provider_ref"`
	ProviderRefKind         string            `json:"provider_ref_kind"`
	DisplayName             string            `json:"display_name"`
	ProviderMetadata        json.RawMessage   `json:"provider_metadata"`
	CreatedAt               time.Time         `json:"created_at"`
	UpdatedAt               time.Time         `json:"updated_at"`
	Created                 bool              `json:"-"`
}
