package events

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Kind string

const (
	KindModelOutput       Kind = "model_output"
	KindToolResult        Kind = "tool_result"
	KindAgentInput        Kind = "agent_input"
	KindContextCheckpoint Kind = "context_checkpoint"
)

type Event struct {
	ID             uuid.UUID `json:"id"`
	AgentID        uuid.UUID `json:"agent_id"`
	Sequence       int64     `json:"sequence"`
	Kind           Kind      `json:"event_kind"`
	At             time.Time `json:"at"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
}

func New(input NewInput) (Event, error) {
	event := Event(input)
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (e Event) Validate() error {
	if e.ID == uuid.Nil {
		return errors.New("event id is required")
	}
	if e.AgentID == uuid.Nil {
		return errors.New("agent id is required")
	}
	if e.Sequence < 1 {
		return fmt.Errorf("event sequence must be positive: %d", e.Sequence)
	}
	if !KnownKind(e.Kind) {
		return fmt.Errorf("unknown event kind: %s", e.Kind)
	}
	if e.At.IsZero() {
		return errors.New("event timestamp is required")
	}
	return nil
}

type NewInput struct {
	ID             uuid.UUID
	AgentID        uuid.UUID
	Sequence       int64
	Kind           Kind
	At             time.Time
	IdempotencyKey string
}

func KnownKind(k Kind) bool {
	switch k {
	case KindModelOutput,
		KindToolResult,
		KindAgentInput,
		KindContextCheckpoint:
		return true
	default:
		return false
	}
}
