package providererrors

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestNativeAndGatewayEvidenceHaveDistinctAuthority(t *testing.T) {
	for _, test := range []struct {
		name     string
		variant  modelprotocol.APIVariant
		evidence Evidence
		want     model.ErrorKind
		wantCode string
	}{
		{
			name:     "OpenAI billing code refines 429",
			evidence: Evidence{HTTPStatus: 429, Native: OpenAI("credit_balance_exhausted", "insufficient_quota")},
			want:     model.ErrorKindBillingAccount, wantCode: "credit_balance_exhausted",
		},
		{
			name:     "OpenAI slow down code in a successful HTTP envelope",
			evidence: Evidence{HTTPStatus: 200, Native: OpenAI("slow_down", "rate_limit_error")},
			want:     model.ErrorKindRateLimit, wantCode: "slow_down",
		},
		{
			name:     "Anthropic spending detail refines rate category",
			evidence: Evidence{HTTPStatus: 429, Native: Anthropic("rate_limit_error", "enforced_spend_limit_reached")},
			want:     model.ErrorKindBillingAccount, wantCode: "enforced_spend_limit_reached",
		},
		{
			name: "detailed spending reason survives coarse gateway type", variant: modelprotocol.APIVariantOpenRouter,
			evidence: Evidence{HTTPStatus: 429, Native: Anthropic("rate_limit_error", "enforced_spend_limit_reached"),
				OpenRouter: openRouterEvidence(t, openRouterError{ErrorType: "rate_limit_exceeded"})},
			want: model.ErrorKindBillingAccount, wantCode: "enforced_spend_limit_reached",
		},
		{
			name: "gateway auth survives lossy Responses code", variant: modelprotocol.APIVariantOpenRouter,
			evidence: Evidence{HTTPStatus: 200, Native: OpenAI("server_error", ""),
				OpenRouter: openRouterEvidence(t, openRouterError{ErrorType: "authentication"})},
			want: model.ErrorKindAuth, wantCode: "authentication",
		},
		{
			name: "gateway field ignored without configured variant",
			evidence: Evidence{HTTPStatus: 200, Native: OpenAI("server_error", ""),
				OpenRouter: openRouterEvidence(t, openRouterError{ErrorType: "authentication"})},
			want: model.ErrorKindProviderUnavailable, wantCode: "server_error",
		},
		{
			name: "upstream Anthropic spend code inside OpenRouter", variant: modelprotocol.APIVariantOpenRouter,
			evidence: Evidence{HTTPStatus: 429, Native: OpenAI("429", ""),
				OpenRouter: openRouterEvidence(t, openRouterError{ProviderCode: "enforced_spend_limit_reached"})},
			want: model.ErrorKindBillingAccount, wantCode: "enforced_spend_limit_reached",
		},
		{
			name: "upstream spending detail refines canonical rate limit", variant: modelprotocol.APIVariantOpenRouter,
			evidence: Evidence{HTTPStatus: 429, Native: OpenAI("429", ""),
				OpenRouter: openRouterEvidence(t, openRouterError{
					ErrorType: "rate_limit_exceeded", ProviderCode: "enforced_spend_limit_reached",
				})},
			want: model.ErrorKindBillingAccount, wantCode: "rate_limit_exceeded",
		},
		{
			name: "upstream billing does not replace gateway authentication", variant: modelprotocol.APIVariantOpenRouter,
			evidence: Evidence{HTTPStatus: 401, Native: OpenAI("401", ""),
				OpenRouter: openRouterEvidence(t, openRouterError{
					ErrorType: "authentication", ProviderCode: "enforced_spend_limit_reached",
				})},
			want: model.ErrorKindAuth, wantCode: "authentication",
		},
		{
			name:     "foreign native field is not silently another vocabulary",
			evidence: Evidence{HTTPStatus: 200, Native: OpenAI("enforced_spend_limit_reached", "")},
			want:     model.ErrorKindUnknown, wantCode: "enforced_spend_limit_reached",
		},
		{
			name:     "native Responses image code",
			evidence: Evidence{HTTPStatus: 200, Native: OpenAI("unsupported_image_media_type", "")},
			want:     model.ErrorKindInvalidRequest, wantCode: "unsupported_image_media_type",
		},
		{
			name:     "OpenRouter image spelling is not a native Responses code",
			evidence: Evidence{HTTPStatus: 200, Native: OpenAI("unsupported_image_format", "")},
			want:     model.ErrorKindUnknown, wantCode: "unsupported_image_format",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Interpret(test.variant, test.evidence)
			if got.Kind != test.want || got.Code != test.wantCode {
				t.Fatalf("interpretation = %+v, want %s/%s", got, test.want, test.wantCode)
			}
		})
	}
}

func TestOpenRouterLimitSourcesAreNotErrorCodes(t *testing.T) {
	for _, source := range []string{
		"openrouter_in_flight_budget", "openrouter_credits", "openrouter_key_limit",
		"openrouter_workspace_budget", "openrouter_guardrail_budget",
	} {
		t.Run(source, func(t *testing.T) {
			want := model.ErrorKindBillingAccount
			if source == "openrouter_in_flight_budget" {
				want = model.ErrorKindTransient
			}
			evidence := Evidence{HTTPStatus: 200, OpenRouter: openRouterEvidence(t, openRouterError{LimitSource: source})}
			if got := Interpret(modelprotocol.APIVariantOpenRouter, evidence); got.Kind != want {
				t.Fatalf("limit source = %+v, want %s", got, want)
			}
			for _, variant := range []modelprotocol.APIVariant{
				modelprotocol.APIVariantDefault, modelprotocol.APIVariantBedrock,
			} {
				if got := Interpret(variant, evidence); got.Kind != model.ErrorKindUnknown {
					t.Fatalf("%s interpreted OpenRouter's limit: %+v", variant, got)
				}
			}
			for _, misplaced := range []Evidence{
				{HTTPStatus: 200, Native: OpenAI(source, "")},
				{HTTPStatus: 200, OpenRouter: openRouterEvidence(t, openRouterError{ErrorType: source})},
				{HTTPStatus: 200, OpenRouter: openRouterEvidence(t, openRouterError{ProviderCode: source})},
			} {
				if got := Interpret(modelprotocol.APIVariantOpenRouter, misplaced); got.Kind != model.ErrorKindUnknown {
					t.Fatalf("limit source in another namespace = %+v", got)
				}
			}
		})
	}
}

func TestForwardedUpstreamFailuresMatchNativeFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		message  string
		detail   string
		status   int
		typeName string
		want     model.ErrorKind
	}{
		{name: "context", message: "prompt is too long: 201312 tokens > 200000 maximum", want: model.ErrorKindContextWindow},
		{name: "replay", message: "Invalid signature in thinking block", want: model.ErrorKindReplayRejected},
		{
			name:    "credit balance",
			message: "Your credit balance is too low to access the Anthropic API.",
			want:    model.ErrorKindBillingAccount,
		},
		{
			name:     "spending detail",
			message:  "Limit reached.",
			detail:   "enforced_spend_limit_reached",
			status:   http.StatusTooManyRequests,
			typeName: "rate_limit_error",
			want:     model.ErrorKindBillingAccount,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, typeName := test.status, test.typeName
			if status == 0 {
				status, typeName = http.StatusBadRequest, "invalid_request_error"
			}
			native := Evidence{HTTPStatus: status, Message: test.message, Native: Anthropic(typeName, test.detail)}
			if got := Interpret(modelprotocol.APIVariantDefault, native); got.Kind != test.want {
				t.Fatalf("native error = %+v, want %s", got, test.want)
			}
			raw, err := json.Marshal(map[string]any{"error": map[string]any{
				"type": typeName, "message": test.message,
				"details": map[string]any{"error_code": test.detail},
			}})
			if err != nil {
				t.Fatal(err)
			}
			// Exercise a generic 400 gateway wrapper independently of the
			// upstream status; other gateway statuses intentionally do not unwrap.
			wrapped := Evidence{HTTPStatus: 400, Message: "Provider returned error", Native: OpenAI("400", ""),
				OpenRouter: openRouterEvidence(t, openRouterError{
					ErrorType: "invalid_request", ProviderCode: "INVALID_ARGUMENT", Raw: raw,
				}),
			}
			got := Interpret(modelprotocol.APIVariantOpenRouter, wrapped)
			if got.Kind != test.want || got.Message != test.message {
				t.Fatalf("relayed error = %+v, want %s/%s", got, test.want, test.message)
			}
			// Gateways such as LiteLLM also forward the vendor's message as prose.
			// A detail-only reason cannot be recovered from prose that omits it.
			if test.detail == "" {
				prefixed := Evidence{HTTPStatus: 400, Message: "litellm.BadRequestError: AnthropicException - " + test.message,
					Native: OpenAI("400", "invalid_request_error")}
				if got := Interpret(modelprotocol.APIVariantDefault, prefixed); got.Kind != test.want {
					t.Fatalf("prefixed error = %+v, want %s", got, test.want)
				}
			}
		})
	}
}

func TestOpenRouterRawRefinementProtectsOuterAuthority(t *testing.T) {
	for _, test := range []struct {
		name         string
		status       int
		canonical    string
		upstreamCode string
		limit        string
		want         model.ErrorKind
		refined      bool
	}{
		{
			name:         "unknown upstream code allows raw detail",
			status:       400,
			canonical:    "invalid_request",
			upstreamCode: "INVALID_ARGUMENT",
			want:         model.ErrorKindContextWindow,
			refined:      true,
		},
		{
			name:      "unknown canonical type stays conservative",
			status:    400,
			canonical: "future_gateway_type",
			want:      model.ErrorKindInvalidRequest,
		},
		{
			name:         "known upstream auth stays authoritative",
			status:       400,
			upstreamCode: "authentication_error",
			want:         model.ErrorKindAuth,
		},
		{
			name:         "known upstream permission stays authoritative",
			status:       400,
			upstreamCode: "permission_error",
			want:         model.ErrorKindAuth,
		},
		{name: "canonical auth stays authoritative", status: 400, canonical: "authentication", want: model.ErrorKindAuth},
		{
			name:      "masked server failure",
			status:    502,
			canonical: "provider_unavailable",
			want:      model.ErrorKindProviderUnavailable,
		},
		{name: "HTTP auth failure", status: 401, canonical: "provider_unavailable", want: model.ErrorKindAuth},
		{name: "unknown spending constraint", status: 400, limit: "future_budget", want: model.ErrorKindInvalidRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := Evidence{
				HTTPStatus: test.status, Message: "Provider returned error", Native: OpenAI("", "invalid_request_error"),
				OpenRouter: openRouterEvidence(t, openRouterError{
					ErrorType: test.canonical, ProviderCode: test.upstreamCode, LimitSource: test.limit,
					Raw: json.RawMessage(`{"error":{"message":"prompt is too long"}}`),
				}),
			}
			got := Interpret(modelprotocol.APIVariantOpenRouter, evidence)
			wantMessage := "Provider returned error"
			if test.refined {
				wantMessage = "prompt is too long"
			}
			if got.Kind != test.want || got.Message != wantMessage {
				t.Fatalf("interpretation = %+v, want %s/%s", got, test.want, wantMessage)
			}
		})
	}
}

func TestAffordableOutputTokensAreOnlyAProviderHint(t *testing.T) {
	for _, test := range []struct {
		message string
		want    *int
	}{
		{message: "You can only afford 100 tokens.", want: new(100)},
		{message: "You can only afford 0 tokens.", want: new(0)},
		{message: "You can only afford 999999999999999999999999999999999 tokens."},
		{message: "Insufficient credits."},
	} {
		got := Interpret(modelprotocol.APIVariantOpenRouter, Evidence{
			HTTPStatus: http.StatusPaymentRequired, Message: test.message,
		})
		if (got.AffordableOutputTokens == nil) != (test.want == nil) ||
			got.AffordableOutputTokens != nil && *got.AffordableOutputTokens != *test.want {
			t.Fatalf("%q allowance = %v, want %v", test.message, got.AffordableOutputTokens, test.want)
		}
	}
}
