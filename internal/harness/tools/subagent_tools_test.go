package tools

import (
	"encoding/json"
	"strings"
	"testing"
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
		{name: "read ok", validate: validateReadAgentInput, input: `{"agent_ref":"agtr-abcdefgh"}`},
		{
			name:     "read ok ranged",
			validate: validateReadAgentInput,
			input:    `{"agent_ref":"agtr-abcdefgh","after_sequence":5,"limit":10}`,
		},
		{name: "read missing ref", validate: validateReadAgentInput, input: `{}`, wantErr: "agent_ref is required"},
		{
			name:     "read bad limit",
			validate: validateReadAgentInput,
			input:    `{"agent_ref":"agtr-abcdefgh","limit":0}`,
			wantErr:  "limit",
		},
		{name: "send ok", validate: validateSendAgentMessageInput, input: `{"agent_ref":"agtr-abcdefgh","message":"hi"}`},
		{
			name:     "send bad interaction",
			validate: validateSendAgentMessageInput,
			input:    `{"agent_ref":"agtr-abcdefgh","message":"hi","interaction_id":"nope"}`,
			wantErr:  "interaction_id",
		},
		{name: "stop ok", validate: validateStopAgentInput, input: `{"agent_ref":"agt_abcdefghijklmnopqrstuvwxyz"}`},
		{name: "stop missing", validate: validateStopAgentInput, input: `{}`, wantErr: "agent_ref is required"},
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
