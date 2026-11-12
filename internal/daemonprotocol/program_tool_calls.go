package daemonprotocol

import "encoding/json"

const (
	ProgramToolCallStateAwaitingApproval = "awaiting_approval"
	ProgramToolCallStateCompleted        = "completed"

	ProgramToolCallOutcomeSucceeded = "succeeded"
	ProgramToolCallOutcomeFailed    = "failed"

	ProgramToolCallContentText           = "text"
	ProgramToolCallContentStructuredData = "structured_data"

	ProgramToolCallMaxInputBytes  = MaxMessageBytes - 16*1024
	ProgramToolCallMaxResultBytes = MaxMessageBytes - 16*1024
)

type ProgramToolCallRequest struct {
	Key   string          `json:"key"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type ProgramToolCallResult struct {
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

type ToolCallOffer struct {
	ProcessID string `json:"process_id"`
	ProgramToolCallRequest
}

type ToolCallReport struct {
	ProcessID string `json:"process_id"`
	Key       string `json:"key"`
	ProgramToolCallResult
	AckStatus AckStatus `json:"ack_status,omitempty"`
	ErrorCode string    `json:"error_code,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type ToolCallCancel struct {
	ProcessID string `json:"process_id"`
	Key       string `json:"key"`
}
