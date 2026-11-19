//go:build live

package openrouterbatch_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openrouterbatch"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelbatch"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
)

const (
	// The model must have a batch variant on OpenRouter; override it with
	// OMNARA_LIVE_OPENROUTER_BATCH_MODEL.
	defaultLiveBatchModel = "anthropic/claude-haiku-5.5"
	// Batches may take up to 24 hours. The test waits this long for the
	// callback (override with OMNARA_LIVE_OPENROUTER_BATCH_WAIT) and then skips.
	defaultLiveBatchWait = 15 * time.Minute
	// A callback can arrive shortly before the result is readable.
	liveBatchCollectRetries = 12
	liveBatchCollectDelay   = 10 * time.Second
)

type liveBatchCallback struct {
	Body           []byte
	IdempotencyKey string
	Signature      string
}

// TestLiveOpenRouterBatchRoundTrip is deliberately the only live batch test:
// batch inference is slow, so one request through one wrapped format covers the
// real submit, callback, and result wire shapes. The other formats share this
// transport and are covered by the offline tests.
//
// Like production, it does not poll: it exposes a local listener through a
// cloudflared quick tunnel, hands that URL to OpenRouter as the callback, and
// only reads the batch once the callback arrives.
func TestLiveOpenRouterBatchRoundTrip(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		t.Fatal("OPENROUTER_API_KEY is required for the live OpenRouter batch test")
	}
	baseURL := strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	modelSlug := strings.TrimSpace(os.Getenv("OMNARA_LIVE_OPENROUTER_BATCH_MODEL"))
	if modelSlug == "" {
		modelSlug = defaultLiveBatchModel
	}
	wait := defaultLiveBatchWait
	if raw := strings.TrimSpace(os.Getenv("OMNARA_LIVE_OPENROUTER_BATCH_WAIT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			t.Fatalf("OMNARA_LIVE_OPENROUTER_BATCH_WAIT %q is not a positive duration", raw)
		}
		wait = parsed
	}
	batchesURL, err := openrouterbatch.BatchesURLForBaseURL(baseURL)
	if err != nil {
		t.Fatalf("derive batches URL: %v", err)
	}
	auth := route.Chain{
		route.BearerToken{Token: apiKey},
		route.Headers{"HTTP-Referer": "https://omnara.com", "X-OpenRouter-Title": "Omnara Live Test"},
	}
	client := openrouterbatch.Client{
		Inner: openaichatcompletions.Client{
			Auth:              auth,
			BaseURL:           baseURL,
			EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions),
			ProviderModelSlug: modelSlug,
			APIVariant:        modelprotocol.APIVariantOpenRouter,
		},
		BatchesURL: batchesURL,
		Auth:       auth,
		HTTPClient: outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{Timeout: 2 * time.Minute}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), wait+10*time.Minute)
	defer cancel()

	// The callback URL has the same shape the worker hands to OpenRouter and
	// the API serves: a per-request id and capability token in the path.
	requestID := uuid.New()
	callbackToken, callbackTokenHash, err := modelbatch.NewCallbackToken()
	if err != nil {
		t.Fatalf("new callback token: %v", err)
	}
	callbacks := make(chan liveBatchCallback, 4)
	publicURL := exposeLiveBatchCallback(t, ctx, requestID, callbackTokenHash, callbacks)
	callbackURL := modelbatch.OpenRouterCallbackURL(publicURL, requestID, callbackToken)
	if callbackURL == "" {
		t.Fatalf("tunnel URL %q cannot be used as a callback URL", publicURL)
	}

	token := "OMNARA_LIVE_BATCH_" + time.Now().UTC().Format("20060102T150405.000000000")
	content, err := json.Marshal([]map[string]string{{
		"type": "text", "text": "Reply with exactly this token and no extra words: " + token,
	}})
	if err != nil {
		t.Fatalf("marshal live text content: %v", err)
	}
	// Prepare through the wrapper so the batch carries the exact body the
	// kernel would submit, including the streaming fields that must be stripped.
	prepared, err := client.Prepare(ctx, model.PrepareInput{
		Context: modelcontext.Bundle{
			SafetyIdentifier: modeltest.LiveSafetyIdentifier,
			SystemPrompt:     "Follow the user response format exactly.",
			Messages: []modelcontext.Message{{
				Sequence: 1, Role: modelprotocol.RoleUser, Content: content,
			}},
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare live batch request: %v", err)
	}

	// Providers restrict custom ids to [A-Za-z0-9_-]; the kernel sends the
	// model call context UUID, so the test uses the same shape.
	customID := uuid.NewString()
	submittedAt := time.Now()
	submission, err := client.SubmitDeferred(ctx, model.DeferredRequest{
		ProviderRequest: prepared.Body,
		CustomID:        customID,
		CallbackURL:     callbackURL,
	})
	if err != nil {
		t.Fatalf("submit live OpenRouter batch for model %q with a callback URL: %v", modelSlug, err)
	}
	if strings.TrimSpace(submission.ProviderHandle) == "" {
		t.Fatalf("live batch submission has no batch id: %+v", submission)
	}
	handle := model.DeferredHandle{
		ProviderHandle: submission.ProviderHandle, CustomID: customID, SubmittedAt: submittedAt,
	}
	t.Logf("submitted batch %s (expires %s); waiting up to %s for its callback",
		submission.ProviderHandle, submission.ExpiresAt, wait)

	var callback liveBatchCallback
	select {
	case callback = <-callbacks:
	case <-ctx.Done():
		t.Fatalf("waiting for the callback of batch %s: %v", submission.ProviderHandle, ctx.Err())
	case <-time.After(wait):
		// One read tells a slow provider apart from a callback that was lost.
		if _, err := client.CollectDeferred(ctx, handle); errors.Is(err, model.ErrDeferredPending) {
			t.Skipf(
				"batch %s was accepted but is still pending after %s, so no callback was due yet; "+
					"callback delivery and result parsing were not verified. "+
					"Raise OMNARA_LIVE_OPENROUTER_BATCH_WAIT to wait longer.",
				submission.ProviderHandle, wait,
			)
		} else {
			t.Fatalf("batch %s reached a final state (collect error: %v) but no callback was delivered to %s within %s",
				submission.ProviderHandle, err, publicURL, wait)
		}
	}
	t.Logf("callback after %s: idempotency key %q, signed=%t, body %s",
		time.Since(submittedAt).Round(time.Second), callback.IdempotencyKey, callback.Signature != "", callback.Body)
	assertLiveBatchCallback(t, callback, submission.ProviderHandle)

	// The callback only says the batch is final; the outcome is read from the
	// batch itself, as the worker does when it resumes the parked model call.
	for attempt := 1; ; attempt++ {
		response, err := client.CollectDeferred(ctx, handle)
		if err == nil {
			assertLiveBatchResponse(t, response, token)
			t.Logf("batch %s collected on read %d after its callback", submission.ProviderHandle, attempt)
			return
		}
		if !errors.Is(err, model.ErrDeferredPending) {
			t.Fatalf("collect live OpenRouter batch %s after its callback: %v", submission.ProviderHandle, err)
		}
		if attempt == liveBatchCollectRetries {
			t.Fatalf("batch %s still not readable %d reads after its callback: %v",
				submission.ProviderHandle, attempt, err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("collecting live OpenRouter batch %s: %v", submission.ProviderHandle, ctx.Err())
		case <-time.After(liveBatchCollectDelay):
		}
	}
}

// exposeLiveBatchCallback serves the batch callback route locally and makes it
// reachable from OpenRouter through a cloudflared quick tunnel. It returns the
// public HTTPS origin once requests through the tunnel reach the listener.
func exposeLiveBatchCallback(
	t *testing.T, ctx context.Context, requestID uuid.UUID, tokenHash []byte, callbacks chan<- liveBatchCallback,
) string {
	t.Helper()
	cloudflared, err := exec.LookPath("cloudflared")
	if err != nil {
		t.Fatalf("cloudflared is required so OpenRouter can reach the batch callback listener: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST "+modelbatch.OpenRouterCallbackPathPattern, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
		if err != nil || r.PathValue("batch_request_id") != requestID.String() ||
			!bytes.Equal(modelbatch.HashCallbackToken(r.PathValue("token")), tokenHash) {
			http.NotFound(w, r)
			return
		}
		select {
		case callbacks <- liveBatchCallback{
			Body:           body,
			IdempotencyKey: r.Header.Get("X-Openrouter-Idempotency-Key"),
			Signature:      r.Header.Get(modelbatch.SignatureHeader),
		}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	})
	listener := httptest.NewServer(mux)
	t.Cleanup(listener.Close)

	tunnel := exec.Command(cloudflared, "tunnel", "--no-autoupdate", "--url", listener.URL)
	stderr, err := tunnel.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := tunnel.Start(); err != nil {
		t.Fatalf("start cloudflared: %v", err)
	}
	t.Cleanup(func() {
		_ = tunnel.Process.Kill()
		_ = tunnel.Wait()
	})
	tunnelURLs := make(chan string, 1)
	var logsMu sync.Mutex
	var logs strings.Builder
	go func() {
		pattern := regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			logsMu.Lock()
			logs.WriteString(scanner.Text() + "\n")
			logsMu.Unlock()
			if found := pattern.FindString(scanner.Text()); found != "" {
				select {
				case tunnelURLs <- found:
				default:
				}
			}
		}
	}()
	tunnelLogs := func() string {
		logsMu.Lock()
		defer logsMu.Unlock()
		return logs.String()
	}
	var tunnelURL string
	select {
	case tunnelURL = <-tunnelURLs:
	case <-time.After(time.Minute):
		t.Fatalf("cloudflared did not report a tunnel URL: %s", tunnelLogs())
	}

	// OpenRouter rejects a callback host that does not resolve publicly yet.
	deadline := time.Now().Add(3 * time.Minute)
	for {
		var status string
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, tunnelURL+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		if response, err := http.DefaultClient.Do(request); err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				t.Logf("batch callback listener is public at %s", tunnelURL)
				return tunnelURL
			}
			status = response.Status
		} else {
			status = err.Error()
		}
		if time.Now().After(deadline) {
			t.Fatalf("tunnel %s never reached the callback listener (%s): %s", tunnelURL, status, tunnelLogs())
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for tunnel %s: %v", tunnelURL, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

func assertLiveBatchCallback(t *testing.T, callback liveBatchCallback, batchID string) {
	t.Helper()
	var event struct {
		Object string `json:"object"`
		Type   string `json:"type"`
		Data   struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(callback.Body, &event); err != nil {
		t.Fatalf("decode batch callback %s: %v", callback.Body, err)
	}
	if event.Type != "batch.completed" || event.Data.ID != batchID {
		t.Fatalf("batch callback = %s, want batch.completed for %s", callback.Body, batchID)
	}
	// Signatures are only sent when the workspace has a signing secret; when
	// the test is given that secret, the real signature must verify.
	if secret := strings.TrimSpace(os.Getenv("OMNARA_OPENROUTER_BATCH_WEBHOOK_SECRET")); secret != "" {
		if err := modelbatch.VerifyOpenRouterSignature(secret, callback.Signature, callback.Body, time.Now()); err != nil {
			t.Fatalf("batch callback signature %q did not verify: %v", callback.Signature, err)
		}
	}
}

func assertLiveBatchResponse(t *testing.T, response model.Response, token string) {
	t.Helper()
	if strings.TrimSpace(response.ID) == "" {
		t.Fatalf("live batch response id is empty: %+v", response)
	}
	if !strings.Contains(response.Text(), token) {
		t.Fatalf("live batch response text %q does not contain token %q", response.Text(), token)
	}
	if response.StopReason != model.StopReasonEndTurn {
		t.Fatalf("live batch response stop reason = %q, want end turn", response.StopReason)
	}
	if response.Usage.InputTokens == 0 || response.Usage.OutputTokens == 0 {
		t.Fatalf("live batch response usage not populated: %+v", response.Usage)
	}
}
