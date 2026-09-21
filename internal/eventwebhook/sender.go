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
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/httpapi/publicevents"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	attemptTimeout     = 5 * time.Second
	storeTimeout       = 5 * time.Second
	idleInterval       = time.Second
	responseDrainLimit = 4096
)

type Store interface {
	GetAgentEventWebhookTarget(context.Context, uuid.UUID) (executionstore.EventWebhookTarget, error)
	GetAgentEventForWebhook(context.Context, uuid.UUID, uuid.UUID, int64) (executionstore.AgentEventReadRecord, error)
	ClaimEventWebhookDelivery(context.Context, int) (executionstore.EventWebhookDelivery, error)
	CompleteEventWebhookDelivery(context.Context, uuid.UUID, uuid.UUID) error
	RetryEventWebhookDelivery(
		context.Context, uuid.UUID, uuid.UUID, time.Duration,
	) (executionstore.EventWebhookRetryResult, error)
	ReadEventWebhookSigningSecret(context.Context, executionstore.EventWebhookTarget) (string, error)
}

type Sender struct {
	maxInFlight int
	perOrgLimit int
	store       Store
	client      *http.Client
	log         *slog.Logger
}

func New(store Store, log *slog.Logger, maxInFlight, perOrgLimit int) *Sender {
	return &Sender{store: store, log: log, maxInFlight: maxInFlight, perOrgLimit: perOrgLimit,
		client: outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{Timeout: attemptTimeout})}
}

func (s *Sender) Run(ctx context.Context) {
	defer s.client.CloseIdleConnections()
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
				deliveries.Go(func() {
					s.deliver(ctx, delivery)
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
	ctx, event := logent.EventWebhookDelivery(logpkg.WithLogger(ctx, s.log), delivery)
	defer event.Done(ctx)
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	err := s.send(attemptCtx, delivery)
	cancel()
	if ctx.Err() != nil {
		logent.EventWebhookDeliveryResult(ctx, "canceled", ctx.Err(), time.Time{})
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
			logent.EventWebhookDeliveryResult(ctx, "retry_failed", err, time.Time{})
			return
		}
		outcome := "retry_scheduled"
		if retry.GaveUp {
			outcome = "gave_up"
		}
		logent.EventWebhookDeliveryResult(ctx, outcome, err, retry.NextAttemptAt)
		return
	}
	completeErr := s.store.CompleteEventWebhookDelivery(updateCtx, delivery.ID, delivery.ClaimToken)
	if completeErr != nil {
		logent.EventWebhookStoreFailed(ctx, "complete", completeErr)
		logent.EventWebhookDeliveryResult(ctx, "complete_failed", nil, time.Time{})
	} else {
		logent.EventWebhookDeliveryResult(ctx, "completed", nil, time.Time{})
	}
}

func retryDelay(attempt int32) time.Duration {
	ceiling := 2 * time.Second
	for range min(max(attempt-1, 0), 5) {
		ceiling = min(ceiling*2, time.Minute)
	}
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)))
}

func (s *Sender) send(ctx context.Context, delivery executionstore.EventWebhookDelivery) error {
	target, err := s.store.GetAgentEventWebhookTarget(ctx, delivery.AgentID)
	if err != nil {
		return err
	}
	logent.EventWebhookTarget(ctx, target)
	if target.URL == "" {
		return nil
	}
	var data any
	var event string
	if delivery.EventSequence != nil {
		record, readErr := s.store.GetAgentEventForWebhook(ctx, target.ProjectID, delivery.AgentID, *delivery.EventSequence)
		if readErr != nil {
			return readErr
		}
		event = record.EventKind
		data, err = publicevents.EventFromReadRecord(record)
	} else {
		event = "tool_call_update"
		data, err = publicevents.ToolCallUpdate(notifications.ToolCallUpdatedCommitted{
			AgentID: delivery.AgentID, ToolCallID: *delivery.ToolCallID, State: *delivery.ToolState,
		})
	}
	if err != nil {
		return err
	}
	logent.EventWebhookKind(ctx, event)
	body, err := json.Marshal(struct {
		Event string `json:"event"`
		Data  any    `json:"data"`
	}{Event: event, Data: data})
	if err != nil {
		return fmt.Errorf("encode event webhook: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("create event webhook request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Webhook-Id", delivery.ID.String())
	req.Header.Set("Webhook-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	if target.SigningSecretID != "" {
		secret, readErr := s.store.ReadEventWebhookSigningSecret(ctx, target)
		if readErr != nil {
			return fmt.Errorf("read event webhook signing secret: %w", readErr)
		}
		signature, signErr := sign(secret, req.Header.Get("Webhook-Id"), req.Header.Get("Webhook-Timestamp"), body)
		if signErr != nil {
			return signErr
		}
		req.Header.Set("Webhook-Signature", signature)
	}
	response, err := s.client.Do(req)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return fmt.Errorf("post event webhook: %w", err)
	}
	logent.EventWebhookResponse(ctx, response.StatusCode)
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, responseDrainLimit))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("event webhook returned HTTP %d", response.StatusCode)
	}
	return nil
}

func sign(secret, id, timestamp string, body []byte) (string, error) {
	key, err := agentconfig.DecodeEventWebhookSigningKey(secret)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(id + "." + timestamp + "."))
	_, _ = mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}
