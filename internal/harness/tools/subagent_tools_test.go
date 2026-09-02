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
			wantErr:  "unsupported field",
		},
		{name: "wait ok empty", validate: validateWaitAgentsInput, input: `{}`},
		{
			name:     "wait ok explicit",
			validate: validateWaitAgentsInput,
			input:    `{"agents":["a"],"mode":"any","timeout_seconds":30}`,
		},
		{name: "wait bad mode", validate: validateWaitAgentsInput, input: `{"mode":"some"}`, wantErr: "mode must be"},
		{
			name:     "wait bad timeout",
			validate: validateWaitAgentsInput,
			input:    `{"timeout_seconds":0}`,
			wantErr:  "timeout_seconds",
		},
		{name: "send ok", validate: validateSendAgentMessageInput, input: `{"agent":"a","message":"hi"}`},
		{
			name:     "send bad interaction",
			validate: validateSendAgentMessageInput,
			input:    `{"agent":"a","message":"hi","interaction_id":"nope"}`,
			wantErr:  "interaction_id",
		},
		{name: "stop ok", validate: validateStopAgentInput, input: `{"agent":"agt_abcdefghijklmnopqrstuvwxyz"}`},
		{name: "stop missing", validate: validateStopAgentInput, input: `{}`, wantErr: "agent is required"},
		{name: "list ok", validate: validateListAgentsInput, input: `{}`},
		{name: "list extra", validate: validateListAgentsInput, input: `{"x":1}`, wantErr: "unsupported field"},
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
