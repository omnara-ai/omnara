package eventwebhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

type failingUpdateStore struct {
	*testStore
	err error
}

func (s failingUpdateStore) RetryEventWebhookDelivery(
	context.Context, uuid.UUID, uuid.UUID, time.Duration,
) (executionstore.EventWebhookRetryResult, error) {
	return executionstore.EventWebhookRetryResult{}, s.err
}

func (s failingUpdateStore) CompleteEventWebhookDelivery(context.Context, uuid.UUID, uuid.UUID) error {
	return s.err
}

func TestDeliveryLogsOutcomeWithoutURLSecrets(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		storeError bool
		outcome    string
		level      string
	}{
		{"success", 204, false, "completed", "INFO"},
		{"retry", 503, false, "retry_scheduled", "WARN"},
		{"exhausted", 503, false, "gave_up", "ERROR"},
		{"retry store failure", 503, true, "retry_failed", "ERROR"},
		{"complete store failure", 204, true, "complete_failed", "ERROR"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sender, store, delivery := testSender()
			store.retryResult = executionstore.EventWebhookRetryResult{
				NextAttemptAt: time.Date(2026, time.September, 21, 12, 0, 0, 123000, time.UTC),
				GaveUp:        test.outcome == "gave_up",
			}
			var output bytes.Buffer
			sender.log = slog.New(slog.NewJSONHandler(&output, nil))
			store.target.URL = "https://example.com/private-path?token=private-token"
			if test.storeError {
				sender.store = failingUpdateStore{testStore: store, err: errors.New("store unavailable")}
			}
			sender.client = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Body: http.NoBody}, nil
			})}
			sender.deliver(t.Context(), delivery)
			var record map[string]any
			lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
			require.NoError(t, json.Unmarshal(lines[len(lines)-1], &record))
			require.Equal(t, "event_webhook.delivery", record["event.name"])
			require.Equal(t, test.outcome, record["event_webhook.outcome"])
			require.Equal(t, test.level, record["level"])
			require.Equal(t, delivery.ID.String(), record["event_webhook.delivery_id"])
			require.Equal(t, delivery.OrgID.String(), record["org.id"])
			require.Equal(t, store.target.ProjectID.String(), record["project.id"])
			require.Equal(t, "tool_call_update", record["event_webhook.event_kind"])
			require.Equal(t, float64(test.status), record["http.status_code"])
			require.Equal(t, "example.com", record["event_webhook.target_host"])
			require.NotContains(t, output.String(), "private-path")
			require.NotContains(t, output.String(), "private-token")
			if test.status == 503 {
				require.Contains(t, record["error.message"], "503")
			} else {
				require.NotContains(t, record, "error.message")
			}
			if test.outcome == "retry_scheduled" || test.outcome == "gave_up" {
				require.Equal(t, store.retryResult.NextAttemptAt.Format(time.RFC3339Nano), record["event_webhook.retry_at"])
				require.Equal(t, int64(1), store.retried.Load())
				require.Zero(t, store.completed.Load())
			}
			if test.outcome == "gave_up" {
				require.Equal(t, true, record["event_webhook.gave_up"])
			} else {
				require.NotContains(t, record, "event_webhook.gave_up")
			}
			if test.storeError {
				require.Len(t, lines, 2)
				var storeRecord map[string]any
				require.NoError(t, json.Unmarshal(lines[0], &storeRecord))
				require.Equal(t, "event_webhook.store_failed", storeRecord["event.name"])
				require.Equal(t, "ERROR", storeRecord["level"])
				require.Equal(t, record["event.id"], storeRecord["parent.event.id"])
				operation := "retry"
				if test.status == 204 {
					operation = "complete"
				}
				require.Equal(t, operation, storeRecord["event_webhook.store_operation"])
				require.Equal(t, "store unavailable", storeRecord["error.message"])
				require.NotEqual(t, storeRecord["error.message"], record["error.message"])
			} else {
				require.Len(t, lines, 1)
			}
		})
	}
}
