package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestSubagentToolInputValidation(t *testing.T) {
	for _, test := range []struct {
		name     string
		validate func(json.RawMessage) error
		input    string
		wantErr  string
	}{
		{name: "spawn ok", validate: validateSpawnAgentInput, input: `{"agent":"fork","task":"do it","name":"worker-1"}`},
		{
			name:     "spawn missing task",
			validate: validateSpawnAgentInput,
			input:    `{"agent":"fork"}`,
			wantErr:  "task is required",
		},
		{
			name:     "spawn unknown field",
			validate: validateSpawnAgentInput,
			input:    `{"agent":"fork","task":"x","extra":1}`,
			wantErr:  "unknown field",
		},
		{name: "read ok", validate: validateReadAgentInput, input: `{"agent_id":"agt_abcdefghijklmnopqrstuvwxyz"}`},
		{
			name:     "read ok turns paged",
			validate: validateReadAgentInput,
			input:    `{"agent_id":"agt_abcdefghijklmnopqrstuvwxyz","before_turn_sequence":5,"limit":10}`,
		},
		{
			name:     "read ok turn events",
			validate: validateReadAgentInput,
			input: `{"agent_id":"agt_abcdefghijklmnopqrstuvwxyz","turn_id":"trn_abcdefghijklmnopqrstuvwxyz",` +
				`"before_sequence":9,"limit":200}`,
		},
		{name: "read missing id", validate: validateReadAgentInput, input: `{}`, wantErr: "agent_id is required"},
		{
			name:     "read before_sequence without turn",
			validate: validateReadAgentInput,
			input:    `{"agent_id":"agt_abcdefghijklmnopqrstuvwxyz","before_sequence":3}`,
			wantErr:  "requires turn_id",
		},
		{
			name:     "read before_turn_sequence with turn",
			validate: validateReadAgentInput,
			input: `{"agent_id":"agt_abcdefghijklmnopqrstuvwxyz","turn_id":"trn_abcdefghijklmnopqrstuvwxyz",` +
				`"before_turn_sequence":3}`,
			wantErr: "before_turn_sequence",
		},
		{
			name:     "read bad limit",
			validate: validateReadAgentInput,
			input:    `{"agent_id":"agt_abcdefghijklmnopqrstuvwxyz","limit":0}`,
			wantErr:  "limit",
		},
		{
			name:     "read turn limit too high",
			validate: validateReadAgentInput,
			input:    `{"agent_id":"agt_abcdefghijklmnopqrstuvwxyz","limit":200}`,
			wantErr:  "limit",
		},
		{
			name:     "read unknown field",
			validate: validateReadAgentInput,
			input:    `{"agent_id":"agt_abcdefghijklmnopqrstuvwxyz","after_sequence":1}`,
			wantErr:  "unknown field",
		},
		{
			name:     "send ok",
			validate: validateSendAgentMessageInput,
			input:    `{"agent_id":"agt_abcdefghijklmnopqrstuvwxyz","message":"hi"}`,
		},
		{
			name:     "send extra",
			validate: validateSendAgentMessageInput,
			input:    `{"agent_id":"agt_abcdefghijklmnopqrstuvwxyz","message":"hi","interaction_id":"x"}`,
			wantErr:  "unknown field",
		},
		{name: "stop ok", validate: validateStopAgentInput, input: `{"agent_id":"agt_abcdefghijklmnopqrstuvwxyz"}`},
		{name: "stop missing", validate: validateStopAgentInput, input: `{}`, wantErr: "agent_id is required"},
		{
			name:     "stop archive",
			validate: validateStopAgentInput,
			input:    `{"agent_id":"agt_abcdefghijklmnopqrstuvwxyz","archive":true}`,
		},
		{
			name:     "stop archive not boolean",
			validate: validateStopAgentInput,
			input:    `{"agent_id":"agt_abcdefghijklmnopqrstuvwxyz","archive":"yes"}`,
			wantErr:  "archive",
		},
		{name: "list ok", validate: validateListAgentsInput, input: `{}`},
		{name: "list extra", validate: validateListAgentsInput, input: `{"x":1}`, wantErr: "unknown field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.validate(json.RawMessage(test.input))
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want mention of %q", err, test.wantErr)
			}
		})
	}
}

type recordingPoolMachineManager struct {
	deleted [][]executionstore.MachineRecord
}

func (m *recordingPoolMachineManager) ProvisionMachine(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (m *recordingPoolMachineManager) StartLaunchProvisioning(
	context.Context,
	*slog.Logger,
	uuid.UUID,
	[]uuid.UUID,
) {
}

func (m *recordingPoolMachineManager) DeleteMachine(
	context.Context,
	executionstore.PoolMachineCleanupCandidate,
) error {
	return nil
}

func (m *recordingPoolMachineManager) DeleteMachines(
	_ context.Context,
	machines []executionstore.MachineRecord,
) (int, error) {
	m.deleted = append(m.deleted, machines)
	return len(machines), nil
}

func (m *recordingPoolMachineManager) WakeMachine(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

func TestStopAgentInBackgroundDeletesReleasedMachines(t *testing.T) {
	manager := &recordingPoolMachineManager{}
	machines := []executionstore.MachineRecord{{ID: uuid.New()}, {ID: uuid.New()}}
	err := stopAgentInBackground(context.Background(), backgroundToolContext{
		Executor:      Executor{MachinePoolManager: manager},
		CommandResult: machines,
	})
	if err != nil {
		t.Fatalf("stop agent background: %v", err)
	}
	if len(manager.deleted) != 1 || len(manager.deleted[0]) != 2 {
		t.Fatalf("deleted batches = %+v, want one batch of two machines", manager.deleted)
	}
	if err := stopAgentInBackground(context.Background(), backgroundToolContext{
		Executor:      Executor{MachinePoolManager: manager},
		CommandResult: []executionstore.MachineRecord(nil),
	}); err != nil {
		t.Fatalf("stop agent background without machines: %v", err)
	}
	if len(manager.deleted) != 1 {
		t.Fatalf("a stop that released no machines must not call delete, got %d batches", len(manager.deleted))
	}
	err = stopAgentInBackground(context.Background(), backgroundToolContext{
		Executor: Executor{MachinePoolManager: manager},
	})
	if err == nil || !strings.Contains(err.Error(), "command result") {
		t.Fatalf("stop agent background with a missing command result: err = %v, want a wiring error", err)
	}

}
