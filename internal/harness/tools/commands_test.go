package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestStructuredQuestionAnsweredResultUsesPublicInteractionID(t *testing.T) {
	t.Parallel()

	interactionID := uuid.MustParse("019b18be-0000-7000-8000-000000000007")
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

func TestRunCommandRequestRejectsInvalidOptionalScalars(t *testing.T) {
	t.Parallel()

	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"command":"pwd","cwd":null}`),
		json.RawMessage(`{"command":"pwd","machine_id":null}`),
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
	require.NoError(t, err)
	contentParts, err := result.contentParts()
	require.NoError(t, err)
	var parts []struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	require.NoError(t, json.Unmarshal(contentParts, &parts))
	if len(parts) != 1 || parts[0].Type != "structured_data" {
		t.Fatalf("already-stopped content parts = %s", contentParts)
	}
	var body struct {
		ProcessID       string `json:"process_id"`
		Mode            string `json:"mode"`
		State           string `json:"state"`
		StateReasonCode string `json:"state_reason_code"`
	}
	require.NoError(t, json.Unmarshal(parts[0].Value, &body))
	if body.ProcessID != "prc_stopped" ||
		body.Mode != "terminate" ||
		body.State != "applied" ||
		body.StateReasonCode != "already_stopped" {
		t.Fatalf("already-stopped result = %+v", body)
	}
}

func TestProcessActionPayloadRequiresData(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		`{"process_id":"prc"}`,
		`{"process_id":"prc","data":""}`,
		`{"process_id":"prc","close_stdin":true}`,
	} {
		if _, _, err := writeProcessPayload(json.RawMessage(raw)); err == nil {
			t.Fatalf("expected write_process request %s to be rejected", raw)
		}
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

	if _, err := resolveProcessObservationRequest(json.RawMessage(`{"offset_byte":0}`), processObservationList); err == nil {
		t.Fatal("expected list_processes to reject read observation fields")
	}
	if _, err := resolveProcessObservationRequest(json.RawMessage(`null`), processObservationList); err == nil {
		t.Fatal("expected list_processes to reject null")
	}
	if _, err := resolveProcessObservationRequest(json.RawMessage(`{}`), processObservationList); err != nil {
		t.Fatalf("empty list_processes request should be accepted: %v", err)
	}
}

func TestProcessObservationActionPayload(t *testing.T) {
	t.Parallel()

	offset := int64(0)
	shellWait := processaction.OutputWait{WaitForExit: true, HeadBytes: processaction.AgentHeadBytes}
	tests := []struct {
		name    string
		request processObservationRequest
		wait    processaction.OutputWait
		want    string
	}{
		{
			name:    "read preserves explicit zero offset without wait",
			request: processObservationRequest{OffsetByte: &offset},
			want:    `{"cursor":0,"max_bytes":16384}`,
		},
		{
			name:    "wait waits for exit and keeps head and tail",
			request: processObservationRequest{WaitMS: 2000},
			wait:    shellWait,
			want:    `{"max_bytes":16384,"wait_ms":2000,"wait_for_exit":true,"head_bytes":2048}`,
		},
		{
			name:    "all optional fields absent",
			request: processObservationRequest{},
			want:    `{"max_bytes":16384}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := processObservationActionPayload(test.request, test.wait)
			require.NoError(t, err)
			if string(got) != test.want {
				t.Fatalf("process observation payload = %s, want %s", got, test.want)
			}
		})
	}
}

func TestProcessObserveRequestCapsObservationFields(t *testing.T) {
	t.Parallel()

	if _, err := resolveProcessObservationRequest(
		json.RawMessage(`{"process_id":"prc","limit_bytes":64}`),
		processObservationRead,
	); err == nil {
		t.Fatal("expected removed limit_bytes field to be rejected")
	}
	if _, err := resolveProcessObservationRequest(
		json.RawMessage(`{"process_id":"prc","wait_ms":null}`),
		processObservationRead,
	); err == nil {
		t.Fatal("expected null wait_ms to be rejected")
	}
	if _, err := resolveProcessObservationRequest(
		json.RawMessage(`{"process_id":"prc","offset_byte":-1}`),
		processObservationRead,
	); err == nil || err.Error() != "offset_byte must be non-negative" {
		t.Fatalf("negative offset_byte error = %v", err)
	}
	if _, err := resolveProcessObservationRequest(
		json.RawMessage(`{"process_id":"prc","cursor":0}`),
		processObservationRead,
	); err == nil {
		t.Fatal("expected legacy cursor field to be rejected")
	}
	read, err := resolveProcessObservationRequest(
		json.RawMessage(`{"process_id":"prc","offset_byte":7}`),
		processObservationRead,
	)
	if err != nil {
		t.Fatalf("resolve read_process range: %v", err)
	}
	if read.OffsetByte == nil || *read.OffsetByte != 7 {
		t.Fatalf("read_process range = %+v", read)
	}
	if _, _, err := writeProcessPayload(
		json.RawMessage(`{"process_id":"prc","data":""}`),
	); err == nil {
		t.Fatal("expected empty write data to be rejected")
	}
}
