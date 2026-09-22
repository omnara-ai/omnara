package executionstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestBindToolCallsUsesProviderEnvelopeAsCanonicalSource(t *testing.T) {
	id := uuid.New()
	envelope := modelenvelope.ResponseEnvelope{
		Normalized: modelenvelope.ResponseNormalized{
			Content: []modelenvelope.ResponsePart{
				{Type: modelenvelope.ResponsePartTypeText, Text: "before"},
				{
					Type:           modelenvelope.ResponsePartTypeToolCall,
					ProviderCallID: "call_1",
					ToolName:       "lookup_customer",
					ToolInput:      json.RawMessage(`{"email":"ada@example.com"}`),
				},
			},
		},
	}
	calls, err := bindToolCalls(envelope, []ToolCallBindingInput{{
		ID:             id,
		ProviderCallID: "call_1",
		Type:           toolcatalog.ToolTypeCustom,
	}})
	if err != nil {
		t.Fatalf("bind tool calls: %v", err)
	}
	if len(calls) != 1 ||
		calls[0].ID != id ||
		calls[0].ProviderCallID != "call_1" ||
		calls[0].Name != "lookup_customer" ||
		string(calls[0].Input) != `{"email":"ada@example.com"}` ||
		calls[0].Type != toolcatalog.ToolTypeCustom {
		t.Fatalf("bound tool call = %+v", calls)
	}
}

func TestBindToolCallsRequiresExactProviderCallCoverage(t *testing.T) {
	envelope := modelenvelope.ResponseEnvelope{
		Normalized: modelenvelope.ResponseNormalized{
			Content: []modelenvelope.ResponsePart{{
				Type:           modelenvelope.ResponsePartTypeToolCall,
				ProviderCallID: "call_1",
				ToolName:       "run_command",
				ToolInput:      json.RawMessage(`{"command":"true"}`),
			}},
		},
	}
	for _, bindings := range [][]ToolCallBindingInput{
		{{
			ProviderCallID: "call_other",
			Type:           toolcatalog.ToolTypeBuiltIn,
		}},
		{
			{
				ProviderCallID: "call_1",
				Type:           toolcatalog.ToolTypeBuiltIn,
			},
			{
				ProviderCallID: "call_extra",
				Type:           toolcatalog.ToolTypeBuiltIn,
			},
		},
	} {
		if _, err := bindToolCalls(envelope, bindings); err == nil {
			t.Fatalf("bindings %+v did not fail", bindings)
		}
	}
}

func TestBindToolCallsRejectsInvalidToolInput(t *testing.T) {
	for _, input := range []json.RawMessage{
		json.RawMessage(`{"command":`),
		json.RawMessage(`null`),
		json.RawMessage(`[]`),
		json.RawMessage(`"command"`),
	} {
		envelope := modelenvelope.ResponseEnvelope{
			Normalized: modelenvelope.ResponseNormalized{
				Content: []modelenvelope.ResponsePart{{
					Type:           modelenvelope.ResponsePartTypeToolCall,
					ProviderCallID: "call_1",
					ToolName:       "run_command",
					ToolInput:      input,
				}},
			},
		}
		_, err := bindToolCalls(envelope, []ToolCallBindingInput{{
			ID:             uuid.New(),
			ProviderCallID: "call_1",
			Type:           toolcatalog.ToolTypeBuiltIn,
		}})
		if err == nil {
			t.Fatalf("bindToolCalls accepted input %s", input)
		}
	}
}

func TestStructuredToolResultKeepsEmptyObject(t *testing.T) {
	parts, err := ToolResultContentParts(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("convert empty object tool result: %v", err)
	}
	if string(parts) != `[{"type":"structured_data","value":{}}]` {
		t.Fatalf("empty object tool result = %s", parts)
	}
}

func TestCommandTerminalToolResultUsesCanonicalProcessID(t *testing.T) {
	processID := uuid.MustParse("019c0000-0000-7000-8000-000000000001")
	publicProcessID := publicResourceID(publicid.KindProcess, processID)
	tests := []struct {
		name     string
		input    json.RawMessage
		want     string
		wantFail bool
	}{
		{
			name:  "object",
			input: json.RawMessage(`{"done":true,"process_id":"prc_should_not_escape"}`),
			want:  `{"done":true,"process_id":"` + publicProcessID + `"}`,
		},
		{
			name:  "empty object",
			input: json.RawMessage(`{}`),
			want:  `{"process_id":"` + publicProcessID + `"}`,
		},
		{name: "array", input: json.RawMessage(`["done"]`), wantFail: true},
		{name: "string", input: json.RawMessage(`"done"`), wantFail: true},
		{name: "invalid", input: json.RawMessage(`{"done":true`), wantFail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := commandTerminalToolResult(processID, tt.input)
			if tt.wantFail {
				if err == nil {
					t.Fatalf("commandTerminalToolResult succeeded, want failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("commandTerminalToolResult: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("result = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestStartedProcessToolResultKeepsProcessFactsAuthoritative(t *testing.T) {
	process := ProcessRecord{
		ID:      uuid.New(),
		State:   ProcessStateRunning,
		Command: "go test ./...",
	}
	result, err := startedProcessToolResult(
		process,
		json.RawMessage(`{"state":"exited","command":"other","next_action":"stop","output":"ready"}`),
	)
	if err != nil {
		t.Fatalf("startedProcessToolResult: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(result, &body); err != nil {
		t.Fatalf("decode started process result: %v", err)
	}
	if body["state"] != string(ProcessStateRunning) ||
		body["command"] != process.Command ||
		body["next_action"] == "stop" ||
		body["output"] != "ready" {
		t.Fatalf("started process result = %s", result)
	}
}

func TestUploadMemoryToolResult(t *testing.T) {
	const path = "/memory/team/notes.md"
	digest := "sha256:" + strings.Repeat("a", 64)
	metadata := `{"path":"` + path + `","digest":"` + digest + `"}`
	for _, test := range []struct {
		name      string
		output    string
		truncated bool
		wantOK    bool
	}{
		{name: "metadata", output: metadata + "\n", wantOK: true},
		{name: "missing output"},
		{name: "invalid JSON", output: "upload failed"},
		{name: "missing digest", output: `{"path":"` + path + `"}`},
		{name: "invalid digest", output: `{"path":"` + path + `","digest":"invalid"}`},
		{name: "wrong path", output: strings.Replace(metadata, "notes.md", "other.md", 1)},
		{name: "truncated output", output: metadata, truncated: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed := map[string]any{
				"process_id": "prc_test", "output": test.output, "truncated": test.truncated,
				"cursor": 0, "next_cursor": len(test.output), "state": "exited", "done": true,
			}
			result, err := json.Marshal(observed)
			if err != nil {
				t.Fatal(err)
			}
			outcome, got, err := uploadMemoryToolResult(json.RawMessage(`{"path":"`+path+`"}`), result)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantOK {
				if outcome != ToolResultOutcomeSucceeded || string(got) != metadata {
					t.Fatalf("result = %s %s, want success %s", outcome, got, metadata)
				}
				return
			}
			observed["error"] = "upload completed without valid file metadata"
			want, err := json.Marshal(observed)
			if err != nil {
				t.Fatal(err)
			}
			if outcome != ToolResultOutcomeFailed || string(got) != string(want) {
				t.Fatalf("result = %s %s, want failure %s", outcome, got, want)
			}
		})
	}
}

func TestIsUploadArtifactToolCall(t *testing.T) {
	tests := []struct {
		name string
		call ToolCallRecord
		want bool
	}{
		{
			name: "removed legacy tool",
			call: ToolCallRecord{Type: toolcatalog.ToolTypeBuiltIn, Name: "upload_artifact"},
		},
		{
			name: "vfs artifact",
			call: ToolCallRecord{
				Type:  toolcatalog.ToolTypeBuiltIn,
				Name:  toolcatalog.ToolNameUploadFile,
				Input: json.RawMessage(`{"path":"/artifacts","source":"report.pdf"}`),
			},
			want: true,
		},
		{
			name: "future vfs resource",
			call: ToolCallRecord{
				Type:  toolcatalog.ToolTypeBuiltIn,
				Name:  toolcatalog.ToolNameUploadFile,
				Input: json.RawMessage(`{"path":"/memory/notes.md","source":"notes.md"}`),
			},
		},
		{
			name: "custom collision",
			call: ToolCallRecord{
				Type:  toolcatalog.ToolTypeCustom,
				Name:  toolcatalog.ToolNameUploadFile,
				Input: json.RawMessage(`{"path":"/artifacts"}`),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isUploadArtifactToolCall(test.call); got != test.want {
				t.Fatalf("isUploadArtifactToolCall() = %t, want %t", got, test.want)
			}
		})
	}
}
