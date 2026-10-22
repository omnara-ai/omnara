package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerHandler(t *testing.T) {
	set := New()
	handler := ServerHandler(set, nil)

	healthReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthRec := httptest.NewRecorder()
	handler.ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("health status=%d want=%d", healthRec.Code, http.StatusOK)
	}
	var health map[string]string
	if err := json.Unmarshal(healthRec.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if health["status"] != "ok" {
		t.Fatalf("health status body=%q want=%q", health["status"], "ok")
	}
	if health["started_at"] == "" {
		t.Fatal("expected started_at")
	}

	readyReq := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyRec := httptest.NewRecorder()
	handler.ServeHTTP(readyRec, readyReq)
	if readyRec.Code != http.StatusOK {
		t.Fatalf("ready status=%d want=%d", readyRec.Code, http.StatusOK)
	}
	var ready map[string]string
	if err := json.Unmarshal(readyRec.Body.Bytes(), &ready); err != nil {
		t.Fatalf("decode ready response: %v", err)
	}
	if ready["status"] != "ready" {
		t.Fatalf("ready status body=%q want=%q", ready["status"], "ready")
	}

	metricsReq := httptest.NewRequest(http.MethodGet, ScrapePath, nil)
	metricsRec := httptest.NewRecorder()
	handler.ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK {
		t.Fatalf("metrics status=%d want=%d", metricsRec.Code, http.StatusOK)
	}
	body := metricsRec.Body.String()
	if body == "" {
		t.Fatal("expected metrics output")
	}
	if strings.Contains(body, "omnara_") {
		t.Fatalf("metrics server should not record its own HTTP traffic:\n%s", body)
	}
}

func TestServerHandlerReadyzNotReady(t *testing.T) {
	set := New()
	handler := ServerHandler(set, func(context.Context) error {
		return errors.New("database unavailable")
	})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status=%d want=%d", rec.Code, http.StatusServiceUnavailable)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ready response: %v", err)
	}
	if body["status"] != "not_ready" {
		t.Fatalf("ready status body=%q want=%q", body["status"], "not_ready")
	}
}

func TestProfilingRequiresOptInAndLoopback(t *testing.T) {
	for _, value := range []string{"", "1"} {
		t.Setenv("OMNARA_PPROF", value)
		rec := httptest.NewRecorder()
		ServerHandler(New(), nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
		want := http.StatusNotFound
		if value == "1" {
			want = http.StatusOK
		}
		if rec.Code != want {
			t.Fatalf("profiling %q: status %d, want %d", value, rec.Code, want)
		}
	}
	for _, addr := range []string{":9090", "0.0.0.0:9090", "[::]:9090", "example.com:9090"} {
		got, err := profilingAddress(addr)
		if err != nil || got != "127.0.0.1:9090" {
			t.Fatalf("profiling address %q: %q, %v", addr, got, err)
		}
	}
}
