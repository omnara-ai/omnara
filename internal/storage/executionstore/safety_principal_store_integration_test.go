//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestSafetyPrincipalFollowsSubagentsToTheUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "safety-principal@example.com", "Safety Principal")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "safety-principal", "Safety Principal", subagentParentYAML,
	)
	parent, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		Message:        "Start.",
		IdempotencyKey: "safety-principal-parent",
	})
	require.NoError(t, err)
	child, err := spawnSubagentForTest(
		t, ctx, store, parent.Agent, profile.CurrentConfigID, "worker", "safety-principal-child", intPtrForSubagentTest(1),
	)
	require.NoError(t, err)

	sendAs := func(from executionstore.AgentRecord, to uuid.UUID, i int) uuid.UUID {
		actor, err := executionstore.SubagentActorParams(from.OrgID, from)
		require.NoError(t, err)
		input, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        to,
			Actor:          actor,
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"round trip"}]`),
			DeliveryMode:   executionstore.DeliveryModeSteering,
			IdempotencyKey: fmt.Sprintf("safety-principal-%s-%d", from.ID, i),
		})
		require.NoError(t, err)
		return input.ID
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx, testProjectID, parent.Agent.ID, testWorkerProcessID, testAgentRuntimeLockLeaseDuration,
	)
	require.NoError(t, err)
	var reply uuid.UUID
	for i := range 3 {
		reply = sendAs(child.Agent, parent.Agent.ID, i)
		_, admitted := admitNextAgentInputAndOpenTurnForTest(t, ctx, store, testProjectID, parent.Agent.ID, lock.ID)
		require.True(t, admitted)
	}
	message := sendAs(parent.Agent, child.Agent.ID, 0)

	want := executionstore.SafetyPrincipal{Kind: publicid.KindUser, ID: user.ID}
	for _, tc := range []struct {
		agentID, inputID uuid.UUID
	}{
		{parent.Agent.ID, parent.AgentInput.ID},
		{child.Agent.ID, child.AgentInput.ID},
		{parent.Agent.ID, reply},
		{child.Agent.ID, message},
	} {
		got, err := store.Execution().SafetyPrincipal(ctx, testProjectID, tc.agentID, []uuid.UUID{tc.inputID})
		require.NoError(t, err)
		require.Equal(t, want, got)
	}

	bob := mustCreateProjectDeveloperUser(t, ctx, store, "safety-principal-bob@example.com", "Bob")
	bobInput, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        parent.Agent.ID,
		Actor:          mustOmnaraActorParams(t, bob.ID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"steer"}]`),
		DeliveryMode:   executionstore.DeliveryModeSteering,
		IdempotencyKey: "safety-principal-bob",
	})
	require.NoError(t, err)
	laterReply := sendAs(child.Agent, parent.Agent.ID, 3)
	got, err := store.Execution().SafetyPrincipal(
		ctx, testProjectID, parent.Agent.ID, []uuid.UUID{bobInput.ID, laterReply},
	)
	require.NoError(t, err)
	require.Equal(t, executionstore.SafetyPrincipal{Kind: publicid.KindUser, ID: bob.ID}, got)

	got, err = store.Execution().SafetyPrincipal(ctx, testProjectID, parent.Agent.ID, []uuid.UUID{uuid.New()})
	require.NoError(t, err)
	require.Equal(t, executionstore.SafetyPrincipal{Kind: publicid.KindProject, ID: testProjectID}, got)
}
