package machinedaemon

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

func TestSleepPlatformSelection(t *testing.T) {
	if _, err := newSleepPlatform("unsupported"); err == nil {
		t.Fatal("unsupported sleep platform must fail closed")
	}
	platform, err := newSleepPlatform("")
	if err != nil {
		t.Fatalf("empty sleep platform: %v", err)
	}
	if err := platform.allowSleep(); err != nil {
		t.Fatalf("noop allow sleep: %v", err)
	}
	if err := platform.preventSleep(); err != nil {
		t.Fatalf("noop prevent sleep: %v", err)
	}
}

func TestUnikraftSleepPlatformWritesCounter(t *testing.T) {
	controlPath := filepath.Join(t.TempDir(), "scale_to_zero_disable")
	if err := os.WriteFile(controlPath, []byte("=1"), 0o600); err != nil {
		t.Fatalf("seed control file: %v", err)
	}
	platform := unikraftSleepPlatform{controlPath: controlPath}
	if err := platform.allowSleep(); err != nil {
		t.Fatalf("allow sleep: %v", err)
	}
	if data, _ := os.ReadFile(controlPath); string(data) != "=0" {
		t.Fatalf("allow sleep wrote %q, want =0", data)
	}
	if err := platform.preventSleep(); err != nil {
		t.Fatalf("prevent sleep: %v", err)
	}
	if data, _ := os.ReadFile(controlPath); string(data) != "=1" {
		t.Fatalf("prevent sleep wrote %q, want =1", data)
	}

	missing := unikraftSleepPlatform{controlPath: filepath.Join(t.TempDir(), "missing", "control")}
	if err := missing.preventSleep(); err == nil {
		t.Fatal("missing control file must fail closed")
	}
}

func TestWakeListenerSignalsSleepingDaemon(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := New(Config{
		APIURL:         "https://api.test",
		MachineToken:   "token",
		SleepAfter:     daemonprotocol.MinimumSleepAfter,
		WakeListenAddr: "127.0.0.1:0",
	}, nil, nil)
	client.sleepPlatform = noopSleepPlatform{}

	address, err := client.startWakeListener(ctx)
	if err != nil {
		t.Fatalf("start wake listener: %v", err)
	}

	woke := make(chan error, 1)
	go func() { woke <- client.sleepUntilWake(ctx) }()

	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("dial wake listener: %v", err)
	}
	_ = conn.Close()

	select {
	case err := <-woke:
		if err != nil {
			t.Fatalf("sleep until wake: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wake listener poke did not wake the daemon")
	}
}

func TestSleepUntilWakeConsumesBufferedTransitionSignal(t *testing.T) {
	ctx := context.Background()
	client := New(Config{
		APIURL:       "https://api.test",
		MachineToken: "token",
		SleepAfter:   daemonprotocol.MinimumSleepAfter,
	}, nil, nil)
	client.sleepPlatform = noopSleepPlatform{}

	client.signalWake()
	if err := client.sleepUntilWake(ctx); err != nil {
		t.Fatalf("sleep until wake with buffered signal: %v", err)
	}
}

func TestSleepUntilWakeStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := New(Config{
		APIURL:       "https://api.test",
		MachineToken: "token",
		SleepAfter:   daemonprotocol.MinimumSleepAfter,
	}, nil, nil)
	client.sleepPlatform = noopSleepPlatform{}

	done := make(chan error, 1)
	go func() { done <- client.sleepUntilWake(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("sleep until wake = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sleep until wake did not stop on cancel")
	}
}

func TestSleepNotWakeCapableDisablesSleepForSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sleep") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"not_wake_capable","error":"machine is not wake capable"}`))
	}))
	defer server.Close()

	client := New(Config{
		APIURL:       server.URL,
		MachineToken: "token",
		SleepAfter:   daemonprotocol.MinimumSleepAfter,
	}, server.Client(), nil)

	if !client.sleepEnabled() {
		t.Fatal("sleep must start enabled")
	}
	err := client.Sleep(context.Background(), DaemonRuntime{ID: "rt_1"})
	if !errors.Is(err, errDaemonSleepNotWakeCapable) {
		t.Fatalf("sleep = %v, want errDaemonSleepNotWakeCapable", err)
	}
	client.sleepDisabled.Store(true)
	if client.sleepEnabled() {
		t.Fatal("sleep must be disabled for the session after not_wake_capable")
	}
}

func TestSleepRequestHasNoBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sleep") {
			http.NotFound(w, r)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(data) != 0 {
			http.Error(w, "request body is not allowed", http.StatusBadRequest)
			return
		}
		if contentType := r.Header.Get("Content-Type"); contentType != "" {
			http.Error(w, "content type is not allowed", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"rt_1","state":"ended"}`))
	}))
	defer server.Close()

	client := New(Config{
		APIURL:       server.URL,
		MachineToken: "token",
		SleepAfter:   daemonprotocol.MinimumSleepAfter,
	}, server.Client(), nil)

	if err := client.Sleep(context.Background(), DaemonRuntime{ID: "rt_1"}); err != nil {
		t.Fatalf("sleep = %v, want no body accepted", err)
	}
}

func TestSleepPendingWorkConflictIsVeto(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"pending_work","error":"machine has pending daemon work"}`))
	}))
	defer server.Close()

	client := New(Config{
		APIURL:       server.URL,
		MachineToken: "token",
		SleepAfter:   daemonprotocol.MinimumSleepAfter,
	}, server.Client(), nil)

	if err := client.Sleep(context.Background(), DaemonRuntime{ID: "rt_1"}); !errors.Is(err, errDaemonSleepVetoed) {
		t.Fatalf("sleep = %v, want errDaemonSleepVetoed", err)
	}
}
