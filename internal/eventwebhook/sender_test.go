package eventwebhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/publicevents"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type testStore struct {
	retryResult executionstore.EventWebhookRetryResult
	target      executionstore.EventWebhookTarget
	record      executionstore.AgentEventReadRecord
	secret      string
	secretErr   error
	pending     chan executionstore.EventWebhookDelivery
	completed   atomic.Int64
	retried     atomic.Int64
	claims      atomic.Int64
	perOrgLimit atomic.Int64
}

func (s *testStore) GetAgentEventWebhookTarget(context.Context, uuid.UUID) (executionstore.EventWebhookTarget, error) {
	return s.target, nil
}

func (s *testStore) GetAgentEventForWebhook(
	_ context.Context, _, _ uuid.UUID, sequence int64,
) (executionstore.AgentEventReadRecord, error) {
	if sequence != s.record.Sequence {
		return executionstore.AgentEventReadRecord{}, storeerr.ErrNotFound
	}
	return s.record, nil
}

func (s *testStore) ReadEventWebhookSigningSecret(context.Context, executionstore.EventWebhookTarget) (string, error) {
	return s.secret, s.secretErr
}

func (s *testStore) ClaimEventWebhookDelivery(
	_ context.Context, perOrgLimit int,
) (executionstore.EventWebhookDelivery, error) {
	s.claims.Add(1)
	s.perOrgLimit.Store(int64(perOrgLimit))
	select {
	case delivery := <-s.pending:
		return delivery, nil
	default:
		return executionstore.EventWebhookDelivery{}, storeerr.ErrNotFound
	}
}

func (s *testStore) CompleteEventWebhookDelivery(context.Context, uuid.UUID, uuid.UUID) error {
	s.completed.Add(1)
	return nil
}

func (s *testStore) RetryEventWebhookDelivery(
	context.Context, uuid.UUID, uuid.UUID, time.Duration,
) (executionstore.EventWebhookRetryResult, error) {
	s.retried.Add(1)
	return s.retryResult, nil
}

type testTransport func(*http.Request) (*http.Response, error)

func (transport testTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func testSender() (*Sender, *testStore, executionstore.EventWebhookDelivery) {
	store := &testStore{
		target:  executionstore.EventWebhookTarget{ProjectID: uuid.New(), URL: "https://example.com/events"},
		pending: make(chan executionstore.EventWebhookDelivery, 32),
	}
	toolID, state := uuid.New(), "ready"
	delivery := executionstore.EventWebhookDelivery{
		ID: uuid.New(), AgentID: uuid.New(), OrgID: uuid.New(), ToolCallID: &toolID, ToolState: &state,
		ClaimToken: uuid.New(), AttemptCount: 1,
	}
	return New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), 8, 7), store, delivery
}

func TestSenderSignsExactBodyAndKeepsRetryIdentity(t *testing.T) {
	sender, store, delivery := testSender()
	key := []byte("12345678901234567890123456789012")
	store.secret = "whsec_" + base64.StdEncoding.EncodeToString(key)
	store.target.SigningSecretID = "secret"
	var bodies [][]byte
	sender.client = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		bodies = append(bodies, body)
		require.Equal(t, delivery.ID.String(), req.Header.Get("Webhook-Id"))
		mac := hmac.New(sha256.New, key)
		_, err = io.WriteString(mac, delivery.ID.String()+"."+req.Header.Get("Webhook-Timestamp")+".")
		require.NoError(t, err)
		_, err = mac.Write(body)
		require.NoError(t, err)
		require.Equal(t, "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)), req.Header.Get("Webhook-Signature"))
		code := http.StatusServiceUnavailable
		if len(bodies) == 2 {
			code = http.StatusNoContent
		}
		return &http.Response{StatusCode: code, Body: http.NoBody}, nil
	})}
	sender.deliver(t.Context(), delivery)
	require.Equal(t, int64(1), store.retried.Load())
	require.Zero(t, store.completed.Load())
	sender.deliver(t.Context(), delivery)
	require.Equal(t, int64(1), store.completed.Load())
	require.Equal(t, bodies[0], bodies[1])
	var payload struct {
		Event string `json:"event"`
		Data  struct {
			State string `json:"state"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(bodies[0], &payload))
	require.Equal(t, "tool_call_update", payload.Event)
	require.Equal(t, "ready", payload.Data.State)
}

func TestSenderRetryUsesCurrentWebhookConfiguration(t *testing.T) {
	sender, store, delivery := testSender()
	store.target.SigningSecretID = "secret"
	store.secret = base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	var calls int
	sender.client = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, delivery.ID.String(), req.Header.Get("Webhook-Id"))
		if calls == 1 {
			require.Equal(t, "https://example.com/events", req.URL.String())
			require.NotEmpty(t, req.Header.Get("Webhook-Signature"))
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody}, nil
		}
		require.Equal(t, "https://example.com/new", req.URL.String())
		require.Empty(t, req.Header.Get("Webhook-Signature"))
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	})}
	sender.deliver(t.Context(), delivery)
	require.Equal(t, int64(1), store.retried.Load())
	store.target.URL = "https://example.com/new"
	store.target.SigningSecretID = ""
	sender.deliver(t.Context(), delivery)
	require.Equal(t, 2, calls)
	require.Equal(t, int64(1), store.completed.Load())
}

func TestSenderUsesExistingTimelinePayload(t *testing.T) {
	sender, store, delivery := testSender()
	store.record = executionstore.AgentEventReadRecord{
		ID: uuid.New(), OrgID: uuid.New(), ProjectID: store.target.ProjectID, AgentID: delivery.AgentID,
		TurnID: uuid.New(), Sequence: 17, EventKind: "context_checkpoint",
		ContextCheckpointID: uuid.New(), CheckpointSummary: "summary", CreatedAt: time.Now(),
	}
	delivery.EventSequence = &store.record.Sequence
	delivery.ToolCallID, delivery.ToolState = nil, nil
	sender.client = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		require.Empty(t, req.Header.Get("Webhook-Signature"))
		var body struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
		expected, err := publicevents.EventFromReadRecord(store.record)
		require.NoError(t, err)
		encoded, err := json.Marshal(expected)
		require.NoError(t, err)
		require.Equal(t, "context_checkpoint", body.Event)
		require.JSONEq(t, string(encoded), string(body.Data))
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})}
	require.NoError(t, sender.send(t.Context(), delivery))
	sequence := int64(18)
	delivery.EventSequence = &sequence
	require.ErrorIs(t, sender.send(t.Context(), delivery), storeerr.ErrNotFound)
}

func TestSenderSkipsRemovedWebhookAndRetriesSigningErrors(t *testing.T) {
	for _, scenario := range []string{"removed", "secret unavailable", "invalid secret"} {
		t.Run(scenario, func(t *testing.T) {
			sender, store, delivery := testSender()
			sender.client = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
				t.Error("unexpected HTTP request")
				return nil, errors.New("unexpected request")
			})}
			switch scenario {
			case "removed":
				store.target.URL = ""
			case "secret unavailable":
				store.target.SigningSecretID = "secret"
				store.secretErr = errors.New("access revoked")
			case "invalid secret":
				store.target.SigningSecretID = "secret"
				store.secret = "invalid"
			}
			sender.deliver(t.Context(), delivery)
			if scenario == "secret unavailable" || scenario == "invalid secret" {
				require.Equal(t, int64(1), store.retried.Load())
				require.Zero(t, store.completed.Load())
			} else {
				require.Equal(t, int64(1), store.completed.Load())
			}
		})
	}
}

func TestSenderBlocksPrivateDestinationsAndRedirects(t *testing.T) {
	var calls atomic.Int64
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()
	sender, store, delivery := testSender()
	store.target.URL = destination.URL
	require.Error(t, sender.send(t.Context(), delivery))
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	store.target.URL = redirect.URL
	sender.client = outboundhttp.CloneWithoutRedirects(redirect.Client())
	require.ErrorContains(t, sender.send(t.Context(), delivery), "HTTP 307")
	require.Zero(t, calls.Load())
}

func TestSenderTimeoutAndShutdownLeaveClaimsRecoverable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender, store, delivery := testSender()
		sender.client = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		})}
		start := time.Now()
		sender.deliver(t.Context(), delivery)
		require.Equal(t, attemptTimeout, time.Since(start))
		require.Equal(t, int64(1), store.retried.Load())
		ctx, cancel := context.WithCancel(t.Context())
		for range sender.maxInFlight + 2 {
			delivery.OrgID = uuid.New()
			store.pending <- delivery
		}
		done := make(chan struct{})
		go func() {
			sender.Run(ctx)
			close(done)
		}()
		synctest.Wait()
		require.Len(t, store.pending, 2)
		cancel()
		<-done
		require.Zero(t, store.completed.Load())
		require.Equal(t, int64(1), store.retried.Load())
	})
}

func TestRetryDelayIsBounded(t *testing.T) {
	for _, attempt := range []int32{0, 1, 2, 6, 1000} {
		for range 100 {
			delay := retryDelay(attempt)
			require.GreaterOrEqual(t, delay, time.Second)
			require.LessOrEqual(t, delay, time.Minute)
		}
	}
}

func TestSenderPollsOnceWhenIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender, store, _ := testSender()
		sender.maxInFlight = 128
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { sender.Run(ctx); close(done) }()
		synctest.Wait()
		require.Equal(t, int64(1), store.claims.Load())
		require.Equal(t, int64(7), store.perOrgLimit.Load())
		<-time.After(idleInterval)
		synctest.Wait()
		require.Equal(t, int64(2), store.claims.Load())
		cancel()
		<-done
	})
}

func TestSenderRefillsAvailableCapacity(t *testing.T) {
	for _, capacity := range []int{1, 2, 8, 128} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sender, store, delivery := testSender()
				sender.maxInFlight = capacity
				store.pending = make(chan executionstore.EventWebhookDelivery, capacity+1)
				release := make(chan struct{})
				sender.client = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
					select {
					case <-release:
						return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
					case <-req.Context().Done():
						return nil, req.Context().Err()
					}
				})}
				for range capacity + 1 {
					delivery.ID = uuid.New()
					store.pending <- delivery
				}
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				start := time.Now()
				go func() { sender.Run(ctx); close(done) }()
				synctest.Wait()
				require.Len(t, store.pending, 1)
				require.Equal(t, int64(capacity), store.claims.Load())
				release <- struct{}{}
				synctest.Wait()
				require.Empty(t, store.pending)
				require.Equal(t, int64(capacity+1), store.claims.Load())
				require.Equal(t, int64(1), store.completed.Load())
				require.Zero(t, time.Since(start))
				cancel()
				<-done
			})
		})
	}
}

func TestSenderCompletionWakesIdleLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender, store, delivery := testSender()
		release := make(chan struct{})
		sender.client = &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
			select {
			case <-release:
				return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		})}
		store.pending <- delivery
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		start := time.Now()
		go func() { sender.Run(ctx); close(done) }()
		synctest.Wait()
		require.Equal(t, int64(2), store.claims.Load())
		store.pending <- delivery
		release <- struct{}{}
		synctest.Wait()
		require.Empty(t, store.pending)
		require.Equal(t, int64(1), store.completed.Load())
		require.Zero(t, time.Since(start))
		cancel()
		<-done
	})
}

func TestSenderDrainsResponseForConnectionReuse(t *testing.T) {
	var connections atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		select {
		case <-time.After(100 * time.Millisecond):
			_, _ = io.WriteString(w, "ok")
		case <-req.Context().Done():
		}
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()
	sender, store, delivery := testSender()
	sender.client = server.Client()
	store.target.URL = server.URL
	for range 3 {
		sender.deliver(t.Context(), delivery)
	}
	require.Equal(t, int64(3), store.completed.Load())
	require.Equal(t, int64(1), connections.Load())
}

func TestSenderBoundsResponseDrainAndPreservesAcknowledgment(t *testing.T) {
	for _, fails := range []bool{false, true} {
		sender, store, delivery := testSender()
		body := strings.NewReader(strings.Repeat("x", responseDrainLimit+1))
		sender.client = &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
			var reader io.Reader = body
			if fails {
				reader = iotest.ErrReader(errors.New("body read failed"))
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(reader)}, nil
		})}
		sender.deliver(t.Context(), delivery)
		require.Equal(t, int64(1), store.completed.Load())
		require.Zero(t, store.retried.Load())
		if !fails {
			require.Equal(t, 1, body.Len())
		}
	}
}
