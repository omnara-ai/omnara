//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

type integrationToolFixture struct {
	Pool               *pgxpool.Pool
	Store              *storage.Store
	User               identitystore.UserRecord
	Profile            executionstore.AgentProfileRecord
	Agent              executionstore.AgentRecord
	AgentConfig        executionstore.AgentConfigRecord
	Lock               executionstore.AgentRuntimeLockRecord
	TurnID             uuid.UUID
	ModelCallContextID uuid.UUID
	ModelOutputEventID uuid.UUID
	Install            integrationstore.IntegrationInstallRecord
	Target             integrationstore.IntegrationTargetRecord
	OriginChannel      connectorToolChannel
	OriginChannels     []connectorToolChannel
	Now                time.Time
	WithMCP            bool
}

type connectorToolChannel struct {
	App     integrationstore.IntegrationAppRecord
	Install integrationstore.IntegrationInstallRecord
	Route   integrationstore.IntegrationRouteRecord
	Target  integrationstore.IntegrationTargetRecord
	Binding integrationstore.IntegrationTargetBindingRecord
}

func toolsTestUserPrincipal(userID uuid.UUID) identitystore.PrincipalRecord {
	return identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: userID}
}

func integrationToolInteraction(
	t *testing.T,
	ctx context.Context,
	fixture integrationToolFixture,
	toolCallID uuid.UUID,
	kind executionstore.AgentInteractionKind,
) executionstore.AgentInteractionRecord {
	t.Helper()
	interaction, found, err := fixture.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		toolCallID,
		kind,
	)
	if err != nil {
		t.Fatalf("get %s interaction: %v", kind, err)
	}
	if !found {
		t.Fatalf("%s interaction not found", kind)
	}
	return interaction
}

func immediateIntegrationBackgroundRunner(ctx context.Context) BackgroundRunner {
	return backgroundRunnerFunc(func(_ string, task func(context.Context) error) bool {
		_ = task(ctx)
		return true
	})
}

func TestListChannelsPaginatesWithoutRepeatingTargets(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "list-channels-pagination")
	createConnectorToolChannel(t, ctx, fixture, "list-channels-pagination")
	turn := fixture.turn()
	turn.Tools[toolcatalog.ToolNameListChannels] = ToolSpec{
		Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
	}
	executor := Executor{Store: fixture.Store, ChannelOperations: unexpectedChannelOperations(t),
		Now: func() time.Time { return fixture.Now.Add(21 * time.Second) }}
	page, err := fixture.Store.Integrations().ListAgentChannelTargets(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		integrationstore.ListAgentChannelTargetsInput{Limit: 1},
	)
	if err != nil || page.Next == nil {
		t.Fatalf("prepare channel page cursor = %+v, %v", page, err)
	}
	wantCursor, err := encodeChannelListCursor(*page.Next, turn, "")
	if err != nil {
		t.Fatalf("encode expected channel page cursor: %v", err)
	}
	secondInput, err := json.Marshal(map[string]any{"cursor": wantCursor, "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	calls := []model.ToolCall{
		{
			ID: "call_list_channels_page_one", Name: toolcatalog.ToolNameListChannels,
			Input: json.RawMessage(`{"limit":1}`),
		},
		{
			ID: "call_list_channels_page_two", Name: toolcatalog.ToolNameListChannels,
			Input: secondInput,
		},
	}
	fixture.recordToolCalls(t, ctx, calls, fixture.Now.Add(20*time.Second))
	firstCall := calls[0]
	firstResult, err := executor.Dispatch(ctx, turn, firstCall)
	if err != nil {
		t.Fatalf("dispatch first channel page: %v", err)
	}
	firstBody := toolResultMapFromTestParts(t, firstResult.ContentParts)
	firstChannels, ok := firstBody["channels"].([]any)
	if !ok || len(firstChannels) != 1 {
		t.Fatalf("first channel page = %#v", firstBody)
	}
	cursor, ok := firstBody["next_cursor"].(string)
	if !ok || cursor == "" {
		t.Fatalf("first channel page cursor = %#v", firstBody["next_cursor"])
	}
	if cursor != wantCursor {
		t.Fatalf("first channel page cursor = %q, want %q", cursor, wantCursor)
	}
	firstChannel, ok := firstChannels[0].(map[string]any)
	if !ok {
		t.Fatalf("first channel = %#v", firstChannels[0])
	}
	assertListedChannelShape(t, firstChannel)

	secondCall := calls[1]
	secondResult, err := executor.Dispatch(ctx, turn, secondCall)
	if err != nil {
		t.Fatalf("dispatch second channel page: %v", err)
	}
	secondBody := toolResultMapFromTestParts(t, secondResult.ContentParts)
	secondChannels, ok := secondBody["channels"].([]any)
	if !ok || len(secondChannels) != 1 {
		t.Fatalf("second channel page = %#v", secondBody)
	}
	secondChannel, ok := secondChannels[0].(map[string]any)
	if !ok {
		t.Fatalf("second channel = %#v", secondChannels[0])
	}
	assertListedChannelShape(t, secondChannel)
	if firstChannel["channel_id"] == secondChannel["channel_id"] {
		t.Fatalf("channel repeated across pages: %#v", firstChannel["channel_id"])
	}
	providers := map[any]bool{
		firstChannel["provider"]:  true,
		secondChannel["provider"]: true,
	}
	if !providers[integrationstore.IntegrationProviderSlack] || !providers["discord"] {
		t.Fatalf("listed channel providers = %#v, want slack and discord", providers)
	}
	if _, ok := secondBody["next_cursor"]; ok {
		t.Fatalf("unexpected cursor after final channel page: %#v", secondBody)
	}
}

func assertListedChannelShape(t *testing.T, channel map[string]any) {
	t.Helper()
	for _, field := range []string{"channel_id", "provider", "address_kind", "name", "state"} {
		if value, ok := channel[field].(string); !ok || value == "" {
			t.Fatalf("listed channel %s = %#v, want a non-empty string", field, channel[field])
		}
	}
	if channel["state"] != "active" {
		t.Fatalf("listed channel state = %#v, want active", channel["state"])
	}
	for _, field := range []string{"can_receive", "can_send"} {
		if value, ok := channel[field].(bool); !ok || !value {
			t.Fatalf("listed channel %s = %#v, want true", field, channel[field])
		}
	}
}

func TestChannelSendRequiresDestinationRegardlessOfOrigins(t *testing.T) {
	for _, originCount := range []int{0, 1, 2} {
		t.Run(strconv.Itoa(originCount), func(t *testing.T) {
			ctx := context.Background()
			fixture := newIntegrationToolFixtureWithConnectorOrigins(t,
				ctx,
				"channel-required-"+strconv.Itoa(originCount),
				originCount)
			call := fixture.recordToolCall(
				t, ctx, "call_send_channel_without_destination", toolcatalog.ToolNameSendChannelMessage,
				`{"message":{"text":"do not guess a destination"}}`, fixture.Now.Add(20*time.Second),
			)
			turn := fixture.turn()
			turn.Tools[toolcatalog.ToolNameSendChannelMessage] = ToolSpec{
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			}
			result, err := dispatchAsyncToolToTerminal(t, ctx,
				Executor{Store: fixture.Store, ChannelOperations: unexpectedChannelOperations(t),
					Now: func() time.Time { return fixture.Now.Add(21 * time.Second) }},
				turn, call,
			)
			if err != nil {
				t.Fatalf("dispatch channel send without destination: %v", err)
			}
			body := toolResultMapFromTestParts(t, result.ContentParts)
			if result.Disposition != DispatchCompleted || body["error_code"] != "malformed" {
				t.Fatalf("channel send without destination = %+v, disposition %v, want terminal malformed",
					body,
					result.Disposition)
			}
			var interactionCount int
			require.NoError(t, fixture.Pool.QueryRow(ctx,
				`SELECT count(*) FROM agent_interactions WHERE agent_id=$1`, fixture.Agent.ID).Scan(&interactionCount))
			require.Zero(t, interactionCount)

		})
	}
}

func TestChannelToolEligibilityUsesAllSendableBindingsInChannelMode(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "channel-tool-eligibility")
	eligibility, err := fixture.Store.Integrations().GetAgentChannelToolEligibility(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
	)
	if err != nil {
		t.Fatalf("get native-only channel tool eligibility: %v", err)
	}
	if !eligibility.List || !eligibility.Read || !eligibility.Send {
		t.Fatalf("registered Slack channel eligibility = %+v, want list/read/send", eligibility)
	}
	connector := createConnectorToolChannel(t, ctx, fixture, "channel-tool-eligibility")
	receiveOnlyBinding, err := fixture.Store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID,
			IntegrationInstallID: connector.Install.ID,
			IntegrationTargetID:  connector.Target.ID,
			IntegrationRouteID:   connector.Route.ID,
			ReceiveAllowed:       true, SendAllowed: false,
			Source: "test", Metadata: json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatalf("replace connector binding with receive-only binding: %v", err)
	}
	// All registered bindings contribute independently, including the managed
	// Slack destination and this second connector destination.
	sendable := createConnectorToolChannel(t, ctx, fixture, "channel-tool-other")
	eligibility, err = fixture.Store.Integrations().GetAgentChannelToolEligibility(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
	)
	if err != nil {
		t.Fatalf("get channel tool eligibility: %v", err)
	}
	if !eligibility.List || !eligibility.Send {
		t.Fatalf("multiple connector eligibility = %+v, want list and send", eligibility)
	}
	legacyBinding, err := fixture.Store.Integrations().GetActiveSendBindingForTarget(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		fixture.Target.ID,
	)
	if err != nil {
		t.Fatalf("load legacy Slack binding: %v", err)
	}
	if err := fixture.Store.Integrations().RevokeIntegrationTargetBinding(
		ctx,
		toolsTestProjectID,
		legacyBinding.ID,
	); err != nil {
		t.Fatalf("revoke legacy Slack binding: %v", err)
	}
	require.NoError(t, fixture.Store.Integrations().RevokeIntegrationTargetBinding(
		ctx, toolsTestProjectID, sendable.Binding.ID))
	eligibility, err = fixture.Store.Integrations().GetAgentChannelToolEligibility(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
	)
	if err != nil {
		t.Fatalf("get receive-only channel tool eligibility: %v", err)
	}
	if !eligibility.List || eligibility.Send {
		t.Fatalf("receive-only connector eligibility = %+v, want list-only", eligibility)
	}
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, connector.Target.ID)
	if err != nil {
		t.Fatalf("encode receive-only connector channel: %v", err)
	}
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_send_channel_receive_only",
		toolcatalog.ToolNameSendChannelMessage,
		`{"channel_id":"`+channelID+`","message":{"text":"must not send"}}`,
		fixture.Now.Add(20*time.Second),
	)
	turn := fixture.turn()
	turn.Tools[toolcatalog.ToolNameSendChannelMessage] = ToolSpec{
		Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
	}
	result, err := dispatchAsyncToolToTerminal(
		t,
		ctx,
		Executor{Store: fixture.Store, ChannelOperations: unexpectedChannelOperations(t),
			Now: func() time.Time { return fixture.Now.Add(21 * time.Second) }},
		turn,
		call,
	)
	if err != nil {
		t.Fatalf("dispatch receive-only connector send: %v", err)
	}
	body := toolResultMapFromTestParts(t, result.ContentParts)
	if body["code"] != "invalid_channel_request" {
		t.Fatalf("receive-only connector send = %+v, want invalid_channel_request", body)
	}
	sendBinding, err := fixture.Store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID,
			IntegrationInstallID: connector.Install.ID,
			IntegrationTargetID:  connector.Target.ID,
			IntegrationRouteID:   connector.Route.ID,
			ReceiveAllowed:       true, SendAllowed: true,
			Source: "test", Metadata: json.RawMessage(`{}`),
		},
	)
	if err != nil || sendBinding.ID == receiveOnlyBinding.ID {
		t.Fatalf("replace receive-only binding with send binding = %+v, %v", sendBinding, err)
	}
	eligibility, err = fixture.Store.Integrations().GetAgentChannelToolEligibility(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
	)
	if err != nil || !eligibility.List || !eligibility.Send {
		t.Fatalf("sendable connector eligibility = %+v, %v", eligibility, err)
	}
	if err := fixture.Store.Integrations().RevokeIntegrationTargetBinding(
		ctx,
		toolsTestProjectID,
		sendBinding.ID,
	); err != nil {
		t.Fatalf("detach connector channel: %v", err)
	}
	if err := fixture.Store.Integrations().RevokeIntegrationTargetBinding(
		ctx,
		toolsTestProjectID,
		sendBinding.ID,
	); err != nil {
		t.Fatalf("replay connector channel detach: %v", err)
	}
	if _, err := fixture.Store.Integrations().GetActiveSendBindingForTarget(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		sendBinding.IntegrationTargetID,
	); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("detached connector send binding error = %v, want not found", err)
	}
	eligibility, err = fixture.Store.Integrations().GetAgentChannelToolEligibility(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
	)
	if err != nil || eligibility.List || eligibility.Send {
		t.Fatalf("detached channel tool eligibility = %+v, %v", eligibility, err)
	}
	channels, err := fixture.Store.Integrations().ListAgentChannelTargets(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		integrationstore.ListAgentChannelTargetsInput{Limit: 10},
	)
	if err != nil || len(channels.Targets) != 0 {
		t.Fatalf("channels after detaching all bindings = %+v, %v", channels, err)
	}
}

func newIntegrationToolFixture(t *testing.T, ctx context.Context, label string) integrationToolFixture {
	return newIntegrationToolFixtureWithMCP(t, ctx, label, false)
}

func newIntegrationToolFixtureWithConnectorOrigins(
	t *testing.T,
	ctx context.Context,
	label string,
	count int,
) integrationToolFixture {
	return newIntegrationToolFixtureConfigured(t, ctx, label, toolFixtureOptions{}, count)
}

func newIntegrationToolFixtureWithMCP(
	t *testing.T,
	ctx context.Context,
	label string,
	withMCP bool,
	storeOptions ...storage.Option,
) integrationToolFixture {
	return newIntegrationToolFixtureWithOptions(t, ctx, label, toolFixtureOptions{withMCP: withMCP}, storeOptions...)
}

type toolFixtureOptions struct {
	withMCP       bool
	withSubagents bool
}

func newIntegrationToolFixtureWithOptions(
	t *testing.T,
	ctx context.Context,
	label string,
	options toolFixtureOptions,
	storeOptions ...storage.Option,
) integrationToolFixture {
	return newIntegrationToolFixtureConfigured(t, ctx, label, options, 0, storeOptions...)
}

func newIntegrationToolFixtureConfigured(
	t *testing.T,
	ctx context.Context,
	label string,
	fixtureOptions toolFixtureOptions,
	connectorOriginCount int,
	storeOptions ...storage.Option,
) integrationToolFixture {
	t.Helper()
	withMCP := fixtureOptions.withMCP
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	options := []storage.Option{
		storage.WithSecretKeyWrapper(integrationToolKeyWrapper(t)),
		storage.WithMachinePoolProviders(toolsTestMachinePoolProviders{}),
	}
	options = append(options, storeOptions...)
	store := storage.NewStore(pool, options...)
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		pool,
		storagetest.CreateVerifiedUserInput{
			Email:       "tools-integration-" + label + "@example.com",
			DisplayName: "Tools Integration " + label,
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`
INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at)
VALUES ($1, 'Tools Integration Org', $2, $3, $3)
`,
		toolsTestOrgID,
		"tools-integration-org-"+label,
		now,
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`
INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
VALUES ($1, $2, 'Tools Integration Project', $3, $4, $4)
`,
		toolsTestProjectID,
		toolsTestOrgID,
		"tools-integration-project-"+label,
		now,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	ensureIntegrationToolsProjectAdmin(t, ctx, store, user.ID, now)

	profile := createIntegrationToolProfile(t, ctx, store, user.ID, label, fixtureOptions)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      toolsTestProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     toolsTestUserPrincipal(user.ID),
			IdempotencyKey: "tools-integration-launch-" + label,
		},
	)
	if err != nil {
		t.Fatalf("launch integration tool agent: %v", err)
	}
	agent := launch.Agent
	install, definition := createIntegrationToolInstall(t, ctx, store, user.ID, label, now.Add(3*time.Second))
	target, err := store.Integrations().CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID:            toolsTestProjectID,
			ChannelDefinitionID:  definition.ID,
			IntegrationInstallID: install.ID,
			ProviderRef:          "C123:111.222",
			ProviderRefKind:      "thread",
		},
	)
	if err != nil {
		t.Fatalf("create integration target: %v", err)
	}
	bindIntegrationToolTarget(t, ctx, store, agent.ID, target)
	if err := seedIntegrationToolCurrentChannel(
		ctx,
		pool,
		toolsTestProjectID,
		agent.ID,
		target.ID,
	); err != nil {
		t.Fatalf("set integration target: %v", err)
	}
	var originChannel connectorToolChannel
	var originChannels []connectorToolChannel
	var inputIDs []uuid.UUID
	if connectorOriginCount > 0 {
		originChannels = make([]connectorToolChannel, 0, connectorOriginCount)
		deliveryMode := executionstore.DeliveryModeQueued
		if connectorOriginCount > 1 {
			deliveryMode = executionstore.DeliveryModeSteering
		}
		for index := range connectorOriginCount {
			suffix := label + "-origin-" + strconv.Itoa(index)
			originChannel = createConnectorToolChannel(t, ctx, integrationToolFixture{
				Store: store, User: user, Agent: agent,
			}, suffix)
			// The send/context fixture starts from a resolved receive binding;
			// provider receipt authorization is exercised by workflow tests.
			actorDisplayName := "Connector User"
			input, _, _, createErr := store.Execution().CreateAgentContentInput(
				ctx,
				executionstore.CreateAgentContentInputInput{
					ProjectID:                  toolsTestProjectID,
					IntegrationTargetID:        originChannel.Target.ID,
					IntegrationTargetBindingID: originChannel.Binding.ID,
					AgentID:                    agent.ID,
					Actor: &executionstore.ActorParams{
						Provider:         originChannel.Install.Provider,
						ProviderTenantID: originChannel.Install.ProviderTenantID,
						ProviderUserID:   "connector-user-" + suffix,
						DisplayName:      &actorDisplayName,
					},
					ContentBlocks:    json.RawMessage(`[{"type":"text","text":"send an integration reply"}]`),
					Metadata:         json.RawMessage(`{}`),
					DeliveryMode:     deliveryMode,
					IdempotencyScope: integrationstore.IdempotencyScope(originChannel.Install),
					IdempotencyKey:   "tools-integration-input-" + suffix,
				},
			)
			if createErr != nil {
				t.Fatalf("create connector agent input: %v", createErr)
			}
			originChannels = append(originChannels, originChannel)
			inputIDs = append(inputIDs, input.ID)
		}
	} else {
		var producer *executionstore.ActorParams
		producer, err = executionstore.OmnaraActorParams(
			toolsTestOrgID,
			toolsTestUserPrincipal(user.ID),
		)
		if err == nil {
			input, _, _, createErr := store.Execution().CreateAgentContentInput(
				ctx,
				executionstore.CreateAgentContentInputInput{
					ProjectID:      toolsTestProjectID,
					AgentID:        agent.ID,
					Actor:          producer,
					ContentBlocks:  json.RawMessage(`[{"type":"text","text":"send an integration reply"}]`),
					IdempotencyKey: "tools-integration-input-" + label,
				},
			)
			err = createErr
			if createErr == nil {
				inputIDs = append(inputIDs, input.ID)
			}
		}
	}
	if err != nil {
		t.Fatalf("create agent input: %v", err)
	}
	claim, found, err := store.Execution().ClaimNextAgentWork(ctx, toolsTestClaimInput())
	if err != nil {
		t.Fatalf("claim input work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel ||
		len(claim.Model.AdmittedInputTurn.Inputs) != len(inputIDs) {
		t.Fatalf(
			"claim input found=%v executable=%v input=%+v want %v",
			found,
			claim.Kind == executionstore.AgentWorkModel,
			claim.Model.AdmittedInputTurn.Inputs,
			inputIDs,
		)
	}
	lock := claim.RuntimeLock
	admitted := claim.Model.AdmittedInputTurn
	modelCall := claimNormalModelCallForToolsTest(
		t,
		ctx,
		store,
		toolsTestProjectID,
		agent.ID,
		lock,
		inputIDs,
		launch.AgentConfig.ID,
		admitted.Events[len(admitted.Events)-1].Sequence,
		uuid.Nil,
	)
	return integrationToolFixture{
		Pool:               pool,
		Store:              store,
		User:               user,
		Profile:            profile,
		Agent:              agent,
		AgentConfig:        launch.AgentConfig,
		Lock:               lock,
		TurnID:             admitted.Turn.ID,
		ModelCallContextID: modelCall.Context.ID,
		Install:            install,
		Target:             target,
		OriginChannel:      originChannel,
		OriginChannels:     originChannels,
		Now:                now,
		WithMCP:            withMCP,
	}
}

func (f *integrationToolFixture) turn() Turn {
	turn := Turn{
		ProjectID:          toolsTestProjectID,
		AgentID:            f.Agent.ID,
		TurnID:             f.TurnID,
		SourceEventID:      f.ModelOutputEventID,
		RuntimeLockID:      f.Lock.ID,
		ModelCallContextID: f.ModelCallContextID,
		Tools: map[string]ToolSpec{
			"ask_question": {
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			},
		},
	}
	if f.WithMCP {
		turn.Tools[toolcatalog.MCPRuntimeToolName("docs", "greet")] = ToolSpec{
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Permission:  toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			Type:        toolcatalog.ToolTypeMCP,
		}
	}
	return turn
}

func (f *integrationToolFixture) recordToolCall(
	t *testing.T,
	ctx context.Context,
	providerCallID, name, rawInput string,
	at time.Time,
) model.ToolCall {
	t.Helper()
	call := model.ToolCall{ID: providerCallID, Name: name, Input: json.RawMessage(rawInput)}
	f.recordToolCalls(t, ctx, []model.ToolCall{call}, at)
	return call
}

func (f *integrationToolFixture) recordPendingToolCall(
	t *testing.T,
	ctx context.Context,
	providerCallID, name, rawInput string,
	at time.Time,
) model.ToolCall {
	t.Helper()
	call := model.ToolCall{ID: providerCallID, Name: name, Input: json.RawMessage(rawInput)}
	f.recordPendingToolCalls(t, ctx, []model.ToolCall{call}, at)
	return call
}

func (f *integrationToolFixture) recordToolCalls(
	t *testing.T,
	ctx context.Context,
	calls []model.ToolCall,
	at time.Time,
) {
	t.Helper()
	f.recordPendingToolCalls(t, ctx, calls, at)
	for _, call := range calls {
		if _, err := f.Store.Execution().MarkToolCallReady(
			ctx,
			executionstore.MarkToolCallReadyInput{
				ProjectID:     toolsTestProjectID,
				AgentID:       f.Agent.ID,
				ID:            f.toolCallID(t, ctx, call.ID),
				RuntimeLockID: f.Lock.ID,
			},
		); err != nil {
			t.Fatalf("mark tool call %s allowed: %v", call.ID, err)
		}
	}
}

func (f *integrationToolFixture) recordPendingToolCalls(
	t *testing.T,
	ctx context.Context,
	calls []model.ToolCall,
	at time.Time,
) {
	t.Helper()
	if f.ModelOutputEventID != uuid.Nil {
		t.Fatal("integration tool fixture already published its complete tool proposal batch")
	}
	if len(calls) == 0 {
		t.Fatal("integration tool fixture requires at least one tool proposal")
	}
	bindings := make([]executionstore.ToolCallBindingInput, 0, len(calls))
	for _, call := range calls {
		toolType := toolcatalog.ToolTypeBuiltIn
		if toolcatalog.IsMCPRuntimeToolName(call.Name) {
			toolType = toolcatalog.ToolTypeMCP
		}
		bindings = append(bindings, executionstore.ToolCallBindingInput{
			ProviderCallID: call.ID,
			Type:           toolType,
		})
	}
	providerResponse, err := model.NewResponseEnvelopeForStorage(
		"gpt-test",
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		model.Response{
			ID:         "resp_tools_integration_" + f.ModelCallContextID.String(),
			StopReason: model.StopReasonToolUse,
			Content:    modeltest.ResponsePartsForToolCalls(calls),
		},
	)
	if err != nil {
		t.Fatalf("build integration tool provider response: %v", err)
	}
	modelOutputEvent, records, err := f.Store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          toolsTestProjectID,
			AgentID:            f.Agent.ID,
			ModelCallContextID: f.ModelCallContextID,
			RuntimeLockID:      f.Lock.ID,
			ProviderResponse:   providerResponse,
			ToolCallBindings:   bindings,
		},
	)
	if err != nil {
		t.Fatalf("record integration tool proposal batch: %v", err)
	}
	if len(records) != len(calls) {
		t.Fatalf("recorded integration tool calls = %d, want %d", len(records), len(calls))
	}
	f.ModelOutputEventID = modelOutputEvent.ID
}

func (f *integrationToolFixture) toolCallID(t *testing.T, ctx context.Context, providerCallID string) uuid.UUID {
	t.Helper()
	record, found, err := f.Store.Execution().GetToolCallByProviderCall(
		ctx,
		toolsTestProjectID,
		f.Agent.ID,
		f.ModelCallContextID,
		providerCallID,
	)
	if err != nil {
		t.Fatalf("get tool call %s: %v", providerCallID, err)
	}
	if !found {
		t.Fatalf("tool call %s not found", providerCallID)
	}
	return record.ID
}

func createIntegrationToolProfile(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID uuid.UUID,
	label string,
	fixtureOptions toolFixtureOptions,
) executionstore.AgentProfileRecord {
	t.Helper()
	withMCP := fixtureOptions.withMCP
	sourceYAML := `instruction: Reply to users.
model:
  provider_config: openai-prod
  name: gpt-test
tools:
  run_command:
    permission:
      mode: always_allow
      parameters: {}
`
	if withMCP {
		sourceYAML += `mcp:
  docs:
    url: https://example.com/mcp
    permission:
      mode: always_allow
      parameters: {}
`
	}
	if fixtureOptions.withSubagents {
		sourceYAML += `subagents:
  fork:
    type: self
    instruction:
      append: You are a fork.
`
	}
	compiled := compileToolsAgentYAMLResolved(t, ctx, store, userID, sourceYAML)
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               toolsTestProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create integration tool config: %v", err)
	}
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       toolsTestProjectID,
		Name:            "Integration Tool Agent " + label,
		CurrentConfigID: config.ID,
		IdempotencyKey:  "tools-integration-profile-" + label,
	})
	if err != nil {
		t.Fatalf("create integration tool profile: %v", err)
	}
	return profile
}

func compileToolsAgentYAMLResolved(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID uuid.UUID,
	sourceYAML string,
) agentconfig.Result {
	t.Helper()
	source, err := agentconfig.ParseSource(agentconfig.SourceFormatYAML, []byte(sourceYAML))
	if err != nil {
		t.Fatalf("parse agent config source: %v", err)
	}
	provider := storagefixture.EnsureModelProvider(t, ctx, store.Models(), store.Secrets(),
		storagefixture.ModelProviderInput{OrgID: toolsTestOrgID, UserID: userID, Name: source.Model.ProviderConfig})
	configuredModel := storagefixture.EnsureModelAccess(t, ctx, store.Models(), toolsTestProjectID,
		storagefixture.DefaultModelInput(toolsTestOrgID, provider.ID, source.Model.Name))
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(sourceYAML), agentconfig.CompileOptions{
		ResolveModelSelection: func(
			providerConfigName string,
			configuredModelName string,
		) (agentconfig.ResolvedModelSelection, error) {
			return resolvedToolsAgentConfigModel(configuredModel), nil
		},
		ResolveMachineName: func(machineName string) (string, error) {
			machineID, err := store.Execution().ResolveAgentConfigMachineName(ctx, toolsTestProjectID, machineName)
			if err != nil {
				return "", err
			}
			return publicid.Encode(publicid.KindMachine, machineID)
		},
		ResolveMachinePoolName: func(machinePoolName string) (string, error) {
			machinePoolID, err := store.Execution().ResolveAgentConfigMachinePoolName(
				ctx,
				toolsTestOrgID,
				toolsTestProjectID,
				machinePoolName,
			)
			if err != nil {
				return "", err
			}
			return publicid.Encode(publicid.KindMachinePool, machinePoolID)
		},
		ResolveSkillID: func(skillID string) (agentconfig.SkillResolution, error) {
			records, _, err := store.Skills().GetSkillsByIDsForCompile(ctx, skillstore.GetSkillsByIDsInput{
				OrgID:     toolsTestOrgID,
				ProjectID: toolsTestProjectID,
				IDs:       []string{skillID},
			})
			if err != nil {
				return agentconfig.SkillResolution{}, err
			}
			if len(records) != 1 {
				return agentconfig.SkillResolution{}, storeerr.ErrNotFound
			}
			return agentconfig.SkillResolution{
				PublicID: skillID,
				Name:     records[0].Name,
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("compile resolved agent config: %v", err)
	}
	return compiled
}

func resolvedToolsAgentConfigModel(
	configuredModel modelstore.ConfiguredModelRecord,
) agentconfig.ResolvedModelSelection {
	supportsTools := configuredModel.SupportsTools
	return agentconfig.ResolvedModelSelection{
		ConfiguredModelID: configuredModel.ID.String(),
		SupportsTools:     &supportsTools,
	}
}

func parseConfiguredModelID(t *testing.T, compiled agentconfig.Result) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(compiled.Compiled.Model.ConfiguredModelID)
	if err != nil {
		t.Fatalf("parse compiled configured model id: %v", err)
	}
	return id
}

func createIntegrationToolInstall(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID uuid.UUID, label string,
	now time.Time,
) (integrationstore.IntegrationInstallRecord, integrationstore.ChannelDefinition) {
	t.Helper()
	secretID := createIntegrationToolSecrets(t, ctx, store, userID, label, now)
	app, err := store.Integrations().CreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
		OrgID: toolsTestOrgID, OwnerProjectID: toolsTestProjectID,
		Provider: integrationstore.IntegrationProviderSlack, ProviderAppRef: "A123",
		DisplayName: "Slack test app", ConnectorKey: channelconnector.BuiltInConnectorKey,
		InstallationCredentialKind: string(secrets.KindSlackAppCredentials),
		CredentialSecretID:         secretID, ProviderConfig: json.RawMessage(`{}`), ProviderMetadata: json.RawMessage(`{}`),
		State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	install, err := store.Integrations().UpsertIntegrationInstall(ctx, integrationstore.UpsertIntegrationInstallInput{
		OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, IntegrationAppID: app.ID,
		InstalledBy: identitystore.NewUserPrincipal(userID),
		Provider:    integrationstore.IntegrationProviderSlack, IntegrationKind: integrationstore.IntegrationKindManaged,
		ConnectionMode: slack.ConnectionModeWebhook, State: integrationstore.IntegrationInstallStateActive,
		ProviderTenantID: "T123", ProviderAccountRef: "A123", CredentialSecretID: secretID,
		ProviderIdentity: json.RawMessage(`{"bot_user_id":"B123"}`), Metadata: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	definition, err := store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: toolsTestProjectID, IntegrationInstallID: install.ID,
			ImplementationKey: "slack-thread", Kind: integrationstore.ChannelKindSlackThread,
			SendParamsSchema: json.RawMessage(`{"type":"object"}`),
			Capabilities: integrationstore.ChannelCapabilities{
				Read: true, Send: true, Text: true, Artifacts: true, Permissions: true, Questions: true,
			},
			ConnectorCapabilities: []channelconnector.Capability{{
				ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: integrationstore.IntegrationProviderSlack,
			}},
		})
	require.NoError(t, err)
	return install, definition
}

func createConnectorToolChannel(
	t *testing.T,
	ctx context.Context,
	fixture integrationToolFixture,
	label string,
) connectorToolChannel {
	t.Helper()
	app, err := fixture.Store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: toolsTestOrgID, OwnerProjectID: toolsTestProjectID,
			Provider: "discord", ProviderAppRef: "discord-app-" + label,
			DisplayName: "Discord " + label, ConnectorKey: channelconnector.BuiltInConnectorKey,
			ProviderConfig: json.RawMessage(`{}`), ProviderMetadata: json.RawMessage(`{}`),
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create connector app: %v", err)
	}
	install, err := fixture.Store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, IntegrationAppID: app.ID,
			InstalledBy: identitystore.NewUserPrincipal(fixture.User.ID),
			Provider:    "discord", IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "webhook",
			State:            integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "guild-" + label, ProviderAccountRef: "bot-" + label,
			ProviderConfig: json.RawMessage(`{}`), ProviderIdentity: json.RawMessage(`{}`),
			Metadata: json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatalf("create connector install: %v", err)
	}
	route, err := fixture.Store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID:            toolsTestProjectID,
			IntegrationInstallID: install.ID, DeploymentKey: "single-agent-channel-" + label,
			BehaviorKey:   "single_agent_channel",
			Configuration: json.RawMessage(`{}`), State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create connector route: %v", err)
	}
	definition, err := fixture.Store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: toolsTestProjectID, IntegrationInstallID: install.ID,
			ImplementationKey: "test-channel", Kind: integrationstore.ChannelKindExternal,
			SendParamsSchema: json.RawMessage(`{"type":"object"}`),
			Capabilities: integrationstore.ChannelCapabilities{Read: true,
				Send:        true,
				Text:        true,
				Permissions: true,
				Questions:   true},
			ConnectorCapabilities: []channelconnector.Capability{{ConnectorKey: app.ConnectorKey, Provider: app.Provider}},
		})
	if err != nil {
		t.Fatalf("publish connector channel definition: %v", err)
	}
	target, err := fixture.Store.Integrations().CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID:            toolsTestProjectID,
			ChannelDefinitionID:  definition.ID,
			IntegrationInstallID: install.ID, ProviderRef: "channel-" + label,
			ProviderRefKind: "channel", DisplayName: "Connector channel " + label,
		},
	)
	if err != nil {
		t.Fatalf("create connector target: %v", err)
	}
	binding, err := fixture.Store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: true,
			Source: "test", Metadata: json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatalf("create connector binding: %v", err)
	}
	return connectorToolChannel{App: app, Install: install, Route: route, Target: target, Binding: binding}
}

func createIntegrationToolSecrets(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID uuid.UUID,
	label string,
	now time.Time,
) uuid.UUID {
	t.Helper()
	secret, _, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:          toolsTestOrgID,
			OwnerKind:      secretstore.SecretOwnerProject,
			OwnerProjectID: toolsTestProjectID,
			Name:           "tools-integration-" + label + "-credentials",
			Material: secrets.SlackAppCredentialsMaterial{
				AccessToken:   "xoxb-test",
				ClientID:      "client-id",
				ClientSecret:  "client-secret",
				SigningSecret: "signing-secret",
			},
			Actor: toolsTestUserPrincipal(userID),
		},
	)
	if err != nil {
		t.Fatalf("create integration credential secret: %v", err)
	}
	return secret.ID
}

func integrationToolKeyWrapper(t *testing.T) secrets.KeyWrapper {
	t.Helper()
	wrapper, err := secrets.NewLocalKeyWrapper(
		"test-key",
		map[string][]byte{"test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("create test key wrapper: %v", err)
	}
	return wrapper
}

func dispatchToolAndDrainAsync(
	t *testing.T,
	ctx context.Context,
	executor Executor,
	turn Turn,
	call model.ToolCall,
) (Result, error) {
	t.Helper()
	scope := NewAsyncExecutionScope(nil)
	result, err := executor.Dispatch(WithAsyncExecutionScope(ctx, scope), turn, call)
	scope.Seal()
	select {
	case <-scope.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for async tool dispatch")
	}
	if err != nil {
		return result, err
	}
	return result, scope.Err()
}

func dispatchAsyncToolToTerminal(
	t *testing.T,
	ctx context.Context,
	executor Executor,
	turn Turn,
	call model.ToolCall,
) (Result, error) {
	t.Helper()
	result, err := dispatchToolAndDrainAsync(t, ctx, executor, turn, call)
	if err != nil || result.Disposition != DispatchDeferred {
		return result, err
	}
	return executor.Dispatch(ctx, turn, call)
}

func toolResultMapFromTestParts(t *testing.T, parts json.RawMessage) map[string]any {
	t.Helper()
	var decoded []struct {
		Type  string         `json:"type"`
		Value map[string]any `json:"value"`
	}
	if err := json.Unmarshal(parts, &decoded); err != nil {
		t.Fatalf("decode result parts: %v raw=%s", err, parts)
	}
	for _, part := range decoded {
		if part.Type == "structured_data" {
			return part.Value
		}
	}
	t.Fatalf("missing structured result in %s", parts)
	return nil
}

func ensureIntegrationToolsProjectAdmin(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID uuid.UUID,
	now time.Time,
) {
	t.Helper()
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: toolsTestOrgID, UserID: userID, Role: "admin"},
	); err != nil {
		t.Fatalf("add integration tools org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     toolsTestOrgID,
			ProjectID: toolsTestProjectID,
			UserID:    userID,
			Role:      "admin",
		},
	); err != nil {
		t.Fatalf("add integration tools project membership: %v", err)
	}
}
