package events

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	testEventID = uuid.MustParse("018f0000-0000-7000-8000-000000000001")
	testAgentID = uuid.MustParse("018f0000-0000-7000-8000-000000000002")
)

func TestNewCreatesValidEvent(t *testing.T) {
	at := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)

	event, err := New(NewInput{
		ID:             testEventID,
		AgentID:        testAgentID,
		Sequence:       1,
		Kind:           KindAgentInput,
		At:             at,
		IdempotencyKey: "idem_1",
	})
	if err != nil {
		t.Fatalf("new event: %v", err)
	}

	if event.ID != testEventID || event.AgentID != testAgentID {
		t.Fatalf("unexpected ids: %+v", event)
	}
	if event.Sequence != 1 {
		t.Fatalf("unexpected sequence: %d", event.Sequence)
	}
	if event.Kind != KindAgentInput {
		t.Fatalf("unexpected kind: %s", event.Kind)
	}
	if !event.At.Equal(at) {
		t.Fatalf("unexpected time: %s", event.At)
	}
}

func TestNewRequiresTimestamp(t *testing.T) {
	_, err := New(NewInput{
		ID:       testEventID,
		AgentID:  testAgentID,
		Sequence: 1,
		Kind:     KindAgentInput,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNewRejectsInvalidEvents(t *testing.T) {
	tests := []struct {
		name  string
		input NewInput
	}{
		{name: "missing id", input: NewInput{AgentID: testAgentID, Sequence: 1, Kind: KindAgentInput}},
		{name: "missing agent", input: NewInput{ID: testEventID, Sequence: 1, Kind: KindAgentInput}},
		{
			name:  "bad sequence",
			input: NewInput{ID: testEventID, AgentID: testAgentID, Sequence: 0, Kind: KindAgentInput},
		},
		{
			name:  "unknown kind",
			input: NewInput{ID: testEventID, AgentID: testAgentID, Sequence: 1, Kind: "unknown"},
		},
		{
			name:  "non-frontier kind",
			input: NewInput{ID: testEventID, AgentID: testAgentID, Sequence: 1, Kind: Kind("process")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.input); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
