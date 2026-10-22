//go:build integration && servicee2e && loadtest

package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadNativeCPUDeltaKeepsSurvivingProcesses(t *testing.T) {
	start := parsePGProcesses("10 1 1:00.00 1024\n11 10 0:30.00 2048\n12 10 0:10.00 1024\n99 1 8:00.00 100", "10")
	end := parsePGProcesses("10 1 1:00.10 1024\n12 10 0:11.00 1024\n13 10 0:00.50 1024", "10")
	seconds, exited := loadPGCPUDelta(start, end)
	require.InDelta(t, 1.6, *seconds, .0001)
	require.Equal(t, 1, *exited)
	require.Equal(t, 3.0, end.rss)
	missing, count := loadPGCPUDelta(start, nil)
	require.Nil(t, missing)
	require.Nil(t, count)
}

func TestLoadModelProfileSwitchAndRequestWindow(t *testing.T) {
	model := &loadFakeModel{}
	model.cfg.Store(&loadModelConfig{
		toolProbability: 1, toolTokensMin: 1, toolTokensMax: 1,
		tpsMin: 1000000, tpsMax: 1000000, inputTokensMin: 1, inputTokensMax: 1,
	})
	body := `{"input":[{"role":"user","content":"load input 1 for agent"},{"type":"function_call_output"}]}`
	start := time.Now()
	first := httptest.NewRecorder()
	model.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(body)))
	require.Contains(t, first.Body.String(), `"name":"list_agents"`)
	model.cfg.Store(&loadModelConfig{
		textTokensMin: 1, textTokensMax: 1, tpsMin: 1000000, tpsMax: 1000000, inputTokensMin: 7, inputTokensMax: 7,
	})
	second := httptest.NewRecorder()
	model.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(body)))
	require.Contains(t, second.Body.String(), `"input_tokens":7`)
	require.Contains(t, second.Body.String(), `"type":"message"`)
	avg, p95, messages := model.requestWindow(start, time.Now())
	require.Equal(t, float64(len(body)), avg)
	require.Equal(t, avg, p95)
	require.Equal(t, 1.0, messages)
}

func TestLoadPoolURLPreservesOptions(t *testing.T) {
	t.Setenv("LOADTEST_POOL_MAX_CONN_LIFETIME", "3s")
	t.Setenv("LOADTEST_POOL_MAX_CONN_IDLE_TIME", "2s")
	t.Setenv("LOADTEST_STATEMENT_CACHE_CAPACITY", "16")
	parsed, err := url.Parse(withPoolMax("postgres://local/db?pool_max_conns=1&search_path=fixture", 4))
	require.NoError(t, err)
	require.Equal(t, "4", parsed.Query().Get("pool_max_conns"))
	require.Equal(t, "fixture", parsed.Query().Get("search_path"))
	require.Equal(t, "3s", parsed.Query().Get("pool_max_conn_lifetime"))
	require.Equal(t, "2s", parsed.Query().Get("pool_max_conn_idle_time"))
	require.Equal(t, "16", parsed.Query().Get("statement_cache_capacity"))
}

func TestLoadCPUProfilesEveryWorker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/debug/pprof/profile", r.URL.Path)
		assert.Equal(t, "1", r.URL.Query().Get("seconds"))
		_, _ = w.Write([]byte("profile"))
	}))
	defer server.Close()
	dir := t.TempDir()
	require.NoError(t, <-loadCPUProfiles(context.Background(), []string{server.URL, server.URL}, dir, 2, 1))
	files, err := filepath.Glob(filepath.Join(dir, "stage-02-worker-*.pprof"))
	require.NoError(t, err)
	require.Len(t, files, 2)
	for _, file := range files {
		data, err := os.ReadFile(file)
		require.NoError(t, err)
		require.Equal(t, "profile", string(data))
	}
}
