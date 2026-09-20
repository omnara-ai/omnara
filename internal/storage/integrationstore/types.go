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

type ProjectAppState string

const (
	ProjectAppStateActive       ProjectAppState = "active"
	ProjectAppStateDisconnected ProjectAppState = "disconnected"
)

// ConfigureProjectAppInput contains setup verified against the provider before
// entering storage. Setup and secret revisions fence that verification.
type ConfigureProjectAppInput struct {
	OrgID                    uuid.UUID
	ProjectID                uuid.UUID
	AppID                    uuid.UUID
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
	RoutingRole      TargetRoutingRole `json:"routing_role"`
	AppID            uuid.UUID         `json:"app_id,omitempty"`
	SelectionSlot    string            `json:"selection_slot,omitempty"`
	DeletedAt        *time.Time        `json:"deleted_at,omitempty"`
	ID               uuid.UUID         `json:"id"`
	OrgID            uuid.UUID         `json:"org_id"`
	ProjectID        uuid.UUID         `json:"project_id"`
	AgentID          uuid.UUID         `json:"agent_id"`
	TargetRef        string            `json:"target_ref"`
	ProviderRef      string            `json:"provider_ref"`
	ProviderRefKind  string            `json:"provider_ref_kind"`
	DisplayName      string            `json:"display_name"`
	ProviderMetadata json.RawMessage   `json:"provider_metadata"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	Created          bool              `json:"-"`
}
