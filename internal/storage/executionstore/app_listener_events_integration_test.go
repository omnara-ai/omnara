//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestListenerEventChangesRewriteRuntimeSubscriptionsAndFenceFrozenInput(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	f.app = inboxInputApp(t, f, "github")
	_, err := f.store.Integrations().UpdateProjectApp(f.ctx, f.app.ID, integrationstore.SaveProjectAppInput{
		OrgID: testOrgID, ProjectID: testProjectID, Name: f.app.Name, DefinitionID: f.app.DefinitionID,
		Settings: integrationstore.ProjectAppSettings{Launcher: &integrationstore.AppLauncher{
			Trigger: "mention", ScopeKind: "installation", ScopeRef: f.app.ProviderAccountRef,
			Slots: []integrationstore.AppLaunchSlot{{Key: "review", AgentProfileID: &f.profile.ID}},
		}},
	})
	require.NoError(t, err)
	listener := agentconfig.AppCapabilityCompiled{
		AppID: publicResourceID(publicid.KindProjectApp, f.app.ID),
		Config: json.RawMessage(
			`{"conversations":[{"repository_id":123,"pull_request":41}],"events":["discussion_comment"]}`,
		),
	}
	definition := f.definition(t, "Review comments", map[string]agentconfig.AppCapabilityCompiled{
		"github__pull_request": listener,
	})
	address := integrationstore.ConversationAddress{Kind: "pull_request", Ref: "123#42"}
	launch := f.launchInput(uuid.Nil, "listener-events-launch")
	launch.ProfileID, launch.DerivedBaseConfigID = f.profile.ID, f.profile.CurrentConfigID
	launch.DerivedConfig = &definition
	launch.InitialInput = &executionstore.LaunchInitialInput{
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"Review this pull request"}]`),
		Actor: &executionstore.ActorParams{
			Provider: "github", ProviderTenantID: f.app.ProviderTenantID, ProviderUserID: "789",
		},
		Origin:           &executionstore.LaunchInputOrigin{AppID: f.app.ID, Address: address},
		SemanticEventKey: "launch-review",
	}
	slot := executionstore.InboxLaunchSlot{
		AgentID: uuid.Must(uuid.NewV7()), ListenerKey: "github__pull_request", Launch: launch,
		Selection: integrationstore.InboxAppSelection{AppID: f.app.ID, Address: address, Slot: "review"},
	}
	_, _, err = f.store.Integrations().AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: testProjectID, AppID: f.app.ID, ReceiptKey: "launch", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	receipt, found, err := f.store.Integrations().ClaimIntegrationInbox(f.ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: testProjectID, AppID: f.app.ID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, found)
	plan, err := json.Marshal(map[string]executionstore.InboxLaunchSlot{"review": slot})
	require.NoError(t, err)
	require.NoError(t, f.store.Integrations().WithIntegrationInboxLease(f.ctx, receipt.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.FreezePlan(f.ctx, plan) }))
	launched, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, receipt.Lease(), "review")
	require.NoError(t, err)
	require.True(t, launched.Created)
	before := f.listeners(t, launched.Agent.ID)
	require.Len(t, before, 2, "one configured PR and the PR followed by launcher admission")
	var runtimeID uuid.UUID
	for _, row := range before {
		require.Equal(t, []string{"discussion_comment"}, row.Events)
		if row.Origin == string(integrationstore.ListenerRuntime) {
			runtimeID = row.ID
			require.Equal(t, address.Ref, row.ScopeRef)
		}
	}
	require.NotEqual(t, uuid.Nil, runtimeID)

	// Both inputs are frozen under the old config. Admission must use the live
	// event selection, even when the listener key and followed PR stay the same.
	freeze := func(event string) integrationstore.IntegrationInboxRecord {
		input := inboxInputPlan(launched.Agent.ID, f.app, event)
		input.Listener = &executionstore.InboxListenerAuthority{
			Event:        event,
			Alternatives: []executionstore.InboxListenerReference{{ListenerKey: "github__pull_request", Address: address}},
		}
		return freezeInboxInput(t, f, input, event, time.Minute)
	}
	discussion, review := freeze("discussion_comment"), freeze("review_comment")
	require.NoError(t, f.store.Execution().CheckInboxConversationAuthority(
		f.ctx, discussion.Lease(), "recipient", address))
	require.ErrorIs(t, f.store.Execution().CheckInboxConversationAuthority(
		f.ctx, review.Lease(), "recipient", address), storeerr.ErrUnauthorized)
	listener.Config = json.RawMessage(
		`{"conversations":[{"repository_id":123,"pull_request":41}],"events":["review_comment","commit"]}`,
	)
	changed, err := f.store.Execution().ChangeAgentConfig(f.ctx, f.changeInput(t, launched.Agent.ID,
		"Review comments and commits", map[string]agentconfig.AppCapabilityCompiled{"github__pull_request": listener},
		"new-listener-events"))
	require.NoError(t, err)
	after := f.listeners(t, launched.Agent.ID)
	require.Len(t, after, 2)
	for _, row := range after {
		require.Equal(t, []string{"commit", "review_comment"}, row.Events)
		require.Equal(t, changed.AgentConfig.ID, row.SourceConfigID)
		if row.Origin == string(integrationstore.ListenerRuntime) {
			require.Equal(t, runtimeID, row.ID, "event changes update the existing runtime subscription")
			require.Equal(t, address.Ref, row.ScopeRef)
		}
	}
	_, err = f.store.Execution().AdmitInboxInputSlot(f.ctx, discussion.Lease(), "recipient")
	require.ErrorIs(t, err, storeerr.ErrUnauthorized, "the old event must stop reaching the followed PR")
	accepted, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, review.Lease(), "recipient")
	require.NoError(t, err)
	require.True(t, accepted.Created)
	require.Equal(t, launched.Agent.ID, accepted.AgentInput.AgentID)
	replayed, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, review.Lease(), "recipient")
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, accepted.AgentInput.ID, replayed.AgentInput.ID)
}
