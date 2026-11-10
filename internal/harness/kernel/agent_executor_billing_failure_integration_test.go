//go:build integration

package kernel

import (
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestCreditRefusalRetriesWithAffordableOutputTokens(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name                string
		affordable          int
		wantMaxOutputTokens []int
		wantSucceeded       int
	}{
		{
			name:                "retries at minimum",
			affordable:          minimumAffordableOutputTokens,
			wantMaxOutputTokens: []int{128_000, minimumAffordableOutputTokens},
			wantSucceeded:       1,
		},
		{
			name:                "below minimum",
			affordable:          minimumAffordableOutputTokens - 1,
			wantMaxOutputTokens: []int{128_000},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newKernelFixture(t, ctx)
			agentID, userID := f.createAgent(t, ctx, "openai/affordable", f.Now)
			input := f.admitContentInputTurn(t, ctx, agentID, userID, "start", f.Now.Add(time.Millisecond))
			client := &sequenceKernelModel{
				providerModelSlug:           "affordable",
				capabilities:                model.Capabilities{ContextWindowTokens: 1_000_000, MaxOutputTokens: new(128_000)},
				bodyIncludesMaxOutputTokens: true,
				errs: []error{model.ProviderError{
					Kind: model.ErrorKindBillingAccount, Source: "fixture", Message: "requires more credits",
					AffordableOutputTokens: new(test.affordable),
				}},
				responses: []model.Response{{
					ID:         "resp-affordable",
					Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "done"}},
					StopReason: model.StopReasonEndTurn,
				}},
			}
			executor := AgentExecutor{Store: f.Store, ModelResolver: liveTestModelResolver(f.Store, client)}
			require.NoError(t, executor.ExecuteModelWork(ctx, input))
			var maxOutputTokens []int
			for _, responded := range client.responded {
				maxOutputTokens = append(maxOutputTokens, responded.Policy.MaxOutputTokens)
			}
			require.Equal(t, test.wantMaxOutputTokens, maxOutputTokens)
			var succeeded, total int
			require.NoError(t, f.Pool.QueryRow(ctx, `
				SELECT count(*) FILTER (WHERE state = 'succeeded'), count(*)
				FROM model_call_contexts WHERE agent_id = $1`, agentID).Scan(&succeeded, &total))
			require.Equal(t, test.wantSucceeded, succeeded)
			require.Equal(t, 1, total)
		})
	}
}

func TestBillingFailureSaysWhoseCreditsRanOut(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		managed   bool
		admission bool
		want      string
	}{
		{name: "omnara", managed: true, admission: true, want: storeerr.InsufficientOmnaraCreditsMessage},
		{
			name:    "operator-key",
			managed: true,
			want:    `Model provider "operator-key-prod" returned a billing error: requires more credits`,
		},
		{
			name: "own-key",
			want: `Model provider "own-key-prod" returned a billing error: requires more credits`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newKernelFixture(t, ctx)
			if test.managed {
				f.provisionClusterModel(t, ctx, test.name+"-prod", "billing-model")
			}
			if test.admission {
				f.setManagedWorkAdmission(t, ctx, true)
			}
			agentID, userID := f.createAgent(t, ctx, test.name+"/billing-model", f.Now)
			input := f.admitContentInputTurn(t, ctx, agentID, userID, "start", f.Now.Add(time.Millisecond))
			client := &sequenceKernelModel{
				providerModelSlug: "billing-model",
				errs: []error{model.ProviderError{
					Kind: model.ErrorKindBillingAccount, Source: "fixture", Message: "requires more credits",
				}},
			}
			executor := AgentExecutor{Store: f.Store, ModelResolver: liveTestModelResolver(f.Store, client)}
			require.NoError(t, executor.ExecuteModelWork(ctx, input))
			var shown, recorded string
			require.NoError(t, f.Pool.QueryRow(ctx, `
				SELECT block.text_content, context.error_message
				FROM model_outputs output
				JOIN content_blocks block ON block.agent_id = output.agent_id AND block.owner_model_output_id = output.id
				JOIN model_call_contexts context
				  ON context.agent_id = output.agent_id AND context.id = output.model_call_context_id
				WHERE output.agent_id = $1 AND block.block_kind = 'error'`, agentID).Scan(&shown, &recorded))
			require.Equal(t, test.want, shown)
			require.Equal(t, "requires more credits", recorded)
		})
	}
}
