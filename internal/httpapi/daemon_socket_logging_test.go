package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/metrics"
)

func TestDaemonSocketExitAppearsInRequestLog(t *testing.T) {
	for _, tt := range []struct {
		name string
		code websocket.StatusCode
		want string
	}{
		{name: "normal peer close", code: websocket.StatusNormalClosure, want: "socket_closed"},
		{name: "peer going away", code: websocket.StatusGoingAway, want: "socket_closed"},
		{name: "peer policy error", code: websocket.StatusPolicyViolation, want: "socket_failure"},
		{name: "transport closed", want: "socket_failure"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			buf, logger := newRequestEventCapture()
			recorder := metrics.NewDBRecorder(metrics.New(), metrics.SubsystemDB)
			finished := make(chan error, 1)
			var handlerErr error
			handler := requestLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					handlerErr = err
					return
				}
				defer func() { _ = conn.CloseNow() }()
				socket := &daemonSocket{wire: daemonprotocol.NewBackendSocket(conn, ""), done: make(chan struct{})}
				ctx, cancel := context.WithCancelCause(r.Context())
				defer cancel(nil)
				traceCtx := recorder.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "-- name: InFlightQuery :one"})
				cancel(socket.cancellationCause(socket.readLoop(ctx)))
				recorder.TraceQueryEnd(traceCtx, nil, pgx.TraceQueryEndData{Err: ctx.Err()})
			}))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handler.ServeHTTP(w, r)
				finished <- handlerErr
			}))
			defer server.Close()
			conn, response, err := websocket.Dial(t.Context(), server.URL, nil)
			if response != nil && response.Body != nil {
				defer func() { _ = response.Body.Close() }()
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.CloseNow() }()
			if tt.code == 0 {
				_ = conn.CloseNow()
			} else {
				_ = conn.Close(tt.code, "private_close_reason")
			}
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-t.Context().Done():
				t.Fatal("socket handler did not finish")
			}
			event := decodeRequestEvent(t, buf)
			if event["db.queries.0.cancel_source"] != tt.want {
				t.Fatalf("source = %v, want %q", event["db.queries.0.cancel_source"], tt.want)
			}
			if event["http.status_code"] != float64(http.StatusSwitchingProtocols) {
				t.Fatal("socket status changed")
			}
			if event["db.queries.0.error"] != "context_canceled" {
				t.Fatal("DB error classification changed")
			}
		})
	}
}

func TestDaemonSocketCancellationClassification(t *testing.T) {
	for _, tt := range []struct {
		name       string
		err        error
		localClose websocket.StatusCode
		want       error
	}{
		{name: "deadline", err: fmt.Errorf("read: %w", context.DeadlineExceeded), want: logpkg.ErrSocketTimeout},
		{name: "network timeout", err: &net.OpError{Op: "write", Net: "tcp", Err: os.ErrDeadlineExceeded},
			want: logpkg.ErrSocketTimeout},
		{name: "canceled", err: context.Canceled, want: context.Canceled},
		{name: "local close", localClose: websocket.StatusNormalClosure, want: logpkg.ErrSocketClosed},
		{name: "local policy failure", localClose: websocket.StatusPolicyViolation, want: logpkg.ErrSocketFailure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			socket := &daemonSocket{done: make(chan struct{})}
			if tt.localClose != 0 {
				socket.close(tt.localClose, "private_close_reason")
			}
			if got := socket.cancellationCause(tt.err); !errors.Is(got, tt.want) {
				t.Fatalf("cause = %v, want %v", got, tt.want)
			}
		})
	}
}
