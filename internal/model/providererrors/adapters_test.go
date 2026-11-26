package providererrors_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/modelretry"
)

// Exercise the public clients: each protocol must preserve the distinction
// between native fields and OpenRouter extensions through every error path.
// This does not enable additional format/variant combinations in configuration.
func TestAdaptersInterpretOpenRouterOnlyWhenConfigured(t *testing.T) {
	t.Parallel()
	for _, format := range []struct {
		name          string
		body          string
		malformedBody string
		newClient     func(*httptest.Server, modelprotocol.APIVariant) model.Client
	}{
		{
			name: "chat completions",
			body: `{"error":{"code":"invalid_request_error","message":"Request rejected.",` +
				`"metadata":{"error_type":"context_length_exceeded"}}}`,
			malformedBody: `{"error":{"code":"invalid_request_error","message":"Request rejected.",` +
				`"metadata":{"error_type":17,"raw":{"message":"prompt is too long"}}}}`,
			newClient: func(s *httptest.Server, variant modelprotocol.APIVariant) model.Client {
				return openaichatcompletions.Client{
					BaseURL: s.URL, EndpointPath: "/chat/completions", HTTPClient: s.Client(), APIVariant: variant,
				}
			},
		},
		{
			name: "responses",
			body: `{"error_type":"context_length_exceeded",` +
				`"error":{"code":"invalid_request_error","message":"Request rejected."}}`,
			malformedBody: `{"error_type":{"unrelated":"metadata"},` +
				`"error":{"code":"invalid_request_error","message":"Request rejected."}}`,
			newClient: func(s *httptest.Server, variant modelprotocol.APIVariant) model.Client {
				return openairesponses.Client{
					BaseURL: s.URL, EndpointPath: "/responses", HTTPClient: s.Client(), APIVariant: variant,
				}
			},
		},
		{
			name: "messages",
			body: `{"type":"error","error":{"type":"invalid_request_error",` +
				`"error_type":"context_length_exceeded","message":"Request rejected."}}`,
			malformedBody: `{"type":"error","error":{"type":"invalid_request_error",` +
				`"error_type":[],"message":"Request rejected."}}`,
			newClient: func(s *httptest.Server, variant modelprotocol.APIVariant) model.Client {
				return anthropicmessages.Client{
					BaseURL: s.URL, EndpointPath: "/messages", HTTPClient: s.Client(), APIVariant: variant,
				}
			},
		},
	} {
		for _, variant := range []modelprotocol.APIVariant{
			modelprotocol.APIVariantDefault, modelprotocol.APIVariantOpenRouter, modelprotocol.APIVariantBedrock,
		} {
			for _, delivery := range []string{"http failure", "in-band JSON", "stream error"} {
				for _, malformed := range []bool{false, true} {
					name := fmt.Sprintf("%s/%s/%s/malformed=%t", format.name, variant, delivery, malformed)
					t.Run(name, func(t *testing.T) {
						t.Parallel()
						status := http.StatusOK
						if delivery == "http failure" {
							status = http.StatusBadRequest
						}
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							w.Header().Set("X-Request-ID", "req-boundary")
							w.Header().Set("Retry-After", "3")
							w.Header().Set("Content-Type", "application/json")
							body := format.body
							if malformed {
								body = format.malformedBody
							}
							if delivery == "stream error" {
								w.Header().Set("Content-Type", "text/event-stream")
								body = "event: error\ndata: " + body + "\n\n"
							}
							w.WriteHeader(status)
							_, _ = w.Write([]byte(body))
						}))
						defer server.Close()
						_, err := format.newClient(server, variant).Respond(t.Context(), model.Request{
							ProviderRequest: json.RawMessage(`{"model":"test-model"}`),
						})
						got, ok := model.ClassifyError(err)
						wantKind, wantCode := model.ErrorKindInvalidRequest, "invalid_request_error"
						if variant == modelprotocol.APIVariantOpenRouter && !malformed {
							wantKind, wantCode = model.ErrorKindContextWindow, "context_length_exceeded"
						}
						if !ok || got.Kind != wantKind || got.Code != wantCode || got.Message != "Request rejected." ||
							model.IsAmbiguousProviderOutcome(err) {
							t.Fatalf("error = %+v (%v), want %s/%s", got, err, wantKind, wantCode)
						}
						if got.Source != server.URL || got.StatusCode != status || got.RequestID != "req-boundary" ||
							got.RetryAfter == nil || got.RetryAfter.DeltaSeconds == nil || *got.RetryAfter.DeltaSeconds != 3 {
							t.Fatalf("transport evidence lost: %+v", got)
						}
					})
				}
			}
		}
	}
}

func TestOpenRouterDiagnosticsSurviveRetryEvidenceWithoutRawPayloads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"code":402,"message":"You can only afford 100 tokens.",` +
			`"metadata":{"error_type":"payment_required","limit_source":"openrouter_credits",` +
			`"reason":"weight_exceeds_budget","provider_code":"credit_balance_exhausted",` +
			`"raw":{"message":"private upstream payload"},"flagged_input":"private prompt"}}}`))
	}))
	defer server.Close()
	client := openaichatcompletions.Client{
		BaseURL: server.URL, EndpointPath: "/chat/completions", HTTPClient: server.Client(),
		APIVariant: modelprotocol.APIVariantOpenRouter,
	}
	_, err := client.Respond(t.Context(), model.Request{ProviderRequest: json.RawMessage(`{"model":"test"}`)})
	got, ok := model.ClassifyError(err)
	if !ok || got.Kind != model.ErrorKindBillingAccount {
		t.Fatalf("provider error = %+v, %v", got, err)
	}
	recorded := modelretry.EvidenceFor(err)
	var details struct {
		AffordableOutputTokens *int `json:"affordable_output_tokens"`
		ProviderMetadata       struct {
			OpenRouter map[string]string `json:"openrouter"`
		} `json:"provider_metadata"`
	}
	if err := json.Unmarshal(recorded.Details, &details); err != nil {
		t.Fatal(err)
	}
	fields := details.ProviderMetadata.OpenRouter
	if details.AffordableOutputTokens == nil || *details.AffordableOutputTokens != 100 ||
		len(fields) != 5 || fields["native_code"] != "402" || fields["error_type"] != "payment_required" ||
		fields["provider_code"] != "credit_balance_exhausted" || fields["limit_source"] != "openrouter_credits" ||
		fields["reason"] != "weight_exceeds_budget" {
		t.Fatalf("persistable evidence = %s", recorded.Details)
	}
}

func TestAdaptersKeepUnrecognizedJSONErrorText(t *testing.T) {
	const body = `{"message":"Access denied by the serving platform"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	for _, client := range []model.Client{
		openaichatcompletions.Client{BaseURL: server.URL, EndpointPath: "/chat/completions", HTTPClient: server.Client()},
		openairesponses.Client{BaseURL: server.URL, EndpointPath: "/responses", HTTPClient: server.Client()},
		anthropicmessages.Client{BaseURL: server.URL, EndpointPath: "/messages", HTTPClient: server.Client()},
	} {
		t.Run(string(client.APIFormat()), func(t *testing.T) {
			_, err := client.Respond(t.Context(), model.Request{ProviderRequest: json.RawMessage(`{"model":"test"}`)})
			got, ok := model.ClassifyError(err)
			if !ok || got.Kind != model.ErrorKindAuth || got.Message != body {
				t.Fatalf("unrecognized error = %+v, %v", got, err)
			}
		})
	}
}

func TestEmptyGatewayExtensionsAreNotErrorEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/chat/completions":
			_, _ = w.Write([]byte(`{"id":"chat-ok","model":"test","error":{"metadata":{ }},"choices":[` +
				`{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
		case "/messages":
			_, _ = w.Write([]byte(`{"id":"msg-ok","type":"message","model":"test","error":{"error_type":null},` +
				`"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`))
		case "/responses":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: error\ndata: {\"error_type\":null}\n\n"))
		}
	}))
	defer server.Close()
	for _, variant := range []modelprotocol.APIVariant{
		modelprotocol.APIVariantDefault, modelprotocol.APIVariantOpenRouter,
	} {
		for _, client := range []model.Client{
			openaichatcompletions.Client{
				BaseURL: server.URL, EndpointPath: "/chat/completions", HTTPClient: server.Client(), APIVariant: variant,
			},
			anthropicmessages.Client{
				BaseURL: server.URL, EndpointPath: "/messages", HTTPClient: server.Client(), APIVariant: variant,
			},
			openairesponses.Client{
				BaseURL: server.URL, EndpointPath: "/responses", HTTPClient: server.Client(), APIVariant: variant,
			},
		} {
			t.Run(string(client.APIFormat())+"/"+string(variant), func(t *testing.T) {
				response, err := client.Respond(t.Context(), model.Request{
					ProviderRequest: json.RawMessage(`{"model":"test"}`),
				})
				if client.APIFormat() == modelprotocol.APIFormatOpenAIResponses {
					got, ok := model.ClassifyError(err)
					if !ok || got.Code != "malformed_error_event" {
						t.Fatalf("empty error event = %+v, %v", got, err)
					}
					return
				}
				if err != nil || len(response.Content) != 1 || response.Content[0].Text != "done" {
					t.Fatalf("empty extension hid a successful response: %+v, %v", response, err)
				}
			})
		}
	}
}
