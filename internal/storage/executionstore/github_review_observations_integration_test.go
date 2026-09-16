//go:build integration

package executionstore_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestGitHubReviewObservationsRetainOldOwnershipAndValidateMarkers(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newGitHubReviewPreparationFixture(t)
	operations := f.tools(t,
		githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")},
		githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")},
		githubReviewSend{params: `{"publish_review":true}`})
	oldPrepared := prepareGitHubReviewOperation(t, operations[0])
	oldTime := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	// The historical fact references a real admitted tool and its actual pin.
	// Insert its old timestamp directly rather than rewriting immutable history.
	_, err := f.Store.pool.Exec(ctx, `INSERT INTO github_pr_reviews (
project_id, agent_id, creating_tool_call_id, integration_install_id,
pr_channel_id, creating_binding_id, commit_id, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, testProjectID, f.AgentID, operations[0].input.ToolCallID,
		f.install.ID, f.target.ID, oldPrepared.Binding().ID, strings.ToLower(githubReviewCommit), oldTime)
	require.NoError(t, err)
	oldRecord := githubReviewRecordInput(f, operations[0], "PRR_old")
	_, err = f.Store.Execution().RecordGitHubReviewIdentity(ctx, oldRecord)
	require.NoError(t, err)
	finishGitHubObservationCreator(t, operations[0])
	prepareGitHubObservationCreator(t, operations[1])
	oldBefore := f.creator(t, operations[0].input.ToolCallID)
	markerBefore := f.creator(t, operations[1].input.ToolCallID)
	require.Equal(t, oldTime, oldBefore.CreatedAt.UTC())

	other := f
	other.processDaemonFixture = newProcessDaemonFixtureInStore(
		t, ctx, f.Store, f.UserID, "github-observation-other", f.Now)
	other.grant(t, f.target.ID)
	foreignOperation := other.tools(t, githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")})[0]
	prepareGitHubObservationCreator(t, foreignOperation)
	_, err = f.Store.Execution().RecordGitHubReviewIdentity(ctx,
		githubReviewRecordInput(other, foreignOperation, "PRR_foreign"))
	require.NoError(t, err)
	foreignBefore := other.creator(t, foreignOperation.input.ToolCallID)
	observations := []executionstore.GitHubReviewObservation{
		{ReviewID: "PRR_old"},
		{ReviewID: "PRR_marker", CreatingToolCallID: operations[1].input.ToolCallID},
		// A provider-selected foreign native ID stays foreign even with a forged own marker.
		{ReviewID: "PRR_foreign", CreatingToolCallID: operations[1].input.ToolCallID},
		{ReviewID: "PRR_foreign_marker", CreatingToolCallID: foreignOperation.input.ToolCallID},
		{ReviewID: "PRR_unknown"},
		{ReviewID: "PRR_rebound", CreatingToolCallID: operations[0].input.ToolCallID},
	}
	for _, observedCommit := range []string{githubReviewCommit, "", strings.Repeat("1", 40)} {
		for i := range observations {
			observations[i].CommitID = observedCommit
		}
		results, err := f.Store.Execution().LookupGitHubReviewObservations(ctx,
			githubReviewObservationScope(f, operations[2]), observations)
		require.NoError(t, err)
		require.Len(t, results, len(observations))
		for i, ownership := range []executionstore.GitHubReviewOwnership{
			executionstore.GitHubReviewOwned, executionstore.GitHubReviewOwned, executionstore.GitHubReviewOtherAgent,
			executionstore.GitHubReviewUnknown, executionstore.GitHubReviewUnknown, executionstore.GitHubReviewUnknown,
		} {
			expected := executionstore.GitHubReviewObservationResult{ReviewID: observations[i].ReviewID, Ownership: ownership}
			if i < 2 {
				expected.CreatingToolCallID = operations[i].input.ToolCallID
				expected.CommitID = strings.ToLower(githubReviewCommit)
			}
			require.Equal(t, expected, results[i], "ownership and creator pin must not depend on native commit")
		}
	}
	require.Equal(t, oldBefore, f.creator(t, operations[0].input.ToolCallID))
	require.Equal(t, markerBefore, f.creator(t, operations[1].input.ToolCallID), "lookup cannot bind a marker's native ID")
	require.Equal(t, foreignBefore, other.creator(t, foreignOperation.input.ToolCallID))
	for _, nativeID := range []string{"PRR_old", "PRR_foreign"} {
		for _, evidence := range []executionstore.GitHubReviewIdentityEvidence{
			executionstore.GitHubReviewCreateResponse, executionstore.GitHubReviewMarker,
		} {
			input := githubReviewRecordInput(f, operations[1], nativeID)
			input.Evidence = evidence
			result, err := f.Store.Execution().RecordGitHubReviewIdentity(ctx, input)
			require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict, "a native ID cannot move to another creator")
			require.Equal(t, executionstore.RecordGitHubReviewIdentityResult{}, result)
			require.Equal(t, markerBefore, f.creator(t, operations[1].input.ToolCallID))
		}
	}
	foreignMarker := githubReviewRecordInput(f, operations[2], "PRR_foreign_marker")
	foreignMarker.Evidence = executionstore.GitHubReviewMarker
	foreignMarker.Observation.CreatingToolCallID = foreignOperation.input.ToolCallID
	result, err := f.Store.Execution().RecordGitHubReviewIdentity(ctx, foreignMarker)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.False(t, result.Recorded)
	require.Equal(t, foreignBefore, other.creator(t, foreignOperation.input.ToolCallID))
}

func TestGitHubReviewRecordRequiresOriginalCreateResponseOrOwnedMarker(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newGitHubReviewPreparationFixture(t)
	operations := f.tools(t,
		githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")},
		githubReviewSend{params: `{"publish_review":true}`},
		githubReviewSend{params: githubReviewFinding(githubReviewCommit, "PRR_explicit")})
	prepareGitHubObservationCreator(t, operations[0])
	before := f.creator(t, operations[0].input.ToolCallID)
	input := githubReviewRecordInput(f, operations[0], "PRR_recorded")
	for _, name := range []string{
		"another_request", "another_commit", "missing_commit", "explicit_review", "missing_creator",
	} {
		invalid := input
		wantErr := storeerr.ErrUnauthorized
		switch name {
		case "another_request":
			invalid.Scope = githubReviewObservationScope(f, operations[1])
		case "another_commit":
			invalid.Observation.CommitID = strings.Repeat("2", 40)
		case "missing_commit":
			invalid.Observation.CommitID = ""
		case "explicit_review":
			invalid.Scope = githubReviewObservationScope(f, operations[2])
			invalid.Observation.CreatingToolCallID = operations[2].input.ToolCallID
		case "missing_creator":
			invalid.Observation.CreatingToolCallID = operations[1].input.ToolCallID
			invalid.Evidence = executionstore.GitHubReviewMarker
			wantErr = storeerr.ErrNotFound
		}
		result, err := f.Store.Execution().RecordGitHubReviewIdentity(ctx, invalid)
		require.ErrorIs(t, err, wantErr, name)
		require.False(t, result.Recorded)
		require.False(t, result.Continue)
		require.Equal(t, before, f.creator(t, operations[0].input.ToolCallID))
	}
	// A later admitted summary may recover a positively identified own creator.
	// It records that fact but cannot continue the original creation request.
	input.Scope, input.Evidence = githubReviewObservationScope(f, operations[1]), executionstore.GitHubReviewMarker
	result, err := f.Store.Execution().RecordGitHubReviewIdentity(ctx, input)
	require.NoError(t, err)
	require.Equal(t, executionstore.RecordGitHubReviewIdentityResult{Recorded: true, Continue: false}, result)
	input.Scope, input.Evidence = githubReviewObservationScope(f, operations[0]), executionstore.GitHubReviewCreateResponse
	result, err = f.Store.Execution().RecordGitHubReviewIdentity(ctx, input)
	require.NoError(t, err)
	require.Equal(t, executionstore.RecordGitHubReviewIdentityResult{Recorded: true, Continue: true}, result)
	before.NativeID = &input.Observation.ReviewID
	require.Equal(t, before, f.creator(t, operations[0].input.ToolCallID), "only native identity may change")
}

func TestGitHubReviewObservationsRejectWrongAuthorityWithoutWriting(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newGitHubReviewPreparationFixture(t)
	otherChannel := f.channel(t, "PR_unrelated")
	operations := f.tools(t, githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")},
		githubReviewSend{params: `{}`}, githubReviewSend{channel: otherChannel.ID, params: `{"publish_review":true}`})
	prepareGitHubObservationCreator(t, operations[0])
	before := f.creator(t, operations[0].input.ToolCallID)
	input := githubReviewRecordInput(f, operations[0], "PRR_scoped")
	for _, name := range []string{
		"zero_scope", "app", "installation", "channel", "request", "agent",
		"capability", "no_capability", "timeline", "other_pr_marker",
	} {
		invalid := input
		wantErr := storeerr.ErrNotFound
		switch name {
		case "zero_scope":
			invalid.Scope = executionstore.GitHubReviewOperationScope{}
			wantErr = storeerr.ErrInvalidRequest
		case "app":
			invalid.Scope.IntegrationAppID = uuid.New()
		case "installation":
			invalid.Scope.IntegrationInstallID = uuid.New()
		case "channel":
			invalid.Scope.ChannelID = otherChannel.ID
			wantErr = storeerr.ErrUnauthorized
		case "request":
			invalid.Scope.RequestID = uuid.New()
		case "agent":
			invalid.Scope.AgentID = uuid.New()
		case "capability":
			wantErr = storeerr.ErrUnauthorized
			invalid.Scope.Capabilities = []channelconnector.Capability{
				{ConnectorKey: testChannelConnector, Provider: "discord"},
				{ConnectorKey: "other_connector", Provider: "github"},
			}
		case "no_capability":
			invalid.Scope.Capabilities = nil
			wantErr = storeerr.ErrUnauthorized
		case "timeline":
			invalid.Scope = githubReviewObservationScope(f, operations[1])
			wantErr = storeerr.ErrUnauthorized
		case "other_pr_marker":
			invalid.Scope, invalid.Evidence = githubReviewObservationScope(f, operations[2]), executionstore.GitHubReviewMarker
			wantErr = storeerr.ErrUnauthorized
		}
		rows, err := f.Store.Execution().LookupGitHubReviewObservations(ctx, invalid.Scope,
			[]executionstore.GitHubReviewObservation{invalid.Observation})
		if name == "other_pr_marker" {
			require.NoError(t, err)
			require.Equal(t, []executionstore.GitHubReviewObservationResult{
				{ReviewID: input.Observation.ReviewID, Ownership: executionstore.GitHubReviewUnknown},
			}, rows)
		} else {
			require.ErrorIs(t, err, wantErr, name)
			require.Empty(t, rows)
		}
		result, err := f.Store.Execution().RecordGitHubReviewIdentity(ctx, invalid)
		require.ErrorIs(t, err, wantErr, name)
		require.Equal(t, executionstore.RecordGitHubReviewIdentityResult{}, result)
		require.Equal(t, before, f.creator(t, operations[0].input.ToolCallID))
	}
}

func TestGitHubReviewObservationsAllowExactReadWithoutRecording(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newGitHubReviewPreparationFixture(t)
	otherChannel := f.channel(t, "PR_read_other")
	f.definition.Capabilities.Read = true
	_, err := f.Store.Integrations().PublishConnectorChannelDefinition(ctx, f.definition)
	require.NoError(t, err)
	for _, channelID := range []uuid.UUID{f.target.ID, otherChannel.ID} {
		_, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx,
			integrationstore.CreateIntegrationTargetBindingInput{
				ProjectID: testProjectID, AgentID: f.AgentID, IntegrationInstallID: f.install.ID,
				IntegrationTargetID: channelID, ReadAllowed: true, Source: "github-review-read",
			})
		require.NoError(t, err)
	}
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, f.target.ID)
	require.NoError(t, err)
	ids := createToolCallBatchForProcessTest(t, ctx, f.processDaemonFixture, "github-review-read",
		[]processToolCallBatchItem{
			{TestName: "creator", ToolName: toolcatalog.ToolNameSendChannelMessage,
				ToolType: toolcatalog.ToolTypeBuiltIn, Allowed: true,
				Input: json.RawMessage(`{"channel_id":"` + channelID + `","message":{"text":"Finding"},"params":` +
					githubReviewFinding(githubReviewCommit, "") + `}`)},
			{TestName: "reader", ToolName: toolcatalog.ToolNameReadChannel,
				ToolType: toolcatalog.ToolTypeBuiltIn, Allowed: true,
				Input: json.RawMessage(`{"channel_id":"` + channelID + `","limit":3}`)},
		})
	operations := make([]managedOperationFixture, len(ids))
	for i, id := range ids {
		operation := integrationstore.ChannelBindingOperationSend
		if i == 1 {
			operation = integrationstore.ChannelBindingOperationRead
		}
		operations[i] = managedOperationFixture{processDaemonFixture: f.processDaemonFixture,
			input: executionstore.PrepareChannelOperationInput{
				ExecuteToolCallInput: executionstore.ExecuteToolCallInput{
					ProjectID: testProjectID, AgentID: f.AgentID, ToolCallID: id, RuntimeLockID: f.Lock.ID,
				},
				TurnID:    turnIDForProcessToolCallTest(t, ctx, f.processDaemonFixture, id),
				ChannelID: f.target.ID, Operation: operation,
			}}
		operations[i].start(t, ctx)
	}
	prepareGitHubObservationCreator(t, operations[0])
	readPrepared, err := f.Store.Execution().PrepareChannelOperation(ctx, operations[1].input)
	require.NoError(t, err)
	require.True(t, readPrepared.Binding().ReadAllowed)
	require.False(t, readPrepared.Binding().SendAllowed, "reading uses its own read-only grant")
	_, err = f.Store.Execution().RecheckChannelOperation(ctx, readPrepared)
	require.NoError(t, err)
	before := f.creator(t, ids[0])
	input := githubReviewRecordInput(f, operations[0], "PRR_read_owned")
	readScope := githubReviewObservationScope(f, operations[1])
	rows, err := f.Store.Execution().LookupGitHubReviewObservations(ctx, readScope,
		[]executionstore.GitHubReviewObservation{input.Observation})
	require.NoError(t, err)
	require.Equal(t, []executionstore.GitHubReviewObservationResult{{
		ReviewID: input.Observation.ReviewID, Ownership: executionstore.GitHubReviewOwned,
		CreatingToolCallID: ids[0], CommitID: strings.ToLower(githubReviewCommit),
	}}, rows)
	for _, evidence := range []executionstore.GitHubReviewIdentityEvidence{
		executionstore.GitHubReviewMarker, executionstore.GitHubReviewCreateResponse,
	} {
		readRecord := input
		readRecord.Scope, readRecord.Evidence = readScope, evidence
		result, err := f.Store.Execution().RecordGitHubReviewIdentity(ctx, readRecord)
		require.ErrorIs(t, err, storeerr.ErrUnauthorized)
		require.Equal(t, executionstore.RecordGitHubReviewIdentityResult{}, result)
		require.Equal(t, before, f.creator(t, ids[0]), "a read must not record even a positively owned marker")
	}
	_, err = f.Store.Execution().RecordGitHubReviewIdentity(ctx, input)
	require.NoError(t, err)
	before = f.creator(t, ids[0])
	observation := executionstore.GitHubReviewObservation{ReviewID: input.Observation.ReviewID}
	rows, err = f.Store.Execution().LookupGitHubReviewObservations(ctx, readScope,
		[]executionstore.GitHubReviewObservation{observation, {ReviewID: "PRR_unknown"}})
	require.NoError(t, err)
	require.Equal(t, []executionstore.GitHubReviewObservationResult{
		{ReviewID: observation.ReviewID, Ownership: executionstore.GitHubReviewOwned,
			CreatingToolCallID: ids[0], CommitID: strings.ToLower(githubReviewCommit)},
		{ReviewID: "PRR_unknown", Ownership: executionstore.GitHubReviewUnknown},
	}, rows)
	for _, name := range []string{"channel", "agent", "install", "app", "capability"} {
		invalid := readScope
		wantErr := storeerr.ErrNotFound
		switch name {
		case "channel":
			invalid.ChannelID = otherChannel.ID
			wantErr = storeerr.ErrUnauthorized
		case "agent":
			invalid.AgentID = uuid.New()
		case "install":
			invalid.IntegrationInstallID = uuid.New()
		case "app":
			invalid.IntegrationAppID = uuid.New()
		case "capability":
			invalid.Capabilities = testChannelCapabilities("discord")
			wantErr = storeerr.ErrUnauthorized
		}
		rows, err := f.Store.Execution().LookupGitHubReviewObservations(ctx, invalid,
			[]executionstore.GitHubReviewObservation{observation})
		require.ErrorIs(t, err, wantErr, name)
		require.Empty(t, rows)
	}
	_, err = f.Store.Execution().CompleteRuntimeToolCall(ctx, executionstore.CompleteRuntimeToolCallInput{
		ProjectID: testProjectID, AgentID: f.AgentID, ID: ids[1], RuntimeLockID: f.Lock.ID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"Review history read"}]`),
	})
	require.NoError(t, err)
	rows, err = f.Store.Execution().LookupGitHubReviewObservations(ctx, readScope,
		[]executionstore.GitHubReviewObservation{observation})
	require.ErrorIs(t, err, storeerr.ErrNotFound, "a completed read cannot authorize another provider observation")
	require.Empty(t, rows)
	require.Equal(t, before, f.creator(t, ids[0]))
}

func TestGitHubReviewRecordMarkerPreservesCreatorCommit(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"missing_native_commit", "different_native_commit"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newGitHubReviewPreparationFixture(t)
			operations := f.tools(t, githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")},
				githubReviewSend{params: `{"publish_review":true}`})
			prepareGitHubObservationCreator(t, operations[0])
			before := f.creator(t, operations[0].input.ToolCallID)
			input := githubReviewRecordInput(f, operations[0], "PRR_marker_commit")
			input.Scope = githubReviewObservationScope(f, operations[1])
			input.Evidence = executionstore.GitHubReviewMarker
			input.Observation.CommitID = ""
			if name == "different_native_commit" {
				input.Observation.CommitID = strings.Repeat("3", 40)
			}
			rows, err := f.Store.Execution().LookupGitHubReviewObservations(ctx, input.Scope,
				[]executionstore.GitHubReviewObservation{input.Observation})
			require.NoError(t, err)
			require.Equal(t, []executionstore.GitHubReviewObservationResult{{
				ReviewID: input.Observation.ReviewID, Ownership: executionstore.GitHubReviewOwned,
				CreatingToolCallID: operations[0].input.ToolCallID, CommitID: strings.ToLower(githubReviewCommit),
			}}, rows)
			for range 2 {
				result, err := f.Store.Execution().RecordGitHubReviewIdentity(ctx, input)
				require.NoError(t, err)
				require.Equal(t, executionstore.RecordGitHubReviewIdentityResult{Recorded: true}, result)
			}
			before.NativeID = &input.Observation.ReviewID
			require.Equal(t, before, f.creator(t, operations[0].input.ToolCallID), "the stored commit pin is immutable")
			// The original creator can still acknowledge its own exact commit and continue.
			result, err := f.Store.Execution().RecordGitHubReviewIdentity(ctx,
				githubReviewRecordInput(f, operations[0], input.Observation.ReviewID))
			require.NoError(t, err)
			require.Equal(t, executionstore.RecordGitHubReviewIdentityResult{Recorded: true, Continue: true}, result)
		})
	}
}

func TestGitHubReviewConcurrentFirstFindingsHaveOneCreator(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newGitHubReviewPreparationFixture(t)
	operations := f.tools(t, githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")},
		githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")})
	prepared := []executionstore.PreparedChannelOperation{
		prepareGitHubReviewOperation(t, operations[0]), prepareGitHubReviewOperation(t, operations[1]),
	}
	blocker := integrationdb.BeginTx(t, ctx, f.Store.pool)
	var blockerPID int32
	require.NoError(t, blocker.QueryRow(ctx, `SELECT pg_backend_pid() FROM agents
WHERE project_id=$1 AND id=$2 FOR UPDATE`, testProjectID, f.AgentID).Scan(&blockerPID))
	done := make([]<-chan error, len(prepared))
	for i := range prepared {
		done[i] = integrationdb.RunAsyncError(func() error {
			_, _, err := f.Store.Execution().PrepareChannelSend(ctx, prepared[i])
			return err
		})
	}
	// Both real dispatch transactions must reach the same agent lock before either
	// can inspect creator state. The loser must see the winner's committed insert.
	integrationdb.WaitForNamedLockWaitersBlockedByChain(t, ctx, f.Store.pool, "LockAgentInProject", blockerPID, 2)
	f.requireCount(t, 0)
	require.NoError(t, blocker.Commit(ctx))
	winner, successes := -1, 0
	for i, result := range done {
		err := integrationdb.Await(t, result, "concurrent first review finding")
		if err == nil {
			winner, successes = i, successes+1
		} else {
			requireGitHubReviewError(t, err, executionstore.GitHubReviewError{
				Code: channelconnector.FailureReviewCreationInProgress,
			})
		}
	}
	require.Equal(t, 1, successes)
	f.requireCount(t, 1)
	row := f.creator(t, operations[winner].input.ToolCallID)
	require.Equal(t, testProjectID, row.ProjectID)
	require.Equal(t, f.AgentID, row.AgentID)
	require.Equal(t, f.install.ID, row.InstallID)
	require.Equal(t, f.target.ID, row.ChannelID)
	require.Equal(t, prepared[winner].Binding().ID, row.BindingID)
	require.Equal(t, strings.ToLower(githubReviewCommit), row.CommitID)
	require.Nil(t, row.NativeID, "preparation cannot claim that provider I/O already happened")
	for _, operation := range operations {
		call, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, operation.input.ToolCallID)
		require.NoError(t, err)
		require.Equal(t, executionstore.ToolCallStateRunning, call.State)
	}
}

func TestGitHubReviewRecordConcurrentIdentitiesAreWriteOnce(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"identical", "conflicting"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newGitHubReviewPreparationFixture(t)
			operation := f.tools(t, githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")})[0]
			prepareGitHubObservationCreator(t, operation)
			before := f.creator(t, operation.input.ToolCallID)
			inputs := []executionstore.RecordGitHubReviewIdentityInput{
				githubReviewRecordInput(f, operation, "PRR_first"), githubReviewRecordInput(f, operation, "PRR_first"),
			}
			if name == "conflicting" {
				inputs[1].Observation.ReviewID = "PRR_second"
			}
			start := make(chan struct{})
			results := make([]<-chan integrationdb.AsyncResult[executionstore.RecordGitHubReviewIdentityResult], 2)
			for i := range inputs {
				results[i] = integrationdb.RunAsync(func() (executionstore.RecordGitHubReviewIdentityResult, error) {
					<-start
					return f.Store.Execution().RecordGitHubReviewIdentity(t.Context(), inputs[i])
				})
			}
			close(start)
			successes := 0
			var recordedID string
			for i, done := range results {
				result := integrationdb.Await(t, done, "concurrent review observation")
				if result.Err == nil {
					successes++
					require.True(t, result.Value.Recorded)
					require.True(t, result.Value.Continue)
					recordedID = inputs[i].Observation.ReviewID
				} else {
					require.Equal(t, "conflicting", name)
					require.ErrorIs(t, result.Err, storeerr.ErrIdempotencyConflict)
					require.Equal(t, executionstore.RecordGitHubReviewIdentityResult{}, result.Value)
				}
			}
			wantSuccesses := 2
			if name == "conflicting" {
				wantSuccesses = 1
			}
			require.Equal(t, wantSuccesses, successes)
			before.NativeID = &recordedID
			require.Equal(t, before, f.creator(t, operation.input.ToolCallID))
		})
	}
}

func TestGitHubReviewRecordAfterRetirementPreservesFactWithoutContinuing(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"stop", "revoke", "replacement", "disconnect", "app_disabled", "runtime_expired"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newGitHubReviewPreparationFixture(t)
			operation := f.tools(t, githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")})[0]
			prepared := prepareGitHubObservationCreator(t, operation)
			before := f.creator(t, operation.input.ToolCallID)
			input := githubReviewRecordInput(f, operation, "PRR_late")
			switch action {
			case "stop":
				result, err := f.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
					ProjectID: testProjectID, AgentID: f.AgentID, Actor: mustOmnaraActorParams(t, f.UserID),
				})
				require.NoError(t, err)
				require.True(t, result.Affected)
			case "revoke", "replacement":
				require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, prepared.Binding().ID))
				if action == "replacement" {
					replacement := f.grant(t, f.target.ID)
					require.NotEqual(t, prepared.Binding().ID, replacement.ID)
				}
			case "disconnect":
				require.NoError(t, f.Store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, f.install.ID))
			case "app_disabled":
				_, err := f.Store.pool.Exec(ctx, `UPDATE integration_apps SET state='disabled' WHERE id=$1`,
					f.install.IntegrationAppID)
				require.NoError(t, err)
			case "runtime_expired":
				expireAgentRuntimeLockForTest(t, ctx, f.Store, f.Lock.ID)
			}
			callBefore, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, operation.input.ToolCallID)
			require.NoError(t, err)
			for range 2 {
				result, err := f.Store.Execution().RecordGitHubReviewIdentity(ctx, input)
				require.NoError(t, err, "late provider acknowledgment is independent of further-send authority")
				require.Equal(t, executionstore.RecordGitHubReviewIdentityResult{Recorded: true, Continue: false}, result)
			}
			before.NativeID = &input.Observation.ReviewID
			require.Equal(t, before, f.creator(t, operation.input.ToolCallID))
			callAfter, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, operation.input.ToolCallID)
			require.NoError(t, err)
			require.Equal(t, callBefore, callAfter, "recording identity must not reopen or finish the original tool")
			rows, err := f.Store.Execution().LookupGitHubReviewObservations(ctx, input.Scope,
				[]executionstore.GitHubReviewObservation{input.Observation})
			require.NoError(t, err)
			require.Equal(t, executionstore.GitHubReviewOwned, rows[0].Ownership)
		})
	}
}

func TestGitHubReviewObservationsRejectMalformedAndBoundedBatches(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newGitHubReviewPreparationFixture(t)
	operation := f.tools(t, githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")})[0]
	prepareGitHubObservationCreator(t, operation)
	before := f.creator(t, operation.input.ToolCallID)
	input := githubReviewRecordInput(f, operation, "PRR_valid")
	for _, name := range []string{"empty_id", "long_id", "whitespace", "nul", "control", "bad_commit", "short_commit"} {
		invalid := input
		switch name {
		case "empty_id":
			invalid.Observation.ReviewID = ""
		case "long_id":
			invalid.Observation.ReviewID = strings.Repeat("x", 513)
		case "whitespace":
			invalid.Observation.ReviewID = " PRR_valid"
		case "nul":
			invalid.Observation.ReviewID = "PRR_\x00invalid"
		case "control":
			invalid.Observation.ReviewID = "PRR_\ninvalid"
		case "bad_commit":
			invalid.Observation.CommitID = strings.Repeat("z", 40)
		case "short_commit":
			invalid.Observation.CommitID = "abcd"
		}
		rows, err := f.Store.Execution().LookupGitHubReviewObservations(ctx, input.Scope,
			[]executionstore.GitHubReviewObservation{invalid.Observation})
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest, name)
		require.Empty(t, rows)
		result, err := f.Store.Execution().RecordGitHubReviewIdentity(ctx, invalid)
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest, name)
		require.Equal(t, executionstore.RecordGitHubReviewIdentityResult{}, result)
	}
	for _, name := range []string{"zero_creator", "unknown_evidence"} {
		invalid := input
		if name == "zero_creator" {
			invalid.Observation.CreatingToolCallID = uuid.Nil
		} else {
			invalid.Evidence = "unverified"
		}
		result, err := f.Store.Execution().RecordGitHubReviewIdentity(ctx, invalid)
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
		require.Equal(t, executionstore.RecordGitHubReviewIdentityResult{}, result)
	}
	_, err := f.Store.Execution().LookupGitHubReviewObservations(ctx, input.Scope,
		[]executionstore.GitHubReviewObservation{input.Observation, input.Observation})
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest, "duplicate native IDs make a batch ambiguous")
	batch := make([]executionstore.GitHubReviewObservation, executionstore.MaxGitHubReviewObservations+1)
	for i := range batch {
		batch[i] = executionstore.GitHubReviewObservation{ReviewID: fmt.Sprintf("PRR_%d", i), CommitID: githubReviewCommit}
	}
	_, err = f.Store.Execution().LookupGitHubReviewObservations(ctx, input.Scope, batch)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	rows, err := f.Store.Execution().LookupGitHubReviewObservations(ctx, input.Scope,
		batch[:executionstore.MaxGitHubReviewObservations])
	require.NoError(t, err)
	require.Len(t, rows, executionstore.MaxGitHubReviewObservations)
	for _, row := range rows {
		require.Equal(t, executionstore.GitHubReviewUnknown, row.Ownership)
		require.Equal(t, uuid.Nil, row.CreatingToolCallID)
		require.Empty(t, row.CommitID)
	}
	require.Equal(t, before, f.creator(t, operation.input.ToolCallID))
}

func TestGitHubReviewObservationsResolveRetiredThreadParent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newGitHubReviewPreparationFixture(t)
	definitionInput := f.definition
	definitionInput.ImplementationKey = "github_review_thread"
	definitionInput.Kind = integrationstore.ChannelKindGitHubReviewThread
	definitionInput.SendParamsSchema = json.RawMessage(`{"type":"object","additionalProperties":false}`)
	definition, err := f.Store.Integrations().PublishConnectorChannelDefinition(ctx, definitionInput)
	require.NoError(t, err)
	thread, err := f.Store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: f.install.ID, ChannelDefinitionID: definition.ID,
		ParentChannelID: f.target.ID, ProviderRef: "PRRT_first", ProviderRefKind: "review_thread",
	})
	require.NoError(t, err)
	f.grant(t, thread.ID)
	operations := f.tools(t, githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")},
		githubReviewSend{channel: thread.ID, params: `{}`})
	prepareGitHubObservationCreator(t, operations[0])
	before := f.creator(t, operations[0].input.ToolCallID)
	input := githubReviewRecordInput(f, operations[0], "PRR_recovered")
	input.Scope, input.Evidence = githubReviewObservationScope(f, operations[1]), executionstore.GitHubReviewMarker
	input.Observation.CommitID = ""
	require.NoError(t, f.Store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, f.install.ID))
	rows, err := f.Store.Execution().LookupGitHubReviewObservations(ctx, input.Scope,
		[]executionstore.GitHubReviewObservation{input.Observation})
	require.NoError(t, err)
	require.Equal(t, []executionstore.GitHubReviewObservationResult{{
		ReviewID: input.Observation.ReviewID, Ownership: executionstore.GitHubReviewOwned,
		CreatingToolCallID: operations[0].input.ToolCallID, CommitID: strings.ToLower(githubReviewCommit),
	}}, rows, "retired thread and parent rows still establish historical PR ownership")
	result, err := f.Store.Execution().RecordGitHubReviewIdentity(ctx, input)
	require.NoError(t, err)
	require.Equal(t, executionstore.RecordGitHubReviewIdentityResult{Recorded: true, Continue: false}, result)
	before.NativeID = &input.Observation.ReviewID
	require.Equal(t, before, f.creator(t, operations[0].input.ToolCallID))
}

func githubReviewObservationScope(
	f githubReviewPreparationFixture, operation managedOperationFixture,
) executionstore.GitHubReviewOperationScope {
	return executionstore.GitHubReviewOperationScope{
		IntegrationAppID: f.install.IntegrationAppID, IntegrationInstallID: f.install.ID,
		AgentID: operation.AgentID, ChannelID: operation.input.ChannelID, RequestID: operation.input.ToolCallID,
		Capabilities: testChannelCapabilities("github"),
	}
}

func githubReviewRecordInput(
	f githubReviewPreparationFixture, operation managedOperationFixture, nativeID string,
) executionstore.RecordGitHubReviewIdentityInput {
	return executionstore.RecordGitHubReviewIdentityInput{
		Scope: githubReviewObservationScope(f, operation), Evidence: executionstore.GitHubReviewCreateResponse,
		Observation: executionstore.GitHubReviewObservation{
			ReviewID: nativeID, CommitID: githubReviewCommit, CreatingToolCallID: operation.input.ToolCallID,
		},
	}
}

func prepareGitHubObservationCreator(
	t *testing.T, operation managedOperationFixture,
) executionstore.PreparedChannelOperation {
	t.Helper()
	prepared := prepareGitHubReviewOperation(t, operation)
	_, _, err := operation.Store.Execution().PrepareChannelSend(t.Context(), prepared)
	require.NoError(t, err)
	return prepared
}

func finishGitHubObservationCreator(t *testing.T, operation managedOperationFixture) {
	t.Helper()
	_, err := operation.Store.Execution().CompleteRuntimeToolCall(t.Context(), executionstore.CompleteRuntimeToolCallInput{
		ProjectID: testProjectID, AgentID: operation.AgentID,
		ID: operation.input.ToolCallID, RuntimeLockID: operation.Lock.ID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"Review identity recorded"}]`),
	})
	require.NoError(t, err)
}
