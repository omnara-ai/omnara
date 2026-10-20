//go:build integration && servicee2e && loadtest

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type loadModelConfig struct {
	ttftMin, ttftMax time.Duration
	textTokensMin    int
	textTokensMax    int
	toolTokensMin    int
	toolTokensMax    int
	tpsMin, tpsMax   int
	toolProbability  float64
	inputTokensMin   int
	inputTokensMax   int
	deltaInterval    time.Duration
}

type loadFakeModel struct {
	cfg         loadModelConfig
	onStart     func(key string, at time.Time)
	onEnd       func(key string, at time.Time, tool bool)
	seq         atomic.Int64
	calls       atomic.Int64
	toolCalls   atomic.Int64
	inflight    atomic.Int64
	maxInflight atomic.Int64
}

func loadEnvInt(name string, fallback int) int {
	if raw := os.Getenv(name); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			return v
		}
	}
	return fallback
}

func loadEnvFloat(name string, fallback float64) float64 {
	if raw := os.Getenv(name); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			return v
		}
	}
	return fallback
}

func loadEnvInts(name string, fallback []int) []int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	var out []int
	for _, part := range strings.Split(raw, ",") {
		v, err := strconv.Atoi(strings.TrimSpace(part))
		if err == nil {
			out = append(out, v)
		}
	}
	return out
}

func randBetween(rng *rand.Rand, lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + rng.IntN(hi-lo+1)
}

func randDuration(rng *rand.Rand, lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	return lo + time.Duration(rng.Int64N(int64(hi-lo)))
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

var loadInputPattern = regexp.MustCompile(`load input \d+ for ([A-Za-z0-9_-]+)`)

func (m *loadFakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/responses" {
		http.NotFound(w, r)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(raw, &body)
	agentKey := ""
	if matches := loadInputPattern.FindAllSubmatch(raw, -1); len(matches) > 0 {
		agentKey = string(matches[len(matches)-1][1])
	}
	current := m.inflight.Add(1)
	defer m.inflight.Add(-1)
	for {
		prev := m.maxInflight.Load()
		if current <= prev || m.maxInflight.CompareAndSwap(prev, current) {
			break
		}
	}
	m.calls.Add(1)
	id := m.seq.Add(1)
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), uint64(id)))
	respID := fmt.Sprintf("resp_load_%d", id)
	isTool := rng.Float64() < m.cfg.toolProbability
	if isTool {
		m.toolCalls.Add(1)
	}
	inputTokens := randBetween(rng, m.cfg.inputTokensMin, m.cfg.inputTokensMax)
	tps := randBetween(rng, m.cfg.tpsMin, m.cfg.tpsMax)
	ctx := r.Context()
	sleep := func(d time.Duration) bool {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if m.onStart != nil {
		m.onStart(agentKey, time.Now())
	}
	if m.onEnd != nil {
		defer func() { m.onEnd(agentKey, time.Now(), isTool) }()
	}
	if !sleep(randDuration(rng, m.cfg.ttftMin, m.cfg.ttftMax)) {
		return
	}
	flusher, _ := w.(http.Flusher)
	if body.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}
	write := func(event, data string) {
		if !body.Stream {
			return
		}
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	var output string
	var outputTokens int
	if isTool {
		fcID := fmt.Sprintf("fc_load_%d", id)
		callID := fmt.Sprintf("call_load_%d", id)
		outputTokens = randBetween(rng, m.cfg.toolTokensMin, m.cfg.toolTokensMax)
		if !sleep(time.Duration(float64(outputTokens) / float64(tps) * float64(time.Second))) {
			return
		}
		write("response.output_item.added",
			`{"item":{"id":"`+fcID+`","type":"function_call","call_id":"`+callID+`","name":"list_agents"}}`)
		write("response.function_call_arguments.delta", `{"item_id":"`+fcID+`","delta":"{}"}`)
		write("response.output_item.done", `{"item":{"id":"`+fcID+`","type":"function_call"}}`)
		output = `{"type":"function_call","id":"` + fcID + `","call_id":"` + callID +
			`","name":"list_agents","arguments":"{}"}`
	} else {
		msgID := fmt.Sprintf("msg_load_%d", id)
		outputTokens = randBetween(rng, m.cfg.textTokensMin, m.cfg.textTokensMax)
		write("response.output_item.added", `{"item":{"id":"`+msgID+`","type":"message"}}`)
		write("response.content_part.added",
			`{"item_id":"`+msgID+`","content_index":0,"part":{"type":"output_text"}}`)
		perTick := int(math.Max(1, float64(tps)*m.cfg.deltaInterval.Seconds()))
		var text strings.Builder
		for sent := 0; sent < outputTokens; {
			chunk := perTick
			if outputTokens-sent < chunk {
				chunk = outputTokens - sent
			}
			delta := strings.Repeat("lorem ", chunk)
			text.WriteString(delta)
			write("response.output_text.delta",
				`{"item_id":"`+msgID+`","content_index":0,"delta":`+jsonString(delta)+`}`)
			sent += chunk
			if !sleep(m.cfg.deltaInterval) {
				return
			}
		}
		write("response.content_part.done", `{"item_id":"`+msgID+`","content_index":0}`)
		output = `{"id":"` + msgID + `","type":"message","content":[{"type":"output_text","text":` +
			jsonString(text.String()) + `}]}`
	}
	completed := `{"id":"` + respID + `","model":"service-e2e-local","status":"completed","output":[` + output +
		`],"usage":{"input_tokens":` + strconv.Itoa(inputTokens) + `,"output_tokens":` +
		strconv.Itoa(outputTokens) + `}}`
	if body.Stream {
		write("response.completed", `{"response":`+completed+`}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(completed))
}

type loadAgent struct {
	id        string
	uuid      string
	project   *deterministicProject
	sending   bool
	waiting   bool
	seenBusy  bool
	sentAt    time.Time
	nextAt    time.Time
	started   bool
	inputs    int
	turnsDone int
	turnTimes []time.Duration
}

type providerSample struct {
	at time.Time
	ms float64
}

type providerObserver struct {
	requests   []time.Time
	mu         sync.Mutex
	agents     map[string]*providerAgentState
	toolGaps   []providerSample
	turnStarts []providerSample
}

type providerAgentState struct {
	lastEnd       time.Time
	lastWasTool   bool
	awaitingFirst bool
	sentAt        time.Time
}

func newProviderObserver() *providerObserver {
	return &providerObserver{agents: map[string]*providerAgentState{}}
}

func (o *providerObserver) state(key string) *providerAgentState {
	st := o.agents[key]
	if st == nil {
		st = &providerAgentState{}
		o.agents[key] = st
	}
	return st
}

func (o *providerObserver) inputSent(key string, at time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	st := o.state(key)
	st.awaitingFirst = true
	st.sentAt = at
	st.lastWasTool = false
}

func (o *providerObserver) start(key string, at time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests = append(o.requests, at)
	st := o.state(key)
	if st.lastWasTool {
		o.toolGaps = append(o.toolGaps, providerSample{at: at, ms: float64(at.Sub(st.lastEnd).Microseconds()) / 1000})
		st.lastWasTool = false
	}
	if st.awaitingFirst {
		o.turnStarts = append(o.turnStarts, providerSample{at: at, ms: float64(at.Sub(st.sentAt).Microseconds()) / 1000})
		st.awaitingFirst = false
	}
}

func (o *providerObserver) end(key string, at time.Time, tool bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	st := o.state(key)
	st.lastEnd = at
	st.lastWasTool = tool
}

func (o *providerObserver) window(from, to time.Time) ([]float64, []float64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	pick := func(samples []providerSample) []float64 {
		var out []float64
		for _, sample := range samples {
			if !sample.at.Before(from) && sample.at.Before(to) {
				out = append(out, sample.ms)
			}
		}
		sort.Float64s(out)
		return out
	}
	return pick(o.toolGaps), pick(o.turnStarts)
}

func (o *providerObserver) requestsInWindow(from, to time.Time) int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	var count int64
	for _, at := range o.requests {
		if !at.Before(from) && at.Before(to) {
			count++
		}
	}
	return count
}

func loadRedisCPU(ctx context.Context, client *redis.Client) *float64 {
	info, err := client.Info(ctx, "cpu").Result()
	if err != nil {
		return nil
	}
	var seconds float64
	found := 0
	for _, line := range strings.Split(info, "\n") {
		name, raw, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || (name != "used_cpu_user" && name != "used_cpu_sys") {
			continue
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil
		}
		seconds += value
		found++
	}
	if found != 2 {
		return nil
	}
	return &seconds
}

type loadExplicitTransactions struct {
	begins  int64
	commits int64
	resetAt time.Time
	dealloc int64
}

func (e *serviceE2EEnvironment) loadExplicitTransactions(ctx context.Context) (*loadExplicitTransactions, error) {
	var stats loadExplicitTransactions
	var trackUtility bool
	err := e.db.QueryRow(ctx, `SELECT
 coalesce(sum(calls) FILTER (WHERE upper(trim(query)) ~ '^(BEGIN|START TRANSACTION)(;| |$)'),0)::bigint,
 coalesce(sum(calls) FILTER (WHERE upper(trim(query)) ~ '^(COMMIT|END)(;| |$)'),0)::bigint,
 (SELECT stats_reset FROM public.pg_stat_statements_info),
 (SELECT dealloc FROM public.pg_stat_statements_info),
 current_setting('pg_stat_statements.track_utility')::boolean
 FROM public.pg_stat_statements
 WHERE dbid = (SELECT oid FROM pg_database WHERE datname=current_database()) AND toplevel`).Scan(
		&stats.begins, &stats.commits, &stats.resetAt, &stats.dealloc, &trackUtility)
	if err != nil {
		return nil, err
	}
	if !trackUtility {
		return nil, fmt.Errorf("pg_stat_statements.track_utility is disabled")
	}
	return &stats, nil
}

func loadExplicitTransactionRates(start, end *loadExplicitTransactions, seconds float64) (*float64, *float64) {
	if start == nil || end == nil || seconds <= 0 || !start.resetAt.Equal(end.resetAt) ||
		start.dealloc != end.dealloc || end.begins < start.begins || end.commits < start.commits {
		return nil, nil
	}
	begins := float64(end.begins-start.begins) / seconds
	commits := float64(end.commits-start.commits) / seconds
	return &begins, &commits
}

type loadProcess struct {
	label string
	pid   int
}

func psCPUSeconds(pid int) (float64, float64) {
	out, err := exec.Command("ps", "-o", "time=,rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, 0
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return 0, 0
	}
	cpu := 0.0
	timeField := fields[0]
	days := 0.0
	if d, rest, ok := strings.Cut(timeField, "-"); ok {
		days, _ = strconv.ParseFloat(d, 64)
		timeField = rest
	}
	parts := strings.Split(timeField, ":")
	mult := 1.0
	for i := len(parts) - 1; i >= 0; i-- {
		v, _ := strconv.ParseFloat(parts[i], 64)
		cpu += v * mult
		mult *= 60
	}
	cpu += days * 86400
	rssKB, _ := strconv.ParseFloat(fields[1], 64)
	return cpu, rssKB / 1024
}

func pgTreeCPU() (float64, float64) {
	pidfile := os.Getenv("LOADTEST_PG_PIDFILE")
	if pidfile == "" {
		return 0, 0
	}
	raw, err := os.ReadFile(pidfile)
	if err != nil {
		return 0, 0
	}
	postmaster := strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0])
	out, err := exec.Command("ps", "-Ao", "pid=,ppid=,time=,rss=").Output()
	if err != nil {
		return 0, 0
	}
	total, rss := 0.0, 0.0
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || (f[0] != postmaster && f[1] != postmaster) {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		cpu, mem := psCPUSeconds(pid)
		total += cpu
		rss += mem
	}
	return total, rss
}

type poolSnapshot struct {
	acquires, acquireSeconds, emptyAcquires, emptyWaitSeconds float64
}

func scrapePool(urls []string) poolSnapshot {
	var snap poolSnapshot
	for _, url := range urls {
		resp, err := loadHTTPClient.Get(url + "/metrics")
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			value, _ := strconv.ParseFloat(fields[1], 64)
			switch fields[0] {
			case "omnara_db_pool_acquires_total":
				snap.acquires += value
			case "omnara_db_pool_acquire_duration_seconds_total":
				snap.acquireSeconds += value
			case "omnara_db_pool_empty_acquires_total":
				snap.emptyAcquires += value
			case "omnara_db_pool_empty_acquire_wait_seconds_total":
				snap.emptyWaitSeconds += value
			}
		}
	}
	return snap
}

type dockerSample struct {
	cpuPercent float64
	memMB      float64
}

func dockerStats(names ...string) map[string]dockerSample {
	args := append([]string{"stats", "--no-stream", "--format", "{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}"}, names...)
	out, err := exec.Command("docker", args...).Output()
	result := map[string]dockerSample{}
	if err != nil {
		return result
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			continue
		}
		cpu, _ := strconv.ParseFloat(strings.TrimSuffix(parts[1], "%"), 64)
		memRaw := strings.TrimSpace(strings.Split(parts[2], "/")[0])
		mem := 0.0
		switch {
		case strings.HasSuffix(memRaw, "GiB"):
			v, _ := strconv.ParseFloat(strings.TrimSuffix(memRaw, "GiB"), 64)
			mem = v * 1024
		case strings.HasSuffix(memRaw, "MiB"):
			v, _ := strconv.ParseFloat(strings.TrimSuffix(memRaw, "MiB"), 64)
			mem = v
		case strings.HasSuffix(memRaw, "KiB"):
			v, _ := strconv.ParseFloat(strings.TrimSuffix(memRaw, "KiB"), 64)
			mem = v / 1024
		}
		result[parts[0]] = dockerSample{cpuPercent: cpu, memMB: mem}
	}
	return result
}

type pgStat struct {
	commits, rollbacks, inserted, updated, deleted int64
}

func (e *serviceE2EEnvironment) loadPGStat(ctx context.Context) (pgStat, error) {
	var s pgStat
	err := e.db.QueryRow(ctx, `
SELECT xact_commit, xact_rollback, tup_inserted, tup_updated, tup_deleted
FROM pg_stat_database WHERE datname = current_database()`).Scan(
		&s.commits, &s.rollbacks, &s.inserted, &s.updated, &s.deleted)
	return s, err
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func (e *serviceE2EEnvironment) createLoadAgent(
	ctx context.Context,
	project *deterministicProject,
	index int,
) (string, error) {
	req, err := e.newAPIRequest(ctx, http.MethodPost, project.projectPath+"/agents",
		bytes.NewReader(mustJSON(map[string]any{"profile": project.agentID, "config": project.configID})))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", fmt.Sprintf("load-agent-%s-%d", project.projectID, index))
	req.Header.Set("Authorization", "Bearer "+project.adminToken)
	resp, err := loadHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("create agent status=%d body=%s", resp.StatusCode, data)
	}
	var out struct {
		Agent struct {
			ID string `json:"id"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	return out.Agent.ID, nil
}

func (e *serviceE2EEnvironment) sendLoadInput(ctx context.Context, agent *loadAgent) error {
	body := map[string]any{"content_blocks": []map[string]any{{
		"type": "text",
		"text": fmt.Sprintf("load input %d for %s", agent.inputs, agent.id),
	}}}
	req, err := e.newAPIRequest(ctx, http.MethodPost,
		agent.project.projectPath+"/agents/"+agent.id+"/inputs", bytes.NewReader(mustJSON(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", fmt.Sprintf("load-input-%s-%d", agent.id, agent.inputs))
	req.Header.Set("Authorization", "Bearer "+agent.project.adminToken)
	resp, err := loadHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("create input status=%d body=%s", resp.StatusCode, data)
	}
	return nil
}

var loadHTTPClient = &http.Client{
	Timeout: 60 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        512,
		MaxIdleConnsPerHost: 512,
		MaxConnsPerHost:     512,
		IdleConnTimeout:     90 * time.Second,
	},
}

func withPoolMax(databaseURL string, poolMax int) string {
	sep := "?"
	if strings.Contains(databaseURL, "?") {
		sep = "&"
	}
	return databaseURL + sep + "pool_max_conns=" + strconv.Itoa(poolMax)
}

type loadStageResult struct {
	AcceptedModelOutputs       int64    `json:"accepted_model_outputs"`
	AcceptedModelOutputsPerSec float64  `json:"accepted_model_outputs_per_sec"`
	ProviderRequests           int64    `json:"provider_requests"`
	ProviderRequestsPerSec     float64  `json:"provider_requests_per_sec"`
	PGExplicitBeginsPerSec     *float64 `json:"pg_explicit_begins_per_sec"`
	PGExplicitCommitsPerSec    *float64 `json:"pg_explicit_commits_per_sec"`
	PGAllTransactionsPerSec    float64  `json:"pg_all_transactions_per_sec"`
	PGStatisticsScope          string   `json:"pg_statistics_scope"`
	WorkerCPUCores             float64  `json:"worker_cpu_cores"`
	RedisCPUCores              *float64 `json:"redis_cpu_cores"`
	DatabaseCPUCores           *float64 `json:"database_cpu_cores"`
	DatabaseCPUSource          string   `json:"database_cpu_source"`
	RedisCPUScope              string   `json:"redis_cpu_scope"`

	Active                 int                `json:"active_agents"`
	WindowSeconds          float64            `json:"window_seconds"`
	TurnsPerSec            float64            `json:"turns_per_sec"`
	ModelCallsPerSec       float64            `json:"model_calls_per_sec"`
	ToolResultsPerSec      float64            `json:"tool_results_per_sec"`
	FailedModelCalls       int64              `json:"failed_model_calls"`
	PGCommitsPerSec        float64            `json:"pg_commits_per_sec"`
	PGRowsWrittenPerSec    float64            `json:"pg_rows_written_per_sec"`
	PGRollbacksPerSec      float64            `json:"pg_rollbacks_per_sec"`
	StepGapP50ms           float64            `json:"step_gap_p50_ms"`
	StepGapP95ms           float64            `json:"step_gap_p95_ms"`
	StepGapP99ms           float64            `json:"step_gap_p99_ms"`
	ProviderToolGapP50ms   float64            `json:"provider_tool_gap_p50_ms"`
	ProviderToolGapP95ms   float64            `json:"provider_tool_gap_p95_ms"`
	ProviderTurnStartP50ms float64            `json:"provider_turn_start_p50_ms"`
	ProviderTurnStartP95ms float64            `json:"provider_turn_start_p95_ms"`
	PoolAcquiresPerSec     float64            `json:"pool_acquires_per_sec"`
	PoolAcquireWaitAvgMS   float64            `json:"pool_acquire_wait_avg_ms"`
	PoolEmptyAcquirePct    float64            `json:"pool_empty_acquire_pct"`
	TurnStartP50ms         float64            `json:"turn_start_p50_ms"`
	TurnStartP95ms         float64            `json:"turn_start_p95_ms"`
	TurnP50s               float64            `json:"turn_p50_s"`
	TurnP95s               float64            `json:"turn_p95_s"`
	ReadyQueueAvg          float64            `json:"ready_queue_avg"`
	ReadyQueueMax          int64              `json:"ready_queue_max"`
	LocksAvg               float64            `json:"locks_avg"`
	ModelInflightAvg       float64            `json:"model_inflight_avg"`
	AgentsInTurnAvg        float64            `json:"agents_in_turn_avg"`
	AgentsInTurnMax        int                `json:"agents_in_turn_max"`
	TotalAgents            int                `json:"total_agents"`
	Cores                  map[string]float64 `json:"cores"`
	RSSMB                  map[string]float64 `json:"rss_mb"`
}

func TestServiceE2ELoadAgents(t *testing.T) {
	stages := loadEnvInts("LOADTEST_STAGES", []int{100, 250})
	stageSeconds := loadEnvInt("LOADTEST_STAGE_SECONDS", 90)
	warmupSeconds := loadEnvInt("LOADTEST_WARMUP_SECONDS", 30)
	projectCount := loadEnvInt("LOADTEST_PROJECTS", 2)
	workerCount := loadEnvInt("LOADTEST_WORKERS", 2)
	workerCapacity := loadEnvInt("LOADTEST_WORKER_CAPACITY", 500)
	workerPool := loadEnvInt("LOADTEST_WORKER_POOL", 30)
	apiPool := loadEnvInt("LOADTEST_API_POOL", 30)
	thinkMaxMS := loadEnvInt("LOADTEST_THINK_MAX_MS", 0)
	idleAgents := loadEnvInt("LOADTEST_IDLE_AGENTS", 0)
	setupSeconds := loadEnvInt("LOADTEST_SETUP_SECONDS", 900)
	createConcurrency := loadEnvInt("LOADTEST_CREATE_CONCURRENCY", 64)
	staggerMS := loadEnvInt("LOADTEST_STAGGER_MS", 0)
	maxActive := 0
	for _, n := range stages {
		if n > maxActive {
			maxActive = n
		}
	}
	totalAgents := maxActive + idleAgents
	total := time.Duration(len(stages)*stageSeconds+setupSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), total)
	defer cancel()

	model := &loadFakeModel{cfg: loadModelConfig{
		ttftMin:         time.Duration(loadEnvInt("LOADTEST_TTFT_MIN_MS", 500)) * time.Millisecond,
		ttftMax:         time.Duration(loadEnvInt("LOADTEST_TTFT_MAX_MS", 2000)) * time.Millisecond,
		textTokensMin:   loadEnvInt("LOADTEST_TEXT_TOKENS_MIN", 100),
		textTokensMax:   loadEnvInt("LOADTEST_TEXT_TOKENS_MAX", 800),
		toolTokensMin:   loadEnvInt("LOADTEST_TOOL_TOKENS_MIN", 20),
		toolTokensMax:   loadEnvInt("LOADTEST_TOOL_TOKENS_MAX", 150),
		tpsMin:          loadEnvInt("LOADTEST_TPS_MIN", 50),
		tpsMax:          loadEnvInt("LOADTEST_TPS_MAX", 100),
		toolProbability: loadEnvFloat("LOADTEST_TOOL_PROBABILITY", 0.6),
		inputTokensMin:  2000,
		inputTokensMax:  20000,
		deltaInterval:   time.Duration(loadEnvInt("LOADTEST_DELTA_INTERVAL_MS", 100)) * time.Millisecond,
	}}
	observer := newProviderObserver()
	model.onStart = observer.start
	model.onEnd = observer.end
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake model: %v", err)
	}
	modelServer := &http.Server{Handler: model}
	go func() { _ = modelServer.Serve(listener) }()
	defer modelServer.Close()
	modelURL := "http://" + listener.Addr().String()

	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "loadtest")
	redisOptions, err := redis.ParseURL(env.redisURL)
	require.NoError(t, err)
	redisClient := redis.NewClient(redisOptions)
	defer redisClient.Close()
	baseURL := env.databaseURL
	env.databaseURL = withPoolMax(baseURL, apiPool)
	api := env.startAPI(t, ctx)
	env.databaseURL = baseURL

	projects := make([]*deterministicProject, 0, projectCount)
	for i := range projectCount {
		project := env.bootstrapProjectViaAPIWithTools(
			t, ctx, fmt.Sprintf("load%d", i), "openai-prod", "service-e2e-local", "list_agents")
		env.updateServiceE2EProviderBaseURL(t, ctx, project.projectID, "openai-prod", modelURL)
		projects = append(projects, &project)
	}

	t.Logf("creating %d agents across %d projects", totalAgents, projectCount)
	agents := make([]*loadAgent, totalAgents)
	createStart := time.Now()
	{
		var wg sync.WaitGroup
		sem := make(chan struct{}, createConcurrency)
		var firstErr atomic.Value
		var created atomic.Int64
		for i := range totalAgents {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				project := projects[i%projectCount]
				id, err := env.createLoadAgent(ctx, project, i)
				if err != nil {
					firstErr.CompareAndSwap(nil, err)
					return
				}
				agents[i] = &loadAgent{id: id, project: project}
				if c := created.Add(1); c%50000 == 0 {
					t.Logf("created %d/%d agents, %.0f/s", c, totalAgents, float64(c)/time.Since(createStart).Seconds())
				}
			}(i)
		}
		wg.Wait()
		if v := firstErr.Load(); v != nil {
			t.Fatalf("create agents: %v", v)
		}
	}
	t.Logf("created %d agents in %s", totalAgents, time.Since(createStart).Round(time.Millisecond))
	uuidByID := map[string]*loadAgent{}
	for _, agent := range agents {
		agent.uuid = mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agent.id)
		uuidByID[agent.uuid] = agent
	}

	workerEnv := []string{
		"OMNARA_DATABASE_URL=" + withPoolMax(baseURL, workerPool),
		"OMNARA_WORKER_CAPACITY=" + strconv.Itoa(workerCapacity),
	}
	processes := []loadProcess{{label: "api", pid: api.cmd.Process.Pid}}
	var workerMetricURLs []string
	for range workerCount {
		worker := env.startWorker(t, ctx, projects[0].projectID, serviceWorkerOptions{ExtraEnv: workerEnv})
		processes = append(processes, loadProcess{label: "workers", pid: worker.cmd.Process.Pid})
		workerMetricURLs = append(workerMetricURLs, env.workerURL)
	}
	maintenance := env.startMaintenance(t, ctx)
	processes = append(processes,
		loadProcess{label: "maintenance", pid: maintenance.cmd.Process.Pid},
		loadProcess{label: "fake_model_and_driver", pid: os.Getpid()})

	var mu sync.Mutex
	active := 0
	var turnsDone atomic.Int64
	var sendErrors atomic.Int64
	var lastSendErr atomic.Value
	driverCtx, stopDriver := context.WithCancel(ctx)
	defer stopDriver()
	sendSem := make(chan struct{}, 128)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-driverCtx.Done():
				return
			case <-ticker.C:
			}
			mu.Lock()
			n := active
			ids := make([]string, 0, 1024)
			for _, agent := range agents[:n] {
				if agent.waiting {
					ids = append(ids, agent.uuid)
				}
			}
			mu.Unlock()
			rows, err := env.db.Query(driverCtx, `
SELECT a.id::text
FROM unnest($1::text[]) AS raw(id)
CROSS JOIN LATERAL (SELECT raw.id::uuid AS id) a
WHERE NOT EXISTS (SELECT 1 FROM agent_wakeups w WHERE w.agent_id = a.id)
  AND NOT EXISTS (SELECT 1 FROM agent_runtime_locks l WHERE l.agent_id = a.id)`, ids)
			if err != nil {
				continue
			}
			idle := map[string]bool{}
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					idle[id] = true
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				continue
			}
			rows.Close()
			now := time.Now()
			mu.Lock()
			var toSend []*loadAgent
			for _, agent := range agents[:n] {
				if !agent.started {
					agent.started = true
					if staggerMS > 0 {
						agent.nextAt = now.Add(time.Duration(rand.IntN(staggerMS)) * time.Millisecond)
					}
				}
				if agent.sending {
					continue
				}
				if agent.waiting {
					if !idle[agent.uuid] {
						agent.seenBusy = true
						continue
					}
					if !agent.seenBusy && now.Sub(agent.sentAt) < 5*time.Second {
						continue
					}
					agent.waiting = false
					agent.turnsDone++
					agent.turnTimes = append(agent.turnTimes, now.Sub(agent.sentAt))
					turnsDone.Add(1)
					think := time.Duration(0)
					if thinkMaxMS > 0 {
						think = time.Duration(rand.IntN(thinkMaxMS)) * time.Millisecond
					}
					agent.nextAt = now.Add(think)
				}
				if now.Before(agent.nextAt) {
					continue
				}
				agent.sending = true
				toSend = append(toSend, agent)
			}
			mu.Unlock()
			for _, agent := range toSend {
				sendSem <- struct{}{}
				go func(agent *loadAgent) {
					defer func() { <-sendSem }()
					observer.inputSent(agent.id, time.Now())
					err := env.sendLoadInput(driverCtx, agent)
					mu.Lock()
					defer mu.Unlock()
					agent.sending = false
					if err != nil {
						sendErrors.Add(1)
						lastSendErr.Store(err.Error())
						agent.nextAt = time.Now().Add(time.Second)
						return
					}
					agent.inputs++
					agent.waiting = true
					agent.seenBusy = false
					agent.sentAt = time.Now()
				}(agent)
			}
		}
	}()

	var results []loadStageResult
	for _, n := range stages {
		mu.Lock()
		active = n
		mu.Unlock()
		t.Logf("stage: %d active agents (warmup %ds, measure %ds)", n, warmupSeconds, stageSeconds-warmupSeconds)
		select {
		case <-ctx.Done():
			t.Fatalf("context done during warmup")
		case <-time.After(time.Duration(warmupSeconds) * time.Second):
		}
		if os.Getenv("LOADTEST_ANALYZE_BEFORE_MEASURE") == "1" {
			analyzeStart := time.Now()
			if _, err := env.db.Exec(ctx, `ANALYZE agent_inputs, agent_events, model_call_contexts,
 model_outputs, tool_calls, tool_call_results, agent_turns, content_blocks,
 agent_execution_state, agent_wakeups, agent_runtime_locks`); err != nil {
				t.Logf("analyze before measure: %v", err)
			} else {
				t.Logf("analyzed agent tables in %s", time.Since(analyzeStart).Round(time.Millisecond))
			}
		}
		if os.Getenv("LOADTEST_PGSS_RESET") == "1" {
			_, _ = env.db.Exec(ctx, `SELECT public.pg_stat_statements_reset()`)
		}
		windowStart := time.Now()
		startPG, err := env.loadPGStat(ctx)
		require.NoError(t, err)
		startExplicit, err := env.loadExplicitTransactions(ctx)
		if err != nil {
			t.Logf("explicit transaction statistics unavailable: %v", err)
		}
		redisCPUStart := loadRedisCPU(ctx, redisClient)
		startTurns := turnsDone.Load()
		pgCPUStart, _ := pgTreeCPU()
		poolStart := scrapePool(workerMetricURLs)
		cpuStart := map[int]float64{}
		for _, p := range processes {
			cpuStart[p.pid], _ = psCPUSeconds(p.pid)
		}
		var readySum, lockSum, inflightSum, inTurnSum float64
		inTurnMax := 0
		var readyMax int64
		var samples int
		dockerCPU := map[string]float64{}
		dockerMem := map[string]float64{}
		dockerSamples := 0
		rss := map[string]float64{}
		measureEnd := windowStart.Add(time.Duration(stageSeconds-warmupSeconds) * time.Second)
		sampleTicker := time.NewTicker(3 * time.Second)
		for time.Now().Before(measureEnd) {
			var ready, locks int64
			_ = env.db.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM agent_wakeups w WHERE w.ready_at <= now()
     AND NOT EXISTS (SELECT 1 FROM agent_runtime_locks l WHERE l.agent_id = w.agent_id)),
  (SELECT count(*) FROM agent_runtime_locks)`).Scan(&ready, &locks)
			readySum += float64(ready)
			lockSum += float64(locks)
			if ready > readyMax {
				readyMax = ready
			}
			inflightSum += float64(model.inflight.Load())
			inTurn := 0
			mu.Lock()
			for _, agent := range agents[:n] {
				if agent.waiting || agent.sending {
					inTurn++
				}
			}
			mu.Unlock()
			inTurnSum += float64(inTurn)
			if inTurn > inTurnMax {
				inTurnMax = inTurn
			}
			samples++
			stats := dockerStats("omnara-loadtest-pg", "omnara-loadtest-redis")
			for name, s := range stats {
				dockerCPU[name] += s.cpuPercent
				dockerMem[name] += s.memMB
			}
			if len(stats) > 0 {
				dockerSamples++
			}
			select {
			case <-ctx.Done():
				sampleTicker.Stop()
				t.Fatal("context done during measurement")
			case <-sampleTicker.C:
			}
		}
		sampleTicker.Stop()
		windowEnd := time.Now()
		window := windowEnd.Sub(windowStart).Seconds()
		endPG, err := env.loadPGStat(ctx)
		require.NoError(t, err)
		endExplicit, err := env.loadExplicitTransactions(ctx)
		if err != nil {
			t.Logf("explicit transaction statistics unavailable: %v", err)
		}
		explicitBegins, explicitCommits := loadExplicitTransactionRates(startExplicit, endExplicit, window)
		var redisCores *float64
		if end := loadRedisCPU(ctx, redisClient); end != nil && redisCPUStart != nil && *end >= *redisCPUStart {
			redisCores = new((*end - *redisCPUStart) / window)
		}
		poolEnd := scrapePool(workerMetricURLs)
		poolAcquires := poolEnd.acquires - poolStart.acquires
		poolWaitMS := 0.0
		if poolAcquires > 0 {
			poolWaitMS = (poolEnd.acquireSeconds - poolStart.acquireSeconds) * 1000 / poolAcquires
		}
		cores := map[string]float64{}
		for _, p := range processes {
			cpu, mem := psCPUSeconds(p.pid)
			cores[p.label] += (cpu - cpuStart[p.pid]) / window
			rss[p.label] += mem
		}
		var databaseCores *float64
		databaseCPUSource := "unavailable"
		if pgCPUEnd, pgRSS := pgTreeCPU(); pgCPUStart > 0 && pgCPUEnd >= pgCPUStart {
			cores["pg_native"] = (pgCPUEnd - pgCPUStart) / window
			databaseCores = new(cores["pg_native"])
			databaseCPUSource = "postmaster_process_tree"
			rss["pg_native_sum_rss"] = pgRSS
		}
		for name, v := range dockerCPU {
			label := strings.TrimPrefix(name, "omnara-loadtest-")
			cores[label] = v / float64(dockerSamples) / 100
			rss[label] = dockerMem[name] / float64(dockerSamples)
		}
		if databaseCores == nil {
			if value, ok := cores["pg"]; ok {
				databaseCores = &value
				databaseCPUSource = "docker_samples"
			}
		}
		var acceptedOutputs int64
		require.NoError(t, env.db.QueryRow(ctx, `SELECT count(*) FROM model_outputs
 WHERE created_at >= $1 AND created_at < $2`, windowStart, windowEnd).Scan(&acceptedOutputs))
		providerRequests := observer.requestsInWindow(windowStart, windowEnd)
		var modelCalls, failed, toolResults int64
		_ = env.db.QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE state = 'succeeded'),
  count(*) FILTER (WHERE state = 'failed')
FROM model_call_contexts WHERE completed_at >= $1 AND completed_at < $2`,
			windowStart, windowEnd).Scan(&modelCalls, &failed)
		_ = env.db.QueryRow(ctx, `SELECT count(*) FROM tool_call_results WHERE completed_at >= $1 AND completed_at < $2`,
			windowStart, windowEnd).Scan(&toolResults)
		var gaps []float64
		rows, err := env.db.Query(ctx, `
WITH calls AS (
  SELECT c.agent_id, c.created_at, c.completed_at, o.stop_reason,
         lag(c.completed_at) OVER (PARTITION BY c.agent_id ORDER BY c.created_at) AS prev_done,
         lag(o.stop_reason) OVER (PARTITION BY c.agent_id ORDER BY c.created_at) AS prev_stop
  FROM model_call_contexts c
  LEFT JOIN model_outputs o ON o.agent_id = c.agent_id AND o.model_call_context_id = c.id
  WHERE c.created_at >= $1::timestamptz - interval '120 seconds' AND c.created_at < $2::timestamptz
)
SELECT extract(epoch FROM created_at - prev_done) * 1000
FROM calls
WHERE created_at >= $1::timestamptz AND prev_stop = 'tool_use' AND prev_done IS NOT NULL`, windowStart, windowEnd)
		if err == nil {
			for rows.Next() {
				var v float64
				if rows.Scan(&v) == nil {
					gaps = append(gaps, v)
				}
			}
			rows.Close()
			require.NoError(t, rows.Err())
		} else {
			t.Logf("gap query: %v", err)
		}
		sort.Float64s(gaps)
		providerGaps, providerStarts := observer.window(windowStart, windowEnd)
		var starts []float64
		startRows, err := env.db.Query(ctx, `
SELECT extract(epoch FROM first_call.created_at - input.queued_at) * 1000
FROM agent_inputs input
CROSS JOIN LATERAL (
  SELECT c.created_at FROM model_call_contexts c
  WHERE c.agent_id = input.agent_id AND c.created_at >= input.queued_at
  ORDER BY c.created_at LIMIT 1
) first_call
WHERE input.queued_at >= $1::timestamptz AND input.queued_at < $2::timestamptz`, windowStart, windowEnd)
		if err == nil {
			for startRows.Next() {
				var v float64
				if startRows.Scan(&v) == nil {
					starts = append(starts, v)
				}
			}
			startRows.Close()
			require.NoError(t, startRows.Err())
		} else {
			t.Logf("turn start query: %v", err)
		}
		sort.Float64s(starts)
		if os.Getenv("LOADTEST_PGSS_RESET") == "1" {
			topRows, err := env.db.Query(ctx, `
SELECT calls, round(mean_exec_time::numeric, 2), round(total_exec_time::numeric / 1000, 1),
       coalesce(substring(query from '-- name: (\w+)'), left(regexp_replace(query, '\s+', ' ', 'g'), 60))
FROM public.pg_stat_statements ORDER BY total_exec_time DESC LIMIT 15`)
			if err == nil {
				for topRows.Next() {
					var calls int64
					var mean, total float64
					var name string
					if topRows.Scan(&calls, &mean, &total, &name) == nil {
						t.Logf("PGSS calls=%d mean_ms=%.2f total_s=%.1f %s", calls, mean, total, name)
					}
				}
				topRows.Close()
				require.NoError(t, topRows.Err())
			}
		}
		var turnTimes []float64
		mu.Lock()
		for _, agent := range agents[:n] {
			for _, d := range agent.turnTimes {
				turnTimes = append(turnTimes, d.Seconds())
			}
			agent.turnTimes = nil
		}
		mu.Unlock()
		sort.Float64s(turnTimes)
		result := loadStageResult{
			AcceptedModelOutputs:       acceptedOutputs,
			AcceptedModelOutputsPerSec: float64(acceptedOutputs) / window,
			ProviderRequests:           providerRequests,
			ProviderRequestsPerSec:     float64(providerRequests) / window,
			PGExplicitBeginsPerSec:     explicitBegins,
			PGExplicitCommitsPerSec:    explicitCommits,
			PGAllTransactionsPerSec:    float64(endPG.commits-startPG.commits+endPG.rollbacks-startPG.rollbacks) / window,
			PGStatisticsScope:          "database",
			WorkerCPUCores:             cores["workers"],
			RedisCPUCores:              redisCores,
			DatabaseCPUCores:           databaseCores,
			DatabaseCPUSource:          databaseCPUSource,
			RedisCPUScope:              "server",

			Active:            n,
			WindowSeconds:     window,
			TurnsPerSec:       float64(turnsDone.Load()-startTurns) / window,
			ModelCallsPerSec:  float64(modelCalls) / window,
			ToolResultsPerSec: float64(toolResults) / window,
			FailedModelCalls:  failed,
			PGCommitsPerSec:   float64(endPG.commits-startPG.commits) / window,
			PGRollbacksPerSec: float64(endPG.rollbacks-startPG.rollbacks) / window,
			PGRowsWrittenPerSec: float64((endPG.inserted-startPG.inserted)+
				(endPG.updated-startPG.updated)+(endPG.deleted-startPG.deleted)) / window,
			StepGapP50ms:           percentile(gaps, 0.50),
			StepGapP95ms:           percentile(gaps, 0.95),
			StepGapP99ms:           percentile(gaps, 0.99),
			ProviderToolGapP50ms:   percentile(providerGaps, 0.50),
			ProviderToolGapP95ms:   percentile(providerGaps, 0.95),
			ProviderTurnStartP50ms: percentile(providerStarts, 0.50),
			ProviderTurnStartP95ms: percentile(providerStarts, 0.95),
			PoolAcquiresPerSec:     poolAcquires / window,
			PoolAcquireWaitAvgMS:   poolWaitMS,
			PoolEmptyAcquirePct:    100 * (poolEnd.emptyAcquires - poolStart.emptyAcquires) / math.Max(poolAcquires, 1),
			TurnStartP50ms:         percentile(starts, 0.50),
			TurnStartP95ms:         percentile(starts, 0.95),
			TurnP50s:               percentile(turnTimes, 0.50),
			TurnP95s:               percentile(turnTimes, 0.95),
			ReadyQueueAvg:          readySum / float64(samples),
			ReadyQueueMax:          readyMax,
			LocksAvg:               lockSum / float64(samples),
			ModelInflightAvg:       inflightSum / float64(samples),
			AgentsInTurnAvg:        inTurnSum / float64(samples),
			AgentsInTurnMax:        inTurnMax,
			TotalAgents:            totalAgents,
			Cores:                  cores,
			RSSMB:                  rss,
		}
		results = append(results, result)
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		t.Logf("RESULT %s", encoded)
		if v := lastSendErr.Load(); v != nil {
			t.Logf("send errors so far: %d (last: %v)", sendErrors.Load(), v)
		}
	}
	stopDriver()
	var dbSize string
	_ = env.db.QueryRow(ctx, `SELECT pg_size_pretty(pg_database_size(current_database()))`).Scan(&dbSize)
	var events, agentsCount int64
	_ = env.db.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM agent_events), (SELECT count(*) FROM agents)`).Scan(&events, &agentsCount)
	t.Logf("FINAL agents=%d events=%d db_size=%s model_calls=%d max_model_inflight=%d send_errors=%d",
		agentsCount, events, dbSize, model.calls.Load(), model.maxInflight.Load(), sendErrors.Load())
	encoded, err := json.MarshalIndent(results, "", "  ")
	require.NoError(t, err)
	_ = os.WriteFile("/tmp/omnara-loadtest-results.json", encoded, 0o644)
	if os.Getenv("LOADTEST_HOLD") == "1" {
		t.Logf("holding environment; schema=%s", env.databaseSchema)
		<-ctx.Done()
	}
}
