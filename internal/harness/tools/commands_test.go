package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestStructuredQuestionAnsweredResultUsesPublicInteractionID(t *testing.T) {
	t.Parallel()

	interactionID := testID("019b18be-0000-7000-8000-000000000007")
	publicInteractionID, err := publicid.Encode(publicid.KindAgentInteraction, interactionID)
	if err != nil {
		t.Fatalf("encode interaction id: %v", err)
	}
	answer := json.RawMessage(`"ship it"`)
	result, err := marshalJSON(
		map[string]any{"answers": []map[string]any{{"interaction_id": publicInteractionID, "answer": answer}}},
	)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	content, err := structuredToolResultContent(result)
	if err != nil {
		t.Fatalf("build structured tool result: %v", err)
	}
	contentParts, err := content.contentParts()
	if err != nil {
		t.Fatalf("marshal content parts: %v", err)
	}
	body := string(contentParts)
	if !strings.Contains(body, publicInteractionID) {
		t.Fatalf("structured question result missing public interaction id: %s", body)
	}
	if strings.Contains(body, interactionID.String()) {
		t.Fatalf("structured question result leaked raw interaction UUID: %s", body)
	}
}

func testID(raw string) storage.ID {
	id, err := storage.ParseID(raw)
	if err != nil {
		panic(err)
	}
	return id
}

func TestRunCommandRequestRejectsInvalidOptionalScalars(t *testing.T) {
	t.Parallel()

	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"command":"pwd","cwd":null}`),
		json.RawMessage(`{"command":"pwd","machine_ref":null}`),
		json.RawMessage(`{"command":"pwd","wait_ms":null}`),
		json.RawMessage(`{"command":"pwd","io_mode":null}`),
		json.RawMessage(`{"command":"pwd","tty":true}`),
	} {
		if _, err := resolveRunCommandRequest(raw); err == nil {
			t.Fatalf("expected null scalar rejection for %s", raw)
		}
	}
}

func TestToolRequestsRejectTrailingJSON(t *testing.T) {
	t.Parallel()

	if _, err := resolveRunCommandRequest(json.RawMessage(`{"command":"pwd"} {}`)); err == nil {
		t.Fatal("expected run_command request with trailing JSON to be rejected")
	}
	if _, _, err := writeProcessPayload(
		json.RawMessage(`{"process_id":"prc","data":"x"} {"process_id":"prc","data":"y"}`),
	); err == nil {
		t.Fatal("expected write_process request with trailing JSON to be rejected")
	}
	if _, _, err := resolveStopProcessRequest(
		json.RawMessage(`{"process_id":"prc","mode":"interrupt"} {"process_id":"other","mode":"terminate"}`),
	); err == nil {
		t.Fatal("expected stop_process request with trailing JSON to be rejected")
	}
}

func TestProcessActionPayloadSupportsWriteData(t *testing.T) {
	t.Parallel()

	processID, payload, err := writeProcessPayload(
		json.RawMessage(`{"process_id":"prc","data":"x"}`),
	)
	if err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if processID != "prc" {
		t.Fatalf("unexpected write process_id: %q", processID)
	}
	if string(payload) != `{"data":"x"}` {
		t.Fatalf("unexpected write payload: %s", payload)
	}
}

func TestAlreadyStoppedProcessResult(t *testing.T) {
	t.Parallel()

	result, err := alreadyStoppedProcessResult(
		"prc_stopped",
		executionstore.ProcessActionKindTerminate,
	)
	if err != nil {
		t.Fatal(err)
	}
	contentParts, err := result.contentParts()
	if err != nil {
		t.Fatal(err)
	}
	var parts []struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(contentParts, &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].Type != "structured_data" {
		t.Fatalf("already-stopped content parts = %s", contentParts)
	}
	var body struct {
		ProcessID       string `json:"process_id"`
		Mode            string `json:"mode"`
		State           string `json:"state"`
		StateReasonCode string `json:"state_reason_code"`
	}
	if err := json.Unmarshal(parts[0].Value, &body); err != nil {
		t.Fatal(err)
	}
	if body.ProcessID != "prc_stopped" ||
		body.Mode != "terminate" ||
		body.State != "applied" ||
		body.StateReasonCode != "already_stopped" {
		t.Fatalf("already-stopped result = %+v", body)
	}
}

func TestProcessActionPayloadSupportsWriteAndClose(t *testing.T) {
	t.Parallel()

	processID, payload, err := writeProcessPayload(
		json.RawMessage(`{"process_id":"prc","data":"x","close_stdin":true}`),
	)
	if err != nil {
		t.Fatalf("write and close payload: %v", err)
	}
	if processID != "prc" || string(payload) != `{"close_stdin":true,"data":"x"}` {
		t.Fatalf("unexpected write-and-close payload process_id=%q payload=%s", processID, payload)
	}
}

func TestProcessActionPayloadCapsWriteBytes(t *testing.T) {
	t.Parallel()

	oversized := strings.Repeat("x", processaction.MaxWriteBytes+1)
	body, err := json.Marshal(map[string]any{"process_id": "prc", "data": oversized})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, _, err := writeProcessPayload(body); err == nil {
		t.Fatal("expected oversized write_process data to be rejected")
	}
}

func TestRunCommandRequestTrimsCwdBeforeCanonicalization(t *testing.T) {
	t.Parallel()

	input, err := resolveRunCommandRequest(json.RawMessage(`{"command":"pwd","cwd":" /tmp "}`))
	if err != nil {
		t.Fatalf("resolve run_command request: %v", err)
	}
	if input.Cwd != "/tmp" {
		t.Fatalf("expected trimmed cwd, got %q", input.Cwd)
	}
}

func TestListProcessesRequestRejectsObservationFields(t *testing.T) {
	t.Parallel()

	if _, err := resolveProcessObservationRequest(json.RawMessage(`{"cursor":0}`), processObservationList); err == nil {
		t.Fatal("expected list_processes to reject read observation fields")
	}
	if _, err := resolveProcessObservationRequest(json.RawMessage(`null`), processObservationList); err == nil {
		t.Fatal("expected list_processes to reject null")
	}
	if _, err := resolveProcessObservationRequest(json.RawMessage(`{}`), processObservationList); err != nil {
		t.Fatalf("empty list_processes request should be accepted: %v", err)
	}
}

func TestProcessObservationActionPayloadOmitsAbsentFields(t *testing.T) {
	t.Parallel()

	cursor := int64(0)
	tests := []struct {
		name    string
		request processObservationRequest
		want    string
	}{
		{
			name: "read preserves explicit zero cursor without wait",
			request: processObservationRequest{
				Cursor:   &cursor,
				MaxBytes: 4096,
			},
			want: `{"cursor":0,"max_bytes":4096}`,
		},
		{
			name: "wait includes only wait duration",
			request: processObservationRequest{
				WaitMS: 2000,
			},
			want: `{"wait_ms":2000}`,
		},
		{
			name:    "all optional fields absent",
			request: processObservationRequest{},
			want:    `{}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := processObservationActionPayload(test.request)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Fatalf("process observation payload = %s, want %s", got, test.want)
			}
		})
	}
}

func TestProcessObserveRequestCapsObservationFields(t *testing.T) {
	t.Parallel()

	if _, err := resolveProcessObservationRequest(
		json.RawMessage(`{"process_id":"prc","max_bytes":65537}`),
		processObservationRead,
	); err == nil {
		t.Fatal("expected oversized max_bytes to be rejected")
	}
	if _, err := resolveProcessObservationRequest(
		json.RawMessage(`{"process_id":"prc","max_bytes":null}`),
		processObservationRead,
	); err == nil {
		t.Fatal("expected null max_bytes to be rejected")
	}
	if _, err := resolveProcessObservationRequest(
		json.RawMessage(`{"process_id":"prc","wait_ms":null}`),
		processObservationRead,
	); err == nil {
		t.Fatal("expected null wait_ms to be rejected")
	}
	if _, _, err := writeProcessPayload(
		json.RawMessage(`{"process_id":"prc","data":""}`),
	); err == nil {
		t.Fatal("expected empty write data to be rejected")
	}
}
