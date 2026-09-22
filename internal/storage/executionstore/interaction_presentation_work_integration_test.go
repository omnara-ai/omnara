//go:build integration

package executionstore_test

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func presentationAttemptForTest(t *testing.T, f appInteractionFixture, id uuid.UUID) *time.Time {
	t.Helper()
	var attemptedAt *time.Time
	require.NoError(t, f.store.pool.QueryRow(f.ctx, `
SELECT presentation_attempted_at FROM agent_interactions WHERE agent_id=$1 AND id=$2`,
		f.process.AgentID, id).Scan(&attemptedAt))
	return attemptedAt
}

func presentationToolBatchForTest(t *testing.T, f appInteractionFixture, count int) []uuid.UUID {
	t.Helper()
	items := make([]processToolCallBatchItem, count)
	for i := range items {
		items[i] = builtInProcessToolCallBatchItem(fmt.Sprintf("presentation-%d", i), "ask_question")
	}
	return createToolCallBatchForProcessTest(t, f.ctx, f.process, "presentations", items)
}

func TestInteractionPresentationWorkConcurrentClaim(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	question := f.question(t)
	appGate := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	require.NoError(t, dbsqlc.New(appGate).LockProjectAppLifecycleExclusive(f.ctx,
		dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: f.app.ID}))
	barrier := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	_, err := dbsqlc.New(barrier).LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: testProjectID, ID: f.process.AgentID,
	})
	require.NoError(t, err)
	// Leave one of the five pool connections free for the lock-wait observer.
	claims := make([]<-chan integrationdb.AsyncResult[bool], 2)
	for i := range claims {
		claims[i] = integrationdb.RunAsync(func() (bool, error) {
			return f.store.Execution().
				ClaimInteractionPresentation(f.ctx, testProjectID, f.process.AgentID, question.ID)
		})
	}
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentInProject", len(claims))
	require.NoError(t, barrier.Commit(f.ctx))
	winners := 0
	for _, claim := range claims {
		if integrationdb.AwaitSuccess(t, claim, "concurrent presentation claim") {
			winners++
		}
	}
	require.Equal(t, 1, winners)
	attemptedAt := presentationAttemptForTest(t, f, question.ID)
	require.NotNil(t, attemptedAt, "true means the attempt is visible in a separate transaction")
	claimed, err := f.store.Execution().
		ClaimInteractionPresentation(f.ctx, testProjectID, f.process.AgentID, question.ID)
	require.NoError(t, err)
	require.False(t, claimed)
	require.Equal(t, attemptedAt, presentationAttemptForTest(t, f, question.ID))
	require.Equal(t, question, f.read(t, question.ID), "claim changes no public interaction field")
}

func TestInteractionPresentationWorkListBoundAndAppTypes(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	calls := presentationToolBatchForTest(t, f, executionstore.MaxPendingInteractionPresentations+1)
	want := make([]executionstore.InteractionPresentationReference, 0, len(calls))
	for _, call := range calls {
		question := createQuestionInteractionForTest(t, f.ctx, f.process, call)
		want = append(want, executionstore.InteractionPresentationReference{
			ProjectID: testProjectID, AgentID: f.process.AgentID, ID: question.ID,
		})
	}
	unsupportedID := uuid.New()
	_, err := f.store.pool.Exec(f.ctx, `
INSERT INTO agent_interactions
    (id, agent_id, tool_call_id, interaction_kind, state, request, created_at, destination)
SELECT $1, agent_id, tool_call_id, 'permission', 'open', '{}', created_at - interval '1 minute',
       jsonb_set(destination, '{app_type}', '"unsupported.interactions"')
FROM agent_interactions WHERE agent_id=$2 AND id=$3`, unsupportedID, f.process.AgentID, want[0].ID)
	require.NoError(t, err)
	appTypes := []string{string(appdefinition.SlackThread), string(appdefinition.DiscordThread)}
	listed, err := f.store.Execution().ListPendingInteractionPresentations(f.ctx, appTypes, 2)
	require.NoError(t, err)
	require.Equal(t, want[:2], listed, "oldest pending rows first")
	listed, err = f.store.Execution().ListPendingInteractionPresentations(f.ctx, appTypes, math.MaxInt)
	require.NoError(t, err)
	require.Equal(t, want[:executionstore.MaxPendingInteractionPresentations], listed)
	rows, err := dbsqlc.New(f.store.pool).ListPendingInteractionPresentations(f.ctx,
		dbsqlc.ListPendingInteractionPresentationsParams{AppTypes: appTypes, BatchLimit: math.MaxInt32})
	require.NoError(t, err)
	require.Len(t, rows, executionstore.MaxPendingInteractionPresentations, "SQL independently caps the batch")
	listed, err = f.store.Execution().
		ListPendingInteractionPresentations(f.ctx, []string{"unsupported.interactions"}, 1)
	require.NoError(t, err)
	require.Equal(t, []executionstore.InteractionPresentationReference{{
		ProjectID: testProjectID, AgentID: f.process.AgentID, ID: unsupportedID,
	}}, listed, "app types are caller policy, not hard-coded in storage")
	merged := append(listed, want[:executionstore.MaxPendingInteractionPresentations-1]...)
	listed, err = f.store.Execution().ListPendingInteractionPresentations(f.ctx,
		[]string{
			string(appdefinition.SlackThread),
			"unsupported.interactions",
			string(appdefinition.SlackThread),
		}, math.MaxInt)
	require.NoError(t, err)
	require.Equal(t, merged, listed, "the final merged batch is also capped and duplicate app types add no rows")
	for _, appTypes := range [][]string{nil, {}, {string(appdefinition.DiscordThread)}} {
		listed, err = f.store.Execution().ListPendingInteractionPresentations(f.ctx, appTypes, 100)
		require.NoError(t, err)
		require.Empty(t, listed)
	}
	for _, limit := range []int{0, -1} {
		_, err = f.store.Execution().ListPendingInteractionPresentations(f.ctx, appTypes, limit)
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	}
}

func TestInteractionPresentationWorkMergeAppTypeOrdering(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	calls := presentationToolBatchForTest(t, f, 6)
	question := createQuestionInteractionForTest(t, f.ctx, f.process, calls[0])
	appTypes := []string{string(appdefinition.SlackThread), string(appdefinition.DiscordThread)}
	refs := make([]executionstore.InteractionPresentationReference, len(calls))
	for i, call := range calls {
		refs[i] = executionstore.InteractionPresentationReference{
			ProjectID: testProjectID, AgentID: f.process.AgentID,
			ID: uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", len(calls)-i)),
		}
		_, err := f.store.pool.Exec(f.ctx, `
INSERT INTO agent_interactions
    (id, agent_id, tool_call_id, interaction_kind, state, request, created_at, destination)
SELECT $1, agent_id, $2, 'permission', 'open', '{}', $3,
       jsonb_set(destination, '{app_type}', to_jsonb($4::text))
FROM agent_interactions WHERE agent_id=$5 AND id=$6`,
			refs[i].ID, call, question.CreatedAt.Add(-time.Hour+time.Duration(i/2)*time.Second),
			appTypes[i%len(appTypes)], f.process.AgentID, question.ID)
		require.NoError(t, err)
	}
	want := []executionstore.InteractionPresentationReference{
		refs[1], refs[0], refs[3], refs[2], refs[5], refs[4],
		{ProjectID: testProjectID, AgentID: f.process.AgentID, ID: question.ID},
	}
	for _, appTypes := range [][]string{
		appTypes,
		{appTypes[1], appTypes[0]},
		{appTypes[1], appTypes[0], appTypes[1], appTypes[0], appTypes[1]},
	} {
		for _, limit := range []int{1, 4, executionstore.MaxPendingInteractionPresentations} {
			listed, err := f.store.Execution().ListPendingInteractionPresentations(f.ctx, appTypes, limit)
			require.NoError(t, err)
			require.Equal(
				t,
				want[:min(limit, len(want))],
				listed,
				"merge orders across app types by creation time then identity, appTypes=%v limit=%d",
				appTypes,
				limit,
			)
		}
	}
}

func TestInteractionPresentationWorkEligibilityAndRevocation(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	calls := presentationToolBatchForTest(t, f, 5)
	dashboard := createQuestionInteractionForTest(t, f.ctx, f.process, calls[0])
	require.Empty(t, dashboard.Destination)
	f.selectOrigin(t, f.a.ID)
	questions := make([]executionstore.AgentInteractionRecord, 0, 4)
	for _, call := range calls[1:] {
		questions = append(questions, createQuestionInteractionForTest(t, f.ctx, f.process, call))
	}
	claimed, err := f.store.Execution().ClaimInteractionPresentation(f.ctx,
		testProjectID, f.process.AgentID, questions[0].ID)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = f.store.Execution().RecordInteractionPresentationReceipt(f.ctx, receiptForAppInteraction(t, questions[1]))
	require.NoError(t, err)
	for _, id := range []uuid.UUID{dashboard.ID, questions[1].ID} {
		_, err = f.store.pool.Exec(f.ctx, `UPDATE agent_interactions
SET presentation_attempted_at=now() WHERE agent_id=$1 AND id=$2`, f.process.AgentID, id)
		require.True(t, isPgReadOnlySQLTransaction(err), "marker requires a capture and no receipt: %v", err)
	}
	_, err = f.store.Execution().ResolveAgentInteractionFromHandler(f.ctx, f.callback(t, questions[2], "U_OTHER"))
	require.NoError(t, err)
	for _, id := range []uuid.UUID{dashboard.ID, questions[0].ID, questions[1].ID, questions[2].ID, uuid.New()} {
		claimed, err = f.store.Execution().ClaimInteractionPresentation(f.ctx, testProjectID, f.process.AgentID, id)
		require.NoError(t, err)
		require.False(t, claimed)
	}
	pending := questions[3]
	for _, scope := range [][2]uuid.UUID{{uuid.New(), f.process.AgentID}, {testProjectID, uuid.New()}} {
		claimed, err = f.store.Execution().ClaimInteractionPresentation(f.ctx, scope[0], scope[1], pending.ID)
		require.NoError(t, err)
		require.False(t, claimed)
	}
	f.disable(t)
	listed, err := f.store.Execution().
		ListPendingInteractionPresentations(f.ctx, []string{string(appdefinition.SlackThread)}, 1)
	require.NoError(t, err)
	require.Equal(t, []executionstore.InteractionPresentationReference{{
		ProjectID: testProjectID, AgentID: f.process.AgentID, ID: pending.ID,
	}}, listed)
	claimed, err = f.store.Execution().ClaimInteractionPresentation(f.ctx, testProjectID, f.process.AgentID, pending.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = f.store.Execution().GetAgentInteractionForPresentation(f.ctx, testProjectID, f.process.AgentID, pending.ID)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	require.Equal(t, executionstore.AgentInteractionStateOpen, f.read(t, pending.ID).State)
	listed, err = f.store.Execution().
		ListPendingInteractionPresentations(f.ctx, []string{string(appdefinition.SlackThread)}, 1)
	require.NoError(t, err)
	require.Empty(t, listed)
}

func TestInteractionPresentationWorkMarkerGuard(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	question := f.question(t)
	_, err := f.store.pool.Exec(f.ctx, `
INSERT INTO agent_interactions
    (agent_id, tool_call_id, interaction_kind, state, created_at, destination, presentation_attempted_at)
VALUES ($1, $2, 'permission', 'open', now(), $3, now())`,
		f.process.AgentID, question.ToolCallID, question.Destination)
	require.True(t, isPgCheckViolation(err), "initial attempt marker forbidden: %v", err)
	for _, update := range []string{
		"state='canceled', resolved_at=now()",
		"state='resolved', resolved_at=now()",
		"presentation_receipt='{}'",
		"request='{}'",
		"resolution='{\"bad\":true}'",
		"destination='{}'",
		"destination=NULL",
		"created_at=created_at + interval '1 second'",
	} {
		_, err = f.store.pool.Exec(f.ctx, `UPDATE agent_interactions SET presentation_attempted_at=now(), `+
			update+` WHERE agent_id=$1 AND id=$2`, f.process.AgentID, question.ID)
		require.True(t, isPgReadOnlySQLTransaction(err), "bundled marker update %s: %v", update, err)
	}
	require.Nil(t, presentationAttemptForTest(t, f, question.ID))
	claimed, err := f.store.Execution().
		ClaimInteractionPresentation(f.ctx, testProjectID, f.process.AgentID, question.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	attemptedAt := presentationAttemptForTest(t, f, question.ID)
	for _, update := range []string{
		"presentation_attempted_at=NULL",
		"presentation_attempted_at=presentation_attempted_at + interval '1 second'",
		"presentation_attempted_at=NULL, state='canceled', resolved_at=now()",
		"presentation_attempted_at=presentation_attempted_at + interval '1 second', state='resolved', resolved_at=now()",
		"presentation_attempted_at=NULL, presentation_receipt='{}'",
	} {
		_, err = f.store.pool.Exec(f.ctx, `UPDATE agent_interactions SET `+update+` WHERE agent_id=$1 AND id=$2`,
			f.process.AgentID, question.ID)
		require.True(t, isPgReadOnlySQLTransaction(err), "immutable attempt marker %s: %v", update, err)
	}
	require.Equal(t, attemptedAt, presentationAttemptForTest(t, f, question.ID))
	_, err = f.store.Execution().ResolveAgentInteractionFromHandler(f.ctx, f.callback(t, question, "U_OTHER"))
	require.NoError(t, err)
	resolved := f.read(t, question.ID)
	_, err = f.store.Execution().RecordInteractionPresentationReceipt(f.ctx, receiptForAppInteraction(t, question))
	require.NoError(t, err)
	require.Equal(t, attemptedAt, presentationAttemptForTest(t, f, question.ID))
	require.Equal(t, resolved.Resolution, f.read(t, question.ID).Resolution)
	require.Equal(t, executionstore.AgentInteractionStateResolved, f.read(t, question.ID).State)
}

func TestInteractionPresentationWorkClaimCancelRace(t *testing.T) {
	t.Parallel()
	for _, claimFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("claim_first=%v", claimFirst), func(t *testing.T) {
			t.Parallel()
			f := newAppInteractionFixture(t)
			f.selectOrigin(t, f.a.ID)
			question := f.question(t)
			barrier := integrationdb.BeginTx(t, f.ctx, f.store.pool)
			_, err := dbsqlc.New(barrier).LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
				ProjectID: testProjectID, ID: f.process.AgentID,
			})
			require.NoError(t, err)
			claim := func() (bool, error) {
				return f.store.Execution().
					ClaimInteractionPresentation(f.ctx, testProjectID, f.process.AgentID, question.ID)
			}
			cancel := func() (executionstore.CancelAgentResult, error) {
				return f.store.Execution().CancelAgent(f.ctx, executionstore.CancelAgentInput{
					ProjectID: testProjectID, AgentID: f.process.AgentID, Actor: mustOmnaraActorParams(t, f.user.ID),
				})
			}
			var claimed <-chan integrationdb.AsyncResult[bool]
			var canceled <-chan integrationdb.AsyncResult[executionstore.CancelAgentResult]
			if claimFirst {
				claimed = integrationdb.RunAsync(claim)
				integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentInProject", 1)
				canceled = integrationdb.RunAsync(cancel)
			} else {
				canceled = integrationdb.RunAsync(cancel)
				integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentInProject", 1)
				claimed = integrationdb.RunAsync(claim)
			}
			integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentInProject", 2)
			require.NoError(t, barrier.Commit(f.ctx))
			require.Equal(t, claimFirst, integrationdb.AwaitSuccess(t, claimed, "claim racing cancellation"))
			integrationdb.AwaitSuccess(t, canceled, "cancel racing claim")
			require.Equal(t, executionstore.AgentInteractionStateCanceled, f.read(t, question.ID).State)
			attemptedAt := presentationAttemptForTest(t, f, question.ID)
			require.Equal(t, claimFirst, attemptedAt != nil)
			replayed, err := claim()
			require.NoError(t, err)
			require.False(t, replayed)
			_, err = f.store.pool.Exec(f.ctx, `UPDATE agent_interactions
SET presentation_attempted_at=now() WHERE agent_id=$1 AND id=$2`, f.process.AgentID, question.ID)
			require.True(t, isPgReadOnlySQLTransaction(err), "terminal marker update: %v", err)
			f.disable(t)
			input := receiptForAppInteraction(t, question)
			receipt, err := f.store.Execution().RecordInteractionPresentationReceipt(f.ctx, input)
			require.NoError(t, err)
			again, err := f.store.Execution().RecordInteractionPresentationReceipt(f.ctx, input)
			require.NoError(t, err)
			require.Equal(t, receipt, again)
			require.Equal(t, attemptedAt, presentationAttemptForTest(t, f, question.ID))
		})
	}
}

func TestInteractionPresentationWorkLiveScopes(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"agent", "project", "org"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			f := newAppInteractionFixture(t)
			f.selectOrigin(t, f.a.ID)
			question := f.question(t)
			var err error
			switch scope {
			case "agent":
				_, err = f.store.pool.Exec(f.ctx, `UPDATE agents SET state='archived', archived_at=now() WHERE id=$1`,
					f.process.AgentID)
			case "project":
				_, err = f.store.pool.Exec(f.ctx, `UPDATE projects SET deleted_at=now() WHERE id=$1`, testProjectID)
			case "org":
				_, err = f.store.pool.Exec(f.ctx, `UPDATE orgs SET deleted_at=now() WHERE id=$1`, testOrgID)
			}
			require.NoError(t, err)
			listed, err := f.store.Execution().ListPendingInteractionPresentations(f.ctx,
				[]string{string(appdefinition.SlackThread)}, 100)
			require.NoError(t, err)
			require.Empty(t, listed)
			claimed, err := f.store.Execution().ClaimInteractionPresentation(f.ctx,
				testProjectID, f.process.AgentID, question.ID)
			require.NoError(t, err)
			require.False(t, claimed)
			require.Nil(t, presentationAttemptForTest(t, f, question.ID))
		})
	}
}

func TestInteractionPresentationWorkCanceledClaimCannotAuthorizeSend(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	question := f.question(t)
	barrier := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	_, err := dbsqlc.New(barrier).LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: testProjectID, ID: f.process.AgentID,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(f.ctx)
	t.Cleanup(cancel)
	done := integrationdb.RunAsync(func() (bool, error) {
		return f.store.Execution().ClaimInteractionPresentation(ctx, testProjectID, f.process.AgentID, question.ID)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentInProject", 1)
	cancel()
	result := integrationdb.Await(t, done, "canceled presentation claim")
	require.ErrorIs(t, result.Err, context.Canceled)
	require.False(t, result.Value)
	require.NoError(t, barrier.Commit(f.ctx))
	require.Nil(t, presentationAttemptForTest(t, f, question.ID))
	claimed, err := f.store.Execution().
		ClaimInteractionPresentation(f.ctx, testProjectID, f.process.AgentID, question.ID)
	require.NoError(t, err)
	require.True(t, claimed)
}

func TestInteractionPresentationWorkCommitFailureCannotAuthorizeSend(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	question := f.question(t)
	_, err := f.store.pool.Exec(f.ctx, `
CREATE FUNCTION fail_presentation_claim_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'injected presentation claim commit failure';
END;
$$;
CREATE CONSTRAINT TRIGGER presentation_claim_commit_failure
AFTER UPDATE ON agent_interactions DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION fail_presentation_claim_commit();`)
	require.NoError(t, err)
	claimed, err := f.store.Execution().
		ClaimInteractionPresentation(f.ctx, testProjectID, f.process.AgentID, question.ID)
	require.ErrorContains(t, err, "injected presentation claim commit failure")
	require.False(t, claimed)
	require.Nil(t, presentationAttemptForTest(t, f, question.ID))
	listed, err := f.store.Execution().ListPendingInteractionPresentations(f.ctx,
		[]string{string(appdefinition.SlackThread)}, 1)
	require.NoError(t, err)
	require.Equal(t, []executionstore.InteractionPresentationReference{{
		ProjectID: testProjectID, AgentID: f.process.AgentID, ID: question.ID,
	}}, listed, "rolled-back claims remain discoverable")
	_, err = f.store.pool.Exec(f.ctx, `DROP TRIGGER presentation_claim_commit_failure ON agent_interactions`)
	require.NoError(t, err)
	claimed, err = f.store.Execution().
		ClaimInteractionPresentation(f.ctx, testProjectID, f.process.AgentID, question.ID)
	require.NoError(t, err)
	require.True(t, claimed)
}
