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

	attemptTimeout = 30 * time.Second
	buildTimeout   = 30 * time.Second
	// isolateTimeout bounds resending a refused batch one trace at a time, so
	// a whole attempt still fits inside the store's claim duration.
	isolateTimeout    = 45 * time.Second
	storeTimeout      = 5 * time.Second
	idleInterval      = time.Second
	responseReadLimit = 64 * 1024
)

type Store interface {
	ClaimOtelExportDeliveries(ctx context.Context, perOrgLimit, batchLimit int) (executionstore.OtelExportBatch, error)
	CompleteOtelExportDeliveries(ctx context.Context, claimToken uuid.UUID) error
	RetryOtelExportDeliveries(
		ctx context.Context, claimToken uuid.UUID, delay time.Duration, deliveryIDs []uuid.UUID,
	) error
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

// trace is the spans of one delivery's turn.
type trace struct {
	deliveryID uuid.UUID
	spans      []oteltrace.Span
}

// sendResult accounts for every delivery of a batch: those in retry are
// attempted again, all others are finished.
type sendResult struct {
	delivered, dropped int
	rejectedSpans      int64
	retry              []uuid.UUID
	err                error
}

func (s *Sender) deliver(ctx context.Context, batch executionstore.OtelExportBatch) {
	ctx, event := logent.OtelExportBatchAttempt(logpkg.WithLogger(ctx, s.log), batch)
	defer event.Done(ctx)
	result := s.send(ctx, batch)
	if ctx.Err() != nil {
		logent.OtelExportBatchResult(ctx, "canceled", "none", ctx.Err())
		return
	}
	logent.OtelExportBatchCounts(ctx, result.delivered, result.dropped, len(result.retry), result.rejectedSpans)
	updateCtx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	if len(result.retry) > 0 {
		if retryErr := s.store.RetryOtelExportDeliveries(
			updateCtx, batch.ClaimToken, retryDelay(batch.MaxAttempt()), result.retry); retryErr != nil {
			logent.OtelExportStoreFailed(ctx, "retry", retryErr)
			logent.OtelExportBatchResult(ctx, "retry_failed", "local", result.err)
			return
		}
		if len(result.retry) == len(batch.Deliveries) {
			logent.OtelExportBatchResult(ctx, "retry_scheduled", failureCause(result.err), result.err)
			return
		}
	}
	// Retried deliveries no longer carry the claim, so this finishes the rest.
	if completeErr := s.store.CompleteOtelExportDeliveries(updateCtx, batch.ClaimToken); completeErr != nil {
		logent.OtelExportStoreFailed(ctx, "complete", completeErr)
		logent.OtelExportBatchResult(ctx, "complete_failed", "local", result.err)
		return
	}
	switch {
	case len(result.retry) > 0:
		logent.OtelExportBatchResult(ctx, "partially_retried", failureCause(result.err), result.err)
	case result.dropped > 0:
		logent.OtelExportBatchResult(ctx, "dropped", failureCause(result.err), result.err)
	case result.rejectedSpans > 0:
		logent.OtelExportBatchResult(ctx, "partially_rejected", "recipient", result.err)
	case result.delivered > 0:
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

func (s *Sender) send(ctx context.Context, batch executionstore.OtelExportBatch) sendResult {
	retryAll := func(err error) sendResult {
		result := sendResult{retry: make([]uuid.UUID, len(batch.Deliveries)), err: err}
		for i, delivery := range batch.Deliveries {
			result.retry[i] = delivery.ID
		}
		return result
	}
	buildCtx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	destination, err := s.store.GetOtelExportDestination(buildCtx, batch.OrgID, batch.ExporterID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return sendResult{}
	}
	if err != nil {
		return retryAll(err)
	}
	logent.OtelExportDestination(ctx, destination)
	traces, err := s.collect(buildCtx, batch.Deliveries, destination.Variant)
	if err != nil {
		return retryAll(err)
	}
	if len(traces) == 0 {
		return sendResult{}
	}
	rejectedSpans, err := s.post(ctx, destination, traces)
	var rejected rejectedError
	switch {
	case err == nil:
		return sendResult{delivered: len(traces), rejectedSpans: rejectedSpans, err: partialError(rejectedSpans)}
	case !errors.As(err, &rejected):
		return retryAll(err)
	case len(traces) == 1:
		return sendResult{dropped: 1, err: err}
	}
	// The endpoint refused the request as a whole. Resend each trace alone so
	// one oversized or malformed trace is dropped without the others.
	result := sendResult{}
	isolateCtx, cancelIsolate := context.WithTimeout(ctx, isolateTimeout)
	defer cancelIsolate()
	for i, single := range traces {
		rejectedSpans, err := s.post(isolateCtx, destination, traces[i:i+1])
		switch {
		case err == nil:
			result.delivered++
			result.rejectedSpans += rejectedSpans
		case errors.As(err, &rejected):
			result.dropped++
			result.err = err
		default:
			result.retry = append(result.retry, single.deliveryID)
			if result.err == nil {
				result.err = err
			}
		}
	}
	if result.err == nil {
		result.err = partialError(result.rejectedSpans)
	}
	return result
}

func partialError(rejectedSpans int64) error {
	if rejectedSpans == 0 {
		return nil
	}
	return rejectedError{fmt.Errorf("otel endpoint rejected %d spans", rejectedSpans)}
}

// post sends the traces in one request and reports how many spans a
// successful response says the endpoint rejected.
func (s *Sender) post(
	ctx context.Context, destination executionstore.OtelExportDestination, traces []trace,
) (int64, error) {
	var spans []oteltrace.Span
	for _, trace := range traces {
		spans = append(spans, trace.spans...)
	}
	body, contentType, err := oteltrace.NewExport(spans).Encode(destination.Encoding)
	if err != nil {
		return 0, fmt.Errorf("encode otel export: %w", err)
	}
	logent.OtelExportPayload(ctx, len(spans), len(body))
	attemptCtx, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)
	defer cancelAttempt()
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, destination.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("create otel export request failed")
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
		return 0, recipientError{fmt.Errorf("post otel export: %w", err)}
	}
	logent.OtelExportResponse(ctx, response.StatusCode)
	defer func() { _ = response.Body.Close() }()
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, responseReadLimit))
	switch status := response.StatusCode; {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		rejectedSpans, message := oteltrace.ParsePartialSuccess(response.Header.Get("Content-Type"), responseBody)
		if rejectedSpans > 0 {
			logent.OtelExportPartialSuccess(ctx, message)
		}
		return rejectedSpans, nil
	case retryableStatus(status):
		return 0, recipientError{fmt.Errorf("otel endpoint returned HTTP %d", status)}
	default:
		return 0, rejectedError{fmt.Errorf("otel endpoint returned HTTP %d", status)}
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
) ([]trace, error) {
	var traces []trace
	for _, delivery := range deliveries {
		turn, err := s.store.LoadTurnTraceForExport(ctx, delivery.AgentID, delivery.TurnID)
		if errors.Is(err, storeerr.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if spans := oteltrace.Build(turn, variant); len(spans) > 0 {
			traces = append(traces, trace{deliveryID: delivery.ID, spans: spans})
		}
	}
	return traces, nil
}
