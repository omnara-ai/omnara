//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/patch"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestAgentExecutorOutputRecoveryRestartsWhenModelRevisionChanges(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	type providerRequest struct {
		MaxTokens int `json:"max_tokens"`
		Thinking  struct {
			BudgetTokens int `json:"budget_tokens"`
		} `json:"thinking"`
	}
	var requests []providerRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request providerRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode provider request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "application/json")
		if len(requests) <= 2 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error",
				"message":"prompt is too long: 150000 tokens > 128000 maximum"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"revision-recovery","type":"message","role":"assistant",
			"model":"output-recovery","content":[{"type":"text","text":"Recovered under the new model."}],
			"stop_reason":"end_turn","usage":{"input_tokens":500,"output_tokens":20}}`))
	}))
	t.Cleanup(server.Close)
	credentialProvider := storagefixture.EnsureModelProvider(t, ctx, fixture.Store.Models(), fixture.Store.Secrets(),
		storagefixture.ModelProviderInput{OrgID: kernelTestOrgID, UserID: kernelTestUserID, Name: "credentials"})
	_, err := fixture.Store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID: kernelTestOrgID, Name: "anthropic-prod", APIFormat: modelprotocol.APIFormatAnthropicMessages,
		BaseURL: server.URL, CredentialSecretID: credentialProvider.CredentialSecretID,
	})
	require.NoError(t, err)
	agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "anthropic/output-recovery", fixture.Now,
		kernelConfiguredModelOptions{ContextWindowTokens: new(128_000), MaxOutputTokens: new(4_096)})
	configuredModel := currentConfiguredModelForKernelConfig(
		t, ctx, fixture.Store, fixture.currentAgentConfig(t, ctx, agentID),
	)
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: modelprovider.Resolver{
		Models: fixture.Store.Models(), Secrets: fixture.Store.Secrets(), AllowLoopback: true,
	}}
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "Continue the same request.", fixture.Now)
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Len(t, requests, 1)
	require.Equal(t, 4_096, requests[0].MaxTokens)
	var originalRecoveryCap int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT recovery_max_output_tokens FROM model_call_contexts
		WHERE agent_id=$1 AND attempt_number=1`, agentID).Scan(&originalRecoveryCap))
	require.Equal(t, 2_048, originalRecoveryCap)

	options := json.RawMessage(`{"thinking":{"type":"enabled","budget_tokens":3072}}`)
	replacement, err := fixture.Store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID: kernelTestOrgID, ModelProviderConfigID: configuredModel.ModelProviderConfigID, ID: configuredModel.ID,
		ContextWindowTokens: new(256_000), MaxOutputTokens: patch.NullableInt{Set: true, Value: new(8_192)},
		APIVariantOptions: &options,
	})
	require.NoError(t, err)
	require.NotEqual(t, configuredModel.CurrentRevisionID, replacement.CurrentRevisionID)
	for i := range 2 {
		work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(time.Duration(i+1)*time.Second))
		require.NoError(t, executor.ExecuteModelWork(ctx, work))
		require.Len(t, requests, i+2)
	}
	require.Equal(t, 8_192, requests[1].MaxTokens)
	require.Equal(t, 4_096, requests[2].MaxTokens)
	require.Equal(t, 3_072, requests[1].Thinking.BudgetTokens)
	require.Equal(t, 3_072, requests[2].Thinking.BudgetTokens)
	var succeeded int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM model_call_contexts
		WHERE agent_id=$1 AND configured_model_revision_id=$2 AND state='succeeded'`,
		agentID, replacement.CurrentRevisionID).Scan(&succeeded))
	require.Equal(t, 1, succeeded)
	assertNoTerminalContextErrors(t, ctx, fixture, agentID)
}
