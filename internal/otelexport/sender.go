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
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const (
	MaxInFlight = 16
	PerOrgLimit = 4

	// attemptTimeout bounds loading, building and posting one turn; it stays
	// inside the store's claim duration.
	attemptTimeout    = 30 * time.Second
	storeTimeout      = 5 * time.Second
	idleInterval      = time.Second
	responseReadLimit = 64 * 1024
	// maxRequestBytes bounds one encoded export request. The trace builder
	// already bounds content, so a larger request is dropped, not sent.
	maxRequestBytes = 32 * 1024 * 1024
)

type Store interface {
	ClaimOtelExportDelivery(ctx context.Context, perOrgLimit int) (executionstore.OtelExportDelivery, error)
	CompleteOtelExportDelivery(ctx context.Context, id, claimToken uuid.UUID) error
	RetryOtelExportDelivery(
		ctx context.Context, id, claimToken uuid.UUID, delay time.Duration,
	) (executionstore.OtelExportDeliveryRetryResult, error)
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

// acknowledgmentError is a success response whose body could not be read as an
// OTLP acknowledgment, so how many spans the endpoint kept is unknown.
type acknowledgmentError struct{ error }

func (e acknowledgmentError) Unwrap() error { return e.error }

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
	var deliveries sync.WaitGroup
	defer deliveries.Wait()
	for ctx.Err() == nil {
		if inFlight < MaxInFlight {
			claimCtx, cancel := context.WithTimeout(ctx, storeTimeout)
			delivery, err := s.store.ClaimOtelExportDelivery(claimCtx, PerOrgLimit)
			cancel()
			if err == nil {
				inFlight++
				deliveries.Go(func() {
					s.deliver(ctx, delivery)
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

func (s *Sender) deliver(ctx context.Context, delivery executionstore.OtelExportDelivery) {
	ctx, event := logent.OtelExportDeliveryAttempt(logpkg.WithLogger(ctx, s.log), delivery)
	defer event.Done(ctx)
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	outcome, err := s.send(attemptCtx, delivery)
	cancel()
	if ctx.Err() != nil {
		logent.OtelExportDeliveryResult(ctx, "canceled", "none", ctx.Err(), time.Time{})
		return
	}
	updateCtx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	var retryAt time.Time
	if outcome == outcomeRetry {
		retry, retryErr := s.store.RetryOtelExportDelivery(
			updateCtx, delivery.ID, delivery.ClaimToken, retryDelay(delivery.AttemptCount))
		if retryErr != nil {
			logent.OtelExportStoreFailed(ctx, "retry", retryErr)
			logent.OtelExportDeliveryResult(ctx, "retry_failed", "local", err, time.Time{})
			return
		}
		retryAt = retry.NextAttemptAt
		if retry.GaveUp {
			outcome = outcomeGaveUp
		}
	} else if completeErr := s.store.CompleteOtelExportDelivery(
		updateCtx, delivery.ID, delivery.ClaimToken); completeErr != nil {
		logent.OtelExportStoreFailed(ctx, "complete", completeErr)
		logent.OtelExportDeliveryResult(ctx, "complete_failed", "local", err, time.Time{})
		return
	}
	logent.OtelExportDeliveryResult(ctx, outcome, failureCause(err), err, retryAt)
}

func failureCause(err error) string {
	var recipient recipientError
	var rejected rejectedError
	var acknowledgment acknowledgmentError
	switch {
	case err == nil:
		return "none"
	case errors.As(err, &recipient), errors.As(err, &rejected), errors.As(err, &acknowledgment):
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

// A delivery's outcome is also its log label. Only outcomeRetry keeps the
// delivery queued.
const (
	outcomeCompleted         = "completed"
	outcomeSkipped           = "skipped"
	outcomeDropped           = "dropped"
	outcomePartiallyRejected = "partially_rejected"
	// outcomeUnacknowledged is a success status with an unreadable or
	// oversized acknowledgment. OTLP makes it non-retryable, and it is not
	// counted as delivered.
	outcomeUnacknowledged = "unacknowledged"
	outcomeRetry          = "retry_scheduled"
	outcomeGaveUp         = "gave_up"
)

func (s *Sender) send(ctx context.Context, delivery executionstore.OtelExportDelivery) (string, error) {
	destination, err := s.store.GetOtelExportDestination(ctx, delivery.OrgID, delivery.ExporterID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return outcomeSkipped, nil
	}
	if err != nil {
		return outcomeRetry, err
	}
	logent.OtelExportDestination(ctx, destination)
	turn, err := s.store.LoadTurnTraceForExport(ctx, delivery.AgentID, delivery.TurnID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return outcomeSkipped, nil
	}
	if err != nil {
		return outcomeRetry, err
	}
	traces := oteltrace.Build(turn, destination.Variant)
	if traces.SpanCount() == 0 {
		return outcomeSkipped, nil
	}
	rejectedSpans, err := s.post(ctx, destination, traces)
	var rejected rejectedError
	var acknowledgment acknowledgmentError
	switch {
	case errors.As(err, &rejected):
		return outcomeDropped, err
	case errors.As(err, &acknowledgment):
		return outcomeUnacknowledged, err
	case err != nil:
		return outcomeRetry, err
	case rejectedSpans > 0:
		// The endpoint kept the other spans, so the turn is not sent again.
		return outcomePartiallyRejected, rejectedError{fmt.Errorf("otel endpoint rejected %d spans", rejectedSpans)}
	}
	return outcomeCompleted, nil
}

// post sends the trace and reports how many spans a successful response says
// the endpoint rejected.
func (s *Sender) post(
	ctx context.Context, destination executionstore.OtelExportDestination, traces ptrace.Traces,
) (int64, error) {
	body, contentType, err := oteltrace.Encode(traces, destination.Encoding)
	if err != nil {
		return 0, fmt.Errorf("encode otel export: %w", err)
	}
	logent.OtelExportPayload(ctx, traces.SpanCount(), len(body))
	if len(body) > maxRequestBytes {
		return 0, rejectedError{fmt.Errorf("otel export request of %d bytes exceeds %d", len(body), maxRequestBytes)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, destination.Endpoint, bytes.NewReader(body))
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
	switch status := response.StatusCode; {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		responseBody, err := io.ReadAll(io.LimitReader(response.Body, responseReadLimit+1))
		if err != nil {
			return 0, acknowledgmentError{errors.New("read otel export response failed")}
		}
		if len(responseBody) > responseReadLimit {
			return 0, acknowledgmentError{fmt.Errorf("otel export response exceeds %d bytes", responseReadLimit)}
		}
		rejectedSpans, err := oteltrace.ParsePartialSuccess(
			response.Header.Get("Content-Type"), destination.Encoding, responseBody)
		if err != nil {
			// The decoder's error can quote the body, so it is not kept.
			return 0, acknowledgmentError{errors.New("otel export response is not an OTLP acknowledgment")}
		}
		if rejectedSpans > 0 {
			logent.OtelExportPartialSuccess(ctx, rejectedSpans)
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
