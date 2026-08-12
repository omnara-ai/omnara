//go:build integration && servicee2e

package e2e

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServiceE2EMetricsEndpoints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "metrics-endpoints")

	env.startAPI(t, ctx)
	assertStatus(t, ctx, env.apiMetricsURL+"/readyz", http.StatusOK)
	env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		"/api/v1/orgs",
		map[string]any{"name": "Metrics Test Org"},
		"",
		"",
		http.StatusUnauthorized,
	)
	assertMetrics(t, ctx, env.apiMetricsURL+"/metrics",
		"go_goroutines",
		"omnara_api_requests_total",
		"omnara_db_query_duration_seconds",
		`route="/api/v1/orgs"`,
	)

	env.startWorker(t, ctx, "", serviceWorkerOptions{})
	assertStatus(t, ctx, env.workerURL+"/readyz", http.StatusOK)
	assertMetrics(t, ctx, env.workerURL+"/metrics",
		"go_goroutines",
		"omnara_db_query_duration_seconds",
	)

	env.startMaintenance(t, ctx)
	assertStatus(t, ctx, env.maintenanceURL+"/readyz", http.StatusOK)
	assertMetrics(t, ctx, env.maintenanceURL+"/metrics",
		"go_goroutines",
		"omnara_db_query_duration_seconds",
	)
}

func assertStatus(t *testing.T, ctx context.Context, url string, want int) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("request %s status=%d want=%d body=%s", url, resp.StatusCode, want, body)
	}
}

func assertMetrics(t *testing.T, ctx context.Context, url string, wants ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("new metrics request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("scrape metrics %s: %v", url, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read metrics response: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("scrape metrics %s status=%d want=%d body=%s", url, resp.StatusCode, http.StatusOK, body)
		}
		text := string(body)
		missing := ""
		for _, want := range wants {
			if !strings.Contains(text, want) {
				missing = want
				break
			}
		}
		if missing == "" {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("metrics %s missing %q:\n%s", url, missing, text)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for metrics %s: %v", url, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
