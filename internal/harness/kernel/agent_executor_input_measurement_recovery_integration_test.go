//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/stretchr/testify/require"
)

func TestAgentExecutorRetainsInputMeasurementAcrossBillingFailures(t *testing.T) {
	ctx := t.Context()
	f := newKernelFixture(t, ctx)
	agentID, userID := f.createAgentWithModelOptions(t, ctx, "openai/input-measurement", f.Now,
		kernelConfiguredModelOptions{ContextWindowTokens: new(20_000), MaxOutputTokens: new(1_000)})
	var providerID string
	require.NoError(t, f.Pool.QueryRow(ctx, `
		SELECT revision.model_provider_config_id
		FROM agents agent
		JOIN agent_configs config ON config.id = agent.current_config_id
		JOIN configured_models model ON model.id = config.configured_model_id
		JOIN configured_model_revisions revision ON revision.id = model.current_revision_id
		WHERE agent.id = $1`, agentID).Scan(&providerID))
	opaque := strings.Repeat("opaque", 20_000)
	client := &inputMeasurementKernelModel{
		Client: openairesponses.Client{
			ModelProviderConfigID: providerID, ProviderModelSlug: "input-measurement", EndpointPath: "/responses",
			ModelCapabilities: model.Capabilities{
				ContextWindowTokens: 20_000, MaxOutputTokens: new(1_000), SupportsReasoning: true,
			},
		},
		response: model.Response{
			ID: "reasoning-output", StopReason: model.StopReasonEndTurn,
			Content: []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "Ready to continue."}},
			Usage:   model.Usage{InputTokens: 4_000, OutputTokens: 100},
			ProviderReplay: json.RawMessage(`[{"type":"reasoning","encrypted_content":"` + opaque + `",` +
				`"summary":[]},{"type":"message","role":"assistant",` +
				`"content":[{"type":"output_text","text":"Ready to continue."}]}]`),
		},
	}
	now := f.Now.Add(time.Second)
	executor := AgentExecutor{Store: f.Store, ModelResolver: liveTestModelResolver(f.Store, client),
		Now: func() time.Time { return now }}
	for turn := range 5 {
		if turn == 1 || turn == 4 {
			client.response = model.Response{
				ID: "text-output", StopReason: model.StopReasonEndTurn,
				Content: []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "Continuing."}},
				Usage:   model.Usage{InputTokens: 4_200, OutputTokens: 10},
			}
			client.failure = nil
		}
		if turn == 2 {
			client.failure = model.ProviderError{
				Kind: model.ErrorKindBillingAccount, Source: "openai_responses",
				Code: "credit_balance_exhausted", Message: "The account has no remaining credits.",
			}
		}
		now = now.Add(time.Second)
		work := f.admitContentInputTurn(t, ctx, agentID, userID, fmt.Sprintf("Continue attempt %d.", turn), now)
		require.NoError(t, executor.ExecuteModelWork(ctx, work))
		f.releaseModelRuntimeLock(t, ctx, work)
		require.Len(t, client.requests, turn+1, "each turn must reach the normal provider request")
		if turn > 0 {
			require.True(t, strings.Contains(string(client.requests[turn]), opaque), "normal request must preserve replay")
			require.Greater(t, modelcontext.EstimatePreparedRequest(client.requests[turn], nil), 20_000,
				"the fallback estimate alone would trigger compaction")
		}
		if turn == 2 || turn == 3 {
			assertDurableModelErrorForKernelTest(t, ctx, f, agentID, work.TurnID,
				string(model.ErrorKindBillingAccount), "credit_balance_exhausted")
		}
	}
	var compactions, successes, failures int
	require.NoError(t, f.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE operation_kind = 'compaction'),
		       count(*) FILTER (WHERE operation_kind = 'normal' AND state = 'succeeded'),
		       count(*) FILTER (WHERE operation_kind = 'normal' AND state = 'failed')
		FROM model_call_contexts WHERE agent_id = $1`, agentID).Scan(&compactions, &successes, &failures))
	require.Zero(t, compactions)
	require.Equal(t, 3, successes)
	require.Equal(t, 2, failures)
	require.Zero(t, pendingModelWork(t, ctx, f, agentID))
}

type inputMeasurementKernelModel struct {
	openairesponses.Client
	response model.Response
	failure  error
	requests []json.RawMessage
}

func (m *inputMeasurementKernelModel) Respond(_ context.Context, request model.Request) (model.Response, error) {
	m.requests = append(m.requests, request.ProviderRequest)
	if m.failure != nil {
		return model.Response{}, m.failure
	}
	return m.response, nil
}
