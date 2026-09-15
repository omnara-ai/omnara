package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
)

type IntegrationAppState string

const (
	IntegrationAppStateActive   IntegrationAppState = "active"
	IntegrationAppStateDisabled IntegrationAppState = "disabled"
)

type CreateIntegrationAppInput struct {
	OrgID                      uuid.UUID
	OwnerProjectID             uuid.UUID
	Provider                   string
	ProviderAppRef             string
	DisplayName                string
	ConnectorKey               string
	CredentialSecretID         uuid.UUID
	InstallationCredentialKind string
	ProviderConfig             json.RawMessage
	ProviderMetadata           json.RawMessage
	State                      IntegrationAppState
}

type IntegrationAppRecord struct {
	ID                         uuid.UUID           `json:"id"`
	OrgID                      uuid.UUID           `json:"org_id"`
	OwnerProjectID             uuid.UUID           `json:"owner_project_id,omitempty"`
	Provider                   string              `json:"provider"`
	ProviderAppRef             string              `json:"provider_app_ref"`
	DisplayName                string              `json:"display_name"`
	ConnectorKey               string              `json:"connector_key"`
	CredentialSecretID         uuid.UUID           `json:"credential_secret_id,omitempty"`
	InstallationCredentialKind string              `json:"installation_credential_kind,omitempty"`
	ProviderConfig             json.RawMessage     `json:"provider_config"`
	ProviderMetadata           json.RawMessage     `json:"provider_metadata"`
	ConfigurationRevision      int64               `json:"configuration_revision"`
	State                      IntegrationAppState `json:"state"`
	CreatedAt                  time.Time           `json:"created_at"`
	UpdatedAt                  time.Time           `json:"updated_at"`
}

type IntegrationRouteState string

const (
	IntegrationRouteStateActive   IntegrationRouteState = "active"
	IntegrationRouteStateDisabled IntegrationRouteState = "disabled"

	MaxActiveIntegrationRoutesPerInstall   = 64
	MaxActiveReceiveBindingsPerTargetRoute = 256
	MaxAgentChannelTargetsPageSize         = 100
)

type CreateIntegrationRouteInput struct {
	AgentProfileID       uuid.UUID
	ProjectID            uuid.UUID
	IntegrationInstallID uuid.UUID
	DeploymentKey        string
	BehaviorKey          string
	Configuration        json.RawMessage
	State                IntegrationRouteState
}

type IntegrationRouteRecord struct {
	AgentProfileID       uuid.UUID             `json:"agent_profile_id,omitempty"`
	ID                   uuid.UUID             `json:"id"`
	ProjectID            uuid.UUID             `json:"project_id"`
	IntegrationInstallID uuid.UUID             `json:"integration_install_id"`
	DeploymentKey        string                `json:"deployment_key"`
	BehaviorKey          string                `json:"behavior_key"`
	Configuration        json.RawMessage       `json:"configuration"`
	State                IntegrationRouteState `json:"state"`
	CreatedAt            time.Time             `json:"created_at"`
	UpdatedAt            time.Time             `json:"updated_at"`
}

// ChannelGrants is one explicit, nonempty receive/read/send permission tuple.
// It grants no authority to create further reply-channel bindings.
type ChannelGrants struct {
	ReceiveAllowed bool `json:"receive_allowed"`
	ReadAllowed    bool `json:"read_allowed"`
	SendAllowed    bool `json:"send_allowed"`
}

type CreateIntegrationTargetBindingInput struct {
	ProjectID            uuid.UUID
	AgentID              uuid.UUID
	IntegrationInstallID uuid.UUID
	IntegrationTargetID  uuid.UUID
	IntegrationRouteID   uuid.UUID
	ReceiveAllowed       bool
	ReadAllowed          bool
	SendAllowed          bool
	ReplyChannelGrants   *ChannelGrants
	Source               string
	Metadata             json.RawMessage
}

type IntegrationTargetBindingRecord struct {
	ID                   uuid.UUID       `json:"id"`
	ProjectID            uuid.UUID       `json:"project_id"`
	AgentID              uuid.UUID       `json:"agent_id"`
	IntegrationInstallID uuid.UUID       `json:"integration_install_id"`
	IntegrationTargetID  uuid.UUID       `json:"integration_target_id"`
	IntegrationRouteID   uuid.UUID       `json:"integration_route_id,omitempty"`
	ReceiveAllowed       bool            `json:"receive_allowed"`
	ReadAllowed          bool            `json:"read_allowed"`
	SendAllowed          bool            `json:"send_allowed"`
	ReplyChannelGrants   *ChannelGrants  `json:"reply_channel_grants,omitempty"`
	Source               string          `json:"source"`
	Metadata             json.RawMessage `json:"metadata"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

type AgentChannelToolEligibility struct {
	List bool
	Read bool
	Send bool
}

type AgentChannelTarget struct {
	IntegrationKind      IntegrationKind         `json:"integration_kind"`
	ParentChannelID      uuid.UUID               `json:"parent_channel_id,omitempty"`
	ID                   uuid.UUID               `json:"id"`
	IntegrationInstallID uuid.UUID               `json:"integration_install_id"`
	TargetRef            string                  `json:"target_ref"`
	ProviderRef          string                  `json:"provider_ref"`
	ProviderRefKind      string                  `json:"provider_ref_kind"`
	DisplayName          string                  `json:"display_name"`
	Provider             string                  `json:"provider"`
	InstallState         IntegrationInstallState `json:"install_state"`
	ConnectorKey         string                  `json:"connector_key"`
	AppState             IntegrationAppState     `json:"app_state"`
	ReceiveAllowed       bool                    `json:"receive_allowed"`
	ReadAllowed          bool                    `json:"read_allowed"`
	SendAllowed          bool                    `json:"send_allowed"`
	CreatedAt            time.Time               `json:"-"`
}

type AgentChannelTargetCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

type ListAgentChannelTargetsInput struct {
	ParentChannelID uuid.UUID
	Limit           int
	After           *AgentChannelTargetCursor
}

type AgentChannelTargetPage struct {
	Targets []AgentChannelTarget
	Next    *AgentChannelTargetCursor
}

type IntegrationRuntimeDesiredState string

const (
	IntegrationRuntimeDesiredStateRunning IntegrationRuntimeDesiredState = "running"
	IntegrationRuntimeDesiredStateStopped IntegrationRuntimeDesiredState = "stopped"
)

type IntegrationRuntimeStatus string

const (
	IntegrationRuntimeStatusIdle    IntegrationRuntimeStatus = "idle"
	IntegrationRuntimeStatusRunning IntegrationRuntimeStatus = "running"
	IntegrationRuntimeStatusError   IntegrationRuntimeStatus = "error"
	IntegrationRuntimeStatusStopped IntegrationRuntimeStatus = "stopped"
)

type UpsertIntegrationRuntimeUnitInput struct {
	OrgID                uuid.UUID
	IntegrationAppID     uuid.UUID
	ProjectID            uuid.UUID
	IntegrationInstallID uuid.UUID
	UnitKey              string
	RuntimeKind          string
	DesiredState         IntegrationRuntimeDesiredState
	SpecRevision         int
	Configuration        json.RawMessage
}

type IntegrationRuntimeUnitRecord struct {
	ID                            uuid.UUID                      `json:"id"`
	OrgID                         uuid.UUID                      `json:"org_id"`
	IntegrationAppID              uuid.UUID                      `json:"integration_app_id"`
	ProjectID                     uuid.UUID                      `json:"project_id,omitempty"`
	IntegrationInstallID          uuid.UUID                      `json:"integration_install_id,omitempty"`
	Provider                      string                         `json:"provider"`
	ConnectorKey                  string                         `json:"connector_key"`
	UnitKey                       string                         `json:"unit_key"`
	RuntimeKind                   string                         `json:"runtime_kind"`
	DesiredState                  IntegrationRuntimeDesiredState `json:"desired_state"`
	SpecRevision                  int                            `json:"spec_revision"`
	Configuration                 json.RawMessage                `json:"configuration"`
	Status                        IntegrationRuntimeStatus       `json:"status"`
	LeaseOwner                    string                         `json:"lease_owner,omitempty"`
	LeaseToken                    uuid.UUID                      `json:"lease_token,omitempty"`
	LeaseGeneration               int64                          `json:"lease_generation"`
	LeasedAt                      *time.Time                     `json:"leased_at,omitempty"`
	RenewedAt                     *time.Time                     `json:"renewed_at,omitempty"`
	LeaseExpiresAt                *time.Time                     `json:"lease_expires_at,omitempty"`
	LeaseSpecRevision             int                            `json:"lease_spec_revision,omitempty"`
	LeaseAppConfigurationRevision int64                          `json:"lease_app_configuration_revision,omitempty"`
	LeaseInstallConfigRevision    int64                          `json:"lease_install_configuration_revision,omitempty"`
	CheckpointVersion             int                            `json:"checkpoint_version"`
	CheckpointRevision            int64                          `json:"checkpoint_revision"`
	Checkpoint                    json.RawMessage                `json:"checkpoint"`
	LastError                     json.RawMessage                `json:"last_error"`
	CreatedAt                     time.Time                      `json:"created_at"`
	UpdatedAt                     time.Time                      `json:"updated_at"`
}

// IntegrationRuntimeLeaseProof identifies the current owner of a persistent
// connector runtime. Stores use it as a transaction-local fence before
// committing mutations that originate from that runtime.
type IntegrationRuntimeLeaseProof struct {
	IntegrationAppID uuid.UUID
	UnitID           uuid.UUID
	LeaseToken       uuid.UUID
	LeaseGeneration  int64
}

type ClaimIntegrationRuntimeUnitsInput struct {
	LeaseOwner    string
	LeaseDuration time.Duration
	Capability    channelconnector.Capability
	Limit         int
}

type HeartbeatIntegrationRuntimeUnitInput struct {
	ID                uuid.UUID
	LeaseToken        uuid.UUID
	LeaseGeneration   int64
	LeaseDuration     time.Duration
	WriteCheckpoint   bool
	CheckpointVersion int
	Checkpoint        json.RawMessage
	Capabilities      []channelconnector.Capability
}

type ReleaseIntegrationRuntimeUnitInput struct {
	ID                uuid.UUID
	LeaseToken        uuid.UUID
	LeaseGeneration   int64
	WriteCheckpoint   bool
	CheckpointVersion int
	Checkpoint        json.RawMessage
	LastError         json.RawMessage
	Capabilities      []channelconnector.Capability
}
