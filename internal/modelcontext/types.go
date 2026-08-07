package modelcontext

import (
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type BuildInput struct {
	ProjectID           storage.ID
	AgentID             storage.ID
	TurnID              storage.ID
	OpeningInputIDs     []storage.ID
	Now                 time.Time
	AgentConfigSnapshot *executionstore.AgentConfigSnapshotRecord
	CheckpointOverride  *CheckpointRef
	MediaProjector      MediaProjector
	ModelWindow         ModelWindow
}

type Bundle struct {
	ProjectID             storage.ID               `json:"-"`
	AgentID               storage.ID               `json:"-"`
	TurnID                storage.ID               `json:"-"`
	OpeningInputIDs       []storage.ID             `json:"-"`
	InputEventSequence    int64                    `json:"-"`
	SystemPrompt          string                   `json:"system_prompt"`
	Messages              []Message                `json:"messages"`
	ToolSpecs             []ToolSpec               `json:"tool_specs"`
	ToolResults           []ToolResultRef          `json:"tool_results"`
	AvailableMachinePools []MachinePoolRef         `json:"machine_pools,omitempty"`
	ActiveProcesses       []ActiveProcessRef       `json:"active_processes,omitempty"`
	AttachedMachines      []AttachedMachineRef     `json:"attached_machines,omitempty"`
	IntegrationTargets    []IntegrationTargetRef   `json:"integration_targets,omitempty"`
	ContextCheckpoint     *CheckpointRef           `json:"context_checkpoint,omitempty"`
	ResolvedMedia         map[string]ResolvedMedia `json:"resolved_media,omitempty"`
	RenderedMedia         []RenderedMedia          `json:"-"`
}

type MediaProjector interface {
	ProjectRenderedMedia(Bundle) []RenderedMedia
}

type mediaOccurrenceOwner uint8

const (
	mediaOccurrenceOwnerUnknown mediaOccurrenceOwner = iota
	mediaOccurrenceOwnerMessage
	mediaOccurrenceOwnerToolResult
)

type MediaOccurrenceRef struct {
	ownerKind  mediaOccurrenceOwner
	ownerIndex int
	partIndex  int
}

type ResolvedMediaOccurrence struct {
	Ref         MediaOccurrenceRef
	Media       ResolvedMedia
	MessageRole modelprotocol.MessageRole
	Opening     bool
}

func (o ResolvedMediaOccurrence) IsToolResult() bool {
	return o.Ref.ownerKind == mediaOccurrenceOwnerToolResult
}

type RenderedMedia struct {
	Occurrence     MediaOccurrenceRef
	Media          ResolvedMedia
	Representation string
	TokenEstimate  int
}

const (
	MediaRepresentationInline     = "inline"
	MediaRepresentationInlineText = "inline_text"
)

type Message struct {
	ID                   string                               `json:"-"`
	AgentInputID         string                               `json:"-"`
	ModelCallContextID   string                               `json:"-"`
	Role                 modelprotocol.MessageRole            `json:"role"`
	Sequence             int64                                `json:"sequence,omitempty"`
	Content              json.RawMessage                      `json:"content"`
	ProviderReplay       json.RawMessage                      `json:"-"`
	ProviderReplaySource modelenvelope.ProviderReplayIdentity `json:"-"`
}

type ToolSpec struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	InputSchema json.RawMessage          `json:"input_schema"`
	Type        string                   `json:"-"`
	Permission  toolpermission.Selection `json:"-"`
}

type ToolResultRef struct {
	ToolCallID          string                           `json:"-"`
	DurableID           string                           `json:"-"`
	EventID             string                           `json:"-"`
	SourceEventSequence int64                            `json:"-"`
	ResultEventSequence int64                            `json:"-"`
	ModelCallContextID  string                           `json:"-"`
	ProviderCallID      string                           `json:"-"`
	Name                string                           `json:"name"`
	Input               json.RawMessage                  `json:"input"`
	Outcome             executionstore.ToolResultOutcome `json:"-"`
	ContentParts        json.RawMessage                  `json:"content_parts"`
}

type ActiveProcessRef struct {
	ProcessID     string `json:"process_id"`
	State         string `json:"state"`
	CommandLabel  string `json:"command_label,omitempty"`
	Command       string `json:"command,omitempty"`
	ShellSelector string `json:"shell_selector,omitempty"`
	Cwd           string `json:"cwd,omitempty"`
}

type AttachedMachineRef struct {
	MachineRef  string `json:"machine_ref"`
	Description string `json:"description,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
}

type IntegrationTargetRef struct {
	TargetRef       string `json:"target_ref"`
	DurableID       string `json:"-"`
	Provider        string `json:"provider"`
	ProviderRefKind string `json:"provider_ref_kind"`
	Label           string `json:"label"`
	InstallState    string `json:"install_state,omitempty"`
	IsCurrent       bool   `json:"is_current,omitempty"`
}

type MachinePoolRef struct {
	MachinePoolName string `json:"machine_pool_name"`
	Description     string `json:"description,omitempty"`
}

type CheckpointRef struct {
	ID                             string `json:"-"`
	SummarizedThroughEventSequence int64  `json:"summarized_through_event_sequence"`
	Summary                        string `json:"summary"`
}
