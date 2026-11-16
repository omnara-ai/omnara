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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

type fakeStore struct {
	destination    executionstore.OtelExportDestination
	destinationErr error
	turns          map[uuid.UUID]oteltrace.Turn
	loads          int
	completed      []uuid.UUID
	retried        []uuid.UUID
	retriedIDs     []uuid.UUID
}

func (s *fakeStore) ClaimOtelExportDeliveries(context.Context, int, int) (executionstore.OtelExportBatch, error) {
	return executionstore.OtelExportBatch{}, storeerr.ErrNotFound
}

func (s *fakeStore) CompleteOtelExportDeliveries(_ context.Context, token uuid.UUID) error {
	s.completed = append(s.completed, token)
	return nil
}

func (s *fakeStore) RetryOtelExportDeliveries(
	_ context.Context, token uuid.UUID, _ time.Duration, ids []uuid.UUID,
) error {
	s.retried = append(s.retried, token)
	s.retriedIDs = append(s.retriedIDs, ids...)
	return nil
}

func (s *fakeStore) GetOtelExportDestination(
	context.Context, uuid.UUID, uuid.UUID,
) (executionstore.OtelExportDestination, error) {
	return s.destination, s.destinationErr
}

func (s *fakeStore) LoadTurnTraceForExport(_ context.Context, _, turnID uuid.UUID) (oteltrace.Turn, error) {
	s.loads++
	turn, ok := s.turns[turnID]
	if !ok {
		return oteltrace.Turn{}, storeerr.ErrNotFound
	}
	return turn, nil
}

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

func testBatch(turnIDs ...uuid.UUID) executionstore.OtelExportBatch {
	batch := executionstore.OtelExportBatch{ClaimToken: uuid.New(), OrgID: uuid.New(), ExporterID: uuid.New()}
	for _, turnID := range turnIDs {
		batch.Deliveries = append(batch.Deliveries, executionstore.OtelExportDelivery{
			ID: uuid.New(), TurnID: turnID, AttemptCount: 1,
		})
	}
	return batch
}

func TestSenderExportsBatchInOneRequest(t *testing.T) {
	turn, other := testTurn(), testTurn()
	store := &fakeStore{
		destination: executionstore.OtelExportDestination{
			Variant: oteltrace.VariantLangfuse, Headers: map[string]string{"Authorization": "Bearer key"},
		},
		turns: map[uuid.UUID]oteltrace.Turn{turn.TurnID: turn, other.TurnID: other},
	}
	var requests int
	var path, contentType, authorization string
	var body []byte
	sender := newTestSender(t, store, func(_ http.ResponseWriter, r *http.Request) {
		requests++
		path, contentType, authorization = r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization")
		body, _ = io.ReadAll(r.Body)
	})
	batch := testBatch(turn.TurnID, other.TurnID, uuid.New())
	sender.deliver(t.Context(), batch)

	require.Equal(t, 1, requests)
	require.Equal(t, "/v1/traces", path)
	require.Equal(t, "application/json", contentType)
	require.Equal(t, "Bearer key", authorization)
	var export oteltrace.Export
	require.NoError(t, json.Unmarshal(body, &export))
	require.Equal(t, 3, store.loads)
	require.Equal(t, []uuid.UUID{batch.ClaimToken}, store.completed)
	require.Empty(t, store.retried)
	spans := export.ResourceSpans[0].ScopeSpans[0].Spans
	require.Len(t, spans, 4)
	require.Equal(t, "invoke_agent researcher", spans[0].Name)
	require.Equal(t, "chat claude", spans[1].Name)
	require.Equal(t, oteltrace.TraceID(turn.TurnID), spans[1].TraceID)
	require.Equal(t, oteltrace.TraceID(other.TurnID), spans[2].TraceID)
}

func TestSenderExportsProtobufWhenConfigured(t *testing.T) {
	turn := testTurn()
	store := &fakeStore{
		destination: executionstore.OtelExportDestination{Encoding: oteltrace.EncodingProtobuf},
		turns:       map[uuid.UUID]oteltrace.Turn{turn.TurnID: turn},
	}
	var contentType string
	var body []byte
	sender := newTestSender(t, store, func(_ http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		body, _ = io.ReadAll(r.Body)
	})
	batch := testBatch(turn.TurnID)
	sender.deliver(t.Context(), batch)

	require.Equal(t, "application/x-protobuf", contentType)
	var decoded tracepb.TracesData
	require.NoError(t, proto.Unmarshal(body, &decoded))
	spans := decoded.GetResourceSpans()[0].GetScopeSpans()[0].GetSpans()
	require.Len(t, spans, 2)
	require.Equal(t, turn.TurnID[:], spans[0].GetTraceId())
	require.Equal(t, []uuid.UUID{batch.ClaimToken}, store.completed)
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
			store := &fakeStore{turns: map[uuid.UUID]oteltrace.Turn{turn.TurnID: turn}}
			sender := newTestSender(t, store, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			})
			batch := testBatch(turn.TurnID)
			sender.deliver(t.Context(), batch)
			if test.expectedRetry {
				require.Equal(t, []uuid.UUID{batch.ClaimToken}, store.retried)
				require.Empty(t, store.completed)
			} else {
				require.Equal(t, []uuid.UUID{batch.ClaimToken}, store.completed)
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
		{"missing turn", &fakeStore{turns: map[uuid.UUID]oteltrace.Turn{}}, false},
		{"misconfigured exporter", &fakeStore{destinationErr: executionstore.ErrOtelExporterMisconfigured}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sender := newTestSender(t, test.store, func(http.ResponseWriter, *http.Request) {
				t.Error("no request expected")
			})
			batch := testBatch(uuid.New())
			sender.deliver(t.Context(), batch)
			if test.retry {
				require.Equal(t, []uuid.UUID{batch.ClaimToken}, test.store.retried)
			} else {
				require.Equal(t, []uuid.UUID{batch.ClaimToken}, test.store.completed)
			}
		})
	}
}

func TestBatchAttemptLogsOneWideEvent(t *testing.T) {
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
			batch := testBatch(turn.TurnID)
			batch.Deliveries[0].CreatedAt = time.Now().Add(-time.Minute)
			sender.deliver(t.Context(), batch)

			lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
			require.Len(t, lines, 1)
			var record map[string]any
			require.NoError(t, json.Unmarshal(lines[0], &record))
			require.Equal(t, "otel_export.batch_attempt", record["event.name"])
			require.Equal(t, test.level, record["level"])
			require.Equal(t, test.outcome, record["otel_export.outcome"])
			require.Equal(t, test.cause, record["otel_export.cause"])
			require.Equal(t, batch.OrgID.String(), record["org.id"])
			require.Equal(t, batch.ExporterID.String(), record["otel_export.exporter_id"])
			require.InDelta(t, 1, record["otel_export.turns"], 0)
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

// A refused batch is resent one trace at a time: the bad trace is dropped,
// a trace the endpoint cannot take right now is retried, the rest are delivered.
func TestSenderIsolatesTracesOfRejectedBatch(t *testing.T) {
	good, bad, busy := testTurn(), testTurn(), testTurn()
	store := &fakeStore{turns: map[uuid.UUID]oteltrace.Turn{good.TurnID: good, bad.TurnID: bad, busy.TurnID: busy}}
	var requests, delivered int
	sender := newTestSender(t, store, func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		switch {
		case bytes.Contains(body, []byte(oteltrace.TraceID(bad.TurnID))):
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		case bytes.Contains(body, []byte(oteltrace.TraceID(busy.TurnID))):
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			delivered++
		}
	})
	batch := testBatch(good.TurnID, bad.TurnID, busy.TurnID)
	sender.deliver(t.Context(), batch)

	require.Equal(t, 4, requests)
	require.Equal(t, 1, delivered)
	require.Equal(t, []uuid.UUID{batch.Deliveries[2].ID}, store.retriedIDs)
	require.Equal(t, []uuid.UUID{batch.ClaimToken}, store.completed)
}

func TestSenderReadsPartialSuccess(t *testing.T) {
	turn := testTurn()
	store := &fakeStore{turns: map[uuid.UUID]oteltrace.Turn{turn.TurnID: turn}}
	sender := newTestSender(t, store, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"partialSuccess":{"rejectedSpans":"1","errorMessage":"span too large"}}`))
	})
	batch := testBatch(turn.TurnID)
	result := sender.send(t.Context(), batch)

	// Rejected spans are reported, and never retried: the endpoint kept the rest.
	require.EqualValues(t, 1, result.rejectedSpans)
	require.Equal(t, 1, result.delivered)
	require.Empty(t, result.retry)
	require.ErrorContains(t, result.err, "rejected 1 spans")
}
