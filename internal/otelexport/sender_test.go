package otelexport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type fakeStore struct {
	destination    executionstore.OtelExportDestination
	destinationErr error
	turns          map[uuid.UUID]oteltrace.Turn
	completed      []uuid.UUID
	retried        []uuid.UUID
}

func (s *fakeStore) ClaimOtelExportDelivery(context.Context, int) (executionstore.OtelExportDelivery, error) {
	return executionstore.OtelExportDelivery{}, storeerr.ErrNotFound
}

func (s *fakeStore) CompleteOtelExportDelivery(_ context.Context, id, _ uuid.UUID) error {
	s.completed = append(s.completed, id)
	return nil
}

func (s *fakeStore) RetryOtelExportDelivery(_ context.Context, id, _ uuid.UUID, _ time.Duration) error {
	s.retried = append(s.retried, id)
	return nil
}

func (s *fakeStore) GetOtelExportDestination(
	context.Context, uuid.UUID, uuid.UUID,
) (executionstore.OtelExportDestination, error) {
	return s.destination, s.destinationErr
}

func (s *fakeStore) LoadTurnTraceForExport(_ context.Context, _, turnID uuid.UUID) (oteltrace.Turn, error) {
	turn, ok := s.turns[turnID]
	if !ok {
		return oteltrace.Turn{}, storeerr.ErrNotFound
	}
	return turn, nil
}

var protobufDestination = executionstore.OtelExportDestination{Encoding: oteltrace.EncodingProtobuf}

func testTurn() oteltrace.Turn {
	start := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	call := uuid.New()
	return oteltrace.Turn{
		TurnID: uuid.New(), AgentName: "researcher", AgentID: "agt_1", RootAgentID: "agt_1",
		ModelCalls: []oteltrace.ModelCall{{ID: call, State: "succeeded", RequestModel: "claude",
			StopReason: "end_turn", StartedAt: start, CompletedAt: start.Add(time.Second)}},
		Events: []oteltrace.Event{
			{Kind: events.KindAgentInput, IsOpening: true, CreatedAt: start,
				ContentBlocks: json.RawMessage(`[{"type":"text","text":"hi"}]`)},
			{Kind: events.KindModelOutput, ModelCallID: call, StopReason: "end_turn",
				CreatedAt: start.Add(time.Second), ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`)},
		},
	}
}

func newTestSender(t *testing.T, store *fakeStore, handler http.HandlerFunc) *Sender {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)
	allowlist, err := ssrf.ParseAllowlist([]string{"127.0.0.1:" + port})
	require.NoError(t, err)
	store.destination.Endpoint = server.URL + "/v1/traces"
	return New(store, slog.New(slog.DiscardHandler), nil, allowlist)
}

func testDelivery(turnID uuid.UUID) executionstore.OtelExportDelivery {
	return executionstore.OtelExportDelivery{
		ID: uuid.New(), OrgID: uuid.New(), ExporterID: uuid.New(), TurnID: turnID,
		AttemptCount: 1, ClaimToken: uuid.New(),
	}
}

func TestSenderExportsTurnInConfiguredEncoding(t *testing.T) {
	for encoding, test := range map[oteltrace.Encoding]struct {
		contentType string
		unmarshaler ptrace.Unmarshaler
	}{
		oteltrace.EncodingProtobuf: {"application/x-protobuf", &ptrace.ProtoUnmarshaler{}},
		oteltrace.EncodingJSON:     {"application/json", &ptrace.JSONUnmarshaler{}},
	} {
		t.Run(string(encoding), func(t *testing.T) {
			turn := testTurn()
			store := &fakeStore{
				destination: executionstore.OtelExportDestination{
					Variant: oteltrace.VariantLangfuse, Encoding: encoding,
					Headers: map[string]string{"Authorization": "Bearer key"},
				},
				turns: map[uuid.UUID]oteltrace.Turn{turn.TurnID: turn},
			}
			var requests int
			var path, contentType, authorization string
			var body []byte
			sender := newTestSender(t, store, func(_ http.ResponseWriter, r *http.Request) {
				requests++
				path, contentType, authorization = r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization")
				body, _ = io.ReadAll(r.Body)
			})
			delivery := testDelivery(turn.TurnID)
			sender.deliver(t.Context(), delivery)

			require.Equal(t, 1, requests)
			require.Equal(t, "/v1/traces", path)
			require.Equal(t, test.contentType, contentType)
			require.Equal(t, "Bearer key", authorization)
			require.Equal(t, []uuid.UUID{delivery.ID}, store.completed)
			require.Empty(t, store.retried)
			traces, err := test.unmarshaler.UnmarshalTraces(body)
			require.NoError(t, err)
			spans := traces.ResourceSpans().At(0).ScopeSpans().At(0).Spans()
			require.Equal(t, 2, spans.Len())
			require.Equal(t, "invoke_agent researcher", spans.At(0).Name())
			require.Equal(t, "chat claude", spans.At(1).Name())
			require.Equal(t, oteltrace.TraceID(turn.TurnID), spans.At(1).TraceID())
		})
	}
}

func TestSenderRetriesOrDropsByResponse(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        int
		expectedRetry bool
	}{
		{"server error", http.StatusServiceUnavailable, true},
		{"bad credentials", http.StatusUnauthorized, true},
		{"malformed payload", http.StatusBadRequest, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			turn := testTurn()
			store := &fakeStore{destination: protobufDestination, turns: map[uuid.UUID]oteltrace.Turn{turn.TurnID: turn}}
			sender := newTestSender(t, store, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			})
			delivery := testDelivery(turn.TurnID)
			sender.deliver(t.Context(), delivery)
			if test.expectedRetry {
				require.Equal(t, []uuid.UUID{delivery.ID}, store.retried)
				require.Empty(t, store.completed)
			} else {
				require.Equal(t, []uuid.UUID{delivery.ID}, store.completed)
				require.Empty(t, store.retried)
			}
		})
	}
}

func TestSenderSkipsRequestWithoutSpansOrExporter(t *testing.T) {
	for _, test := range []struct {
		name  string
		store *fakeStore
		retry bool
	}{
		{"deleted exporter", &fakeStore{destinationErr: storeerr.ErrNotFound}, false},
		{"missing turn", &fakeStore{destination: protobufDestination}, false},
		{"misconfigured exporter", &fakeStore{destinationErr: executionstore.ErrOtelExporterMisconfigured}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sender := newTestSender(t, test.store, func(http.ResponseWriter, *http.Request) {
				t.Error("no request expected")
			})
			delivery := testDelivery(uuid.New())
			sender.deliver(t.Context(), delivery)
			if test.retry {
				require.Equal(t, []uuid.UUID{delivery.ID}, test.store.retried)
				require.Empty(t, test.store.completed)
			} else {
				require.Equal(t, []uuid.UUID{delivery.ID}, test.store.completed)
				require.Empty(t, test.store.retried)
			}
		})
	}
}

func TestDeliveryAttemptLogsOneWideEvent(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		outcome string
		cause   string
		level   string
	}{
		{"success", http.StatusOK, "completed", "none", "INFO"},
		{"retry", http.StatusServiceUnavailable, "retry_scheduled", "recipient", "WARN"},
		{"rejected", http.StatusBadRequest, "dropped", "recipient", "WARN"},
	} {
		t.Run(test.name, func(t *testing.T) {
			turn := testTurn()
			store := &fakeStore{
				destination: executionstore.OtelExportDestination{
					Variant: oteltrace.VariantLangfuse, Encoding: oteltrace.EncodingJSON,
					Headers: map[string]string{"Authorization": "Bearer private-token"},
				},
				turns: map[uuid.UUID]oteltrace.Turn{turn.TurnID: turn},
			}
			sender := newTestSender(t, store, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			})
			var output bytes.Buffer
			sender.log = slog.New(slog.NewJSONHandler(&output, nil))
			delivery := testDelivery(turn.TurnID)
			delivery.CreatedAt = time.Now().Add(-time.Minute)
			sender.deliver(t.Context(), delivery)

			lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
			require.Len(t, lines, 1)
			var record map[string]any
			require.NoError(t, json.Unmarshal(lines[0], &record))
			require.Equal(t, "otel_export.delivery_attempt", record["event.name"])
			require.Equal(t, test.level, record["level"])
			require.Equal(t, test.outcome, record["otel_export.outcome"])
			require.Equal(t, test.cause, record["otel_export.cause"])
			require.Equal(t, delivery.OrgID.String(), record["org.id"])
			require.Equal(t, delivery.ID.String(), record["otel_export.delivery_id"])
			require.Equal(t, delivery.ExporterID.String(), record["otel_export.exporter_id"])
			require.InDelta(t, 2, record["otel_export.spans"], 0)
			require.InDelta(t, 1, record["otel_export.attempt"], 0)
			require.GreaterOrEqual(t, record["otel_export.age_seconds"], 60.0)
			require.Equal(t, "langfuse", record["otel_export.variant"])
			require.Equal(t, "json", record["otel_export.encoding"])
			require.Equal(t, "127.0.0.1", record["otel_export.endpoint_host"])
			require.InDelta(t, test.status, record["http.status_code"], 0)
			require.NotContains(t, output.String(), "private-token")
			require.NotContains(t, output.String(), "/v1/traces")
			if test.status == http.StatusOK {
				require.NotContains(t, record, "error.message")
			} else {
				require.Contains(t, record["error.message"], strconv.Itoa(test.status))
			}
		})
	}
}

func TestSenderReadsPartialSuccess(t *testing.T) {
	turn := testTurn()
	store := &fakeStore{destination: protobufDestination, turns: map[uuid.UUID]oteltrace.Turn{turn.TurnID: turn}}
	sender := newTestSender(t, store, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"partialSuccess":{"rejectedSpans":"1","errorMessage":"span too large"}}`))
	})
	delivery := testDelivery(turn.TurnID)
	outcome, err := sender.send(t.Context(), delivery)
	require.Equal(t, outcomePartiallyRejected, outcome)
	require.ErrorContains(t, err, "rejected 1 spans")
	require.NotContains(t, err.Error(), "span too large")

	// Rejected spans are reported, and never retried: the endpoint kept the rest.
	sender.deliver(t.Context(), delivery)
	require.Equal(t, []uuid.UUID{delivery.ID}, store.completed)
	require.Empty(t, store.retried)
}

func TestSenderKeepsReceiverMessageOutOfLogs(t *testing.T) {
	turn := testTurn()
	store := &fakeStore{destination: protobufDestination, turns: map[uuid.UUID]oteltrace.Turn{turn.TurnID: turn}}
	sender := newTestSender(t, store, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"partialSuccess":{"rejectedSpans":1,"errorMessage":"rejected: tool said secret-tool-output"}}`))
	})
	var output bytes.Buffer
	sender.log = slog.New(slog.NewJSONHandler(&output, nil))
	sender.deliver(t.Context(), testDelivery(turn.TurnID))
	require.Contains(t, output.String(), `"otel_export.rejected_spans":1`)
	require.NotContains(t, output.String(), "secret-tool-output")
}

func TestSenderDoesNotCountUnreadableAcknowledgmentAsDelivered(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"oversized", `{"partialSuccess":{"rejectedSpans":1,"errorMessage":"` +
			strings.Repeat("x", responseReadLimit) + `"}}`},
		{"not an acknowledgment", `<html>secret-page-content</html>`},
	} {
		t.Run(test.name, func(t *testing.T) {
			turn := testTurn()
			store := &fakeStore{destination: protobufDestination, turns: map[uuid.UUID]oteltrace.Turn{turn.TurnID: turn}}
			sender := newTestSender(t, store, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			})
			var output bytes.Buffer
			sender.log = slog.New(slog.NewJSONHandler(&output, nil))
			delivery := testDelivery(turn.TurnID)
			sender.deliver(t.Context(), delivery)
			// OTLP makes it non-retryable, and it is reported apart from full delivery.
			require.Equal(t, []uuid.UUID{delivery.ID}, store.completed)
			require.Empty(t, store.retried)
			require.Contains(t, output.String(), `"otel_export.outcome":"unacknowledged"`)
			require.Contains(t, output.String(), `"level":"WARN"`)
			require.NotContains(t, output.String(), "secret-page-content")
		})
	}
}
