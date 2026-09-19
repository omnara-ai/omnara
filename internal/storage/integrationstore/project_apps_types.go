package integrationstore

import (
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

// ProjectAppSettings is reusable setup. A launcher supplies the concrete event
// scope when deriving a config; it never mutates the profile's base config.
type ProjectAppSettings struct {
	Resource agentconfig.AgentConfigAppResourceSource `json:"resource"`
	Launcher *AppLauncher                             `json:"launcher,omitempty"`
}

type AppLauncher struct {
	Trigger   string          `json:"trigger"`
	ScopeKind string          `json:"scope_kind"`
	ScopeRef  string          `json:"scope_ref"`
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
	ID           uuid.UUID
	ProjectID    uuid.UUID
	Name         string
	DefinitionID string
	Settings     ProjectAppSettings
	Enabled      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type SaveProjectAppInput struct {
	OrgID        uuid.UUID
	ProjectID    uuid.UUID
	Name         string
	DefinitionID string
	Settings     ProjectAppSettings
	Enabled      bool
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
