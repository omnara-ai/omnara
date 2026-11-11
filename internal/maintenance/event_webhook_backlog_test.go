package maintenance

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/observability/metrics"
	"github.com/stretchr/testify/require"
)

func TestEventWebhookBacklogRecoversAndCancelsSample(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var sampleDone <-chan struct{}
	sample := func(ctx context.Context) (executionstore.EventWebhookBacklog, error) {
		sampleDone = ctx.Done()
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("sample requires a deadline")
		}
		panic("test failure")
	}
	_, err := sampleEventWebhookBacklog(t.Context(), logger, sample)
	if err == nil || !strings.Contains(err.Error(), "test failure") {
		t.Fatalf("panic was not returned as a failed sample: %v", err)
	}
	select {
	case <-sampleDone:
	default:
		t.Fatal("sample context was not canceled after panic")
	}
	want := executionstore.EventWebhookBacklog{Due: 3, OldestDueAge: time.Second}
	backlog, err := sampleEventWebhookBacklog(t.Context(), logger,
		func(context.Context) (executionstore.EventWebhookBacklog, error) { return want, nil })
	if err != nil || backlog != want {
		t.Fatalf("next sample failed after recovery: backlog=%+v, err=%v", backlog, err)
	}
}

func TestEventWebhookBacklogJobReturnsSampleFailure(t *testing.T) {
	failure := errors.New("store failed")
	run := func(ctx context.Context, err error) maintenanceResult {
		return recordEventWebhookBacklog(slog.New(slog.DiscardHandler),
			func(context.Context) (executionstore.EventWebhookBacklog, error) {
				return executionstore.EventWebhookBacklog{}, err
			}, nil)(ctx)
	}
	if result := run(t.Context(), nil); result.err != nil {
		t.Fatalf("successful sample returned %v", result.err)
	}
	if result := run(t.Context(), failure); !errors.Is(result.err, failure) {
		t.Fatalf("failed sample returned %v, want %v", result.err, failure)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if result := run(canceled, context.Canceled); result.err != nil {
		t.Fatalf("shutdown returned %v, want nil", result.err)
	}
}

func TestWebhookExpiryCountsOnlySuccessfulCleanup(t *testing.T) {
	set := metrics.New()
	recorder := metrics.NewEventWebhookBacklogRecorder(set)
	logger := slog.New(slog.DiscardHandler)
	for _, err := range []error{nil, errors.New("database unavailable")} {
		deleteExpiredWebhookDeliveries(logger, func(context.Context, int32) (int64, error) {
			return 2, err
		}, recorder)(t.Context())
	}
	scrape := httptest.NewRecorder()
	set.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Contains(t, scrape.Body.String(), "omnara_event_webhook_expired_total 2")
}
