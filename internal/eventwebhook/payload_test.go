package eventwebhook

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
	openapispec "github.com/omnara-ai/omnara/api/openapi"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestSenderPayloadContractAndRetries(t *testing.T) {
	spec, err := openapi3.NewLoader().LoadFromData(openapispec.YAML)
	require.NoError(t, err)
	schema := spec.Components.Schemas["EventWebhookPayload"].Value
	for _, kind := range []string{"agent_input", "model_output", "tool_result", "context_checkpoint", "tool_call_update"} {
		t.Run(kind, func(t *testing.T) {
			sender, store, delivery := testSender()
			if kind != "tool_call_update" {
				store.record = executionstore.AgentEventReadRecord{
					ID: uuid.New(), OrgID: uuid.New(), ProjectID: store.target.ProjectID, AgentID: delivery.AgentID,
					TurnID: uuid.New(), TurnSequence: 1, Sequence: 17, EventKind: kind,
					AgentInputID: uuid.New(), InputKind: "content", ModelCallContextID: uuid.New(), ModelStopReason: "end_turn",
					ToolCallID: uuid.New(), ToolOutcome: "succeeded", ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
					ContextCheckpointID: uuid.New(), SummarizedThroughEventSequence: 16,
					CheckpointSummary: "summary", CreatedAt: time.Now(),
				}
				delivery.EventSequence = &store.record.Sequence
				delivery.ToolCallID, delivery.ToolState = nil, nil
			}
			requests := 0
			sender.client = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
				requests++
				var payload map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
				require.Equal(t, kind, payload["event"])
				require.NoError(t, schema.VisitJSON(payload))
				payload["event"] = "unknown"
				require.Error(t, schema.VisitJSON(payload))
				payload["event"] = "tool_call_update"
				if kind == "tool_call_update" {
					payload["event"] = "agent_input"
				}
				require.Error(t, schema.VisitJSON(payload))
				if requests == 1 {
					return nil, errors.New("connection reset")
				}
				status := http.StatusServiceUnavailable
				if requests == 3 {
					status = http.StatusNoContent
				}
				return &http.Response{StatusCode: status, Body: http.NoBody}, nil
			})}
			sender.deliver(t.Context(), delivery)
			sender.deliver(t.Context(), delivery)
			require.Equal(t, int64(2), store.retried.Load())
			require.Zero(t, store.completed.Load())
			sender.deliver(t.Context(), delivery)
			require.Equal(t, 3, requests)
			require.Equal(t, int64(1), store.completed.Load())
		})
	}
}
