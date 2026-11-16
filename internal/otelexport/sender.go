// Package otelexport delivers queued agent traces to customer OTLP/HTTP
// endpoints. One delivery is one finished turn, exported as one whole trace.
// It follows the event webhook sender: a transactional outbox claimed with a
// lease, bounded concurrency, and jittered retries. Unlike webhooks, one
// request carries a batch of traces for one exporter, as OTLP JSON or protobuf.
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
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/observability/metrics"
)

const (
	// MaxInFlight is how many export requests one worker process sends at once.
	MaxInFlight = 16
	// PerOrgLimit is a soft cap on concurrent export requests per organization
	// across workers.
	PerOrgLimit = 4
	// BatchLimit is the most turns one export request covers.
	BatchLimit = 25

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

// rejectedError marks a response the endpoint will never accept on retry.
type rejectedError struct{ error }

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
				s.log.Warn("claim otel export deliveries", "error", err)
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
	log := s.log.With("org_id", batch.OrgID, "otel_exporter_id", batch.ExporterID, "deliveries", len(batch.Deliveries))
	spans, err := s.send(ctx, batch)
	if ctx.Err() != nil {
		return
	}
	updateCtx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	var rejected rejectedError
	switch {
	case err == nil:
		log.Info("exported otel spans", "spans", spans)
	case errors.As(err, &rejected):
		// Retrying a rejected payload cannot succeed, so the batch is dropped.
		log.Warn("otel export rejected", "spans", spans, "error", err)
	default:
		var attempt int32
		for _, delivery := range batch.Deliveries {
			attempt = max(attempt, delivery.AttemptCount)
		}
		log.Warn("otel export failed", "spans", spans, "attempt", attempt, "error", err)
		if retryErr := s.store.RetryOtelExportDeliveries(updateCtx, batch.ClaimToken, retryDelay(attempt)); retryErr != nil {
			log.Warn("retry otel export deliveries", "error", retryErr)
		}
		return
	}
	if completeErr := s.store.CompleteOtelExportDeliveries(updateCtx, batch.ClaimToken); completeErr != nil {
		log.Warn("complete otel export deliveries", "error", completeErr)
	}
}

func retryDelay(attempt int32) time.Duration {
	ceiling := 2 * time.Second
	for range min(max(attempt-1, 0), 6) {
		ceiling = min(ceiling*2, 2*time.Minute)
	}
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)))
}

// send exports the batch in one request and reports how many spans it held.
// A nil error means the batch is finished, including when it had nothing to
// send or its exporter was deleted.
func (s *Sender) send(ctx context.Context, batch executionstore.OtelExportBatch) (int, error) {
	buildCtx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	destination, err := s.store.GetOtelExportDestination(buildCtx, batch.OrgID, batch.ExporterID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	spans, err := s.collect(buildCtx, batch.Deliveries, destination.Variant)
	if err != nil || len(spans) == 0 {
		return 0, err
	}
	body, contentType, err := oteltrace.NewExport(spans).Encode(destination.Encoding)
	if err != nil {
		return len(spans), rejectedError{fmt.Errorf("encode otel export: %w", err)}
	}
	attemptCtx, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)
	defer cancelAttempt()
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, destination.Endpoint, bytes.NewReader(body))
	if err != nil {
		return len(spans), rejectedError{errors.New("create otel export request failed")}
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
		return len(spans), fmt.Errorf("post otel export: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, responseDrainLimit))
	switch status := response.StatusCode; {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return len(spans), nil
	case retryableStatus(status):
		return len(spans), fmt.Errorf("otel endpoint returned HTTP %d", status)
	default:
		return len(spans), rejectedError{fmt.Errorf("otel endpoint returned HTTP %d", status)}
	}
}

// retryableStatus follows the OTLP/HTTP specification, plus 401, 403 and 404
// so a destination being fixed (a rotated key, a new project) does not lose
// the spans queued in the meantime.
func retryableStatus(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusRequestTimeout,
		http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return status >= http.StatusInternalServerError
}

// collect renders the whole trace of each delivery's turn.
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
