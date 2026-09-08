//go:build live

package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/stretchr/testify/require"
)

func TestLiveAnthropicOutputAllowance(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY is not set")
	}
	auth := route.HeaderAuth{Header: "x-api-key", Value: apiKey}
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	provider := modelstore.ModelProviderConfigRecord{
		ID: uuid.New(), APIFormat: modelprotocol.APIFormatAnthropicMessages,
		BaseURL: baseURL, UpdatedAt: time.Now(),
	}
	slug := modeltest.LiveAnthropicProviderModelSlug
	key := messagesOutputLimitKey{providerID: provider.ID, providerUpdatedAt: provider.UpdatedAt.UnixNano(), slug: slug}
	for _, unavailable := range []bool{false, true} {
		name := "published-limit"
		if unavailable {
			name = "cached-unavailable-limit"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			cache := &MessagesOutputLimits{}
			var published int
			_, err := cache.lookup(ctx, key, func(ctx context.Context) (int, error) {
				if unavailable {
					// Inject only the metadata failure; generation still uses the real provider.
					return 0, errors.New("model metadata unavailable")
				}
				var err error
				published, err = fetchMessagesOutputLimit(ctx, provider.BaseURL, slug, auth, newSSRFHTTPClient(false))
				return published, err
			})
			require.NoError(t, err)
			want := messagesFallbackOutputTokens
			if !unavailable {
				require.Positive(t, published, "live Models API must supply an output limit")
				want = published
			}
			allowance, err := (Resolver{MessagesOutputLimits: cache}).messagesOutputAllowance(
				ctx, provider, uuid.Nil, slug, auth,
			)
			require.NoError(t, err)
			require.Equal(t, want, allowance)
			client := anthropicmessages.Client{
				Auth: auth, BaseURL: provider.BaseURL, EndpointPath: "/messages", ProviderModelSlug: slug,
				HTTPClient:        newSSRFHTTPClient(false),
				ModelCapabilities: model.Capabilities{ContextWindowTokens: 200000, DefaultMaxOutputTokens: allowance},
			}
			prepared, err := model.PrepareForSend(ctx, client, model.PrepareForSendInput{
				Context: modelcontext.Bundle{SystemPrompt: "You are a concise assistant.", Messages: []modelcontext.Message{{
					ID: "input", Sequence: 1, Role: modelprotocol.RoleUser,
					Content: json.RawMessage(`[{"type":"text","text":"Reply with exactly OK."}]`),
				}}},
				Policy: model.RequestPolicyFromCapabilities(client.Capabilities()), ErrorSource: "live-test",
			})
			require.NoError(t, err)
			require.True(t, prepared.InputBudget.Fits())
			response, err := client.Respond(ctx, model.Request{ProviderRequest: prepared.Body})
			require.NoError(t, err)
			require.Equal(t, model.StopReasonEndTurn, response.StopReason)
			require.Contains(t, response.Text(), "OK")
		})
	}
}
