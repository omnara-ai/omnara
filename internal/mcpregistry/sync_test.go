package mcpregistry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

type sleepRecorder struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (r *sleepRecorder) sleep(_ context.Context, delay time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delays = append(r.delays, delay)
	return nil
}

func pageBody(name, next string) string {
	body := `{"servers":[{"server":{"name":"` + name + `","description":"d","version":"1.0.0"},` +
		`"_meta":{"io.modelcontextprotocol.registry/official":{"status":"active","updatedAt":"2026-08-20T00:00:00Z","isLatest":true}}}],` +
		`"metadata":{"count":1`
	if next != "" {
		body += `,"nextCursor":"` + next + `"`
	}
	return body + `}}`
}

func TestSyncerRetriesWithBackoffAndRetryAfter(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		switch attempts {
		case 1:
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		case 3:
			w.Header().Set("Retry-After", time.Now().Add(3*time.Second).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			_, _ = w.Write([]byte(pageBody("io.example/a", "")))
		}
	}))
	defer upstream.Close()
	recorder := &sleepRecorder{}
	syncer := Syncer{
		UpstreamURL: upstream.URL, HTTPClient: upstream.Client(), Sleep: recorder.sleep,
		BaseBackoff: 4 * time.Second, MaxBackoff: 30 * time.Second,
	}
	servers, err := syncer.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(servers) != 1 || attempts != 4 {
		t.Fatalf("servers = %d, attempts = %d", len(servers), attempts)
	}
	if len(recorder.delays) != 3 {
		t.Fatalf("delays = %v", recorder.delays)
	}
	if recorder.delays[0] != 7*time.Second {
		t.Fatalf("retry-after seconds delay = %s, want 7s", recorder.delays[0])
	}
	if recorder.delays[1] < 4*time.Second || recorder.delays[1] > 8*time.Second {
		t.Fatalf("attempt 2 backoff = %s, want within [4s, 8s]", recorder.delays[1])
	}
	if recorder.delays[2] <= 0 || recorder.delays[2] > 3*time.Second {
		t.Fatalf("retry-after date delay = %s, want (0, 3s]", recorder.delays[2])
	}
}

func TestSyncerGivesUpAfterMaxAttempts(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	recorder := &sleepRecorder{}
	syncer := Syncer{UpstreamURL: upstream.URL, HTTPClient: upstream.Client(), Sleep: recorder.sleep, MaxAttempts: 3}
	_, err := syncer.Fetch(context.Background())
	if !errors.Is(err, ErrUpstreamStatus) {
		t.Fatalf("err = %v, want ErrUpstreamStatus", err)
	}
	if attempts != 3 || len(recorder.delays) != 2 {
		t.Fatalf("attempts = %d, delays = %v", attempts, recorder.delays)
	}
}

func TestSyncerDoesNotRetryClientErrors(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer upstream.Close()
	recorder := &sleepRecorder{}
	syncer := Syncer{UpstreamURL: upstream.URL, HTTPClient: upstream.Client(), Sleep: recorder.sleep}
	if _, err := syncer.Fetch(context.Background()); !errors.Is(err, ErrUpstreamStatus) {
		t.Fatalf("err = %v", err)
	}
	if attempts != 1 || len(recorder.delays) != 0 {
		t.Fatalf("attempts = %d, delays = %v", attempts, recorder.delays)
	}
}

func TestSyncerPausesBetweenBatches(t *testing.T) {
	const pages = 7
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := 0
		if cursor := r.URL.Query().Get("cursor"); cursor != "" {
			index, _ = strconv.Atoi(cursor)
		}
		next := ""
		if index+1 < pages {
			next = strconv.Itoa(index + 1)
		}
		_, _ = w.Write([]byte(pageBody("io.example/"+strconv.Itoa(index), next)))
	}))
	defer upstream.Close()
	recorder := &sleepRecorder{}
	syncer := Syncer{
		UpstreamURL: upstream.URL, HTTPClient: upstream.Client(), Sleep: recorder.sleep,
		BatchPages: 3, BatchDelay: 250 * time.Millisecond,
	}
	servers, err := syncer.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(servers) != pages {
		t.Fatalf("servers = %d, want %d", len(servers), pages)
	}
	want := []time.Duration{250 * time.Millisecond, 250 * time.Millisecond}
	if len(recorder.delays) != len(want) || recorder.delays[0] != want[0] || recorder.delays[1] != want[1] {
		t.Fatalf("batch delays = %v, want %v", recorder.delays, want)
	}
}

func TestSyncerStopsSleepingWhenCancelled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	syncer := Syncer{UpstreamURL: upstream.URL, HTTPClient: upstream.Client()}
	if _, err := syncer.Fetch(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("", now); got != 0 {
		t.Fatalf("empty = %s", got)
	}
	if got := parseRetryAfter("12", now); got != 12*time.Second {
		t.Fatalf("seconds = %s", got)
	}
	if got := parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); got != 90*time.Second {
		t.Fatalf("date = %s", got)
	}
	if got := parseRetryAfter(now.Add(-time.Minute).Format(http.TimeFormat), now); got != 0 {
		t.Fatalf("past date = %s", got)
	}
	if got := parseRetryAfter("garbage", now); got != 0 {
		t.Fatalf("garbage = %s", got)
	}
}
