//go:build integration

package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/patch"
	"github.com/stretchr/testify/require"
)

func TestAgentExecutorRearmsOptionalCompactionFromObservedUsageWithoutReusableIdentity(t *testing.T) {
	for _, optionalSucceeds := range []bool{true, false} {
		name := "successful optional summary"
		if !optionalSucceeds {
			name = "failed optional summary"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fixture, agentID, userID := seedSoftContextHistory(t, ctx)
			responses := []model.Response{}
			if optionalSucceeds {
				responses = append(responses, observedOptionalRearmResponse("Preserve the user's earlier objective.", 9_000))
			}
			responses = append(responses,
				observedOptionalRearmResponse("First normal response has ample headroom.", 1_000),
				observedOptionalRearmResponse("The local estimate is still pessimistic.", 2_000),
				observedOptionalRearmResponse("Actual input has now grown past the working target.", 9_000),
				observedOptionalRearmResponse("Preserve the original objective and subsequent progress.", 500),
				observedOptionalRearmResponse("The next normal call has headroom again.", 1_000),
				observedOptionalRearmResponse("Pessimistic estimates still do not cause repeated maintenance.", 1_000),
			)
			client := uncertainSoftContextClient(responses...)
			if !optionalSucceeds {
				client.errs = []error{model.ProviderError{
					Kind: model.ErrorKindTransient, Source: "test", Code: "unavailable", Message: "Optional summary failed",
				}}
			}
			executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
			work := fixture.admitContentInputTurn(t, ctx, agentID, userID,
				"CURRENT_REQUEST "+strings.Repeat("keep making progress ", 200), fixture.Now.Add(time.Second))
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Len(t, client.responded, 1)
			require.True(t, isCompactionRequestBundle(client.responded[0].Bundle))
			work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(2*time.Second))
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Len(t, client.responded, 2)
			require.False(t, isCompactionRequestBundle(client.responded[1].Bundle))
			for turn := range 2 {
				fixture.releaseModelRuntimeLock(t, ctx, work)
				work = fixture.admitContentInputTurn(t, ctx, agentID, userID,
					strings.Repeat("New details for the task. ", 200+turn), fixture.Now.Add(time.Duration(turn+3)*time.Second))
				require.NoError(t, executor.ExecuteModelWork(ctx, work))
				require.Len(t, client.responded, turn+3)
				require.False(t, isCompactionRequestBundle(client.responded[turn+2].Bundle),
					"a successful low-input response must not rearm a bad estimate")
			}
			fixture.releaseModelRuntimeLock(t, ctx, work)
			work = fixture.admitContentInputTurn(t, ctx, agentID, userID,
				"Continue now that actual input has grown.", fixture.Now.Add(5*time.Second))
			executor = AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Len(t, client.responded, 5)
			require.True(t, isCompactionRequestBundle(client.responded[4].Bundle),
				"observed input growth must rearm maintenance even without a reusable request fingerprint")
			work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(6*time.Second))
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Len(t, client.responded, 6)
			require.False(t, isCompactionRequestBundle(client.responded[5].Bundle))
			fixture.releaseModelRuntimeLock(t, ctx, work)
			work = fixture.admitContentInputTurn(t, ctx, agentID, userID, "Continue again.", fixture.Now.Add(7*time.Second))
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Len(t, client.responded, 7)
			require.False(t, isCompactionRequestBundle(client.responded[6].Bundle))
			var optionalAttempts, successfulFingerprints int
			require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT
				count(*) FILTER (WHERE recovery_kind='compact_optional'),
				count(*) FILTER (WHERE request_input_version IS NOT NULL)
				FROM model_call_contexts WHERE agent_id=$1`, agentID).Scan(&optionalAttempts, &successfulFingerprints))
			require.Equal(t, 2, optionalAttempts)
			require.Zero(t, successfulFingerprints)
			assertNoTerminalContextErrors(t, ctx, fixture, agentID)
		})
	}
}

func observedOptionalRearmResponse(text string, inputTokens int) model.Response {
	response := completeProgressiveSummaryResponse(text)
	response.Usage.InputTokens = inputTokens
	return response
}

func TestAgentExecutorRearmsOptionalCompactionAfterLiveTargetChange(t *testing.T) {
	ctx := context.Background()
	fixture, agentID, userID := seedSoftContextHistory(t, ctx)
	config := fixture.currentAgentConfig(t, ctx, agentID)
	configuredModel := currentConfiguredModelForKernelConfig(t, ctx, fixture.Store, config)
	grantID := currentProjectModelGrantIDForKernelConfig(t, ctx, fixture.Store, config)
	client := uncertainSoftContextClient(
		observedOptionalRearmResponse("Preserve the original objective.", 500),
		observedOptionalRearmResponse("There is headroom under the original target.", 5_000),
		observedOptionalRearmResponse("The existing target remains comfortable.", 5_000),
		observedOptionalRearmResponse("Preserve the original objective and subsequent progress.", 500),
		observedOptionalRearmResponse("There is now headroom under the lower target.", 2_000),
		observedOptionalRearmResponse("The new target does not cause repeated maintenance.", 2_000),
	)
	resolver := &productionPolicyTestResolver{
		Resolver: modelprovider.Resolver{Models: fixture.Store.Models(), Secrets: fixture.Store.Secrets()},
		Client:   client,
	}
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: resolver}
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID,
		strings.Repeat("Current task details. ", 200), fixture.Now.Add(time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(2*time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	fixture.releaseModelRuntimeLock(t, ctx, work)
	work = fixture.admitContentInputTurn(t, ctx, agentID, userID,
		strings.Repeat("More current task details. ", 200), fixture.Now.Add(3*time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Len(t, client.responded, 3)
	require.False(t, isCompactionRequestBundle(client.responded[2].Bundle))
	_, err := fixture.Store.Models().UpdateProjectModelGrant(ctx, modelstore.UpdateProjectModelGrantInput{
		OrgID: kernelTestOrgID, ProjectID: kernelTestProjectID, ID: grantID,
		ContextWindowTokens: patch.NullableInt{Set: true, Value: new(6_000)},
	})
	require.NoError(t, err)
	require.Equal(t, configuredModel.CurrentRevisionID,
		currentRevisionIDForKernelConfiguredModelID(t, ctx, fixture.Store, configuredModel.ID))
	fixture.releaseModelRuntimeLock(t, ctx, work)
	work = fixture.admitContentInputTurn(t, ctx, agentID, userID, "Use the lower working target.",
		fixture.Now.Add(4*time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Len(t, client.responded, 4)
	require.True(t, isCompactionRequestBundle(client.responded[3].Bundle))
	work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(5*time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	fixture.releaseModelRuntimeLock(t, ctx, work)
	work = fixture.admitContentInputTurn(t, ctx, agentID, userID, "Continue with the same target.",
		fixture.Now.Add(6*time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	require.Len(t, client.responded, 6)
	require.False(t, isCompactionRequestBundle(client.responded[5].Bundle))
	var oldTarget, newTarget int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT max(optional_input_target_tokens),
		min(optional_input_target_tokens) FROM model_call_contexts
		WHERE agent_id=$1 AND recovery_kind='compact_optional'`, agentID).Scan(&oldTarget, &newTarget))
	require.Greater(t, oldTarget, newTarget)
	require.Greater(t, 5_000, newTarget*optionalCompactionRearmHeadroomPercent/100,
		"old observed usage must not satisfy the new target's headroom gate")
	assertNoTerminalContextErrors(t, ctx, fixture, agentID)
}

func TestAgentExecutorRetriesUnavailableOptionalCompactionWithoutRequiringLowWater(t *testing.T) {
	for _, test := range []struct {
		name         string
		failure      error
		response     model.Response
		inputTokens  int
		interrupted  bool
		wantOptional bool
	}{
		{name: "rate limited with pressure", inputTokens: 9_000, wantOptional: true,
			failure: model.ProviderError{Kind: model.ErrorKindRateLimit, Source: "test", Message: "Rate limited"}},
		{name: "worker interrupted with pressure", inputTokens: 9_000, wantOptional: true, interrupted: true,
			failure: errors.New("summary worker stopped")},
		{name: "rate limited with low actual input", inputTokens: 1_000,
			failure: model.ProviderError{Kind: model.ErrorKindRateLimit, Source: "test", Message: "Rate limited"}},
		{name: "refusal with pressure", inputTokens: 9_000, response: model.Response{
			ID: "refusal", StopReason: model.StopReasonRefusal,
			Content: []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "Cannot summarize."}},
		}},
		{name: "truncated with pressure", inputTokens: 9_000, response: model.Response{
			ID: "truncated", StopReason: model.StopReasonMaxTokens,
			Content: []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "Incomplete summary."}},
		}},
		{name: "ineffective successful summary", inputTokens: 9_000,
			response: completeProgressiveSummaryResponse("Preserve the user's original objective.")},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture, agentID, userID := seedSoftContextHistory(t, ctx)
			var responses []model.Response
			if test.failure == nil {
				responses = append(responses, test.response)
			}
			responses = append(responses, observedOptionalRearmResponse("Normal work continued.", test.inputTokens))
			if test.wantOptional {
				responses = append(responses, completeProgressiveSummaryResponse("Preserve the objective and progress."))
			}
			responses = append(responses, observedOptionalRearmResponse("The next normal request completed.", test.inputTokens))
			client := uncertainSoftContextClient(responses...)
			if test.failure != nil {
				client.errs = []error{test.failure}
			}
			executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
			work := fixture.admitContentInputTurn(t, ctx, agentID, userID,
				strings.Repeat("Current request details. ", 200), fixture.Now.Add(time.Second))
			err := executor.ExecuteModelWork(ctx, work)
			if test.interrupted {
				require.ErrorIs(t, err, test.failure)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, client.responded, 1)
			work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(2*time.Second))
			executor = AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Len(t, client.responded, 2)
			require.False(t, isCompactionRequestBundle(client.responded[1].Bundle))
			fixture.releaseModelRuntimeLock(t, ctx, work)
			work = fixture.admitContentInputTurn(t, ctx, agentID, userID,
				"Continue despite the earlier summary outcome.", fixture.Now.Add(3*time.Second))
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			require.Len(t, client.responded, 3)
			require.Equal(t, test.wantOptional, isCompactionRequestBundle(client.responded[2].Bundle))
			if test.wantOptional {
				work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, fixture.Now.Add(4*time.Second))
				require.NoError(t, executor.ExecuteModelWork(ctx, work))
				require.Len(t, client.responded, 4)
				require.False(t, isCompactionRequestBundle(client.responded[3].Bundle))
			}
			assertNoTerminalContextErrors(t, ctx, fixture, agentID)
		})
	}
}

type optionalCapRecoveryKernelModel struct {
	*sequenceKernelModel
}

func (m optionalCapRecoveryKernelModel) Respond(ctx context.Context, request model.Request) (model.Response, error) {
	if len(m.responded) == 1 {
		m.errs = append([]error{model.ProviderError{
			Kind: model.ErrorKindContextWindow, Source: "test", Message: "Input plus output exceeds capacity",
		}}, m.errs...)
	}
	return m.sequenceKernelModel.Respond(ctx, request)
}

func TestAgentExecutorRecoveryOutputCapDoesNotRearmOptionalCompaction(t *testing.T) {
	ctx := context.Background()
	fixture, agentID, userID := seedSoftContextHistory(t, ctx)
	partial := observedOptionalRearmResponse("Useful partial progress.", 1_000)
	partial.StopReason = model.StopReasonMaxTokens
	client := optionalCapRecoveryKernelModel{uncertainSoftContextClient(
		completeProgressiveSummaryResponse("Preserve the user's objective and continue the current request."),
		partial,
		observedOptionalRearmResponse("Completed the current work.", 1_000),
	)}
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID,
		"Continue the current request.", fixture.Now.Add(time.Second))
	for attempt := range 4 {
		require.NoError(t, executor.ExecuteModelWork(ctx, work))
		require.Len(t, client.responded, attempt+1)
		if attempt < 3 {
			work = continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work,
				fixture.Now.Add(time.Duration(attempt+2)*time.Second))
		}
	}
	require.True(t, isCompactionRequestBundle(client.responded[0].Bundle))
	for _, request := range client.responded[1:] {
		require.False(t, isCompactionRequestBundle(request.Bundle),
			"recovery caps must not appear to change the working target")
	}
	require.Equal(t, 1_024, client.responded[1].Policy.MaxOutputTokens)
	require.Equal(t, 512, client.responded[2].Policy.MaxOutputTokens)
	require.Equal(t, 512, client.responded[3].Policy.MaxOutputTokens)
	assertNoTerminalContextErrors(t, ctx, fixture, agentID)
}
