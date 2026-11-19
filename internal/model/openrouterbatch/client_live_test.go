//go:build live

package openrouterbatch_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openrouterbatch"
	"github.com/omnara-ai/omnara/internal/model/route"
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
	// Batches may take up to 24 hours. The test waits this long for the result
	// (override with OMNARA_LIVE_OPENROUTER_BATCH_WAIT) and then skips.
	defaultLiveBatchWait = 15 * time.Minute
	liveBatchPoll        = 30 * time.Second
)

// TestLiveOpenRouterBatchRoundTrip is deliberately the only live batch test:
// batch inference is slow, so one request through one wrapped format covers the
// real submit, status, and result wire shapes. The other formats share this
// transport and are covered by the offline tests.
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

	ctx, cancel := context.WithTimeout(context.Background(), wait+2*time.Minute)
	defer cancel()
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

	// No callback URL: a test process cannot receive one, so this exercises the
	// polling path that also backs up lost callbacks in production.
	customID := "omnara-live-" + token
	submission, err := client.SubmitDeferred(ctx, model.DeferredRequest{
		ProviderRequest: prepared.Body,
		CustomID:        customID,
	})
	if err != nil {
		t.Fatalf("submit live OpenRouter batch for model %q: %v", modelSlug, err)
	}
	if strings.TrimSpace(submission.ProviderHandle) == "" {
		t.Fatalf("live batch submission has no batch id: %+v", submission)
	}
	t.Logf("submitted batch %s (expires %s); waiting up to %s", submission.ProviderHandle, submission.ExpiresAt, wait)

	handle := model.DeferredHandle{ProviderHandle: submission.ProviderHandle, CustomID: customID}
	deadline := time.Now().Add(wait)
	polls := 0
	for {
		response, err := client.CollectDeferred(ctx, handle)
		polls++
		if err == nil {
			assertLiveBatchResponse(t, response, token)
			t.Logf("batch %s finished after %d polls", submission.ProviderHandle, polls)
			return
		}
		if !errors.Is(err, model.ErrDeferredPending) {
			t.Fatalf("collect live OpenRouter batch %s: %v", submission.ProviderHandle, err)
		}
		if time.Now().Add(liveBatchPoll).After(deadline) {
			// Submission and status reads worked; only the result was not
			// produced in time, which is the provider's schedule, not a defect.
			t.Skipf(
				"batch %s was accepted and readable but still pending after %s (%d polls); "+
					"result parsing was not verified. Raise OMNARA_LIVE_OPENROUTER_BATCH_WAIT to wait longer.",
				submission.ProviderHandle, wait, polls,
			)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for live OpenRouter batch %s: %v", submission.ProviderHandle, ctx.Err())
		case <-time.After(liveBatchPoll):
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
