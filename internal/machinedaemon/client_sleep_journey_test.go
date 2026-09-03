package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func TestDaemonSleepWakeJourney(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var registers atomic.Int64
	var sleeps atomic.Int64
	registered := make(chan int64, 8)
	slept := make(chan int64, 8)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /daemon/bootstrap", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, w, map[string]any{
			"installation_id":  "inst_journey",
			"org_id":           "org_journey",
			"machine_id":       "mch_journey",
			"daemon_base_path": "/daemon",
		})
	})
	mux.HandleFunc("POST /daemon/runtimes", func(w http.ResponseWriter, _ *http.Request) {
		count := registers.Add(1)
		registered <- count
		writeTestJSON(t, w, map[string]any{
			"runtime":        map[string]any{"id": "rt_journey", "next_heartbeat_after_ms": 60_000},
			"reconciliation": map[string]any{},
		})
	})
	mux.HandleFunc("GET /daemon/runtimes/rt_journey/socket", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "server done")
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("POST /daemon/runtimes/rt_journey/sleep", func(w http.ResponseWriter, _ *http.Request) {
		count := sleeps.Add(1)
		slept <- count
		if count == 1 {
			w.WriteHeader(http.StatusConflict)
			writeTestJSON(t, w, map[string]any{"code": "pending_work", "error": "machine has pending daemon work"})
			return
		}
		writeTestJSON(t, w, map[string]any{"id": "rt_journey", "state": "ended", "state_reason_code": "machine_asleep"})
	})
	mux.HandleFunc("POST /daemon/runtimes/rt_journey/end", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, w, map[string]any{"id": "rt_journey", "state": "ended"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	wakeAddr := freeLoopbackAddr(t)
	client := New(Config{
		APIURL:                 server.URL,
		ExpectedInstallationID: "inst_journey",
		ExpectedMachineID:      "mch_journey",
		MachineToken:           "token-journey",
		OmnaraHome:             t.TempDir(),
		RetryInterval:          10 * time.Second,
		SleepAfter:             time.Second,
		WakeListenAddr:         wakeAddr,
	}, server.Client(), nil)

	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx) }()

	waitForCount(t, ctx, registered, 1, "first daemon registration")
	waitForCount(t, ctx, slept, 1, "vetoed sleep attempt")
	reconnectCtx, cancelReconnect := context.WithTimeout(ctx, 2*time.Second)
	waitForCount(t, reconnectCtx, registered, 2, "re-registration after sleep veto")
	cancelReconnect()
	waitForCount(t, ctx, slept, 2, "accepted sleep attempt")

	deadline := time.After(10 * time.Second)
	var conn net.Conn
	var dialErr error
	for {
		conn, dialErr = net.DialTimeout("tcp", wakeAddr, 250*time.Millisecond)
		if dialErr == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("wake listener never accepted a poke: %v", dialErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
	_ = conn.Close()

	waitForCount(t, ctx, registered, 3, "re-registration after wake")

	cancel()
	select {
	case err := <-runDone:
		if err != nil && !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("daemon run returned unexpected error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon run did not stop after cancel")
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, body map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("encode test response: %v", err)
	}
}

func waitForCount(t *testing.T, ctx context.Context, events <-chan int64, want int64, label string) {
	t.Helper()
	for {
		select {
		case count := <-events:
			if count >= want {
				return
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", label)
		}
	}
}
