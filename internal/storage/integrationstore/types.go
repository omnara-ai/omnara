package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
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
	OrgID       uuid.UUID
	ProjectID   uuid.UUID
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
	OrgID              uuid.UUID
	ProjectID          uuid.UUID
	IntegrationAppID   uuid.UUID
	InstalledBy        identitystore.PrincipalRecord
	Provider           string
	IntegrationKind    IntegrationKind
	ConnectionMode     string
	State              IntegrationInstallState
	ProviderTenantID   string
	ProviderAccountRef string
	DisplayName        string
	CredentialSecretID uuid.UUID
	ProviderConfig     json.RawMessage
	ProviderIdentity   json.RawMessage
	Metadata           json.RawMessage
	OAuthFlowID        uuid.UUID
	// InitialRoute, when present, commits with installation credentials and
	// OAuth redemption. Its project and installation are derived internally.
	InitialRoute *CreateIntegrationRouteInput
}

type IntegrationInstallRecord struct {
	ID                    uuid.UUID                     `json:"id"`
	OrgID                 uuid.UUID                     `json:"org_id"`
	ProjectID             uuid.UUID                     `json:"project_id"`
	IntegrationAppID      uuid.UUID                     `json:"integration_app_id"`
	InstalledBy           identitystore.PrincipalRecord `json:"-"`
	Provider              string                        `json:"provider"`
	IntegrationKind       IntegrationKind               `json:"integration_kind"`
	ConnectionMode        string                        `json:"connection_mode"`
	State                 IntegrationInstallState       `json:"state"`
	ProviderTenantID      string                        `json:"provider_tenant_id,omitempty"`
	ProviderAccountRef    string                        `json:"provider_account_ref"`
	DisplayName           string                        `json:"display_name"`
	CredentialSecretID    uuid.UUID                     `json:"credential_secret_id,omitempty"`
	ProviderConfig        json.RawMessage               `json:"provider_config"`
	ProviderIdentity      json.RawMessage               `json:"provider_identity"`
	Metadata              json.RawMessage               `json:"metadata"`
	ConfigurationRevision int64                         `json:"configuration_revision"`
	LastOAuthFlowID       uuid.UUID                     `json:"last_oauth_flow_id,omitempty"`
	CreatedAt             time.Time                     `json:"created_at"`
	UpdatedAt             time.Time                     `json:"updated_at"`
	Created               bool                          `json:"-"`
}

type CreateIntegrationTargetInput struct {
	ProjectID            uuid.UUID
	IntegrationInstallID uuid.UUID
	ProviderRef          string
	ChannelDefinitionID  uuid.UUID `json:"channel_definition_id"`
	ParentChannelID      uuid.UUID `json:"parent_channel_id,omitempty"`
	ProviderRefKind      string
	DisplayName          string
	ProviderMetadata     json.RawMessage
}

type IntegrationTargetRecord struct {
	ID                   uuid.UUID       `json:"id"`
	OrgID                uuid.UUID       `json:"org_id"`
	ProjectID            uuid.UUID       `json:"project_id"`
	IntegrationInstallID uuid.UUID       `json:"integration_install_id"`
	TargetRef            string          `json:"target_ref"`
	ProviderRef          string          `json:"provider_ref"`
	ChannelDefinitionID  uuid.UUID       `json:"channel_definition_id"`
	ParentChannelID      uuid.UUID       `json:"parent_channel_id,omitempty"`
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
