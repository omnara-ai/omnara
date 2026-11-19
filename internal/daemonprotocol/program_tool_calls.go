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
	ProgramToolCallMaxKeyBytes    = 128
	// Names are echoed in errors, so an unbounded one could push a report past the message limit.
	ProgramToolCallMaxNameBytes  = 256
	ProgramToolCallMaxErrorBytes = 4096
)

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
	// KeepOnDisconnect leaves the call open when its command stops waiting or ends, so a command of
	// the same agent can offer the key again to pick it up. Set when the command chose the key.
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
	// OfferID identifies this offer of the key. A key can be offered again, by a rerun or with a
	// different input, so reports and cancels carry the offer they belong to.
	OfferID string `json:"offer_id,omitempty"`
	ProgramToolCallRequest
}

type ToolCallReport struct {
	ProcessID string `json:"process_id"`
	Key       string `json:"key"`
	OfferID   string `json:"offer_id,omitempty"`
	ProgramToolCallResult
	AckStatus AckStatus `json:"ack_status,omitempty"`
	ErrorCode string    `json:"error_code,omitempty"`
	Error     string    `json:"error,omitempty"`
	// CancelRecorded acknowledges a cancel: the server has ended the offer's call, or found nothing
	// left to end. Until it arrives the daemon sends the cancel again on its next connection.
	CancelRecorded bool `json:"cancel_recorded,omitempty"`
}

type ToolCallCancel struct {
	ProcessID string `json:"process_id"`
	Key       string `json:"key"`
	OfferID   string `json:"offer_id,omitempty"`
}
