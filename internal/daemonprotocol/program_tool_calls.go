package daemonprotocol

import "encoding/json"

// Program tool calls let a run_command shell call the agent's other tools
// through `omnarad call`, relayed by the daemon to the backend.
const (
	ProgramToolCallStatePending          = "pending"
	ProgramToolCallStateAwaitingApproval = "awaiting_approval"
	ProgramToolCallStateCompleted        = "completed"

	ProgramToolCallOutcomeSucceeded = "succeeded"
	ProgramToolCallOutcomeFailed    = "failed"
	ProgramToolCallOutcomeDenied    = "denied"
	ProgramToolCallOutcomeCanceled  = "canceled"

	ProgramToolCallContentText           = "text"
	ProgramToolCallContentStructuredData = "structured_data"
	ProgramToolCallContentMediaRef       = "media_ref"

	// ProgramToolCallMaxWaitMs bounds how long one poll may be held open.
	ProgramToolCallMaxWaitMs = 25000
)

type ProgramToolCallRequest struct {
	// Key is a client idempotency key, stable across retries of one call.
	Key   string          `json:"key"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type ProgramToolCallResult struct {
	// ToolCallID is opaque and empty for calls answered inline, such as tool_search.
	ToolCallID string                   `json:"tool_call_id,omitempty"`
	State      string                   `json:"state"`
	Outcome    string                   `json:"outcome,omitempty"`
	Structured json.RawMessage          `json:"structured,omitempty"`
	Content    []ProgramToolCallContent `json:"content,omitempty"`
}

type ProgramToolCallContent struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Value      json.RawMessage `json:"value,omitempty"`
	ArtifactID string          `json:"artifact_id,omitempty"`
}
