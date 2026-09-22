//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

type choiceTestProvider struct {
	appConsumerProvider
	menus             []integrationstore.AppProfileChoiceRecord
	notices           []string
	presentationError error
}

func (p *choiceTestProvider) PresentProfileChoice(
	ctx context.Context, _ integrationstore.ProjectAppRecord,
	choice integrationstore.AppProfileChoiceRecord, check func(context.Context) error) (string, string, error) {
	if err := check(ctx); err != nil {
		return "", "", err
	}
	if p.presentationError != nil {
		return "", "", p.presentationError
	}
	p.menus = append(p.menus, choice)
	return "C123", choice.ID.String(), nil
}

func (p *choiceTestProvider) DismissProfileChoice(_ context.Context, _ integrationstore.ProjectAppRecord,
	_ integrationstore.AppProfileChoiceRecord, text string) error {
	p.notices = append(p.notices, text)
	return nil
}

func (p *choiceTestProvider) NotifyInboxFailure(_ context.Context, _ integrationstore.ProjectAppRecord,
	_ integrationstore.IntegrationInboxRecord, _ string) error {
	p.notices = append(p.notices, "Request failed")
	return nil
}

type choiceJourney struct {
	t        *testing.T
	pool     *pgxpool.Pool
	store    *storage.Store
	ids      storagefixture.ProjectIDs
	appSetup integrationstore.ProjectAppRecord
	profiles []executionstore.AgentProfileRecord
	app      integrationstore.ProjectAppRecord
	provider *choiceTestProvider
	consumer *AppInboxConsumer
	event    AppEvent
}

func newChoiceJourney(t *testing.T, profileCount int) *choiceJourney {
	t.Helper()
	pool, store, ids, appID := appWorkerFixture(t)
	ctx := t.Context()
	appSetup, err := store.Integrations().GetProjectAppByID(ctx, appID)
	require.NoError(t, err)
	f := &choiceJourney{t: t, pool: pool, store: store, ids: ids, appSetup: appSetup, provider: &choiceTestProvider{}}
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	var slots []integrationstore.AppLaunchSlot
	for _, name := range []string{"light", "heavy"}[:profileCount] {
		profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
			ProjectID: ids.ProjectID, Name: name, CurrentConfigID: base.ID,
		})
		require.NoError(t, err)
		f.profiles = append(f.profiles, profile)
		slots = append(slots, integrationstore.AppLaunchSlot{Key: name, AgentProfileID: &profile.ID})
	}
	f.app, err = store.Integrations().UpdateProjectApp(ctx, appID, integrationstore.SaveProjectAppInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat", AppType: appdefinition.SlackThread,
		Settings: integrationstore.ProjectAppSettings{Launcher: &integrationstore.AppLauncher{
			Trigger: "mention", ScopeKind: "channel", ScopeRef: "C123", Slots: slots,
		}},
	})
	require.NoError(t, err)
	f.appSetup = f.app
	f.event = AppEvent{
		Event: appdefinition.Event{Kind: "message", Mentioned: true, Scope: appdefinition.Scope{
			Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"},
		}},
		SemanticKey: "slack:message:T123:C123:1.2", ContentBlocks: json.RawMessage(`[{"type":"text","text":"review my original request"}]`),
		Actor: appTestActor(t, f.app.ID, "U_ORIGINAL"),
	}
	f.restart()
	return f
}

func (f *choiceJourney) restart() {
	providers := map[string]AppInboxProvider{"slack": f.provider}
	router := NewAppRouter(f.store.Execution(), f.store.Integrations())
	launcher := NewChatAppLauncher(f.store.Integrations(), f.store.Execution(), providers)
	workflow := NewAppLaunchWorkflow(router, map[appdefinition.Type]AppLauncher{
		appdefinition.SlackThread: launcher.Decide,
	})
	f.consumer = NewAppInboxConsumer(router, f.store.Integrations(), &appConsumerUploads{}, providers, nil, workflow)
}

func (f *choiceJourney) receive(key string, event AppEvent) []AppSlotAdmission {
	f.t.Helper()
	_, _, err := f.store.Integrations().AcceptIntegrationReceipt(f.t.Context(),
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID: f.ids.ProjectID, AppID: f.appSetup.ID, ReceiptKey: key, Payload: []byte(`{"original":true}`),
		})
	require.NoError(f.t, err)
	// The provider decodes this external sender separately for each owning app.
	event.Actor = appTestActor(f.t, f.appSetup.ID, event.Actor.ProviderUserID)
	f.provider.events = []AppEvent{event}
	receipt := f.claim()
	results, err := f.consumer.Consume(f.t.Context(), receipt.Lease())
	require.NoError(f.t, err)
	return results
}

func (f *choiceJourney) claim() integrationstore.IntegrationInboxRecord {
	f.t.Helper()
	receipt, found, err := f.store.Integrations().ClaimIntegrationInbox(f.t.Context(),
		integrationstore.ClaimIntegrationInboxInput{
			ProjectID: f.ids.ProjectID, AppID: f.appSetup.ID, LeaseDuration: time.Minute,
		})
	require.NoError(f.t, err)
	require.True(f.t, found)
	return receipt
}

func (f *choiceJourney) choose(choice integrationstore.AppProfileChoiceRecord, key string) {
	f.t.Helper()
	selected, err := SelectChatAppProfile(f.t.Context(), f.store.Integrations(), f.appSetup, choice.ID,
		key, "U_CHOOSER", "C123", choice.ID.String())
	require.NoError(f.t, err)
	require.Equal(f.t, key, selected.SelectedKey)
}

func TestChatProfileChoiceLaunchesSelectedProfileAfterRestart(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 2)
	ctx := t.Context()
	var originalConfigs int
	require.NoError(t, f.pool.QueryRow(
		ctx, `SELECT count(*) FROM agent_configs WHERE project_id=$1`, f.ids.ProjectID).Scan(&originalConfigs))
	require.Empty(t, f.receive("mention", f.event))
	require.Len(t, f.provider.menus, 1)
	var agents, inputs, interactions, configs int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agents WHERE project_id=$1),
		(SELECT count(*) FROM agent_inputs WHERE project_id=$1),
		(SELECT count(*) FROM agent_interactions WHERE agent_id IN (SELECT id FROM agents WHERE project_id=$1)),
		(SELECT count(*) FROM agent_configs WHERE project_id=$1)`, f.ids.ProjectID,
	).Scan(&agents, &inputs, &interactions, &configs))
	require.Zero(t, agents)
	require.Zero(t, inputs)
	require.Zero(t, interactions)
	require.Equal(t, originalConfigs, configs)
	choice := f.provider.menus[0]
	require.Equal(t, "light", choice.Options[0].Name)
	require.Equal(t, "heavy", choice.Options[1].Name)

	// A later message must not overwrite the request associated with this menu.
	later := f.event
	later.SemanticKey, later.ContentBlocks = "later-message", json.RawMessage(`[{"type":"text","text":"do not substitute me"}]`)
	require.Empty(t, f.receive("while-waiting", later))
	require.Len(t, f.provider.menus, 1)
	f.choose(choice, "heavy")
	f.choose(choice, "heavy")
	f.restart()
	receipt := f.claim()
	before := f.provider.expansions
	results, err := f.consumer.Consume(ctx, receipt.Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, before, f.provider.expansions, "decided work bypasses provider expansion and the launcher")
	launch := results[0].Launch
	require.NotNil(t, launch)
	require.Equal(t, f.profiles[1].ID, launch.Agent.AgentProfileID)
	var originalText string
	require.NoError(t, f.pool.QueryRow(
		ctx, `SELECT text_content FROM content_blocks WHERE owner_agent_input_id=$1 AND ordinal=0`,
		launch.AgentInput.ID).Scan(&originalText))
	require.Equal(t, "review my original request", originalText)
	actor, err := f.store.Execution().GetActor(ctx, f.ids.ProjectID, launch.AgentInput.ActorID)
	require.NoError(t, err)
	require.Equal(t, "U_ORIGINAL", actor.ProviderUserID)
	require.Equal(t, executionstore.ActorProviderApp, actor.Provider)
	require.Equal(t, f.event.Actor.ProviderTenantID, actor.ProviderTenantID)
	results, err = f.consumer.Consume(ctx, receipt.Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].Launch.Created)
	_, found, err := f.store.Integrations().ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: f.ids.ProjectID, AppID: f.appSetup.ID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.False(t, found, "duplicate selection cannot enqueue a second launch")

	follow := f.event
	follow.Event.Mentioned, follow.SemanticKey = false, "follow-up"
	results = f.receive("follow-up", follow)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Input)
	require.Equal(t, launch.Agent.ID, results[0].Input.AgentInput.AgentID)
}

func TestChatProfileChoiceSingleProfileNeedsNoMenu(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 1)
	results := f.receive("mention", f.event)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Launch)
	require.Empty(t, f.provider.menus)
}

func TestChatProfileChoiceFastClickBeforeOwnerFreeze(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 2)
	ctx := t.Context()
	_, _, err := f.store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.ids.ProjectID, AppID: f.appSetup.ID, ReceiptKey: "owner", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	owner := f.claim()
	f.provider.events = []AppEvent{f.event}
	_, err = f.consumer.launchers.Decide(ctx, owner.Lease(), owner, f.appSetup, f.provider.events)
	require.NoError(t, err)
	require.Len(t, f.provider.menus, 1)
	// The provider menu is confirmed, but the publishing receipt has not frozen
	// its plan. A quick click accepts a separate request before that happens.
	f.choose(f.provider.menus[0], "heavy")
	_, err = f.consumer.Consume(ctx, owner.Lease())
	require.ErrorIs(t, err, integrationstore.ErrAppSelectionReserved)
	decided := f.claim()
	results, err := f.consumer.Consume(ctx, decided.Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Launch)
	launch := results[0].Launch
	require.Equal(t, f.profiles[1].ID, launch.Agent.AgentProfileID)
	for range 2 {
		results, err = f.consumer.Consume(ctx, owner.Lease())
		require.NoError(t, err)
		require.Len(t, results, 1)
		require.NotNil(t, results[0].Input)
		require.Equal(t, launch.AgentInput.ID, results[0].Input.AgentInput.ID)
	}
	var agents, inputs int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agents WHERE project_id=$1),
		(SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_kind='content')`,
		f.ids.ProjectID).Scan(&agents, &inputs))
	require.Equal(t, 1, agents)
	require.Equal(t, 1, inputs)
	require.Len(t, f.provider.menus, 1)
}

type blockedChoiceProvider struct {
	*choiceTestProvider
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (p *blockedChoiceProvider) PresentProfileChoice(
	ctx context.Context, _ integrationstore.ProjectAppRecord,
	choice integrationstore.AppProfileChoiceRecord, check func(context.Context) error) (string, string, error) {
	if err := check(ctx); err != nil {
		return "", "", err
	}
	if p.calls.Add(1) == 1 {
		close(p.started)
		select {
		case <-p.release:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	return "C123", choice.ID.String(), nil
}

func TestChatProfileChoiceConcurrentSiblingDoesNotPostAnotherMenu(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 2)
	ctx := t.Context()
	p := &blockedChoiceProvider{choiceTestProvider: f.provider, started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(p.release) })
	launcher := NewChatAppLauncher(f.store.Integrations(), f.store.Execution(), map[string]AppInboxProvider{"slack": p})
	capture := func(key string) integrationstore.IntegrationInboxRecord {
		_, _, err := f.store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
			ProjectID: f.ids.ProjectID, AppID: f.appSetup.ID, ReceiptKey: key, Payload: []byte(`{}`),
		})
		require.NoError(t, err)
		return f.claim()
	}
	first, second := capture("mention"), capture("files")
	event := f.event
	event.Sibling = &executionstore.InboxMessageSibling{Key: "files"}
	input := AppLaunchContext{
		Receipt: first, App: f.app, Event: event,
		Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"},
	}
	one := integrationdb.RunAsync(func() ([]AppLaunchIntent, error) { return launcher.Decide(ctx, input) })
	integrationdb.Await(t, p.started, "first menu publication")
	other := input
	other.Receipt = second
	other.Event.SemanticKey = "files"
	other.Event.Sibling = &executionstore.InboxMessageSibling{Key: event.SemanticKey, AttachmentNotice: "Original files"}
	intents, err := launcher.Decide(ctx, other)
	require.NoError(t, err)
	require.Empty(t, intents)
	require.EqualValues(t, 1, p.calls.Load(), "sibling enrichment cannot become a second publisher")
	// Let the original durable receipt finish publication after the sibling.
	p.release <- struct{}{}
	require.Empty(t, integrationdb.AwaitSuccess(t, one, "menu publication"))
}

func TestUnavailableChatSetupDoesNotDropOtherLaunchesOrSubscriptions(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 1)
	other := seedIndependentApp(t, f.pool, f.app, "working-setup")
	unavailable := map[uuid.UUID]bool{f.app.ID: true}
	router := NewAppRouter(f.store.Execution(), f.store.Integrations())
	workflow := NewAppLaunchWorkflow(router, map[appdefinition.Type]AppLauncher{appdefinition.SlackThread: func(
		ctx context.Context, input AppLaunchContext,
	) ([]AppLaunchIntent, error) {
		if unavailable[input.App.ID] {
			return nil, ErrAppLaunchUnavailable
		}
		return EverySlotAppLauncher(ctx, input)
	}})
	f.consumer = NewAppInboxConsumer(router, f.store.Integrations(), nil,
		map[string]AppInboxProvider{"slack": f.provider}, nil, workflow)
	require.Empty(t, f.receive("initial", f.event), "the unavailable app settles its own receipt")
	f.appSetup = other
	results := f.receive("initial", f.event)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Launch)
	agentID := results[0].Launch.Agent.ID
	unavailable[other.ID] = true
	follow := f.event
	follow.SemanticKey = "next-message"
	results = f.receive("next", follow)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Input)
	require.Equal(t, agentID, results[0].Input.AgentInput.AgentID)
}

func TestChatProfileChoiceRetainsAttachmentDigestBeforeSelection(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 2)
	ctx := t.Context()
	f.event.Sibling = &executionstore.InboxMessageSibling{Key: "files-callback"}
	require.Empty(t, f.receive("mention", f.event))
	choice := f.provider.menus[0]
	// Slack's attachment callback can enrich the original request while the
	// menu is open. It must update both source representations, without a launch.
	content := []byte("original file")
	id := uuid.New()
	sibling := f.event
	sibling.SemanticKey = "files-callback"
	sibling.Sibling = &executionstore.InboxMessageSibling{Key: f.event.SemanticKey, AttachmentNotice: "Original files"}
	sibling.Files = []AppPlannedFile{{ArtifactID: id, ProviderFileID: "F123", Expected: &artifactstore.PreparedArtifact{
		ID: id, ContentType: "text/plain", Filename: "review.txt",
		Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content)),
	}}}
	blocks, err := json.Marshal([]map[string]any{
		{"type": "text", "text": "review my original request"}, {"type": "media_ref", "artifact_id": id.String()},
	})
	require.NoError(t, err)
	sibling.ContentBlocks = blocks
	f.provider.file = AppInboxFile{Content: content, ContentType: "text/plain", Filename: "review.txt"}
	require.Empty(t, f.receive("files-before-choice", sibling))
	require.Len(t, f.provider.menus, 1)
	f.choose(choice, "light")
	f.restart()
	receipt := f.claim()
	f.provider.file.Content = []byte("changed upstream")
	_, err = f.consumer.Consume(ctx, receipt.Lease())
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	var agents int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM agents WHERE project_id=$1`,
		f.ids.ProjectID).Scan(&agents))
	require.Zero(t, agents, "a changed file must never silently replace the original request")
	f.provider.file.Content = content
	results, err := f.consumer.Consume(ctx, receipt.Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Launch)
	var artifacts int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM artifacts WHERE agent_id=$1`,
		results[0].Launch.Agent.ID).Scan(&artifacts))
	require.Equal(t, 1, artifacts)
}

func TestChatProfileChoiceEditedSlotCannotLaunchReplacement(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 2)
	require.Empty(t, f.receive("mention", f.event))
	f.choose(f.provider.menus[0], "heavy")
	// Keep the same key but replace the profile after the click and before work.
	settings := f.app.Settings
	settings.Launcher.Slots[1].AgentProfileID = &f.profiles[0].ID
	_, err := f.store.Integrations().UpdateProjectApp(t.Context(), f.app.ID, integrationstore.SaveProjectAppInput{
		OrgID: f.ids.OrgID, ProjectID: f.ids.ProjectID, AppType: f.app.AppType,
		Name: f.app.Name, Settings: settings,
	})
	require.NoError(t, err)
	worker := NewAppInboxWorker(f.store.Integrations(), f.consumer, AppInboxWorkerOptions{})
	worked, err := worker.RunOnce(t.Context())
	require.True(t, worked)
	require.ErrorIs(t, err, ErrAppLaunchUnavailable)
	var state string
	var attempts, agents int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT state,attempt_count FROM integration_inbox WHERE project_id=$1 AND events IS NOT NULL`,
		f.ids.ProjectID).Scan(&state, &attempts))
	require.Equal(t, "failed", state)
	require.Equal(t, 1, attempts)
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agents WHERE project_id=$1`,
		f.ids.ProjectID).Scan(&agents))
	require.Zero(t, agents)
	require.Len(t, f.provider.notices, 1)
}

func TestChatProfileChoiceLateFilesReachEachChosenAgentWithoutSubscription(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 2)
	ctx := t.Context()
	other := seedIndependentApp(t, f.pool, f.app, "overlapping")
	f.event.Sibling = &executionstore.InboxMessageSibling{Key: "slack-files:1.2"}
	require.Empty(t, f.receive("both-menus", f.event))
	f.appSetup = other
	require.Empty(t, f.receive("both-menus", f.event))
	require.Len(t, f.provider.menus, 2)
	f.appSetup = f.app
	f.choose(f.provider.menus[0], "light")
	results, err := f.consumer.Consume(ctx, f.claim().Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	first := results[0].Launch.Agent.ID
	f.appSetup = other
	f.choose(f.provider.menus[1], "heavy")
	results, err = f.consumer.Consume(ctx, f.claim().Lease())
	require.NoError(t, err)
	require.Len(t, results, 1, "the second choice must not replay its original request into the first agent")
	second := results[0].Launch.Agent.ID
	require.NotEqual(t, first, second)
	removeTestAgentSubscriptions(t, f.store, f.app, first)
	removeTestAgentSubscriptions(t, f.store, other, second)

	content := []byte("the original attachment")
	id := uuid.New()
	file := AppPlannedFile{ArtifactID: id, ProviderFileID: "F123", Expected: &artifactstore.PreparedArtifact{
		ID: id, ContentType: "text/plain", Filename: "review.txt",
		SizeBytes: int64(len(content)), Digest: blobstore.ContentDigest(content),
	}}
	f.provider.file = AppInboxFile{Content: content, ContentType: "text/plain", Filename: "review.txt"}
	attachment := f.event
	attachment.SemanticKey = f.event.Sibling.Key
	attachment.Sibling = &executionstore.InboxMessageSibling{
		Key: f.event.SemanticKey, AttachmentNotice: "Files from the original message",
	}
	attachment.Files = []AppPlannedFile{file}
	attachment.ContentBlocks, err = json.Marshal([]map[string]any{{"type": "media_ref", "artifact_id": id.String()}})
	require.NoError(t, err)
	for _, app := range []integrationstore.ProjectAppRecord{f.app, other} {
		f.appSetup = app
		results = f.receive("late-files", attachment)
		require.Len(t, results, 1)
		require.NotNil(t, results[0].Input)
		require.True(t, results[0].Input.Created)
		expected := first
		if app.ID == other.ID {
			expected = second
		}
		require.Equal(t, expected, results[0].Input.AgentInput.AgentID)
		results = f.receive("duplicate-late-files", attachment)
		require.Len(t, results, 1)
		require.False(t, results[0].Input.Created)
	}
	require.Len(t, f.provider.menus, 2)
}

func TestChatProfileChoiceAcceptedSelectionHoldsEarlyReplies(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct{ mentioned, frozen bool }{
		{false, false}, {true, false}, {false, true}, {true, true},
	} {
		t.Run(fmt.Sprintf("mentioned=%t/frozen=%t", scenario.mentioned, scenario.frozen), func(t *testing.T) {
			t.Parallel()
			f := newChoiceJourney(t, 2)
			ctx := t.Context()
			require.Empty(t, f.receive("mention", f.event))
			f.choose(f.provider.menus[0], "heavy")
			selected := f.claim() // No agent/subscription exists yet.
			router := NewAppRouter(f.store.Execution(), f.store.Integrations())
			if scenario.frozen {
				var events []AppEvent
				require.NoError(t, json.Unmarshal(selected.Events, &events))
				_, err := router.Freeze(ctx, selected.Lease(), events)
				require.NoError(t, err)
			}
			_, _, err := f.store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: f.ids.ProjectID, AppID: f.appSetup.ID,
				ReceiptKey: "early-reply", Payload: []byte(`{}`),
			})
			require.NoError(t, err)
			reply := f.claim()
			event := f.event
			event.SemanticKey, event.Event.Mentioned = "early-reply", scenario.mentioned
			f.provider.events = []AppEvent{event}
			if !scenario.mentioned {
				_, err = router.freezeEmptyIfUnrouted(ctx, reply.Lease(), f.appSetup, event)
				require.ErrorIs(t, err, integrationstore.ErrAppSelectionReserved,
					"Slack's download shortcut must protect the selection-to-plan gap too")
			}
			_, err = f.consumer.Consume(ctx, reply.Lease())
			require.ErrorIs(t, err, integrationstore.ErrAppSelectionReserved)
			require.Len(t, f.provider.menus, 1, "a later mention cannot create another menu while launch is queued")
			results, err := f.consumer.Consume(ctx, selected.Lease())
			require.NoError(t, err)
			require.Len(t, results, 1)
			agentID := results[0].Launch.Agent.ID
			results, err = f.consumer.Consume(ctx, reply.Lease())
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.NotNil(t, results[0].Input)
			require.Equal(t, agentID, results[0].Input.AgentInput.AgentID)
		})
	}
}

func TestChatProfileChoiceUnavailableMenuRecoversOnNewMention(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 2)
	f.provider.presentationError = ErrAppLaunchUnavailable
	require.Empty(t, f.receive("missing-presentation-configuration", f.event))
	require.Empty(t, f.provider.menus)
	f.provider.presentationError = nil // An administrator fixed the provider configuration.
	next := f.event
	next.SemanticKey = "fresh-mention"
	require.Empty(t, f.receive("fresh-mention", next))
	require.Len(t, f.provider.menus, 1)
	f.choose(f.provider.menus[0], "light")
	results, err := f.consumer.Consume(t.Context(), f.claim().Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Launch)
}

func TestChatProfileChoiceStaleMenuDoesNotBlockFreshSelection(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 2)
	ctx := t.Context()
	require.Empty(t, f.receive("old-menu", f.event))
	old := f.provider.menus[0]
	settings := f.app.Settings
	settings.Launcher.Slots[1].AgentProfileID = &f.profiles[0].ID
	_, err := f.store.Integrations().UpdateProjectApp(ctx, f.app.ID, integrationstore.SaveProjectAppInput{
		OrgID: f.ids.OrgID, ProjectID: f.ids.ProjectID, AppType: f.app.AppType,
		Name: f.app.Name, Settings: settings,
	})
	require.NoError(t, err)
	_, err = SelectChatAppProfile(ctx, f.store.Integrations(), f.appSetup, old.ID,
		"heavy", "U_CHOOSER", "C123", old.ID.String())
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	next := f.event
	next.SemanticKey = "new-mention"
	require.Empty(t, f.receive("new-mention", next))
	require.Len(t, f.provider.menus, 2)
	require.NotEqual(t, old.ID, f.provider.menus[1].ID)
	f.choose(f.provider.menus[1], "heavy")
	results, err := f.consumer.Consume(ctx, f.claim().Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, f.profiles[0].ID, results[0].Launch.Agent.AgentProfileID)
}

func TestChatProfileChoiceFailedSourceCannotLaunchAgain(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 2)
	ctx := t.Context()
	require.Empty(t, f.receive("original-mention", f.event))
	f.choose(f.provider.menus[0], "heavy")
	original := f.claim()
	require.NoError(t, f.store.Integrations().WithIntegrationInboxLease(ctx, original.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.Fail(ctx, "before planning") }))
	changeProfile := func(id uuid.UUID) {
		settings := f.app.Settings
		settings.Launcher.Slots[1].AgentProfileID = &id
		_, err := f.store.Integrations().UpdateProjectApp(ctx, f.app.ID, integrationstore.SaveProjectAppInput{
			OrgID: f.ids.OrgID, ProjectID: f.ids.ProjectID, AppType: f.app.AppType,
			Name: f.app.Name, Settings: settings,
		})
		require.NoError(t, err)
	}
	changeProfile(f.profiles[0].ID)
	next := f.event
	next.SemanticKey = "replacement-mention"
	require.Empty(t, f.receive("replacement-mention", next))
	require.Len(t, f.provider.menus, 2, "failed unplanned work must allow a fresh request")
	f.choose(f.provider.menus[1], "heavy")
	results, err := f.consumer.Consume(ctx, f.claim().Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, f.profiles[0].ID, results[0].Launch.Agent.AgentProfileID)
	changeProfile(f.profiles[1].ID) // Original intent once again matches the saved slot, but not its agent.
	_, err = f.consumer.Consume(ctx, original.Lease())
	require.ErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)
	// Repeating the selection button never queues another attempt.
	f.choose(f.provider.menus[0], "heavy")
	_, found, err := f.store.Integrations().ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: f.ids.ProjectID, AppID: f.app.ID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.False(t, found)
	var inputs int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_kind='content'`,
		results[0].Launch.Agent.ID).Scan(&inputs))
	require.Equal(t, 1, inputs, "the old request cannot be delivered to the replacement profile's agent")
}

func TestChatProfileChoiceSiblingCannotRestartFailedLaunch(t *testing.T) {
	t.Parallel()
	for _, frozen := range []bool{false, true} {
		t.Run(fmt.Sprintf("frozen=%t", frozen), func(t *testing.T) {
			t.Parallel()
			f := newChoiceJourney(t, 2)
			ctx := t.Context()
			inbox := f.store.Integrations()
			f.event.Sibling = &executionstore.InboxMessageSibling{Key: "files-callback"}
			require.Empty(t, f.receive("mention", f.event))
			choice := f.provider.menus[0]
			f.choose(choice, "heavy")
			selected := f.claim()
			if frozen {
				var events []AppEvent
				require.NoError(t, json.Unmarshal(selected.Events, &events))
				_, err := NewAppRouter(f.store.Execution(), inbox).Freeze(ctx, selected.Lease(), events)
				require.NoError(t, err)
			}
			sibling := f.event
			sibling.SemanticKey = f.event.Sibling.Key
			sibling.Sibling = &executionstore.InboxMessageSibling{Key: f.event.SemanticKey}
			f.provider.events = []AppEvent{sibling}
			_, _, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: f.ids.ProjectID, AppID: f.app.ID, ReceiptKey: "late-callback", Payload: []byte(`{}`),
			})
			require.NoError(t, err)
			late := f.claim()
			_, err = f.consumer.Consume(ctx, late.Lease())
			require.ErrorIs(t, err, integrationstore.ErrAppSelectionReserved)
			waiting, err := inbox.GetIntegrationInbox(ctx, f.ids.ProjectID, late.ID)
			require.NoError(t, err)
			require.Empty(t, waiting.Plan, "the sibling cannot acquire independent launch authority")
			require.NoError(t, inbox.WithIntegrationInboxLease(ctx, selected.Lease(),
				func(work *integrationstore.IntegrationInboxLeaseTx) error {
					return work.Fail(ctx, "launch permanently failed")
				}))
			results, err := f.consumer.Consume(ctx, late.Lease())
			require.NoError(t, err)
			require.Empty(t, results)
			var agents int
			require.NoError(t, f.pool.QueryRow(ctx,
				`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
			require.Zero(t, agents)
			require.Len(t, f.provider.menus, 1)
			f.choose(choice, "heavy") // Repeated buttons cannot resurrect the failed handoff.
			next := f.event
			next.SemanticKey, next.Sibling = "fresh-mention", nil
			require.Empty(t, f.receive("fresh-mention", next))
			require.Len(t, f.provider.menus, 2)
			f.choose(f.provider.menus[1], "light")
			results, err = f.consumer.Consume(ctx, f.claim().Lease())
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.True(t, results[0].Launch.Created)
		})
	}
}

func TestChatProfileChoiceCommittedLaunchSurvivesReceiptFailure(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 2)
	ctx := t.Context()
	inbox := f.store.Integrations()
	f.event.Sibling = &executionstore.InboxMessageSibling{Key: "files-callback"}
	require.Empty(t, f.receive("mention", f.event))
	f.choose(f.provider.menus[0], "heavy")
	selected := f.claim()
	var events []AppEvent
	require.NoError(t, json.Unmarshal(selected.Events, &events))
	plan, err := NewAppRouter(f.store.Execution(), inbox).Freeze(ctx, selected.Lease(), events)
	require.NoError(t, err)
	require.Len(t, plan, 1)
	var agentID uuid.UUID
	for key := range plan {
		result, err := f.store.Execution().AdmitInboxLaunchSlot(ctx, selected.Lease(), key)
		require.NoError(t, err)
		agentID = result.Agent.ID
	}
	// Admission committed, but completing the receipt never succeeded.
	require.NoError(t, inbox.WithIntegrationInboxLease(ctx, selected.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.Fail(ctx, "completion bookkeeping exhausted retries")
		}))
	removeTestAgentSubscriptions(t, f.store, f.app, agentID)
	sibling := f.event
	sibling.SemanticKey = f.event.Sibling.Key
	sibling.Sibling = &executionstore.InboxMessageSibling{Key: f.event.SemanticKey}
	results := f.receive("late-callback", sibling)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Input)
	require.Equal(t, agentID, results[0].Input.AgentInput.AgentID)
	var agents int
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
	require.Equal(t, 1, agents)
}
