package openrouterbatch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/model/openrouterbatch"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

const testModel = "anthropic/claude-haiku-5.5"

func chatInner(baseURL string) openrouterbatch.RouteProvider {
	return openaichatcompletions.Client{
		BaseURL: baseURL, EndpointPath: "/chat/completions", ProviderModelSlug: testModel,
		APIVariant: modelprotocol.APIVariantOpenRouter,
	}
}

func newClient(
	t *testing.T, inner func(baseURL string) openrouterbatch.RouteProvider, handler http.HandlerFunc,
) openrouterbatch.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	baseURL := server.URL + "/api/v1"
	batchesURL, err := openrouterbatch.BatchesURLForBaseURL(baseURL)
	if err != nil {
		t.Fatalf("batches url: %v", err)
	}
	return openrouterbatch.Client{
		Inner:      inner(baseURL),
		BatchesURL: batchesURL,
		Auth:       route.BearerToken{Token: "test-key"},
		HTTPClient: server.Client(),
	}
}

func TestBatchesURLForBaseURL(t *testing.T) {
	for baseURL, want := range map[string]string{
		"https://openrouter.ai/api/v1":  "https://openrouter.ai/api/v1/batches",
		"https://openrouter.ai/api/v1/": "https://openrouter.ai/api/v1/batches",
		"https://openrouter.ai/api":     "https://openrouter.ai/api/v1/batches",
	} {
		got, err := openrouterbatch.BatchesURLForBaseURL(baseURL)
		if err != nil || got != want {
			t.Errorf("BatchesURLForBaseURL(%q) = %q, %v; want %q", baseURL, got, err, want)
		}
	}
	if _, err := openrouterbatch.BatchesURLForBaseURL("openrouter.ai/api/v1"); err == nil {
		t.Error("relative base URL was accepted")
	}
}

func TestSubmitDeferredCreatesBatchWithCallback(t *testing.T) {
	var raw []byte
	client := newClient(t, chatInner, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/batches" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		raw, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"batch_123","object":"batch","status":"validating","expires_at":1900000000}`))
	})

	submission, err := client.SubmitDeferred(context.Background(), model.DeferredRequest{
		ProviderRequest: json.RawMessage(`{"model":"` + testModel + `","messages":[{"role":"user","content":"hi"}],` +
			`"stream":true,"stream_options":{"include_usage":true}}`),
		CustomID:    "ctx-1",
		CallbackURL: "https://omnara.example/api/model-batches/openrouter/id/token",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if submission.ProviderHandle != "batch_123" || !submission.ExpiresAt.Equal(time.Unix(1900000000, 0)) {
		t.Fatalf("submission = %+v", submission)
	}
	// OpenRouter streams the body and rejects it unless the batch settings
	// come before the requests.
	body := string(raw)
	requests := strings.Index(body, `"requests"`)
	for _, field := range []string{`"endpoint"`, `"model"`, `"completion_window"`, `"callback_url"`} {
		if at := strings.Index(body, field); at < 0 || at > requests {
			t.Errorf("%s must precede requests in %s", field, body)
		}
	}
	var decoded struct {
		Endpoint         string `json:"endpoint"`
		Model            string `json:"model"`
		CompletionWindow string `json:"completion_window"`
		CallbackURL      string `json:"callback_url"`
		Requests         []struct {
			CustomID string                     `json:"custom_id"`
			Body     map[string]json.RawMessage `json:"body"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode batch request: %v", err)
	}
	if decoded.Endpoint != "/v1/chat/completions" || decoded.Model != testModel ||
		decoded.CompletionWindow != "24h" || !strings.HasSuffix(decoded.CallbackURL, "/id/token") {
		t.Fatalf("batch settings = %+v", decoded)
	}
	if len(decoded.Requests) != 1 || decoded.Requests[0].CustomID != "ctx-1" {
		t.Fatalf("batch requests = %+v", decoded.Requests)
	}
	request := decoded.Requests[0].Body
	if _, ok := request["stream"]; ok {
		t.Error("batch request body kept stream")
	}
	if _, ok := request["stream_options"]; ok {
		t.Error("batch request body kept stream_options")
	}
	if _, ok := request["messages"]; !ok {
		t.Error("batch request body lost messages")
	}
}

func TestSubmitDeferredClassifiesRejection(t *testing.T) {
	client := newClient(t, chatInner, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"model has no batch variant"}}`))
	})
	_, err := client.SubmitDeferred(context.Background(), model.DeferredRequest{
		ProviderRequest: json.RawMessage(`{"model":"x","messages":[]}`), CustomID: "ctx-1",
	})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindInvalidRequest ||
		providerErr.Message != "model has no batch variant" || model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("submit error = %#v", err)
	}
}

func TestCollectDeferredReportsPending(t *testing.T) {
	for name, respond := range map[string]http.HandlerFunc{
		"in progress": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"id":"batch_123","status":"in_progress","results":null}`))
		},
		"completed without results yet": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"id":"batch_123","status":"completed","results":null}`))
		},
		"batch api unavailable": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		},
		"rate limited": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := newClient(t, chatInner, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/batches/batch_123" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				respond(w, r)
			})
			_, err := client.CollectDeferred(context.Background(),
				model.DeferredHandle{ProviderHandle: "batch_123", CustomID: "ctx-1"})
			if !errors.Is(err, model.ErrDeferredPending) {
				t.Fatalf("collect error = %v, want pending", err)
			}
		})
	}
}

func TestCollectDeferredParsesResultWithWrappedProtocol(t *testing.T) {
	tests := []struct {
		name  string
		inner func(baseURL string) openrouterbatch.RouteProvider
		body  string
	}{
		{
			name:  "chat completions",
			inner: chatInner,
			body: `{"id":"gen-1","model":"` + testModel + `","choices":[{"index":0,"finish_reason":"stop",` +
				`"message":{"role":"assistant","content":"hello from batch"}}],` +
				`"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`,
		},
		{
			name: "responses",
			inner: func(baseURL string) openrouterbatch.RouteProvider {
				return openairesponses.Client{BaseURL: baseURL, EndpointPath: "/responses", ProviderModelSlug: testModel}
			},
			body: `{"id":"resp_1","object":"response","status":"completed","model":"` + testModel + `",` +
				`"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed",` +
				`"content":[{"type":"output_text","text":"hello from batch","annotations":[]}]}],` +
				`"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}`,
		},
		{
			name: "messages",
			inner: func(baseURL string) openrouterbatch.RouteProvider {
				return anthropicmessages.Client{BaseURL: baseURL, EndpointPath: "/messages", ProviderModelSlug: testModel}
			},
			body: `{"id":"msg_1","type":"message","role":"assistant","model":"` + testModel + `",` +
				`"content":[{"type":"text","text":"hello from batch"}],"stop_reason":"end_turn",` +
				`"usage":{"input_tokens":3,"output_tokens":4}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newClient(t, test.inner, func(w http.ResponseWriter, _ *http.Request) {
				// Results are not in input order; the request is matched by custom id.
				_, _ = w.Write([]byte(`{"id":"batch_123","status":"completed","results":[` +
					`{"id":"batch_req_0","custom_id":"other","response":{"status_code":200,"request_id":"req-0","body":{}}},` +
					`{"id":"batch_req_1","custom_id":"ctx-1","response":{"status_code":200,"request_id":"req-1","body":` +
					test.body + `},"error":null}]}`))
			})
			response, err := client.CollectDeferred(context.Background(),
				model.DeferredHandle{ProviderHandle: "batch_123", CustomID: "ctx-1"})
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			if response.Text() != "hello from batch" || response.StopReason != model.StopReasonEndTurn {
				t.Fatalf("response = %+v", response)
			}
			if response.ProviderRequestID != "req-1" {
				t.Fatalf("provider request id = %q", response.ProviderRequestID)
			}
			if response.Usage.OutputTokens != 4 {
				t.Fatalf("usage = %+v", response.Usage)
			}
		})
	}
}

func TestCollectDeferredTakesCostFromBatchUsageWhenResultOmitsIt(t *testing.T) {
	result := `{"custom_id":"ctx-1","response":{"status_code":200,"request_id":"req-1","body":` +
		`{"id":"gen-1","model":"` + testModel + `","choices":[{"index":0,"finish_reason":"stop",` +
		`"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":3,"completion_tokens":4%s}}}}`
	for name, test := range map[string]struct {
		resultUsage string
		batchUsage  string
		want        string
	}{
		"batch cost":            {batchUsage: `{"cost":0.0125,"is_byok":false}`, want: "0.0125"},
		"byok adds upstream":    {batchUsage: `{"cost":0.01,"is_byok":true,"cost_details":{"upstream_inference_cost":0.2}}`, want: "0.21"},
		"result cost wins":      {resultUsage: `,"cost":0.5,"is_byok":false`, batchUsage: `{"cost":0.0125,"is_byok":false}`, want: "0.5"},
		"no usage on the batch": {batchUsage: `null`, want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			client := newClient(t, chatInner, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"id":"batch_123","status":"completed","usage":%s,"results":[`+result+`]}`,
					test.batchUsage, test.resultUsage)
			})
			response, err := client.CollectDeferred(context.Background(),
				model.DeferredHandle{ProviderHandle: "batch_123", CustomID: "ctx-1"})
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			if string(response.ProviderReportedCostUSD) != test.want {
				t.Fatalf("reported cost = %q, want %q", response.ProviderReportedCostUSD, test.want)
			}
		})
	}
}

func TestCollectDeferredClassifiesFailures(t *testing.T) {
	tests := []struct {
		name string
		body string
		kind model.ErrorKind
		code string
	}{
		{
			name: "request error",
			body: `{"id":"batch_123","status":"completed","results":[{"custom_id":"ctx-1","response":null,` +
				`"error":{"type":"invalid_request_error","message":"bad tool schema","error_type":"invalid_request"}}]}`,
			kind: model.ErrorKindInvalidRequest, code: "invalid_request",
		},
		{
			name: "batch failed",
			body: `{"id":"batch_123","status":"failed","error":{"message":"validation failed"}}`,
			kind: model.ErrorKindInvalidRequest, code: "batch_failed",
		},
		{
			name: "batch expired",
			body: `{"id":"batch_123","status":"expired"}`,
			kind: model.ErrorKindProviderUnavailable, code: "batch_expired",
		},
		{
			name: "batch canceled",
			body: `{"id":"batch_123","status":"cancelled"}`, //nolint:misspell // OpenRouter's wire spelling.
			kind: model.ErrorKindProviderUnavailable, code: "batch_canceled",
		},
		{
			name: "result missing",
			body: `{"id":"batch_123","status":"completed","results":[]}`,
			kind: model.ErrorKindProviderUnavailable, code: "batch_result_missing",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newClient(t, chatInner, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			})
			_, err := client.CollectDeferred(context.Background(),
				model.DeferredHandle{ProviderHandle: "batch_123", CustomID: "ctx-1"})
			providerErr, ok := model.ClassifyError(err)
			if !ok || errors.Is(err, model.ErrDeferredPending) ||
				providerErr.Kind != test.kind || providerErr.Code != test.code {
				t.Fatalf("collect error = %#v", err)
			}
		})
	}
}

func TestCollectDeferredMissingBatchIsPendingOnlyRightAfterSubmit(t *testing.T) {
	// OpenRouter accepts a batch before it can be read back, so a 404 shortly
	// after submission means "not yet", while a later one means it is gone.
	client := newClient(t, chatInner, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Batch job batch_123 not found."}}`))
	})
	collect := func(submittedAt time.Time) error {
		_, err := client.CollectDeferred(context.Background(), model.DeferredHandle{
			ProviderHandle: "batch_123", CustomID: "ctx-1", SubmittedAt: submittedAt,
		})
		return err
	}
	if err := collect(time.Now().Add(-time.Second)); !errors.Is(err, model.ErrDeferredPending) {
		t.Fatalf("missing batch right after submit: %v, want pending", err)
	}
	for name, submittedAt := range map[string]time.Time{
		"long after submit": time.Now().Add(-time.Hour),
		"unknown":           {},
	} {
		err := collect(submittedAt)
		if _, ok := model.ClassifyError(err); !ok || errors.Is(err, model.ErrDeferredPending) {
			t.Fatalf("missing batch, submitted %s: %v, want a final provider error", name, err)
		}
	}
}

func TestCollectDeferredGoneBatchIsFinal(t *testing.T) {
	client := newClient(t, chatInner, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	})
	_, err := client.CollectDeferred(context.Background(),
		model.DeferredHandle{ProviderHandle: "batch_123", CustomID: "ctx-1"})
	if _, ok := model.ClassifyError(err); !ok || errors.Is(err, model.ErrDeferredPending) {
		t.Fatalf("collect error = %v, want a final provider error", err)
	}
}
