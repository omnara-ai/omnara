package modelcontext

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type BuildInput struct {
	ProjectID           uuid.UUID
	AgentID             uuid.UUID
	TurnID              uuid.UUID
	OpeningInputIDs     []uuid.UUID
	Now                 time.Time
	AgentConfigSnapshot *executionstore.AgentConfigSnapshotRecord
	CheckpointOverride  *CheckpointRef
	MediaProjector      MediaProjector
}

type Bundle struct {
	ProjectID             uuid.UUID                  `json:"-"`
	AgentID               uuid.UUID                  `json:"-"`
	TurnID                uuid.UUID                  `json:"-"`
	OpeningInputIDs       []uuid.UUID                `json:"-"`
	InputEventSequence    int64                      `json:"-"`
	SystemPrompt          string                     `json:"system_prompt"`
	Messages              []Message                  `json:"messages"`
	ToolSpecs             []ToolSpec                 `json:"tool_specs"`
	ToolResults           []ToolResultRef            `json:"tool_results"`
	AvailableMachinePools []MachinePoolRef           `json:"machine_pools,omitempty"`
	InteractionRouting    *InteractionRoutingContext `json:"interaction_routing,omitempty"`
	ContextCheckpoint     *CheckpointRef             `json:"context_checkpoint,omitempty"`
	ResolvedMedia         map[string]ResolvedMedia   `json:"resolved_media,omitempty"`
	RenderedMedia         []RenderedMedia            `json:"-"`
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
	RouteParsed    bool
	TokenEstimate  int
}

const (
	MediaRepresentationInline     = "inline"
	MediaRepresentationInlineText = "inline_text"
)

const (
	InputModalityText  = "text"
	InputModalityImage = "image"
	InputModalityFile  = "file"
)

func (m RenderedMedia) InputModality() string {
	switch m.Media.Kind {
	case AttachmentKindImage:
		return InputModalityImage
	case AttachmentKindDocument:
		if m.RouteParsed || m.Representation == MediaRepresentationInlineText {
			return ""
		}
		return InputModalityFile
	}
	return ""
}

type Message struct {
	ID                   string                               `json:"-"`
	AgentInputID         string                               `json:"-"`
	ModelCallContextID   string                               `json:"-"`
	Role                 modelprotocol.MessageRole            `json:"role"`
	Sequence             int64                                `json:"sequence,omitempty"`
	Content              json.RawMessage                      `json:"content"`
	ProviderReplay       json.RawMessage                      `json:"-"`
	ProviderReplaySource modelenvelope.ProviderReplayIdentity `json:"-"`
	StopReason           modelenvelope.StopReason             `json:"-"`
}

type ToolSpec struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	InputSchema json.RawMessage          `json:"input_schema"`
	Deferred    bool                     `json:"deferred,omitempty"`
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

// InteractionRoutingContext is present only for configs with an enabled interaction handler. A nil destination
// means dashboard-only, not a missing or guessed channel.
type InteractionRoutingContext struct {
	Destination *InteractionDestinationRef `json:"destination"`
}
type InteractionDestinationRef struct {
	Handler string          `json:"handler"`
	Args    json.RawMessage `json:"args"`
}

type MachinePoolRef struct {
	MachinePoolName string `json:"machine_pool_name"`
	Description     string `json:"description,omitempty"`
}

type CheckpointRef struct {
	ID                             string `json:"-"`
	SummarizedThroughEventSequence int64  `json:"summarized_through_event_sequence"`
	Summary                        string `json:"summary"`
	EndsWithOutputLimit            bool   `json:"-"`
}
