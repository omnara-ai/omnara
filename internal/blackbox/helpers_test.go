//go:build blackbox

package blackbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The suite bootstraps one throwaway organization per run and creates every
// other resource inside it. Tests clean up the agents they create; TestMain
// sweeps any leftovers. The org itself is kept: sweeping visible resources
// exercises the public cleanup routes a real operator would use.
var fx *fixture

type fixture struct {
	client  *apiClient
	log     *runLog
	runID   string
	orgID   string
	orgPath string

	projectID   string
	projectPath string

	configID  string
	profileID string

	// Live-model resources, populated only when OMNARA_BLACKBOX_OPENROUTER_KEY
	// is set. Tests that need a real model turn use these and skip otherwise.
	liveConfigID  string
	liveProfileID string
	liveModelSlug string

	// Model stacks created at bootstrap, torn down at the end of the run so
	// provider credentials (real or placeholder) never accumulate on the
	// target deployment.
	stacks []modelStack
}

// modelStack tracks one secret -> provider config -> configured model ->
// project grant chain so teardown can delete it in reverse order.
type modelStack struct {
	name       string
	secretID   string
	providerID string
	modelID    string
	grantID    string
}

// --- run log -----------------------------------------------------------------
//
// Every API exchange the suite makes (bootstrap, tests, cleanup) is appended
// to one chronological log file, one line per request, so a whole run can be
// read top to bottom in a single place: who did what, against which endpoint,
// with what outcome. Set OMNARA_BLACKBOX_LOG_VERBOSE=1 to also record full
// request/response bodies.

type runLog struct {
	mu        sync.Mutex
	file      *os.File
	path      string
	start     time.Time
	verbose   bool
	shortener *strings.Replacer
	lastGroup string
}

func newRunLog() (*runLog, error) {
	path := os.Getenv("OMNARA_BLACKBOX_LOG")
	if path == "" {
		path = "blackbox-run.log"
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create run log %s: %w", path, err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	// lastGroup starts at "suite" so bootstrap traffic needs no header;
	// the first divider appears when the first test starts.
	return &runLog{
		file:      file,
		path:      absPath,
		start:     time.Now(),
		verbose:   os.Getenv("OMNARA_BLACKBOX_LOG_VERBOSE") != "",
		lastGroup: "suite",
	}, nil
}

// setPathAliases shortens the suite's org/project path prefixes to {org} and
// {project} in logged endpoints, keeping lines readable.
func (l *runLog) setPathAliases(projectPath, orgPath string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// projectPath first: it contains orgPath as a prefix.
	l.shortener = strings.NewReplacer(projectPath, "{project}", orgPath, "{org}")
}

// dividerLocked writes a header line when the log transitions to a different
// test (or back to suite traffic). Subtests and cleanup scopes group under
// their parent test. Callers must hold l.mu.
func (l *runLog) dividerLocked(scope string) {
	group, _, _ := strings.Cut(scope, "/")
	if group == l.lastGroup {
		return
	}
	l.lastGroup = group
	fmt.Fprintf(l.file, "\n========== %s ==========\n", group)
}

// printf writes one timestamped line: "+12.345s [scope] message".
func (l *runLog) printf(scope, format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dividerLocked(scope)
	elapsed := time.Since(l.start).Seconds()
	fmt.Fprintf(l.file, "+%8.3fs [%s] %s\n", elapsed, scope, fmt.Sprintf(format, args...))
}

// exchange logs one request as a single line: method, endpoint (with path
// aliases applied), status, latency, and a short description of intent. Error
// responses get their message inlined; full bodies only appear in verbose mode.
func (l *runLog) exchange(scope string, r apiResult, elapsed time.Duration, note string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dividerLocked(scope)
	path := r.path
	if l.shortener != nil {
		path = l.shortener.Replace(path)
	}
	line := fmt.Sprintf("%s %s -> %d (%dms)", r.method, path, r.status, elapsed.Milliseconds())
	if note != "" {
		line += "  # " + note
	}
	if r.status >= 400 {
		if msg := decodeErrorMessage(r.body); msg != "" {
			line += fmt.Sprintf("  error=%q", msg)
		}
	}
	fmt.Fprintf(l.file, "+%8.3fs [%s] %s\n", time.Since(l.start).Seconds(), scope, line)
	if l.verbose {
		if r.requestBody != "" {
			sent := r.requestBody
			// Secret endpoints carry credential material in request bodies;
			// never persist those to the log.
			if strings.Contains(r.path, "/secrets") {
				sent = "(redacted: secret payload)"
			}
			fmt.Fprintf(l.file, "            sent: %s\n", truncateForLog(sent))
		}
		if len(r.body) > 0 {
			fmt.Fprintf(l.file, "            got:  %s\n", truncateForLog(string(r.body)))
		}
	}
}

func decodeErrorMessage(body []byte) string {
	var decoded struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &decoded) != nil {
		return ""
	}
	return decoded.Error
}

func (l *runLog) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.file.Close()
}

func truncateForLog(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	const limit = 2000
	if len(s) > limit {
		return s[:limit] + fmt.Sprintf("... (%d bytes total)", len(s))
	}
	return s
}

const (
	providerConfigName  = "blackbox-openai"
	configuredModelName = "blackbox-gpt"

	liveProviderConfigName  = "blackbox-openrouter"
	liveConfiguredModelName = "blackbox-live"
	defaultLiveModelSlug    = "nvidia/nemotron-3-ultra-550b-a55b:free"
)

func agentConfigYAML() string {
	return strings.Join([]string{
		"instruction: You are a disposable test agent created by the API black-box suite. Reply concisely.",
		"model:",
		"  provider_config: " + providerConfigName,
		"  name: " + configuredModelName,
	}, "\n") + "\n"
}

func liveAgentConfigYAML() string {
	return strings.Join([]string{
		"instruction: You are a disposable test agent created by the API black-box suite. Follow the user's instructions exactly and reply concisely.",
		"model:",
		"  provider_config: " + liveProviderConfigName,
		"  name: " + liveConfiguredModelName,
	}, "\n") + "\n"
}

func TestMain(m *testing.M) {
	baseURL := strings.TrimRight(os.Getenv("OMNARA_BLACKBOX_API_URL"), "/")
	token := os.Getenv("OMNARA_BLACKBOX_TOKEN")
	if baseURL == "" || token == "" {
		fmt.Fprintln(os.Stderr, "blackbox suite: OMNARA_BLACKBOX_API_URL and OMNARA_BLACKBOX_TOKEN must be set")
		fmt.Fprintln(os.Stderr, "example: OMNARA_BLACKBOX_API_URL=https://api.example.com OMNARA_BLACKBOX_TOKEN=omnara_pat_... make test-blackbox")
		os.Exit(2)
	}

	log, err := newRunLog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "blackbox suite: %v\n", err)
		os.Exit(1)
	}

	fx, err = bootstrapFixture(baseURL, token, log)
	if err != nil {
		// Tear down whatever bootstrap managed to create before it failed,
		// so a partial run never strands provider credentials on the target.
		if fx != nil && !fx.teardownModelStacks() {
			fmt.Fprintln(os.Stderr, "blackbox suite: teardown after failed bootstrap left resources behind")
		}
		log.close()
		fmt.Fprintf(os.Stderr, "blackbox suite: bootstrap against %s failed: %v\n", baseURL, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "blackbox suite: target=%s run=%s org=%s project=%s\nblackbox suite: full request/response log: %s\n",
		baseURL, fx.runID, fx.orgID, fx.projectID, log.path)

	code := m.Run()
	fx.sweepAgents()
	if !fx.teardownModelStacks() && code == 0 {
		fmt.Fprintln(os.Stderr, "blackbox suite: teardown left resources (possibly credentials) on the target; failing the run")
		code = 1
	}
	log.printf("suite", "run finished: exit code %d", code)
	log.close()
	fmt.Fprintf(os.Stderr, "blackbox suite: full request/response log: %s\n", log.path)
	os.Exit(code)
}

func bootstrapFixture(baseURL, token string, log *runLog) (*fixture, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate run id: %w", err)
	}
	runID := time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(buf)

	f := &fixture{
		client: &apiClient{
			baseURL: baseURL,
			token:   token,
			http:    &http.Client{Timeout: 60 * time.Second},
			log:     log,
		},
		log:   log,
		runID: runID,
	}
	log.printf("suite", "run %s starting against %s", runID, baseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Validate the token and target before creating anything.
	if _, err := f.jsonStep(ctx, "verify token",
		http.MethodGet, "/api/v1/me", nil, "", http.StatusOK); err != nil {
		return f, fmt.Errorf("verify token via GET /api/v1/me: %w", err)
	}

	// TODO: the public API has no org list/delete endpoints yet, so suite orgs
	// accumulate on the target, and a run killed before teardown (package
	// timeout, canceled CI job) strands that run's model stacks inside its org.
	// Once org deletion exists, bootstrap should sweep stale "API Blackbox"
	// orgs left by crashed runs and teardown should delete this run's org.
	created, err := f.jsonStep(ctx, "create suite org",
		http.MethodPost, "/api/v1/orgs",
		map[string]any{"name": "API Blackbox " + runID},
		"bb-"+runID+"-org", http.StatusCreated)
	if err != nil {
		return f, fmt.Errorf("create suite org: %w", err)
	}
	if f.orgID, err = responseString(created, "org.id"); err != nil {
		return f, fmt.Errorf("create suite org: %w", err)
	}
	if f.projectID, err = responseString(created, "project.id"); err != nil {
		return f, fmt.Errorf("create suite org: %w", err)
	}
	f.orgPath = "/api/v1/orgs/" + f.orgID
	f.projectPath = f.orgPath + "/projects/" + f.projectID
	log.setPathAliases(f.projectPath, f.orgPath)
	log.printf("suite", "log path aliases: {org}=%s {project}=%s", f.orgPath, f.projectPath)

	// Model stack: a dummy provider credential is enough for launch tests,
	// since no test in this suite asserts on model output.
	if err := f.bootstrapModelStack(ctx, modelStackSpec{
		label:        "placeholder",
		secretName:   "blackbox-openai-key",
		secretValue:  "blackbox-placeholder-key",
		preset:       "openai",
		providerName: providerConfigName,
		modelName:    configuredModelName,
		modelSlug:    "gpt-5.5",
	}); err != nil {
		return f, err
	}

	config, err := f.jsonStep(ctx, "create shared agent config",
		http.MethodPost, f.projectPath+"/agent-configs",
		map[string]any{"source_format": "yaml", "source": agentConfigYAML()},
		"", http.StatusCreated)
	if err != nil {
		return f, fmt.Errorf("create agent config: %w", err)
	}
	if f.configID, err = responseString(config, "id"); err != nil {
		return f, fmt.Errorf("create agent config: %w", err)
	}

	profile, err := f.jsonStep(ctx, "create shared agent profile",
		http.MethodPost, f.projectPath+"/agent-profiles",
		map[string]any{"name": "Blackbox Agent", "config": f.configID},
		"bb-"+runID+"-profile", http.StatusCreated)
	if err != nil {
		return f, fmt.Errorf("create agent profile: %w", err)
	}
	if f.profileID, err = responseString(profile, "id"); err != nil {
		return f, fmt.Errorf("create agent profile: %w", err)
	}

	if err := f.bootstrapLiveModel(ctx, runID); err != nil {
		return f, err
	}
	log.printf("suite", "bootstrap complete: org=%s project=%s config=%s profile=%s",
		f.orgID, f.projectID, f.configID, f.profileID)
	return f, nil
}

// modelStackSpec describes one model stack (secret, provider config,
// configured model, project grant) for bootstrapModelStack.
type modelStackSpec struct {
	label        string
	secretName   string
	secretValue  string
	preset       string
	providerName string
	modelName    string
	modelSlug    string
}

// bootstrapModelStack creates a secret -> provider config -> configured model
// -> project grant chain and records the IDs for teardown. The stack is
// registered as soon as it exists and IDs are filled in as resources are
// created, so a mid-chain failure still leaves everything created so far
// tracked for teardown.
func (f *fixture) bootstrapModelStack(ctx context.Context, spec modelStackSpec) error {
	f.stacks = append(f.stacks, modelStack{name: spec.label})
	stack := &f.stacks[len(f.stacks)-1]
	secret, err := f.jsonStep(ctx, "store "+spec.label+" provider key",
		http.MethodPost, f.orgPath+"/secrets",
		map[string]any{
			"owner":   map[string]any{"kind": "org"},
			"name":    spec.secretName,
			"kind":    "generic",
			"payload": map[string]any{"value": spec.secretValue},
		}, "", http.StatusCreated)
	if err != nil {
		return fmt.Errorf("create %s secret: %w", spec.label, err)
	}
	stack.secretID, _ = secret["id"].(string)

	providerConfig, err := f.jsonStep(ctx, "register "+spec.label+" provider config",
		http.MethodPost, f.orgPath+"/model-provider-configs",
		map[string]any{
			"name":                 spec.providerName,
			"preset":               spec.preset,
			"credential_secret_id": stack.secretID,
		}, "", http.StatusCreated)
	if err != nil {
		return fmt.Errorf("create %s provider config: %w", spec.label, err)
	}
	config, ok := providerConfig["config"].(map[string]any)
	if !ok {
		return fmt.Errorf("create %s provider config returned an unexpected response: %+v", spec.label, providerConfig)
	}
	stack.providerID, _ = config["id"].(string)
	if stack.providerID == "" {
		return fmt.Errorf("create %s provider config returned an empty config id: %+v", spec.label, providerConfig)
	}

	configuredModel, err := f.jsonStep(ctx, "define "+spec.label+" model "+spec.modelSlug,
		http.MethodPost,
		f.orgPath+"/model-provider-configs/"+stack.providerID+"/models",
		map[string]any{
			"name":                      spec.modelName,
			"provider_model_slug":       spec.modelSlug,
			"context_window_tokens":     128000,
			"max_output_tokens":         8192,
			"default_max_output_tokens": 4096,
		}, "", http.StatusCreated)
	if err != nil {
		return fmt.Errorf("create %s configured model: %w", spec.label, err)
	}
	stack.modelID, _ = configuredModel["id"].(string)

	granted, err := f.jsonStep(ctx, "grant "+spec.label+" model to project",
		http.MethodPost, f.projectPath+"/model-grants",
		map[string]any{"configured_model_id": stack.modelID},
		"", http.StatusCreated)
	if err != nil {
		return fmt.Errorf("grant %s model to project: %w", spec.label, err)
	}
	if grant, ok := granted["grant"].(map[string]any); ok {
		stack.grantID, _ = grant["id"].(string)
	}
	if stack.secretID == "" || stack.providerID == "" || stack.modelID == "" || stack.grantID == "" {
		return fmt.Errorf("%s model stack bootstrap returned unexpected response shapes: %+v", spec.label, stack)
	}
	return nil
}

// bootstrapLiveModel provisions a real OpenRouter model stack and a "live"
// agent config/profile when OMNARA_BLACKBOX_OPENROUTER_KEY is set. Without
// the key the live tier is left unconfigured and live tests skip.
func (f *fixture) bootstrapLiveModel(ctx context.Context, runID string) error {
	key := os.Getenv("OMNARA_BLACKBOX_OPENROUTER_KEY")
	if key == "" {
		f.log.printf("suite", "no OpenRouter key configured; live-model tests will skip")
		return nil
	}
	f.liveModelSlug = os.Getenv("OMNARA_BLACKBOX_OPENROUTER_MODEL")
	if f.liveModelSlug == "" {
		f.liveModelSlug = defaultLiveModelSlug
	}

	if err := f.bootstrapModelStack(ctx, modelStackSpec{
		label:        "live",
		secretName:   "blackbox-openrouter-key",
		secretValue:  key,
		preset:       "openrouter",
		providerName: liveProviderConfigName,
		modelName:    liveConfiguredModelName,
		modelSlug:    f.liveModelSlug,
	}); err != nil {
		return err
	}

	config, err := f.jsonStep(ctx, "create live agent config",
		http.MethodPost, f.projectPath+"/agent-configs",
		map[string]any{"source_format": "yaml", "source": liveAgentConfigYAML()},
		"", http.StatusCreated)
	if err != nil {
		return fmt.Errorf("create live agent config: %w", err)
	}
	if f.liveConfigID, err = responseString(config, "id"); err != nil {
		return fmt.Errorf("create live agent config: %w", err)
	}

	profile, err := f.jsonStep(ctx, "create live agent profile",
		http.MethodPost, f.projectPath+"/agent-profiles",
		map[string]any{"name": "Blackbox Live Agent", "config": f.liveConfigID},
		"bb-"+runID+"-live-profile", http.StatusCreated)
	if err != nil {
		return fmt.Errorf("create live agent profile: %w", err)
	}
	if f.liveProfileID, err = responseString(profile, "id"); err != nil {
		return fmt.Errorf("create live agent profile: %w", err)
	}
	f.log.printf("suite", "live model tier ready: model=%s config=%s profile=%s",
		f.liveModelSlug, f.liveConfigID, f.liveProfileID)
	return nil
}

// jsonStep is the non-testing.T variant used during bootstrap and sweep.
func (f *fixture) jsonStep(
	ctx context.Context,
	note, method, path string,
	body any,
	idempotencyKey string,
	wantStatus int,
) (map[string]any, error) {
	res, err := f.client.do(ctx, method, path, body, requestOptions{
		idempotencyKey: idempotencyKey,
		useAuth:        true,
		note:           note,
	})
	if err != nil {
		return nil, err
	}
	if res.status != wantStatus {
		return nil, fmt.Errorf("%s %s: expected status %d, got %d: %s",
			method, path, wantStatus, res.status, string(res.body))
	}
	decoded := map[string]any{}
	if len(res.body) > 0 {
		if err := json.Unmarshal(res.body, &decoded); err != nil {
			return nil, fmt.Errorf("%s %s: decode response: %w: %s", method, path, err, string(res.body))
		}
	}
	return decoded, nil
}

// sweepAgents deletes any agents still active in the suite project so a run
// leaves as little state behind as the API allows. The agent list is
// cursor-paged, so it walks every page.
func (f *fixture) sweepAgents() {
	f.log.printf("suite", "sweeping leftover active agents")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cursor := ""
	for {
		path := f.projectPath + "/agents?limit=100"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		listed, err := f.jsonStep(ctx, "find leftover agents",
			http.MethodGet, path, nil, "", http.StatusOK)
		if err != nil {
			fmt.Fprintf(os.Stderr, "blackbox suite: leftover-agent sweep failed: %v\n", err)
			return
		}
		agents, _ := listed["data"].([]any)
		for _, raw := range agents {
			agent, ok := raw.(map[string]any)
			if !ok || agent["state"] != "active" {
				continue
			}
			id, _ := agent["id"].(string)
			if _, err := f.jsonStep(ctx, "archive leftover agent",
				http.MethodPost, f.projectPath+"/agents/"+id+"/archive", nil, "", http.StatusOK); err != nil {
				fmt.Fprintf(os.Stderr, "blackbox suite: sweep delete %s failed: %v\n", id, err)
			}
		}
		cursor, _ = listed["next_cursor"].(string)
		if cursor == "" {
			return
		}
	}
}

// teardownModelStacks dismantles each bootstrap model stack in reverse
// dependency order (grant, model, provider config, secret) so provider
// credentials never accumulate on the target. Provider-config deletion is an
// archive that keeps referencing the secret, so the secret cannot be hard
// deleted; it is scrubbed by rotating in a placeholder value instead.
// Resources a partial bootstrap never created (empty IDs) are skipped.
// Reports whether every step succeeded.
func (f *fixture) teardownModelStacks() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ok := true
	for _, stack := range f.stacks {
		steps := []struct {
			id, note, method, path string
			wantStatus             int
		}{
			{stack.grantID, "delete " + stack.name + " model grant", http.MethodDelete,
				f.projectPath + "/model-grants/" + stack.grantID, http.StatusNoContent},
			{stack.modelID, "delete " + stack.name + " model", http.MethodDelete,
				f.orgPath + "/model-provider-configs/" + stack.providerID + "/models/" + stack.modelID, http.StatusNoContent},
			{stack.providerID, "delete " + stack.name + " provider config", http.MethodDelete,
				f.orgPath + "/model-provider-configs/" + stack.providerID, http.StatusNoContent},
		}
		for _, s := range steps {
			if s.id == "" {
				continue
			}
			if _, err := f.jsonStep(ctx, s.note, s.method, s.path, nil, "", s.wantStatus); err != nil {
				fmt.Fprintf(os.Stderr, "blackbox suite: teardown: %s failed: %v\n", s.note, err)
				ok = false
			}
		}
		if stack.secretID == "" {
			continue
		}
		if _, err := f.jsonStep(ctx, "scrub "+stack.name+" secret value",
			http.MethodPost, f.orgPath+"/secrets/"+stack.secretID+"/versions",
			map[string]any{"payload": map[string]any{"value": "blackbox-scrubbed"}},
			"", http.StatusOK); err != nil {
			fmt.Fprintf(os.Stderr, "blackbox suite: teardown: scrub %s secret failed: %v\n", stack.name, err)
			ok = false
		}
	}
	return ok
}

// --- HTTP client -----------------------------------------------------------

type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
	log     *runLog
}

type requestOptions struct {
	idempotencyKey string
	useAuth        bool
	// tokenOverride replaces the fixture token when useAuth is true.
	tokenOverride string
	// scope labels this exchange in the run log (test name, "suite", ...).
	scope string
	// note is a few-words description of what this request is doing,
	// recorded next to it in the run log.
	note string
}

// apiResult captures one request/response exchange so assertion failures can
// print the complete picture without re-running anything.
type apiResult struct {
	method      string
	path        string
	requestBody string
	status      int
	body        []byte
}

func (c *apiClient) do(ctx context.Context, method, path string, body any, opts requestOptions) (apiResult, error) {
	result := apiResult{method: method, path: path}
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string: // raw payload, used for malformed-body tests
		result.requestBody = b
		reader = strings.NewReader(b)
	default:
		encoded, err := json.Marshal(body)
		if err != nil {
			return result, fmt.Errorf("encode request body: %w", err)
		}
		result.requestBody = string(encoded)
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return result, fmt.Errorf("build request: %w", err)
	}
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if opts.idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", opts.idempotencyKey)
	}
	if opts.useAuth {
		token := c.token
		if opts.tokenOverride != "" {
			token = opts.tokenOverride
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	scope := opts.scope
	if scope == "" {
		scope = "suite"
	}
	started := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.log.printf(scope, "%s %s -> transport error: %v", method, path, err)
		return result, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	result.status = resp.StatusCode
	result.body, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		c.log.printf(scope, "%s %s -> read error: %v", method, path, err)
		return result, fmt.Errorf("%s %s: read response body: %w", method, path, err)
	}
	c.log.exchange(scope, result, time.Since(started), opts.note)
	return result, nil
}

// --- test-facing helpers ---------------------------------------------------

// api issues an authenticated request; note is a few-words description of
// what the request does, recorded next to it in the run log.
func api(t *testing.T, note, method, path string, body any) apiResult {
	t.Helper()
	return apiWith(t, method, path, body, requestOptions{useAuth: true, note: note})
}

// apiIdem is api with an Idempotency-Key header.
func apiIdem(t *testing.T, note, method, path string, body any, key string) apiResult {
	t.Helper()
	return apiWith(t, method, path, body, requestOptions{useAuth: true, idempotencyKey: key, note: note})
}

func apiWith(t *testing.T, method, path string, body any, opts requestOptions) apiResult {
	t.Helper()
	if opts.scope == "" {
		opts.scope = t.Name()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := fx.client.do(ctx, method, path, body, opts)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return res
}

// step records a human-readable narrative line in both the test output and
// the run log, so a run reads as "what we did" rather than raw traffic only.
func step(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Logf(format, args...)
	fx.log.printf(t.Name(), "step: "+format, args...)
}

func (r apiResult) describe() string {
	body := r.requestBody
	if body == "" {
		body = "(none)"
	}
	return fmt.Sprintf("  request:  %s %s\n  body:     %s\n  status:   %d\n  response: %s",
		r.method, r.path, body, r.status, string(r.body))
}

// requireStatus fails the test with the full exchange when the status differs.
func (r apiResult) requireStatus(t *testing.T, want int) apiResult {
	t.Helper()
	if r.status != want {
		t.Fatalf("expected status %d, got %d\n%s", want, r.status, r.describe())
	}
	return r
}

// json decodes the response body as a JSON object.
func (r apiResult) json(t *testing.T) map[string]any {
	t.Helper()
	decoded := map[string]any{}
	if err := json.Unmarshal(r.body, &decoded); err != nil {
		t.Fatalf("response is not a JSON object: %v\n%s", err, r.describe())
	}
	return decoded
}

// errorMessage decodes the standard {"error": "..."} payload.
func (r apiResult) errorMessage(t *testing.T) string {
	t.Helper()
	decoded := r.json(t)
	message, ok := decoded["error"].(string)
	if !ok || message == "" {
		t.Fatalf("expected error response with non-empty \"error\" string\n%s", r.describe())
	}
	return message
}

// responseString extracts a nested non-empty string field ("org.id" style
// dotted path), returning an error instead of panicking on unexpected shapes.
// Bootstrap uses it because a panic there would skip TestMain's teardown and
// could strand provider credentials on the target.
func responseString(obj map[string]any, dottedPath string) (string, error) {
	parts := strings.Split(dottedPath, ".")
	current := any(obj)
	for _, part := range parts {
		asMap, ok := current.(map[string]any)
		if !ok {
			return "", fmt.Errorf("response field %q missing or not an object", dottedPath)
		}
		if current, ok = asMap[part]; !ok {
			return "", fmt.Errorf("response field %q missing (at %q)", dottedPath, part)
		}
	}
	value, ok := current.(string)
	if !ok || value == "" {
		return "", fmt.Errorf("response field %q is not a non-empty string (got %T)", dottedPath, current)
	}
	return value, nil
}

// getString extracts a nested string field ("agent.id" style dotted path).
func getString(t *testing.T, obj map[string]any, dottedPath string) string {
	t.Helper()
	parts := strings.Split(dottedPath, ".")
	current := any(obj)
	for i, part := range parts {
		asMap, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("field %q: %q is not an object", dottedPath, strings.Join(parts[:i], "."))
		}
		current, ok = asMap[part]
		if !ok {
			t.Fatalf("field %q is missing (at %q)", dottedPath, part)
		}
	}
	value, ok := current.(string)
	if !ok {
		t.Fatalf("field %q is %T, expected string", dottedPath, current)
	}
	return value
}

// createAgentForTest launches an agent and registers cleanup that archives it.
func createAgentForTest(t *testing.T, note string, body map[string]any, idempotencyKey string) map[string]any {
	t.Helper()
	res := apiIdem(t, note, http.MethodPost, fx.projectPath+"/agents", body, idempotencyKey).
		requireStatus(t, http.StatusCreated)
	launched := res.json(t)
	agentID := getString(t, launched, "agent.id")
	registerAgentCleanup(t, agentID)
	return launched
}

// registerAgentCleanup archives an agent at test end, tolerating tests that
// already deleted it themselves. If the test failed, the agent's full event
// timeline is dumped first so the log shows what the agent actually did.
func registerAgentCleanup(t *testing.T, agentID string) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			logAgentEventTimeline(t, agentID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := fx.client.do(ctx, http.MethodPost, fx.projectPath+"/agents/"+agentID+"/archive", nil,
			requestOptions{useAuth: true, scope: t.Name() + "/cleanup", note: "archive agent"})
		if err != nil {
			t.Logf("cleanup: archive agent %s failed: %v", agentID, err)
			return
		}
		if res.status != http.StatusOK && res.status != http.StatusNotFound {
			t.Errorf("cleanup: archive agent %s: unexpected status %d: %s", agentID, res.status, string(res.body))
		}
	})
}

// logAgentEventTimeline fetches an agent's event log and writes a compact
// chronological timeline (sequence, turn, kind, sender, content preview) to
// both the test output and the run log. Useful whenever a test needs to show
// what one or more agents did, all in one place.
func logAgentEventTimeline(t *testing.T, agentID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := fx.client.do(ctx, http.MethodGet,
		fx.projectPath+"/agents/"+agentID+"/events?limit=500", nil,
		requestOptions{useAuth: true, scope: t.Name() + "/timeline", note: "fetch event timeline"})
	if err != nil || res.status != http.StatusOK {
		t.Logf("timeline: could not fetch events for %s: err=%v status=%d", agentID, err, res.status)
		return
	}
	var decoded struct {
		Data []struct {
			Sequence      int64            `json:"sequence"`
			TurnSequence  int64            `json:"turn_sequence"`
			EventKind     string           `json:"event_kind"`
			InputKind     string           `json:"input_kind"`
			ContentBlocks []map[string]any `json:"content_blocks"`
			CreatedAt     string           `json:"created_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.body, &decoded); err != nil {
		t.Logf("timeline: decode events for %s: %v", agentID, err)
		return
	}
	logLine := func(format string, args ...any) {
		t.Logf(format, args...)
		fx.log.printf(t.Name(), format, args...)
	}
	logLine("timeline for %s: %d events", agentID, len(decoded.Data))
	for _, ev := range decoded.Data {
		kind := ev.EventKind
		if ev.InputKind != "" {
			kind += "/" + ev.InputKind
		}
		preview := ""
		if len(ev.ContentBlocks) > 0 {
			if raw, err := json.Marshal(ev.ContentBlocks); err == nil {
				preview = truncateForLog(string(raw))
				if len(preview) > 160 {
					preview = preview[:160] + "..."
				}
			}
		}
		logLine("  seq %3d  turn %2d  %-28s %s", ev.Sequence, ev.TurnSequence, kind, preview)
	}
}

// uniqueKey builds a run-scoped idempotency key so reruns never collide.
func uniqueKey(t *testing.T, suffix string) string {
	t.Helper()
	return "bb-" + fx.runID + "-" + strings.ToLower(t.Name()) + "-" + suffix
}

// --- live-model helpers ------------------------------------------------------

// requireLiveModel skips the test unless the live model tier was configured
// at bootstrap (OMNARA_BLACKBOX_OPENROUTER_KEY).
func requireLiveModel(t *testing.T) {
	t.Helper()
	if fx.liveProfileID == "" {
		t.Skip("live-model test: set OMNARA_BLACKBOX_OPENROUTER_KEY to run")
	}
}

// agentEvent is the subset of the public AgentEvent schema the suite asserts on.
type agentEvent struct {
	ID            string           `json:"id"`
	TurnSequence  int64            `json:"turn_sequence"`
	Sequence      int64            `json:"sequence"`
	EventKind     string           `json:"event_kind"`
	ContentBlocks []map[string]any `json:"content_blocks"`
	CreatedAt     string           `json:"created_at"`
}

// textContent concatenates the text of all text content blocks in the event.
func (e agentEvent) textContent() string {
	var parts []string
	for _, block := range e.ContentBlocks {
		if block["type"] == "text" {
			if text, ok := block["text"].(string); ok {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func awaitAgentEvent(
	t *testing.T,
	agentID string,
	timeout time.Duration,
	what string,
	match func(agentEvent) bool,
) agentEvent {
	t.Helper()
	step(t, "wait up to %s for %s", timeout, what)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	afterSequence := int64(0)
	for {
		res, err := fx.client.do(ctx, http.MethodGet,
			fmt.Sprintf("%s/agents/%s/events?after_sequence=%d&limit=500", fx.projectPath, agentID, afterSequence),
			nil,
			requestOptions{useAuth: true, scope: t.Name(), note: "backfill events (" + what + ")"})
		if err != nil {
			t.Fatalf("backfill events: %v", err)
		}
		if res.status != http.StatusOK {
			t.Fatalf("backfill events: unexpected status\n%s", res.describe())
		}
		var page struct {
			Data              []agentEvent `json:"data"`
			NextAfterSequence int64        `json:"next_after_sequence"`
			HasMore           bool         `json:"has_more"`
		}
		if err := json.Unmarshal(res.body, &page); err != nil {
			t.Fatalf("decode events page: %v\n%s", err, res.describe())
		}
		for _, event := range page.Data {
			if match(event) {
				return event
			}
		}
		afterSequence = page.NextAfterSequence
		if !page.HasMore {
			break
		}
	}
	return streamAgentEventUntilMatch(t, ctx, agentID, afterSequence, timeout, what, match)
}

func streamAgentEventUntilMatch(
	t *testing.T,
	ctx context.Context,
	agentID string,
	afterSequence int64,
	timeout time.Duration,
	what string,
	match func(agentEvent) bool,
) agentEvent {
	t.Helper()
	streamPath := fmt.Sprintf("%s/agents/%s/events/stream?after_sequence=%d", fx.projectPath, agentID, afterSequence)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fx.client.baseURL+streamPath, nil)
	if err != nil {
		t.Fatalf("build event stream request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+fx.client.token)
	req.Header.Set("Accept", "text/event-stream")
	fx.log.printf(t.Name(), "GET %s -> streaming (%s)", streamPath, what)
	streamClient := &http.Client{Transport: fx.client.http.Transport}
	resp, err := streamClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			logAgentEventTimeline(t, agentID)
			t.Fatalf("timed out after %s waiting for %s (agent %s)", timeout, what, agentID)
		}
		t.Fatalf("open event stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		t.Fatalf("event stream: unexpected status %d\n%s", resp.StatusCode, string(body))
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	eventName := ""
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if len(data) > 0 {
				frame := strings.Join(data, "\n")
				if eventName == "error" {
					t.Fatalf("event stream error frame: %s (agent %s, waiting for %s)", frame, agentID, what)
				}
				var event agentEvent
				if err := json.Unmarshal([]byte(frame), &event); err != nil {
					t.Fatalf("decode event stream frame: %v\nframe: %s", err, frame)
				}
				if match(event) {
					return event
				}
			}
			eventName = ""
			data = nil
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event: "):
			eventName = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = append(data, strings.TrimPrefix(line, "data: "))
		}
	}
	if ctx.Err() != nil {
		logAgentEventTimeline(t, agentID)
		t.Fatalf("timed out after %s waiting for %s (agent %s)", timeout, what, agentID)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("event stream read: %v (agent %s, waiting for %s)", err, agentID, what)
	}
	t.Fatalf("event stream ended before %s (agent %s)", what, agentID)
	return agentEvent{}
}

// createInput posts a text input to an agent and returns the input id.
func createInput(t *testing.T, note, agentID, text, idempotencyKey string) string {
	t.Helper()
	created := apiIdem(t, note,
		http.MethodPost, fx.projectPath+"/agents/"+agentID+"/inputs",
		map[string]any{
			"content_blocks": []map[string]any{{"type": "text", "text": text}},
		}, idempotencyKey).
		requireStatus(t, http.StatusCreated).json(t)
	return getString(t, created, "agent_input.id")
}
