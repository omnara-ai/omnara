package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/channelconnector"
)

type IntegrationAppState string

const (
	IntegrationAppStateActive   IntegrationAppState = "active"
	IntegrationAppStateDisabled IntegrationAppState = "disabled"
)

type CreateIntegrationAppInput struct {
	OrgID                      ID
	OwnerProjectID             ID
	Provider                   string
	ProviderAppRef             string
	DisplayName                string
	ConnectorKey               string
	CredentialSecretID         ID
	InstallationCredentialKind string
	ProviderConfig             json.RawMessage
	ProviderMetadata           json.RawMessage
	State                      IntegrationAppState
}

type IntegrationAppRecord struct {
	ID                         ID                  `json:"id"`
	OrgID                      ID                  `json:"org_id"`
	OwnerProjectID             ID                  `json:"owner_project_id,omitempty"`
	Provider                   string              `json:"provider"`
	ProviderAppRef             string              `json:"provider_app_ref"`
	DisplayName                string              `json:"display_name"`
	ConnectorKey               string              `json:"connector_key"`
	CredentialSecretID         ID                  `json:"credential_secret_id,omitempty"`
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
	ProjectID            ID
	IntegrationInstallID ID
	DeploymentKey        string
	HandlerKey           string
	HandlerVersion       int
	Configuration        json.RawMessage
	State                IntegrationRouteState
}

type IntegrationRouteRecord struct {
	ID                   ID                    `json:"id"`
	ProjectID            ID                    `json:"project_id"`
	IntegrationInstallID ID                    `json:"integration_install_id"`
	DeploymentKey        string                `json:"deployment_key"`
	HandlerKey           string                `json:"handler_key"`
	HandlerVersion       int                   `json:"handler_version"`
	Configuration        json.RawMessage       `json:"configuration"`
	State                IntegrationRouteState `json:"state"`
	CreatedAt            time.Time             `json:"created_at"`
	UpdatedAt            time.Time             `json:"updated_at"`
}

type CreateIntegrationTargetBindingInput struct {
	ProjectID            ID
	AgentID              ID
	IntegrationInstallID ID
	IntegrationTargetID  ID
	IntegrationRouteID   ID
	ReceiveAllowed       bool
	SendAllowed          bool
	Source               string
	Metadata             json.RawMessage
}

type IntegrationTargetBindingRecord struct {
	ID                   ID              `json:"id"`
	ProjectID            ID              `json:"project_id"`
	AgentID              ID              `json:"agent_id"`
	IntegrationInstallID ID              `json:"integration_install_id"`
	IntegrationTargetID  ID              `json:"integration_target_id"`
	IntegrationRouteID   ID              `json:"integration_route_id,omitempty"`
	ReceiveAllowed       bool            `json:"receive_allowed"`
	SendAllowed          bool            `json:"send_allowed"`
	Source               string          `json:"source"`
	Metadata             json.RawMessage `json:"metadata"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

type IntegrationInputOrigin struct {
	TargetID  ID
	BindingID ID
}

type AgentChannelToolEligibility struct {
	List bool
	Send bool
}

type AgentChannelTarget struct {
	ID                   ID                      `json:"id"`
	IntegrationInstallID ID                      `json:"integration_install_id"`
	TargetRef            string                  `json:"target_ref"`
	ProviderRef          string                  `json:"provider_ref"`
	ProviderRefKind      string                  `json:"provider_ref_kind"`
	DisplayName          string                  `json:"display_name"`
	Provider             string                  `json:"provider"`
	InstallState         IntegrationInstallState `json:"install_state"`
	ConnectorKey         string                  `json:"connector_key"`
	AppState             IntegrationAppState     `json:"app_state"`
	ReceiveAllowed       bool                    `json:"receive_allowed"`
	SendAllowed          bool                    `json:"send_allowed"`
	CreatedAt            time.Time               `json:"-"`
}

type AgentChannelTargetCursor struct {
	CreatedAt time.Time
	ID        ID
}

type ListAgentChannelTargetsInput struct {
	Limit int
	After *AgentChannelTargetCursor
}

type AgentChannelTargetPage struct {
	Targets []AgentChannelTarget
	Next    *AgentChannelTargetCursor
}

type IntegrationDeliveryState string

const (
	IntegrationDeliveryStatePending   IntegrationDeliveryState = "pending"
	IntegrationDeliveryStateClaimed   IntegrationDeliveryState = "claimed"
	IntegrationDeliveryStateRetryWait IntegrationDeliveryState = "retry_wait"
	IntegrationDeliveryStateDelivered IntegrationDeliveryState = "delivered"
	IntegrationDeliveryStateFailed    IntegrationDeliveryState = "failed"
	IntegrationDeliveryStateUnknown   IntegrationDeliveryState = "unknown"
	IntegrationDeliveryStateCanceled  IntegrationDeliveryState = "canceled"

	MaxProviderMessageRefBytes = 2048
)

type IntegrationDeliveryTransport string

const (
	IntegrationDeliveryTransportConnector IntegrationDeliveryTransport = "connector"
	IntegrationDeliveryTransportNative    IntegrationDeliveryTransport = "native"
)

type CreateIntegrationDeliveryInput struct {
	ProjectID                  ID
	AgentID                    ID
	IntegrationTargetBindingID ID
	Transport                  IntegrationDeliveryTransport
	DeliveryKind               string
	PayloadVersion             string
	Payload                    json.RawMessage
	IdempotencyScope           string
	IdempotencyKey             string
	NotifyRef                  ID
}

type IntegrationDeliveryRecord struct {
	ID                           ID                           `json:"id"`
	ProjectID                    ID                           `json:"project_id"`
	AgentID                      ID                           `json:"agent_id"`
	IntegrationAppID             ID                           `json:"integration_app_id"`
	IntegrationInstallID         ID                           `json:"integration_install_id"`
	IntegrationTargetID          ID                           `json:"integration_target_id"`
	IntegrationTargetBindingID   ID                           `json:"integration_target_binding_id"`
	Provider                     string                       `json:"provider"`
	ConnectorKey                 string                       `json:"connector_key"`
	Transport                    IntegrationDeliveryTransport `json:"transport"`
	DeliveryKind                 string                       `json:"delivery_kind"`
	PayloadVersion               string                       `json:"payload_version"`
	Payload                      json.RawMessage              `json:"payload"`
	IdempotencyScope             string                       `json:"idempotency_scope"`
	IdempotencyKey               string                       `json:"idempotency_key"`
	State                        IntegrationDeliveryState     `json:"state"`
	AttemptCount                 int                          `json:"attempt_count"`
	AvailableAt                  time.Time                    `json:"available_at"`
	ClaimToken                   ID                           `json:"claim_token,omitempty"`
	ClaimGeneration              int64                        `json:"claim_generation"`
	ClaimedBy                    string                       `json:"claimed_by,omitempty"`
	ClaimedAt                    *time.Time                   `json:"claimed_at,omitempty"`
	ClaimExpiresAt               *time.Time                   `json:"claim_expires_at,omitempty"`
	NotifyRef                    ID                           `json:"notify_ref,omitempty"`
	ProviderMessageRef           string                       `json:"provider_message_ref,omitempty"`
	LastError                    json.RawMessage              `json:"last_error"`
	CompletedAt                  *time.Time                   `json:"completed_at,omitempty"`
	CreatedAt                    time.Time                    `json:"created_at"`
	UpdatedAt                    time.Time                    `json:"updated_at"`
	AppConfigurationRevision     int64                        `json:"app_configuration_revision,omitempty"`
	InstallConfigurationRevision int64                        `json:"install_configuration_revision,omitempty"`
	Created                      bool                         `json:"-"`
}

type ClaimIntegrationDeliveriesInput struct {
	ClaimedBy     string
	LeaseDuration time.Duration
	Capability    channelconnector.Capability
	Limit         int
}

// MaxIntegrationDeliveryClaims is a final safety backstop for a connector that
// repeatedly reports that no provider I/O began and another attempt is safe.
// Provider adapters should use a much lower bound for actual network sends.
const MaxIntegrationDeliveryClaims = 64

type CompleteIntegrationDeliveryInput struct {
	ID                 ID
	ClaimToken         ID
	ClaimGeneration    int64
	State              IntegrationDeliveryState
	RetryAfter         time.Duration
	ProviderMessageRef string
	LastError          json.RawMessage
	Capabilities       []channelconnector.Capability
}

type IntegrationDeliveryUpdate struct {
	ID        ID
	ProjectID ID
	NotifyRef ID
}

type DeleteRetainedIntegrationDeliveriesInput struct {
	Retention time.Duration
	Limit     int
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
	OrgID                ID
	IntegrationAppID     ID
	ProjectID            ID
	IntegrationInstallID ID
	UnitKey              string
	RuntimeKind          string
	DesiredState         IntegrationRuntimeDesiredState
	SpecRevision         int
	Configuration        json.RawMessage
}

type IntegrationRuntimeUnitRecord struct {
	ID                            ID                             `json:"id"`
	OrgID                         ID                             `json:"org_id"`
	IntegrationAppID              ID                             `json:"integration_app_id"`
	ProjectID                     ID                             `json:"project_id,omitempty"`
	IntegrationInstallID          ID                             `json:"integration_install_id,omitempty"`
	Provider                      string                         `json:"provider"`
	ConnectorKey                  string                         `json:"connector_key"`
	UnitKey                       string                         `json:"unit_key"`
	RuntimeKind                   string                         `json:"runtime_kind"`
	DesiredState                  IntegrationRuntimeDesiredState `json:"desired_state"`
	SpecRevision                  int                            `json:"spec_revision"`
	Configuration                 json.RawMessage                `json:"configuration"`
	Status                        IntegrationRuntimeStatus       `json:"status"`
	LeaseOwner                    string                         `json:"lease_owner,omitempty"`
	LeaseToken                    ID                             `json:"lease_token,omitempty"`
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
	IntegrationAppID ID
	UnitID           ID
	LeaseToken       ID
	LeaseGeneration  int64
}

type ClaimIntegrationRuntimeUnitsInput struct {
	LeaseOwner    string
	LeaseDuration time.Duration
	Capability    channelconnector.Capability
	Limit         int
}

type HeartbeatIntegrationRuntimeUnitInput struct {
	ID                ID
	LeaseToken        ID
	LeaseGeneration   int64
	LeaseDuration     time.Duration
	WriteCheckpoint   bool
	CheckpointVersion int
	Checkpoint        json.RawMessage
	Capabilities      []channelconnector.Capability
}

type ReleaseIntegrationRuntimeUnitInput struct {
	ID                ID
	LeaseToken        ID
	LeaseGeneration   int64
	WriteCheckpoint   bool
	CheckpointVersion int
	Checkpoint        json.RawMessage
	LastError         json.RawMessage
	Capabilities      []channelconnector.Capability
}
