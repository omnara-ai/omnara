//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestSteeringPreservesEveryUnansweredOpeningInput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		createContext bool
	}{
		{name: "worker_crashes_before_context"},
		{name: "retrying_context", createContext: true},
	} {

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newProcessDaemonFixture(t, ctx, "unanswered_openings_"+test.name)
			base := fixture.Now.Add(time.Minute)
			if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
				ctx,
				testProjectID,
				fixture.AgentID,
				fixture.Lock.ID,
			); err != nil {
				t.Fatalf("release fixture runtime: %v", err)
			}

			firstInput := createUnansweredOpeningInput(
				t,
				ctx,
				fixture,
				"first unanswered input",
				executionstore.DeliveryModeQueued,
				test.name+"-first",
				base,
			)
			firstWork, found, err := fixture.Store.Execution().ClaimNextAgentWork(
				ctx,
				testClaimNextAgentWorkInput(),
			)
			if err != nil || !found || firstWork.Kind != executionstore.AgentWorkModel ||
				!claimedOpeningInputIDsEqual(firstWork, firstInput.ID) {
				t.Fatalf("first claim found=%v work=%+v err=%v", found, firstWork, err)
			}

			var firstContextID uuid.UUID
			if test.createContext {
				claim := claimTestNormalModelCallForWork(
					t,
					ctx,
					fixture,
					firstWork,
					base.Add(2*time.Second),
				)
				firstContextID = claim.Context.ID
				retryAt := base.Add(time.Hour)
				if _, err := fixture.Store.Execution().RecordRetryableModelCallFailure(
					ctx,
					executionstore.RecordRecoverableModelCallFailureInput{
						ProjectID:          testProjectID,
						AgentID:            fixture.AgentID,
						ModelCallContextID: claim.Context.ID,
						RuntimeLockID:      firstWork.RuntimeLock.ID,
						ErrorKind:          "transient",
						ErrorCode:          "provider_unavailable",
						ErrorMessage:       "provider is temporarily unavailable",
						RetryDelay:         retryAt.Sub(base.Add(3 * time.Second)),
					},
				); err != nil {
					t.Fatalf("record retryable failure: %v", err)
				}
			}
			if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
				ctx,
				testProjectID,
				fixture.AgentID,
				firstWork.RuntimeLock.ID,
			); err != nil {
				t.Fatalf("release first worker runtime: %v", err)
			}

			steeringInput := createUnansweredOpeningInput(
				t,
				ctx,
				fixture,
				"steer to the new destination",
				executionstore.DeliveryModeSteering,
				test.name+"-steering",
				base.Add(4*time.Second),
			)
			nextWork, found, err := fixture.Store.Execution().ClaimNextAgentWork(
				ctx,
				testClaimNextAgentWorkInput(),
			)
			if err != nil || !found || nextWork.Kind != executionstore.AgentWorkModel {
				t.Fatalf("steered claim found=%v work=%+v err=%v", found, nextWork, err)
			}
			if !claimedOpeningInputIDsEqual(nextWork, firstInput.ID, steeringInput.ID) {
				t.Fatalf(
					"steered opening inputs = %v, want both unanswered inputs [%s %s]",
					nextWork.Model.InputIDs,
					firstInput.ID,
					steeringInput.ID,
				)
			}
			if nextWork.Model.TurnID == firstWork.Model.TurnID {
				t.Fatalf("steering reused turn %s instead of opening a new turn", nextWork.Model.TurnID)
			}

			newClaim := claimTestNormalModelCallForWork(
				t,
				ctx,
				fixture,
				nextWork,
				base.Add(6*time.Second),
			)
			if newClaim.Context.AttemptNumber != 1 {
				t.Fatalf(
					"steered context attempt = %d, want retry count reset to 1",
					newClaim.Context.AttemptNumber,
				)
			}
			if firstContextID != uuid.Nil {
				firstContext, found, err := fixture.Store.Execution().GetModelCallContext(
					ctx,
					testProjectID,
					fixture.AgentID,
					firstContextID,
				)
				if err != nil || !found ||
					firstContext.State != executionstore.ModelCallContextFailed ||
					firstContext.RecoveryKind != executionstore.ModelCallRecoveryRetry {
					t.Fatalf(
						"prior retry context = %+v found=%v err=%v, want immutable failed retry history",
						firstContext,
						found,
						err,
					)
				}
			}
		})
	}
}

func createUnansweredOpeningInput(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	text string,
	deliveryMode executionstore.AgentInputDeliveryMode,
	idempotencyKey string,
	now time.Time,
) executionstore.AgentInputRecord {
	t.Helper()
	input, _, _, err := fixture.Store.Execution().
		CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			Actor:     mustOmnaraActorParams(t, fixture.UserID),
			ContentBlocks: json.RawMessage(
				`[{"type":"text","text":` + mustMarshalJSONString(t, text) + `}]`,
			),
			DeliveryMode:   deliveryMode,
			IdempotencyKey: idempotencyKey,
		})
	if err != nil {
		t.Fatalf("create %s: %v", idempotencyKey, err)
	}
	return input
}

func mustMarshalJSONString(t *testing.T, value string) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON string: %v", err)
	}
	return string(body)
}

func TestContinuationPreparesInheritedUnansweredOpenings(t *testing.T) {
	t.Parallel()
	for _, interrupted := range []string{"before_context", "retry"} {
		for _, continuation := range []string{"tools", "max_tokens"} {
			t.Run(interrupted+"/"+continuation, func(t *testing.T) {
				t.Parallel()
				ctx := t.Context()
				f := newProcessDaemonFixture(t, ctx, "inherited_openings_"+interrupted+"_"+continuation)
				require.NoError(
					t,
					f.Store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, f.AgentID, f.Lock.ID),
				)
				first := ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "first")
				work, found, err := f.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
				require.NoError(t, err)
				require.True(t, found)
				f.Lock = work.RuntimeLock
				if interrupted == "retry" {
					claim := ownedModelClaim(t, ctx, f, work)
					_, err = f.Store.Execution().RecordRetryableModelCallFailure(ctx,
						executionstore.RecordRecoverableModelCallFailureInput{
							ProjectID: testProjectID, AgentID: f.AgentID, RuntimeLockID: f.Lock.ID,
							ModelCallContextID: claim.Context.ID, ErrorKind: "transient", ErrorCode: "unavailable",
							ErrorMessage: "retry later", RetryDelay: time.Hour,
						})
					require.NoError(t, err)
				}
				require.NoError(
					t,
					f.Store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, f.AgentID, f.Lock.ID),
				)
				steering := ownedInput(t, ctx, f, executionstore.DeliveryModeSteering, "steering")
				work, found, err = f.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, []uuid.UUID{first.ID, steering.ID}, work.Model.InputIDs)
				f.Lock = work.RuntimeLock
				claim := ownedModelClaim(t, ctx, f, work)
				if continuation == "tools" {
					completeToolCallForContinuationSeedTest(t, ctx, f, work.Model.TurnID,
						claim.Context.ID, "inherited", f.Now)
				} else {
					slug := modelProviderSlugForContext(t, ctx, f.Store, testProjectID, f.AgentID, claim.Context.ID)
					_, err = f.Store.Execution().RecordModelOutputAndCompleteContext(ctx,
						executionstore.RecordModelOutputAndCompleteContextInput{
							ProjectID: testProjectID, AgentID: f.AgentID, RuntimeLockID: f.Lock.ID,
							ModelCallContextID: claim.Context.ID,
							ProviderResponse: modelenvelope.ResponseEnvelope{
								RequestedProviderModelSlug: slug, ServedProviderModelSlug: slug,
								APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: "default",
								Normalized: modelenvelope.ResponseNormalized{
									ID: "inherited_output", StopReason: modelenvelope.StopReasonMaxTokens,
								},
							},
						})
					require.NoError(t, err)
				}
				seed, found, err := f.Store.Execution().NextAgentModelWork(ctx, testProjectID, f.AgentID)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, executionstore.ModelWorkContinue, seed.Kind)
				require.Equal(t, work.Model.InputIDs, seed.InputIDs)
				prepare := executionstore.PrepareNormalModelCallInput{
					ProjectID: testProjectID, AgentID: f.AgentID, RuntimeLockID: f.Lock.ID,
					OpeningInputIDs: seed.InputIDs, SourceModelCallContextID: seed.ModelCallContextID,
					SourceModelOutputID: seed.SourceModelOutputID,
				}
				prepared, err := f.Store.Execution().PrepareNormalModelCall(ctx, prepare)
				require.NoError(t, err)
				require.True(t, prepared.Claim.Claimed)
				require.Greater(
					t,
					prepared.Claim.Context.InputEventSequence,
					claim.Context.InputEventSequence,
				)
				var ids []uuid.UUID
				require.NoError(
					t,
					f.Store.pool.QueryRow(ctx,
						`SELECT opening_input_ids FROM model_call_contexts WHERE id=$1`,
						prepared.Claim.Context.ID).
						Scan(&ids),
				)
				require.Equal(t, []uuid.UUID{first.ID, steering.ID}, ids)

			})
		}
	}
}
