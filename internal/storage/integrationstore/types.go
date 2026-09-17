package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

const (
	IntegrationProviderSlack   = "slack"
	IntegrationProviderDiscord = "discord"
	IntegrationProviderGitHub  = "github"
)

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
	OrgID            uuid.UUID
	ProjectID        uuid.UUID
	IntegrationAppID uuid.UUID
	// Setup pins the app used for provider verification. Zero preserves callers
	// that do not perform an external verification step.
	ExpectedAppConfigurationRevision int64
	InstalledBy                      identitystore.PrincipalRecord
	Provider                         string
	IntegrationKind                  IntegrationKind
	ConnectionMode                   string
	State                            IntegrationInstallState
	ProviderTenantID                 string
	ProviderAccountRef               string
	DisplayName                      string
	CredentialSecretID               uuid.UUID
	ProviderIdentity                 json.RawMessage
	Metadata                         json.RawMessage
	OAuthFlowID                      uuid.UUID
	// InitialRoute, when present, commits with installation credentials and
	// OAuth redemption. Its project and installation are derived internally.
	InitialRoute *CreateIntegrationRouteInput
	// DiscordRuntimeShardCount is the verified setup recommendation. Zero omits
	// runtime setup; reconnects reuse the existing app shard set instead of resharding.
	DiscordRuntimeShardCount int
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
	ProviderIdentity      json.RawMessage               `json:"provider_identity"`
	Metadata              json.RawMessage               `json:"metadata"`
	ConfigurationRevision int64                         `json:"configuration_revision"`
	LastOAuthFlowID       uuid.UUID                     `json:"last_oauth_flow_id,omitempty"`
	CreatedAt             time.Time                     `json:"created_at"`
	UpdatedAt             time.Time                     `json:"updated_at"`
	Created               bool                          `json:"-"`
	// DiscordRuntimeShardCount is returned only by successful runtime setup,
	// including reconnect. It is the retained configuration, not a persisted
	// installation field or necessarily the current provider recommendation.
	DiscordRuntimeShardCount int `json:"-"`
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
