//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
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
		test := test
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

			var firstContextID ID
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
				t.Fatalf("steered context attempt = %d, want retry count reset to 1", newClaim.Context.AttemptNumber)
			}
			if firstContextID != NilID {
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
	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":` + mustMarshalJSONString(t, text) + `}]`),
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
