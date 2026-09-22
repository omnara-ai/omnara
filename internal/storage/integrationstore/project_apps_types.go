package integrationstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

// ProjectAppSettings is reusable setup. A launcher supplies the concrete event
// scope when deriving a config; it never mutates the profile's base config.
type ProjectAppSettings struct {
	Launcher *AppLauncher `json:"launcher,omitempty"`
}

type AppLauncher struct {
	Trigger   string          `json:"trigger"`
	ScopeKind string          `json:"scope_kind,omitempty"`
	ScopeRef  string          `json:"scope_ref,omitempty"`
	Slots     []AppLaunchSlot `json:"slots"`
}

// AppLaunchSlot keys are stable across edits and frozen into each received event's plan.
// UUIDs here are storage identities; HTTP representations use public IDs.
type AppLaunchSlot struct {
	Key            string     `json:"key"`
	AgentProfileID *uuid.UUID `json:"agent_profile_id,omitempty"`
	AgentID        *uuid.UUID `json:"agent_id,omitempty"`
}

type ProjectAppRecord struct {
	ID                       uuid.UUID
	OrgID                    uuid.UUID
	InstalledByUserID        uuid.UUID
	Provider                 string // Derived transport metadata; never persisted as app classification.
	State                    ProjectAppState
	ProviderTenantID         string
	ProviderAccountRef       string
	ProviderAgentDisplayName string
	CredentialSecretID       uuid.UUID
	ProviderConfig           json.RawMessage
	ProviderIdentity         json.RawMessage
	ProviderMetadata         json.RawMessage
	LastOAuthFlowID          uuid.UUID
	SetupRevision            int64
	DeletedAt                *time.Time
	ProjectID                uuid.UUID
	Name                     string
	AppType                  appdefinition.Type
	Settings                 ProjectAppSettings
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

type SaveProjectAppInput struct {
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	Name      string
	AppType   appdefinition.Type
	Settings  ProjectAppSettings
}

type ListProjectAppsInput struct {
	ProjectID   uuid.UUID
	NamePattern string
	After       listing.KeysetCursor
	Limit       int
}

type ListProjectAppsResult struct {
	Apps    []ProjectAppRecord
	HasMore bool
	Next    listing.KeysetCursor
}
