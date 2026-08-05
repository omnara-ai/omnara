package machinedaemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

func TestRetryBackoffDelayJittersEveryAttemptAndCaps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		failures int
		minimum  time.Duration
		maximum  time.Duration
	}{
		{
			name:     "first failure",
			failures: 1,
			minimum:  500 * time.Millisecond,
			maximum:  time.Second,
		},
		{
			name:     "second failure",
			failures: 2,
			minimum:  time.Second,
			maximum:  2 * time.Second,
		},
		{
			name:     "capped failure",
			failures: 10,
			minimum:  15 * time.Second,
			maximum:  30 * time.Second,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			delay := retryBackoffDelay(
				time.Second,
				30*time.Second,
				test.failures,
			)
			if delay < test.minimum || delay >= test.maximum {
				t.Fatalf(
					"retry delay = %s, want [%s, %s)",
					delay,
					test.minimum,
					test.maximum,
				)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		value     string
		wantDelay time.Duration
		wantOK    bool
	}{
		{
			name:      "seconds",
			value:     "12",
			wantDelay: 12 * time.Second,
			wantOK:    true,
		},
		{
			name:      "http date",
			value:     now.Add(45 * time.Second).Format(http.TimeFormat),
			wantDelay: 45 * time.Second,
			wantOK:    true,
		},
		{
			name:      "past http date",
			value:     now.Add(-time.Minute).Format(http.TimeFormat),
			wantDelay: 0,
			wantOK:    true,
		},
		{
			name:   "negative seconds",
			value:  "-1",
			wantOK: false,
		},
		{
			name:   "invalid",
			value:  "later",
			wantOK: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			delay, ok := parseRetryAfter(test.value, now)
			if ok != test.wantOK || delay != test.wantDelay {
				t.Fatalf(
					"parseRetryAfter(%q) = (%s, %t), want (%s, %t)",
					test.value,
					delay,
					ok,
					test.wantDelay,
					test.wantOK,
				)
			}
		})
	}
}

func TestRetryDelayHonorsServerMinimumWithClientJitter(t *testing.T) {
	t.Parallel()

	delay := retryDelayForDaemonError(
		time.Second,
		30*time.Second,
		1,
		httpStatusError{
			StatusCode: http.StatusTooManyRequests,
			RetryAfter: "10",
		},
	)
	if delay < 10*time.Second+500*time.Millisecond ||
		delay >= 11*time.Second {
		t.Fatalf(
			"retry delay = %s, want server minimum plus first jitter window",
			delay,
		)
	}
}

func TestWebSocketRetryAfterSurvivesRuntimeReconciliation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "17")
			http.Error(w, "temporarily rate limited", http.StatusTooManyRequests)
		},
	))
	t.Cleanup(server.Close)

	client := New(
		Config{
			APIURL:       server.URL,
			MachineToken: "machine-token",
		},
		server.Client(),
		nil,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	heartbeatAcknowledged, err := client.runRegisteredRuntime(
		ctx,
		DaemonRuntime{
			ID:                   "drt_rate_limited",
			NextHeartbeatAfterMS: 1_000,
		},
		localStartupState{},
	)
	if heartbeatAcknowledged {
		t.Fatal("rejected WebSocket handshake marked connection healthy")
	}
	if !errors.Is(err, errDaemonRuntimeUnregistered) {
		t.Fatalf("runtime error = %v, want runtime unregistered", err)
	}
	var statusErr httpStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("runtime error %v lost HTTP status", err)
	}
	if statusErr.StatusCode != http.StatusTooManyRequests ||
		statusErr.RetryAfter != "17" ||
		statusErr.Body != "temporarily rate limited" {
		t.Fatalf("HTTP status error = %+v", statusErr)
	}
}

func TestHeartbeatAcknowledgementMarksConnectionHealthy(t *testing.T) {
	t.Parallel()

	serverDone := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				serverDone <- err
				return
			}
			defer conn.CloseNow()

			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			var heartbeat daemonprotocol.Message
			if err := wsjson.Read(ctx, conn, &heartbeat); err != nil {
				serverDone <- fmt.Errorf("read heartbeat: %w", err)
				return
			}
			if heartbeat.Type != "heartbeat" {
				serverDone <- fmt.Errorf(
					"first daemon message type = %q, want heartbeat",
					heartbeat.Type,
				)
				return
			}
			if err := wsjson.Write(ctx, conn, daemonprotocol.Message{
				Type:                 "heartbeat_ack",
				NextHeartbeatAfterMS: 1_000,
			}); err != nil {
				serverDone <- fmt.Errorf("write heartbeat ack: %w", err)
				return
			}
			if err := wsjson.Write(ctx, conn, daemonprotocol.Message{
				Type:      "runtime_ended",
				ErrorCode: daemonprotocol.ErrorCodeInvalidRuntime,
			}); err != nil {
				serverDone <- fmt.Errorf("write runtime end: %w", err)
				return
			}
			serverDone <- nil
		},
	))
	t.Cleanup(server.Close)

	client := New(
		Config{
			APIURL:       server.URL,
			MachineToken: "machine-token",
			OmnaraHome:   t.TempDir(),
		},
		server.Client(),
		nil,
	)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_heartbeat_reset",
		MachineID:      "mch_heartbeat_reset",
	}
	t.Cleanup(func() {
		if err := client.closeState(); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	heartbeatAcknowledged, err := client.runRegisteredRuntime(
		ctx,
		DaemonRuntime{
			ID:                   "drt_heartbeat_reset",
			NextHeartbeatAfterMS: 10,
		},
		localStartupState{},
	)
	if serverErr := <-serverDone; serverErr != nil {
		t.Fatal(serverErr)
	}
	if !heartbeatAcknowledged {
		t.Fatal("acknowledged heartbeat did not mark connection healthy")
	}
	if !errors.Is(err, errDaemonRuntimeUnregistered) {
		t.Fatalf("runtime error = %v, want runtime unregistered", err)
	}
}

func TestPostJSONRetainsRetryAfter(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "9")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "busy")
		},
	))
	t.Cleanup(server.Close)

	client := New(Config{APIURL: server.URL}, server.Client(), nil)
	err := client.postJSON(context.Background(), "/", nil, nil)
	delay, ok := retryAfterDelay(err, time.Now())
	if !ok || delay != 9*time.Second {
		t.Fatalf("Retry-After = (%s, %t), want (9s, true); error = %v", delay, ok, err)
	}
}

func TestPostJSONRetainsRetryAfterWhenErrorBodyIsTruncated(t *testing.T) {
	t.Parallel()

	readErr := errors.New("response body truncated")
	httpClient := &http.Client{
		Transport: retryTestRoundTripper(func(
			request *http.Request,
		) (*http.Response, error) {
			headers := make(http.Header)
			headers.Set("Retry-After", "11")
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Status:     "429 Too Many Requests",
				Header:     headers,
				Body: io.NopCloser(io.MultiReader(
					strings.NewReader("busy"),
					iotest.ErrReader(readErr),
				)),
				Request: request,
			}, nil
		}),
	}
	client := New(
		Config{APIURL: "https://api.example.test"},
		httpClient,
		nil,
	)
	err := client.postJSON(context.Background(), "/", nil, nil)
	if !errors.Is(err, readErr) {
		t.Fatalf("HTTP error lost body read failure: %v", err)
	}
	var statusErr httpStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("HTTP error lost status: %v", err)
	}
	if statusErr.StatusCode != http.StatusTooManyRequests ||
		statusErr.RetryAfter != "11" ||
		statusErr.Body != "busy" {
		t.Fatalf("HTTP status error = %+v", statusErr)
	}
}

type retryTestRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip retryTestRoundTripper) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	return roundTrip(request)
}
