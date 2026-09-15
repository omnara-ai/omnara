package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

const IntegrationProviderSlack = "slack"

// IntegrationKind identifies the immutable authority owner, independently of transport.
type IntegrationKind string

const (
	IntegrationKindManaged  IntegrationKind = "managed"
	IntegrationKindExternal IntegrationKind = "external"
)

type CreateExternalIntegrationInstallInput struct {
	OrgID       ID
	ProjectID   ID
	InstalledBy identitystore.PrincipalRecord
	DisplayName string
	Metadata    json.RawMessage
}

type IntegrationInstallState string

const (
	IntegrationInstallStateActive   IntegrationInstallState = "active"
	IntegrationInstallStateDisabled IntegrationInstallState = "disabled"
)

type UpsertIntegrationInstallInput struct {
	OrgID              ID
	ProjectID          ID
	IntegrationAppID   ID
	InstalledBy        identitystore.PrincipalRecord
	Provider           string
	IntegrationKind    IntegrationKind
	ConnectionMode     string
	State              IntegrationInstallState
	ProviderTenantID   string
	ProviderAccountRef string
	DisplayName        string
	CredentialSecretID ID
	ProviderConfig     json.RawMessage
	ProviderIdentity   json.RawMessage
	Metadata           json.RawMessage
	OAuthFlowID        ID
	// InitialRoute, when present, commits with installation credentials and
	// OAuth redemption. Its project and installation are derived internally.
	InitialRoute *CreateIntegrationRouteInput
}

type IntegrationInstallRecord struct {
	ID                    ID                            `json:"id"`
	OrgID                 ID                            `json:"org_id"`
	ProjectID             ID                            `json:"project_id"`
	IntegrationAppID      ID                            `json:"integration_app_id"`
	InstalledBy           identitystore.PrincipalRecord `json:"-"`
	Provider              string                        `json:"provider"`
	IntegrationKind       IntegrationKind               `json:"integration_kind"`
	ConnectionMode        string                        `json:"connection_mode"`
	State                 IntegrationInstallState       `json:"state"`
	ProviderTenantID      string                        `json:"provider_tenant_id,omitempty"`
	ProviderAccountRef    string                        `json:"provider_account_ref"`
	DisplayName           string                        `json:"display_name"`
	CredentialSecretID    ID                            `json:"credential_secret_id,omitempty"`
	ProviderConfig        json.RawMessage               `json:"provider_config"`
	ProviderIdentity      json.RawMessage               `json:"provider_identity"`
	Metadata              json.RawMessage               `json:"metadata"`
	ConfigurationRevision int64                         `json:"configuration_revision"`
	LastOAuthFlowID       ID                            `json:"last_oauth_flow_id,omitempty"`
	CreatedAt             time.Time                     `json:"created_at"`
	UpdatedAt             time.Time                     `json:"updated_at"`
	Created               bool                          `json:"-"`
}

type CreateIntegrationTargetInput struct {
	ProjectID            ID
	IntegrationInstallID ID
	ProviderRef          string
	ChannelDefinitionID  ID `json:"channel_definition_id"`
	ParentChannelID      ID `json:"parent_channel_id,omitempty"`
	ProviderRefKind      string
	DisplayName          string
	ProviderMetadata     json.RawMessage
}

type IntegrationTargetRecord struct {
	ID                   ID              `json:"id"`
	OrgID                ID              `json:"org_id"`
	ProjectID            ID              `json:"project_id"`
	IntegrationInstallID ID              `json:"integration_install_id"`
	TargetRef            string          `json:"target_ref"`
	ProviderRef          string          `json:"provider_ref"`
	ChannelDefinitionID  ID              `json:"channel_definition_id"`
	ParentChannelID      ID              `json:"parent_channel_id,omitempty"`
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
