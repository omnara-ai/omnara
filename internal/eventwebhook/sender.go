package eventwebhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	webhookconfig "github.com/omnara-ai/omnara/internal/eventwebhook/config"
	"github.com/omnara-ai/omnara/internal/httpapi/publicevents"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/observability/metrics"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

const (
	attemptTimeout     = 5 * time.Second
	storeTimeout       = 5 * time.Second
	idleInterval       = time.Second
	responseDrainLimit = 4096
)

type Store interface {
	GetEventWebhookEndpoint(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (executionstore.EventWebhookEndpoint, error)
	GetAgentEventForWebhook(context.Context, uuid.UUID, uuid.UUID, int64) (executionstore.AgentEventReadRecord, error)
	GetCustomToolCallInput(
		context.Context, uuid.UUID, uuid.UUID, uuid.UUID,
	) (executionstore.CustomToolCallInput, bool, error)
	ClaimEventWebhookDelivery(context.Context, int) (executionstore.EventWebhookDelivery, error)
	CompleteEventWebhookDelivery(context.Context, uuid.UUID, uuid.UUID) error
	RetryEventWebhookDelivery(
		context.Context, uuid.UUID, uuid.UUID, time.Duration,
	) (executionstore.EventWebhookDeliveryRetryResult, error)
	ReadEventWebhookSigningSecret(context.Context, executionstore.EventWebhookEndpoint) (string, error)
}

type Sender struct {
	maxInFlight int
	perOrgLimit int
	store       Store
	client      *http.Client
	log         *slog.Logger
	recorder    *metrics.EventWebhookRecorder
}

type recipientError struct{ error }

func (e recipientError) Unwrap() error { return e.error }

type configurationError struct{ error }

func (e configurationError) Unwrap() error { return e.error }

func New(
	store Store, log *slog.Logger, maxInFlight, perOrgLimit int,
	recorder *metrics.EventWebhookRecorder, httpRecorder *metrics.HTTPClientRecorder, privateEndpoints *ssrf.Allowlist,
) *Sender {
	return &Sender{store: store, log: log, recorder: recorder, maxInFlight: maxInFlight, perOrgLimit: perOrgLimit,
		client: metrics.NewObservedHTTPClient(
			outboundhttp.NewPublicClient(
				outboundhttp.PublicClientOptions{Timeout: attemptTimeout, Allowlist: privateEndpoints},
			), httpRecorder,
			metrics.WithHTTPClientHostLabel("event_webhook"), metrics.WithHTTPClientPathLabel("/*"))}
}

func (s *Sender) Run(ctx context.Context) {
	defer s.client.CloseIdleConnections()
	s.recorder.RecordCapacity(s.maxInFlight)
	defer s.recorder.RecordCapacity(0)
	inFlight := 0
	completed := make(chan struct{}, s.maxInFlight)
	var deliveries sync.WaitGroup
	defer deliveries.Wait()
	for ctx.Err() == nil {
		if inFlight < s.maxInFlight {
			claimCtx, cancel := context.WithTimeout(ctx, storeTimeout)
			delivery, err := s.store.ClaimEventWebhookDelivery(claimCtx, s.perOrgLimit)
			cancel()
			if err == nil {
				inFlight++
				finished := s.recorder.StartDelivery()
				deliveries.Go(func() {
					s.deliver(ctx, delivery)
					finished()
					completed <- struct{}{}
				})
				continue
			}
			if !errors.Is(err, storeerr.ErrNotFound) && ctx.Err() == nil {
				logent.EventWebhookStoreFailed(logpkg.WithLogger(ctx, s.log), "claim", err)
			}
		}
		var idle <-chan time.Time
		if inFlight < s.maxInFlight {
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

func (s *Sender) deliver(ctx context.Context, delivery executionstore.EventWebhookDelivery) {
	ctx, event := logent.EventWebhookDeliveryAttempt(logpkg.WithLogger(ctx, s.log), delivery)
	defer event.Done(ctx)
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	acknowledged, err := s.send(attemptCtx, delivery)
	if acknowledged {
		s.recorder.RecordAcknowledged(delivery.CreatedAt, delivery.AttemptCount)
	}
	cancel()
	if ctx.Err() != nil {
		s.recordAttempt(ctx, "canceled", ctx.Err(), time.Time{})
		return
	}
	updateCtx, updateCancel := context.WithTimeout(ctx, storeTimeout)
	defer updateCancel()
	if err != nil {
		retry, retryErr := s.store.RetryEventWebhookDelivery(
			updateCtx, delivery.ID, delivery.ClaimToken, retryDelay(delivery.AttemptCount),
		)
		if retryErr != nil {
			logent.EventWebhookStoreFailed(ctx, "retry", retryErr)
			s.recordAttempt(ctx, "retry_failed", err, time.Time{})
			return
		}
		outcome := "retry_scheduled"
		if retry.GaveUp {
			outcome = "gave_up"
		}
		s.recordAttempt(ctx, outcome, err, retry.NextAttemptAt)
		return
	}
	completeErr := s.store.CompleteEventWebhookDelivery(updateCtx, delivery.ID, delivery.ClaimToken)
	if completeErr != nil {
		logent.EventWebhookStoreFailed(ctx, "complete", completeErr)
		s.recordAttempt(ctx, "complete_failed", nil, time.Time{})
	} else {
		outcome := "completed"
		if !acknowledged {
			outcome = "skipped"
		}
		s.recordAttempt(ctx, outcome, nil, time.Time{})
	}
}

func (s *Sender) recordAttempt(ctx context.Context, outcome string, err error, retryAt time.Time) {
	cause := "none"
	if outcome == "retry_failed" || outcome == "complete_failed" {
		cause = "local"
	} else if err != nil && outcome != "canceled" {
		cause = "local"
		var recipient recipientError
		var configuration configurationError
		if errors.As(err, &recipient) {
			cause = "recipient"
		} else if errors.As(err, &configuration) {
			cause = "configuration"
		}
	}
	s.recorder.RecordAttempt(outcome, cause)
	logent.EventWebhookDeliveryAttemptResult(ctx, outcome, cause, err, retryAt)
}

func retryDelay(attempt int32) time.Duration {
	ceiling := 2 * time.Second
	for range min(max(attempt-1, 0), 5) {
		ceiling = min(ceiling*2, time.Minute)
	}
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)))
}

func (s *Sender) send(ctx context.Context, delivery executionstore.EventWebhookDelivery) (bool, error) {
	endpoint, err := s.store.GetEventWebhookEndpoint(ctx, delivery.OrgID, delivery.WebhookID, delivery.AgentID)
	if err != nil {
		return false, err
	}
	logent.EventWebhookEndpoint(ctx, endpoint)
	if endpoint.URL == "" {
		return false, nil
	}
	var data any
	var event string
	if delivery.EventSequence != nil {
		record, readErr := s.store.GetAgentEventForWebhook(ctx, endpoint.ProjectID, delivery.AgentID, *delivery.EventSequence)
		if readErr != nil {
			return false, readErr
		}
		event = record.EventKind
		data, err = publicevents.EventFromReadRecord(record)
	} else {
		event = "tool_call_update"
		update := notifications.ToolCallUpdatedCommitted{
			AgentID: delivery.AgentID, ToolCallID: *delivery.ToolCallID, State: *delivery.ToolState,
			InteractionUpdate: delivery.InteractionUpdate,
		}
		if *delivery.ToolState == string(executionstore.ToolCallStateReady) {
			call, found, readErr := s.store.GetCustomToolCallInput(
				ctx, endpoint.ProjectID, delivery.AgentID, *delivery.ToolCallID,
			)
			if readErr != nil {
				return false, readErr
			}
			if found {
				update.Name, update.Input = call.Name, call.Input
			}
		}
		data, err = publicevents.ToolCallUpdate(update, publicevents.AgentContext{
			OrgID: endpoint.OrgID, ProjectID: endpoint.ProjectID,
			AgentID: delivery.AgentID, AgentProfileID: endpoint.AgentProfileID,
			ParentAgentID: endpoint.ParentAgentID, RootAgentID: endpoint.RootAgentID,
		})
	}
	if err != nil {
		return false, err
	}
	logent.EventWebhookEventKind(ctx, event)
	body, err := json.Marshal(struct {
		Event string `json:"event"`
		Data  any    `json:"data"`
	}{Event: event, Data: data})
	if err != nil {
		return false, fmt.Errorf("encode event webhook: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL, bytes.NewReader(body))
	if err != nil {
		return false, errors.New("create event webhook request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Webhook-Id", delivery.ID.String())
	req.Header.Set("Webhook-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	if endpoint.SigningSecretID != uuid.Nil {
		secret, readErr := s.store.ReadEventWebhookSigningSecret(ctx, endpoint)
		if readErr != nil {
			if errors.Is(readErr, storeerr.ErrNotFound) {
				return false, configurationError{fmt.Errorf("event webhook signing secret unavailable: %w", readErr)}
			}
			return false, fmt.Errorf("read event webhook signing secret: %w", readErr)
		}
		signature, signErr := sign(secret, req.Header.Get("Webhook-Id"), req.Header.Get("Webhook-Timestamp"), body)
		if signErr != nil {
			return false, configurationError{signErr}
		}
		req.Header.Set("Webhook-Signature", signature)
	}
	response, err := s.client.Do(req)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return false, recipientError{fmt.Errorf("post event webhook: %w", err)}
	}
	logent.EventWebhookResponse(ctx, response.StatusCode)
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, responseDrainLimit))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return false, recipientError{fmt.Errorf("event webhook returned HTTP %d", response.StatusCode)}
	}
	return true, nil
}

func sign(secret, id, timestamp string, body []byte) (string, error) {
	key, err := webhookconfig.DecodeSigningKey(secret)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(id + "." + timestamp + "."))
	_, _ = mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}
