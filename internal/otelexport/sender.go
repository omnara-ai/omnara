package otelexport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/observability/metrics"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

const (
	MaxInFlight = 16
	PerOrgLimit = 4
	BatchLimit  = 25

	attemptTimeout     = 30 * time.Second
	buildTimeout       = 30 * time.Second
	storeTimeout       = 5 * time.Second
	idleInterval       = time.Second
	responseDrainLimit = 4096
)

type Store interface {
	ClaimOtelExportDeliveries(ctx context.Context, perOrgLimit, batchLimit int) (executionstore.OtelExportBatch, error)
	CompleteOtelExportDeliveries(ctx context.Context, claimToken uuid.UUID) error
	RetryOtelExportDeliveries(ctx context.Context, claimToken uuid.UUID, delay time.Duration) error
	GetOtelExportDestination(
		ctx context.Context, orgID, exporterID uuid.UUID,
	) (executionstore.OtelExportDestination, error)
	LoadTurnTraceForExport(ctx context.Context, agentID, turnID uuid.UUID) (oteltrace.Turn, error)
}

type Sender struct {
	store  Store
	client *http.Client
	log    *slog.Logger
}

type rejectedError struct{ error }

func (e rejectedError) Unwrap() error { return e.error }

type recipientError struct{ error }

func (e recipientError) Unwrap() error { return e.error }

func New(
	store Store, log *slog.Logger, httpRecorder *metrics.HTTPClientRecorder, endpointAllowlist *ssrf.Allowlist,
) *Sender {
	return &Sender{store: store, log: log,
		client: metrics.NewObservedHTTPClient(
			outboundhttp.NewPublicClient(
				outboundhttp.PublicClientOptions{Timeout: attemptTimeout, Allowlist: endpointAllowlist},
			), httpRecorder,
			metrics.WithHTTPClientHostLabel("otel_export"), metrics.WithHTTPClientPathLabel("/*"))}
}

func (s *Sender) Run(ctx context.Context) {
	defer s.client.CloseIdleConnections()
	inFlight := 0
	completed := make(chan struct{}, MaxInFlight)
	var batches sync.WaitGroup
	defer batches.Wait()
	for ctx.Err() == nil {
		if inFlight < MaxInFlight {
			claimCtx, cancel := context.WithTimeout(ctx, storeTimeout)
			batch, err := s.store.ClaimOtelExportDeliveries(claimCtx, PerOrgLimit, BatchLimit)
			cancel()
			if err == nil {
				inFlight++
				batches.Go(func() {
					s.deliver(ctx, batch)
					completed <- struct{}{}
				})
				continue
			}
			if !errors.Is(err, storeerr.ErrNotFound) && ctx.Err() == nil {
				logent.OtelExportStoreFailed(logpkg.WithLogger(ctx, s.log), "claim", err)
			}
		}
		var idle <-chan time.Time
		if inFlight < MaxInFlight {
			idle = time.After(idleInterval)
		}
		select {
		case <-ctx.Done():
		case <-completed:
			inFlight--
		case <-idle:
		}
	}
}

func (s *Sender) deliver(ctx context.Context, batch executionstore.OtelExportBatch) {
	ctx, event := logent.OtelExportBatchAttempt(logpkg.WithLogger(ctx, s.log), batch)
	defer event.Done(ctx)
	sent, err := s.send(ctx, batch)
	if ctx.Err() != nil {
		logent.OtelExportBatchResult(ctx, "canceled", "none", ctx.Err())
		return
	}
	updateCtx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	var rejected rejectedError
	if err != nil && !errors.As(err, &rejected) {
		var attempt int32
		for _, delivery := range batch.Deliveries {
			attempt = max(attempt, delivery.AttemptCount)
		}
		if retryErr := s.store.RetryOtelExportDeliveries(updateCtx, batch.ClaimToken, retryDelay(attempt)); retryErr != nil {
			logent.OtelExportStoreFailed(ctx, "retry", retryErr)
			logent.OtelExportBatchResult(ctx, "retry_failed", "local", err)
			return
		}
		logent.OtelExportBatchResult(ctx, "retry_scheduled", failureCause(err), err)
		return
	}
	if completeErr := s.store.CompleteOtelExportDeliveries(updateCtx, batch.ClaimToken); completeErr != nil {
		logent.OtelExportStoreFailed(ctx, "complete", completeErr)
		logent.OtelExportBatchResult(ctx, "complete_failed", "local", err)
		return
	}
	switch {
	case err != nil:
		logent.OtelExportBatchResult(ctx, "dropped", failureCause(err), err)
	case sent:
		logent.OtelExportBatchResult(ctx, "completed", "none", nil)
	default:
		logent.OtelExportBatchResult(ctx, "skipped", "none", nil)
	}
}

func failureCause(err error) string {
	var recipient recipientError
	var rejected rejectedError
	switch {
	case errors.As(err, &recipient), errors.As(err, &rejected):
		return "recipient"
	case errors.Is(err, executionstore.ErrOtelExporterMisconfigured):
		return "configuration"
	default:
		return "local"
	}
}

func retryDelay(attempt int32) time.Duration {
	ceiling := 2 * time.Second
	for range min(max(attempt-1, 0), 6) {
		ceiling = min(ceiling*2, 2*time.Minute)
	}
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)))
}

func (s *Sender) send(ctx context.Context, batch executionstore.OtelExportBatch) (bool, error) {
	buildCtx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	destination, err := s.store.GetOtelExportDestination(buildCtx, batch.OrgID, batch.ExporterID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	logent.OtelExportDestination(ctx, destination)
	spans, err := s.collect(buildCtx, batch.Deliveries, destination.Variant)
	if err != nil || len(spans) == 0 {
		return false, err
	}
	body, contentType, err := oteltrace.NewExport(spans).Encode(destination.Encoding)
	if err != nil {
		return false, fmt.Errorf("encode otel export: %w", err)
	}
	logent.OtelExportPayload(ctx, len(spans), len(body))
	attemptCtx, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)
	defer cancelAttempt()
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, destination.Endpoint, bytes.NewReader(body))
	if err != nil {
		return false, errors.New("create otel export request failed")
	}
	for name, value := range destination.Headers {
		req.Header.Set(name, value)
	}
	req.Header.Set("Content-Type", contentType)
	response, err := s.client.Do(req)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return false, recipientError{fmt.Errorf("post otel export: %w", err)}
	}
	logent.OtelExportResponse(ctx, response.StatusCode)
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, responseDrainLimit))
	switch status := response.StatusCode; {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return true, nil
	case retryableStatus(status):
		return false, recipientError{fmt.Errorf("otel endpoint returned HTTP %d", status)}
	default:
		return false, rejectedError{fmt.Errorf("otel endpoint returned HTTP %d", status)}
	}
}

// Beyond the OTLP retryable codes, 401/403/404 are retried so a destination
// being fixed does not lose queued traces.
func retryableStatus(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusRequestTimeout,
		http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return status >= http.StatusInternalServerError
}

func (s *Sender) collect(
	ctx context.Context, deliveries []executionstore.OtelExportDelivery, variant oteltrace.Variant,
) ([]oteltrace.Span, error) {
	var spans []oteltrace.Span
	for _, delivery := range deliveries {
		turn, err := s.store.LoadTurnTraceForExport(ctx, delivery.AgentID, delivery.TurnID)
		if errors.Is(err, storeerr.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		spans = append(spans, oteltrace.Build(turn, variant)...)
	}
	return spans, nil
}
