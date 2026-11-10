package daemonprotocol

import "encoding/json"

// Program tool calls let a run_command shell call the agent's other tools
// through `omnarad call`. The daemon offers each call to the backend on its
// websocket and the backend reports where the call stands, the reverse of
// process offers and reports.
const (
	ProgramToolCallStateAwaitingApproval = "awaiting_approval"
	ProgramToolCallStateCompleted        = "completed"

	ProgramToolCallOutcomeSucceeded = "succeeded"
	ProgramToolCallOutcomeFailed    = "failed"

	ProgramToolCallContentText           = "text"
	ProgramToolCallContentStructuredData = "structured_data"

	// ProgramToolCallMaxInputBytes and ProgramToolCallMaxResultBytes keep an
	// offer and a report inside one websocket message.
	// TODO: decide how calls with larger inputs or results should travel
	// (paging, artifacts, or a bulk channel) instead of failing them.
	ProgramToolCallMaxInputBytes  = MaxMessageBytes - 16*1024
	ProgramToolCallMaxResultBytes = MaxMessageBytes - 16*1024
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
	Content    []ProgramToolCallContent `json:"content,omitempty"`
}

type ProgramToolCallContent struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Value      json.RawMessage `json:"value,omitempty"`
	ArtifactID string          `json:"artifact_id,omitempty"`
}

// ToolCallOffer is a call a process asks the backend to make. The daemon
// offers it again, with the same key, whenever its websocket reconnects before
// the call's completed report arrived.
type ToolCallOffer struct {
	ProcessID string `json:"process_id"`
	ProgramToolCallRequest
}

// ToolCallReport is the state of an offered call. A report with an ack status
// rejects the offer instead: the call was not made, and a transient_error may
// be offered again.
type ToolCallReport struct {
	ProcessID string `json:"process_id"`
	Key       string `json:"key"`
	ProgramToolCallResult
	AckStatus AckStatus `json:"ack_status,omitempty"`
	ErrorCode string    `json:"error_code,omitempty"`
	Error     string    `json:"error,omitempty"`
}
