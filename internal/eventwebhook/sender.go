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
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	perOrgConcurrencyDivisor = 8
	attemptTimeout           = 5 * time.Second
	storeTimeout             = 5 * time.Second
	idleInterval             = time.Second
	responseDrainLimit       = 4096
)

type Store interface {
	GetAgentEventWebhookTarget(context.Context, uuid.UUID) (executionstore.EventWebhookTarget, error)
	GetAgentEventForWebhook(context.Context, uuid.UUID, uuid.UUID, int64) (executionstore.AgentEventReadRecord, error)
	ClaimEventWebhookDelivery(context.Context, []uuid.UUID) (executionstore.EventWebhookDelivery, error)
	CompleteEventWebhookDelivery(context.Context, uuid.UUID, uuid.UUID) error
	RetryEventWebhookDelivery(context.Context, uuid.UUID, uuid.UUID, time.Duration) error
	ReadEventWebhookSigningSecret(context.Context, executionstore.EventWebhookTarget) (string, error)
}

type Sender struct {
	maxInFlight int
	store       Store
	client      *http.Client
	log         *slog.Logger
}

func New(store Store, log *slog.Logger, maxInFlight int) *Sender {
	return &Sender{store: store, log: log, maxInFlight: maxInFlight,
		client: outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{Timeout: attemptTimeout})}
}

func (s *Sender) Run(ctx context.Context) {
	defer s.client.CloseIdleConnections()
	perOrgLimit := max(1, s.maxInFlight/perOrgConcurrencyDivisor)
	activeByOrg := make(map[uuid.UUID]int)
	completed := make(chan uuid.UUID, s.maxInFlight)
	inFlight := 0
	var deliveries sync.WaitGroup
	defer deliveries.Wait()
	for ctx.Err() == nil {
		if inFlight < s.maxInFlight {
			var excluded []uuid.UUID
			for orgID, count := range activeByOrg {
				if count >= perOrgLimit {
					excluded = append(excluded, orgID)
				}
			}
			claimCtx, cancel := context.WithTimeout(ctx, storeTimeout)
			delivery, err := s.store.ClaimEventWebhookDelivery(claimCtx, excluded)
			cancel()
			if err == nil {
				inFlight++
				activeByOrg[delivery.OrgID]++
				deliveries.Go(func() {
					defer func() { completed <- delivery.OrgID }()
					s.deliver(ctx, delivery)
				})
				continue
			}
			if !errors.Is(err, storeerr.ErrNotFound) && ctx.Err() == nil {
				s.log.Warn("claim event webhook", "error", err)
			}
		}
		var timer *time.Timer
		var idle <-chan time.Time
		if inFlight < s.maxInFlight {
			timer = time.NewTimer(idleInterval)
			idle = timer.C
		}
		select {
		case <-ctx.Done():
		case orgID := <-completed:
			inFlight--
			activeByOrg[orgID]--
			if activeByOrg[orgID] == 0 {
				delete(activeByOrg, orgID)
			}
		case <-idle:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func (s *Sender) deliver(ctx context.Context, delivery executionstore.EventWebhookDelivery) {
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	err := s.send(attemptCtx, delivery)
	cancel()
	if ctx.Err() != nil {
		return
	}
	updateCtx, updateCancel := context.WithTimeout(ctx, storeTimeout)
	defer updateCancel()
	if err != nil {
		s.log.Warn("event webhook delivery failed", "delivery_id", delivery.ID, "agent_id", delivery.AgentID, "error", err)
	}
	if err != nil {
		delay := retryDelay(delivery.AttemptCount)
		err = s.store.RetryEventWebhookDelivery(updateCtx, delivery.ID, delivery.ClaimToken, delay)
	} else {
		err = s.store.CompleteEventWebhookDelivery(updateCtx, delivery.ID, delivery.ClaimToken)
	}
	if err != nil {
		s.log.Warn("update event webhook delivery", "delivery_id", delivery.ID, "error", err)
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
