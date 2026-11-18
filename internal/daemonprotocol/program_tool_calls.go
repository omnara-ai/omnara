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
	// ProgramToolCallMaxKeyBytes bounds a call's idempotency key.
	ProgramToolCallMaxKeyBytes = 128
)

// ValidProgramToolCallKey reports whether key can identify a call: 1 to 128 printable ASCII
// characters with no spaces.
func ValidProgramToolCallKey(key string) bool {
	if key == "" || len(key) > ProgramToolCallMaxKeyBytes {
		return false
	}
	for i := range len(key) {
		if key[i] < '!' || key[i] > '~' {
			return false
		}
	}
	return true
}

type ProgramToolCallRequest struct {
	// Key identifies the call for its agent. Offering it again returns the same call.
	Key   string          `json:"key"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// KeepOnDisconnect leaves the call open when its command stops waiting, so the command can
	// offer the key again to pick it up. Set when the command chose the key.
	KeepOnDisconnect bool `json:"keep_on_disconnect,omitempty"`
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
