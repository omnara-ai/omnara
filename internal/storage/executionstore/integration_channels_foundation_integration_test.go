//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type expiringChannelLaunchStore struct {
	*executionstore.Store
	beforeRuntimeLaunch func()
}

type failOnceBoundChannelInputStore struct {
	*executionstore.Store
	createCalls int
	failAt      int
}

func (s *failOnceBoundChannelInputStore) CreateBoundIntegrationTargetContentInput(
	ctx context.Context,
	input executionstore.CreateBoundIntegrationTargetContentInput,
) (executionstore.CreateBoundIntegrationTargetContentResult, error) {
	s.createCalls++
	if s.createCalls == s.failAt {
		return executionstore.CreateBoundIntegrationTargetContentResult{}, storeerr.ErrStateTransitionConflict
	}
	return s.Store.CreateBoundIntegrationTargetContentInput(ctx, input)
}

type blockingDeleteInstallAccess struct {
	reachedClear  chan struct{}
	continueClear chan struct{}
}

func (a *blockingDeleteInstallAccess) ValidateInstallBinding(
	ctx context.Context,
	tx pgx.Tx,
	binding integrationstore.InstallBinding,
) error {
	return (executionstore.IntegrationInstallAccess{}).ValidateInstallBinding(ctx, tx, binding)
}

func (a *blockingDeleteInstallAccess) ClearInstallTargetsFromAgents(
	ctx context.Context,
	tx pgx.Tx,
	projectID, installID ID,
) error {
	close(a.reachedClear)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.continueClear:
	}
	return (executionstore.IntegrationInstallAccess{}).ClearInstallTargetsFromAgents(
		ctx,
		tx,
		projectID,
		installID,
	)
}

func (s *expiringChannelLaunchStore) LaunchAgentWithIntegrationRuntimeLease(
	ctx context.Context,
	input executionstore.LaunchAgentInput,
	integrationInstallID executionstore.ID,
	proof *executionstore.IntegrationRuntimeLeaseProof,
) (executionstore.LaunchAgentResult, error) {
	if s.beforeRuntimeLaunch != nil {
		s.beforeRuntimeLaunch()
		s.beforeRuntimeLaunch = nil
	}
	return s.Store.LaunchAgentWithIntegrationRuntimeLease(
		ctx,
		input,
		integrationInstallID,
		proof,
	)
}

const (
	testChannelConnector = "chat_sdk_v1"
	testChannelProvider  = "discord"
	testChannelHandler   = "test_channel_single_agent"
)

func testChannelCapabilities(provider string) []channelconnector.Capability {
	return []channelconnector.Capability{testChannelCapability(provider)}
}

func testChannelCapability(provider string) channelconnector.Capability {
	return channelconnector.Capability{ConnectorKey: testChannelConnector, Provider: provider}
}

func TestChannelFoundationInboundFanoutAndDeliveryJourney(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "channel-foundation@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "channel-foundation-profile")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "channel-foundation-agent")

	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: testChannelProvider, ProviderAppRef: "discord-app-1",
			DisplayName: "Discord test app", ConnectorKey: testChannelConnector,
			ProviderConfig:   json.RawMessage(`{"intents":["messages"]}`),
			ProviderMetadata: json.RawMessage(`{"environment":"test"}`),
			State:            integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create channel app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
			InstalledByUserID: admin.ID,
			Provider:          testChannelProvider, IntegrationKind: "channel_single_agent",
			ConnectionMode: "gateway", State: integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "guild-1", ProviderAccountRef: "bot-1",
			ProviderAgentDisplayName: "Omnara test bot",
		},
	)
	if err != nil {
		t.Fatalf("create connector install: %v", err)
	}
	if install.IntegrationAppID != app.ID || install.CredentialSecretID != NilID {
		t.Fatalf("connector install = %+v", install)
	}
	compatibilityRoutes, err := store.Integrations().ListActiveIntegrationRoutes(
		ctx,
		testProjectID,
		install.ID,
	)
	if err != nil {
		t.Fatalf("list connector compatibility routes: %v", err)
	}
	if len(compatibilityRoutes) != 0 {
		t.Fatalf("connector install gained native compatibility routes: %+v", compatibilityRoutes)
	}

	routes := make([]integrationstore.IntegrationRouteRecord, 0, 2)
	for index := range 2 {
		route, err := store.Integrations().CreateIntegrationRoute(
			ctx,
			integrationstore.CreateIntegrationRouteInput{
				ProjectID: testProjectID, IntegrationInstallID: install.ID,
				DeploymentKey: "test-route-" + string(rune('a'+index)),
				HandlerKey:    testChannelHandler, HandlerVersion: 1,
				Configuration: json.RawMessage(`{"respond_to":"all"}`), State: integrationstore.IntegrationRouteStateActive,
			},
		)
		if err != nil {
			t.Fatalf("create connector route %d: %v", index, err)
		}
		routes = append(routes, route)
	}

	service := integration.NewChannelService(
		store.Execution(),
		store.Integrations(),
		integration.ChannelRouteHandlers{
			integration.ChannelRouteHandlerKey(testChannelHandler, 1): integration.ChannelRouteHandlerFunc(
				func(
					_ context.Context,
					_ integration.ChannelRouteContext,
					envelope integration.ChannelInboundEnvelope,
				) (integration.ChannelRouteDecision, error) {
					return integration.ChannelRouteDecision{
						Accept: true, ProviderRef: envelope.Conversation.Ref,
						ProviderRefKind: "thread", DisplayName: envelope.Conversation.DisplayName,
						DeliveryMode: executionstore.DeliveryModeQueued,
						Attachments: []integration.ChannelAttachmentAction{{
							AgentID: agent.ID, SendAllowed: true,
							Metadata: json.RawMessage(`{"behavior":"all_messages"}`),
						}},
					}, nil
				},
			),
		},
	)
	envelope := integration.ChannelInboundEnvelope{
		Version: integration.ChannelEnvelopeVersionV1, ProviderEventID: "discord-event-1",
		ExternalTenantID: "guild-1", ExternalAccountRef: "bot-1", EventType: "message.created",
		Conversation: integration.ChannelConversation{
			Ref: "thread-1", Kind: "thread", DisplayName: "support thread",
			ParentRef: "channel-1", Metadata: json.RawMessage(`{"channel_name":"support"}`),
		},
		Actor: integration.ChannelActor{
			Ref: "user-1", DisplayName: "Customer One",
			Metadata: json.RawMessage(`{"role":"member"}`),
		},
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
		OccurredAt:    time.Now().UTC(), Metadata: json.RawMessage(`{"raw_type":0}`),
	}
	process := func() integration.ProcessChannelInboundResult {
		result, err := service.ProcessInbound(ctx, integration.ProcessChannelInboundInput{
			IntegrationAppID: app.ID, Capabilities: testChannelCapabilities(testChannelProvider),
			Envelope:       envelope,
			PrepareContent: passthroughChannelInboundContent,
		})
		if err != nil {
			t.Fatalf("process connector inbound: %v", err)
		}
		return result
	}
	first := process()
	if first.IgnoredRoutes != 0 || len(first.Accepted) != len(routes) {
		t.Fatalf("first connector acceptance = %+v", first)
	}
	if first.Accepted[0].AgentInputID == first.Accepted[1].AgentInputID ||
		first.Accepted[0].BindingID == first.Accepted[1].BindingID {
		t.Fatalf("intentional route fanout collapsed: %+v", first.Accepted)
	}
	second := process()
	for index := range first.Accepted {
		if second.Accepted[index].AgentInputID != first.Accepted[index].AgentInputID ||
			second.Accepted[index].BindingID != first.Accepted[index].BindingID {
			t.Fatalf("route %d replay was not idempotent: first=%+v second=%+v", index, first, second)
		}
	}
	concurrentEnvelope := envelope
	concurrentEnvelope.ProviderEventID = "discord-event-concurrent"
	concurrentEnvelope.Conversation.Ref = "thread-concurrent"
	type processResult struct {
		result integration.ProcessChannelInboundResult
		err    error
	}
	start := make(chan struct{})
	concurrent := make(chan processResult, 2)
	for range 2 {
		go func() {
			<-start
			result, err := service.ProcessInbound(context.Background(), integration.ProcessChannelInboundInput{
				IntegrationAppID: app.ID, Capabilities: testChannelCapabilities(testChannelProvider),
				Envelope: concurrentEnvelope, PrepareContent: passthroughChannelInboundContent,
			})
			concurrent <- processResult{result: result, err: err}
		}()
	}
	close(start)
	concurrentByRoute := make([]map[integrationstore.ID]integration.ChannelInboundAcceptance, 0, 2)
	for range 2 {
		processed := <-concurrent
		if processed.err != nil || len(processed.result.Accepted) != len(routes) {
			t.Fatalf("concurrent inbound replay = %+v, %v", processed.result, processed.err)
		}
		byRoute := make(map[integrationstore.ID]integration.ChannelInboundAcceptance, len(routes))
		for _, acceptance := range processed.result.Accepted {
			byRoute[acceptance.RouteID] = acceptance
		}
		concurrentByRoute = append(concurrentByRoute, byRoute)
	}
	for _, route := range routes {
		left, leftFound := concurrentByRoute[0][route.ID]
		right, rightFound := concurrentByRoute[1][route.ID]
		if !leftFound || !rightFound || left.AgentInputID != right.AgentInputID ||
			left.BindingID != right.BindingID || left.TargetID != right.TargetID {
			t.Fatalf("concurrent route %s replay diverged: left=%+v right=%+v", route.ID, left, right)
		}
	}

	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_routes SET state = 'disabled' WHERE id = $1`,
		routes[0].ID,
	); err != nil {
		t.Fatalf("disable inbound route: %v", err)
	}
	disabledEnvelope := envelope
	disabledEnvelope.ProviderEventID = "discord-event-disabled-route"
	disabledEnvelope.Conversation.Ref = "thread-disabled-route"
	disabledResult, err := service.ProcessInbound(ctx, integration.ProcessChannelInboundInput{
		IntegrationAppID: app.ID, Capabilities: testChannelCapabilities(testChannelProvider),
		Envelope: disabledEnvelope, PrepareContent: passthroughChannelInboundContent,
	})
	if err != nil || len(disabledResult.Accepted) != 1 ||
		disabledResult.Accepted[0].RouteID != routes[1].ID {
		t.Fatalf("disabled inbound route result = %+v, %v", disabledResult, err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_routes SET state = 'active' WHERE id = $1`,
		routes[0].ID,
	); err != nil {
		t.Fatalf("re-enable inbound route: %v", err)
	}

	for _, acceptance := range first.Accepted {
		var actorProvider string
		var targetID, bindingID uuid.UUID
		var inputMetadata json.RawMessage
		if err := pool.QueryRow(
			ctx,
			`SELECT actor.provider, input.integration_target_id, input.integration_target_binding_id,
			        input.metadata
			 FROM agent_inputs input
			 JOIN actors actor ON actor.project_id = input.project_id AND actor.id = input.actor_id
			 WHERE input.project_id = $1 AND input.id = $2`,
			testProjectID,
			acceptance.AgentInputID,
		).Scan(&actorProvider, &targetID, &bindingID, &inputMetadata); err != nil {
			t.Fatalf("load accepted channel input: %v", err)
		}
		if actorProvider != testChannelProvider || targetID != uuid.UUID(acceptance.TargetID) ||
			bindingID != uuid.UUID(acceptance.BindingID) {
			t.Fatalf(
				"accepted input provenance provider=%q target=%s binding=%s, want %+v",
				actorProvider,
				targetID,
				bindingID,
				acceptance,
			)
		}
		var metadata map[string]json.RawMessage
		if err := json.Unmarshal(inputMetadata, &metadata); err != nil {
			t.Fatalf("decode accepted channel input metadata: %v", err)
		}
		if !sameJSON(metadata["binding_metadata"], json.RawMessage(`{"behavior":"all_messages"}`)) {
			t.Fatalf("accepted input binding metadata = %s", metadata["binding_metadata"])
		}
		if _, legacyName := metadata["route_metadata"]; legacyName {
			t.Fatalf("accepted input metadata retained misleading route_metadata: %s", inputMetadata)
		}
	}
	backlog, err := store.Execution().ListQueuedBacklogInputs(
		ctx,
		executionstore.ListQueuedBacklogInputsInput{
			ProjectID: testProjectID,
			AgentID:   agent.ID,
			Limit:     len(first.Accepted),
		},
	)
	if err != nil {
		t.Fatalf("list channel input backlog: %v", err)
	}
	if len(backlog.Inputs) != len(first.Accepted) {
		t.Fatalf("channel input backlog = %+v, want %d inputs", backlog, len(first.Accepted))
	}
	wantBindings := make(map[executionstore.ID]bool, len(first.Accepted))
	for _, acceptance := range first.Accepted {
		wantBindings[acceptance.BindingID] = false
	}
	for _, input := range backlog.Inputs {
		if _, found := wantBindings[input.IntegrationTargetBindingID]; !found {
			t.Fatalf("backlog input lost channel binding provenance: %+v", input)
		}
		wantBindings[input.IntegrationTargetBindingID] = true
	}
	for bindingID, found := range wantBindings {
		if !found {
			t.Fatalf("channel binding %s missing from backlog records", bindingID)
		}
	}
	unchangedAgent, err := store.Execution().GetAgentInProject(ctx, testProjectID, agent.ID)
	if err != nil {
		t.Fatalf("load connector-bound agent: %v", err)
	}
	if unchangedAgent.IntegrationTargetID != NilID {
		t.Fatalf("connector input changed legacy sticky target to %s", unchangedAgent.IntegrationTargetID)
	}
	channelPage, err := store.Integrations().ListAgentChannelTargets(
		ctx,
		testProjectID,
		agent.ID,
		integrationstore.ListAgentChannelTargetsInput{Limit: 10},
	)
	if err != nil {
		t.Fatalf("list agent channels: %v", err)
	}
	channels := channelPage.Targets
	if len(channels) != 3 {
		t.Fatalf("agent channels = %+v", channels)
	}
	foundOriginalChannel := false
	for _, channel := range channels {
		if !channel.ReceiveAllowed || !channel.SendAllowed ||
			channel.Provider != testChannelProvider || channel.ConnectorKey != testChannelConnector {
			t.Fatalf("agent channel = %+v", channel)
		}
		if channel.ProviderRef == envelope.Conversation.Ref {
			foundOriginalChannel = true
		}
	}
	if !foundOriginalChannel {
		t.Fatalf("original channel missing from %+v", channels)
	}

	deliveryInput := integrationstore.CreateIntegrationDeliveryInput{
		ProjectID: testProjectID, AgentID: agent.ID,
		IntegrationTargetBindingID: first.Accepted[0].BindingID,
		Transport:                  integrationstore.IntegrationDeliveryTransportConnector,
		DeliveryKind:               "message", PayloadVersion: "channel-message.v1",
		Payload:          json.RawMessage(`{"destination":{"provider_ref":"thread-1"},"message":{"text":"reply"}}`),
		IdempotencyScope: "test/channel-send", IdempotencyKey: "tool-call-1",
		NotifyRef: first.Accepted[0].AgentInputID,
	}
	delivery, err := store.Integrations().CreateIntegrationDelivery(ctx, deliveryInput)
	if err != nil {
		t.Fatalf("create connector delivery: %v", err)
	}
	replayedDelivery, err := store.Integrations().CreateIntegrationDelivery(ctx, deliveryInput)
	if err != nil {
		t.Fatalf("replay connector delivery: %v", err)
	}
	if !delivery.Created || replayedDelivery.Created || replayedDelivery.ID != delivery.ID {
		t.Fatalf("delivery idempotency first=%+v replay=%+v", delivery, replayedDelivery)
	}
	for name, statement := range map[string]string{
		"id":         `UPDATE integration_deliveries SET id = uuidv7() WHERE id = $1`,
		"notify ref": `UPDATE integration_deliveries SET notify_ref = NULL WHERE id = $1`,
	} {
		if _, err := pool.Exec(ctx, statement, delivery.ID); !isPgCode(err, "25006") {
			t.Fatalf("change delivery %s error = %v, want SQLSTATE 25006", name, err)
		}
	}
	mismatched := deliveryInput
	mismatched.Payload = json.RawMessage(`{"destination":{"provider_ref":"thread-1"},"message":{"text":"different"}}`)
	if _, err := store.Integrations().CreateIntegrationDelivery(ctx, mismatched); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("mismatched delivery replay error = %v, want conflict", err)
	}
	mismatched = deliveryInput
	mismatched.NotifyRef = NilID
	if _, err := store.Integrations().CreateIntegrationDelivery(ctx, mismatched); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("mismatched delivery notify replay error = %v, want conflict", err)
	}
	wrongClaims, err := store.Integrations().ClaimIntegrationDeliveries(
		ctx,
		integrationstore.ClaimIntegrationDeliveriesInput{
			ClaimedBy: "wrong-provider", LeaseDuration: time.Minute,
			Capability: testChannelCapability("telegram"), Limit: 10,
		},
	)
	if err != nil || len(wrongClaims) != 0 {
		t.Fatalf("wrong provider claims = %+v, %v", wrongClaims, err)
	}
	claims, err := store.Integrations().ClaimIntegrationDeliveries(
		ctx,
		integrationstore.ClaimIntegrationDeliveriesInput{
			ClaimedBy: "gateway-a", LeaseDuration: time.Minute,
			Capability: testChannelCapability(testChannelProvider), Limit: 10,
		},
	)
	if err != nil {
		t.Fatalf("claim connector delivery: %v", err)
	}
	if len(claims) != 1 || claims[0].ID != delivery.ID || claims[0].ClaimToken == NilID ||
		claims[0].ClaimGeneration != 1 ||
		claims[0].AppConfigurationRevision != app.ConfigurationRevision ||
		claims[0].InstallConfigurationRevision != install.ConfigurationRevision {
		t.Fatalf("connector delivery claims = %+v", claims)
	}
	completed, err := store.Integrations().CompleteIntegrationDelivery(
		ctx,
		integrationstore.CompleteIntegrationDeliveryInput{
			ID: delivery.ID, ClaimToken: claims[0].ClaimToken,
			ClaimGeneration:    claims[0].ClaimGeneration,
			State:              integrationstore.IntegrationDeliveryStateDelivered,
			ProviderMessageRef: "provider-message-1", LastError: json.RawMessage(`{}`),
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	)
	if err != nil || completed.State != integrationstore.IntegrationDeliveryStateDelivered ||
		completed.ProviderMessageRef != "provider-message-1" {
		t.Fatalf("complete connector delivery = %+v, %v", completed, err)
	}
	if _, err := store.Integrations().CompleteIntegrationDelivery(
		ctx,
		integrationstore.CompleteIntegrationDeliveryInput{
			ID: delivery.ID, ClaimToken: claims[0].ClaimToken,
			ClaimGeneration: claims[0].ClaimGeneration,
			State:           integrationstore.IntegrationDeliveryStateDelivered,
			LastError:       json.RawMessage(`{}`),
			Capabilities:    testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("stale delivery completion error = %v", err)
	}
	if _, err := store.Integrations().DeleteRetainedIntegrationDeliveries(
		ctx,
		integrationstore.DeleteRetainedIntegrationDeliveriesInput{Limit: 10},
	); err == nil {
		t.Fatal("zero delivery retention was accepted")
	}
	deleted, err := store.Integrations().DeleteRetainedIntegrationDeliveries(
		ctx,
		integrationstore.DeleteRetainedIntegrationDeliveriesInput{
			Retention: time.Microsecond,
			Limit:     10,
		},
	)
	if err != nil || deleted != 1 {
		t.Fatalf("delete retained connector delivery = %d, %v", deleted, err)
	}
	if _, err := store.Integrations().GetIntegrationDelivery(
		ctx,
		testProjectID,
		delivery.ID,
	); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("retained connector delivery still exists: %v", err)
	}

	assertUnavailableDelivery := func(
		label, disableStatement, enableStatement string,
		authorityID integrationstore.ID,
	) {
		t.Helper()
		candidate := deliveryInput
		candidate.IdempotencyKey = "unavailable-" + label
		candidate.NotifyRef = NilID
		pending, err := store.Integrations().CreateIntegrationDelivery(ctx, candidate)
		if err != nil {
			t.Fatalf("create %s-gated delivery: %v", label, err)
		}
		if _, err := pool.Exec(ctx, disableStatement, authorityID); err != nil {
			t.Fatalf("disable %s: %v", label, err)
		}
		blocked := candidate
		blocked.IdempotencyKey = "blocked-" + label
		if _, err := store.Integrations().CreateIntegrationDelivery(ctx, blocked); !errors.Is(
			err,
			storeerr.ErrUnauthorized,
		) {
			t.Fatalf("create delivery through disabled %s error = %v, want unauthorized", label, err)
		}
		claims, err := store.Integrations().ClaimIntegrationDeliveries(
			ctx,
			integrationstore.ClaimIntegrationDeliveriesInput{
				ClaimedBy: "gateway-state-gate", LeaseDuration: time.Minute,
				Capability: testChannelCapability(testChannelProvider), Limit: 10,
			},
		)
		if err != nil || len(claims) != 0 {
			t.Fatalf("claims through disabled %s = %+v, %v", label, claims, err)
		}
		canceled, err := store.Integrations().CancelUnavailableIntegrationDeliveries(ctx, 10)
		if err != nil || len(canceled) != 1 || canceled[0].ID != pending.ID {
			t.Fatalf("cancel delivery through disabled %s = %+v, %v", label, canceled, err)
		}
		stored, err := store.Integrations().GetIntegrationDelivery(
			ctx,
			testProjectID,
			pending.ID,
		)
		if err != nil || stored.State != integrationstore.IntegrationDeliveryStateCanceled {
			t.Fatalf("canceled %s-gated delivery = %+v, %v", label, stored, err)
		}
		if _, err := pool.Exec(ctx, enableStatement, authorityID); err != nil {
			t.Fatalf("re-enable %s: %v", label, err)
		}
	}
	assertUnavailableDelivery(
		"route",
		`UPDATE integration_routes SET state = 'disabled' WHERE id = $1`,
		`UPDATE integration_routes SET state = 'active' WHERE id = $1`,
		first.Accepted[0].RouteID,
	)
	assertUnavailableDelivery(
		"installation",
		`UPDATE integration_installs SET state = 'disabled' WHERE id = $1`,
		`UPDATE integration_installs SET state = 'active' WHERE id = $1`,
		install.ID,
	)
	assertUnavailableDelivery(
		"app",
		`UPDATE integration_apps SET state = 'disabled' WHERE id = $1`,
		`UPDATE integration_apps SET state = 'active' WHERE id = $1`,
		app.ID,
	)
}

func TestChannelFoundationUnavailableDeliverySweepMakesBoundedProgress(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "bounded-delivery-sweep")
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "bounded-sweep-thread",
			ProviderRefKind: "thread",
		},
	)
	if err != nil {
		t.Fatalf("create bounded sweep target: %v", err)
	}
	type candidate struct {
		delivery integrationstore.IntegrationDeliveryRecord
		route    integrationstore.IntegrationRouteRecord
	}
	candidates := make([]candidate, 0, 8)
	createCandidate := func(index int) {
		t.Helper()
		route, err := store.Integrations().CreateIntegrationRoute(
			ctx,
			integrationstore.CreateIntegrationRouteInput{
				ProjectID: testProjectID, IntegrationInstallID: install.ID,
				DeploymentKey: fmt.Sprintf("bounded-sweep-%d", index),
				HandlerKey:    "bounded_sweep", HandlerVersion: index + 1, State: integrationstore.IntegrationRouteStateActive,
			},
		)
		if err != nil {
			t.Fatalf("create bounded sweep route %d: %v", index, err)
		}
		binding, err := store.Integrations().CreateIntegrationTargetBinding(
			ctx,
			integrationstore.CreateIntegrationTargetBindingInput{
				ProjectID: testProjectID, AgentID: agent.ID,
				IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
				IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: true,
				Source: "bounded-sweep",
			},
		)
		if err != nil {
			t.Fatalf("create bounded sweep binding %d: %v", index, err)
		}
		delivery, err := store.Integrations().CreateIntegrationDelivery(
			ctx,
			integrationstore.CreateIntegrationDeliveryInput{
				ProjectID: testProjectID, AgentID: agent.ID,
				IntegrationTargetBindingID: binding.ID,
				Transport:                  integrationstore.IntegrationDeliveryTransportConnector,
				DeliveryKind:               "message", PayloadVersion: "channel-message.v1",
				Payload:          json.RawMessage(`{"message":{"text":"hello"}}`),
				IdempotencyScope: "bounded-sweep", IdempotencyKey: route.ID.String(),
			},
		)
		if err != nil {
			t.Fatalf("create bounded sweep delivery %d: %v", index, err)
		}
		candidates = append(candidates, candidate{delivery: delivery, route: route})
	}
	for index := range 4 {
		createCandidate(index)
	}
	sort.Slice(candidates, func(left, right int) bool {
		return candidates[left].delivery.ID.String() < candidates[right].delivery.ID.String()
	})
	unavailable := candidates[0]
	wantCursor := candidates[1].delivery.ID
	first, err := store.Integrations().CancelUnavailableIntegrationDeliveries(ctx, 2)
	if err != nil || len(first) != 0 {
		t.Fatalf("first bounded unavailable sweep = %+v, %v", first, err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_routes SET state = 'disabled' WHERE id = $1`,
		unavailable.route.ID,
	); err != nil {
		t.Fatalf("disable bounded sweep route: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	for index := 4; index < 6; index++ {
		createCandidate(index)
	}
	second, err := store.Integrations().CancelUnavailableIntegrationDeliveries(ctx, 2)
	if err != nil || len(second) != 0 {
		t.Fatalf("second bounded unavailable sweep = %+v, %v", second, err)
	}
	time.Sleep(2 * time.Millisecond)
	for index := 6; index < 8; index++ {
		createCandidate(index)
	}
	third, err := store.Integrations().CancelUnavailableIntegrationDeliveries(ctx, 2)
	if err != nil || len(third) != 1 || third[0].ID != unavailable.delivery.ID {
		t.Fatalf("third bounded unavailable sweep = %+v, %v", third, err)
	}
	var cursor uuid.UUID
	if err := pool.QueryRow(
		ctx,
		`SELECT last_item_id FROM integration_sweep_cursors
		 WHERE sweep_kind = 'delivery_unavailable'`,
	).Scan(&cursor); err != nil {
		t.Fatalf("load unavailable sweep cursor: %v", err)
	}
	if cursor != uuid.UUID(wantCursor) {
		t.Fatalf("unavailable sweep cursor = %s, want %s", cursor, wantCursor)
	}
}

func TestChannelFoundationIdleUnavailableDeliverySweepDoesNotRewriteCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)

	var before uint64
	if err := pool.QueryRow(
		ctx,
		`SELECT xmin::text::bigint FROM integration_sweep_cursors
		 WHERE sweep_kind = 'delivery_unavailable'`,
	).Scan(&before); err != nil {
		t.Fatalf("load idle unavailable sweep cursor version: %v", err)
	}
	canceled, err := store.Integrations().CancelUnavailableIntegrationDeliveries(ctx, 10)
	if err != nil || len(canceled) != 0 {
		t.Fatalf("idle unavailable sweep = %+v, %v", canceled, err)
	}
	var after uint64
	if err := pool.QueryRow(
		ctx,
		`SELECT xmin::text::bigint FROM integration_sweep_cursors
		 WHERE sweep_kind = 'delivery_unavailable'`,
	).Scan(&after); err != nil {
		t.Fatalf("reload idle unavailable sweep cursor version: %v", err)
	}
	if after != before {
		t.Fatalf("idle unavailable sweep rewrote cursor xmin %d -> %d", before, after)
	}
}

func TestChannelFoundationProfileRouteLaunchesDistinctAgentsForOneTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "channel-profile-route@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "channel-profile-route")
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: testChannelProvider, ProviderAppRef: "discord-profile-route-app",
			DisplayName: "Profile route app", ConnectorKey: testChannelConnector,
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create profile route app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
			InstalledByUserID: admin.ID,
			Provider:          testChannelProvider, IntegrationKind: "new_agent_per_message",
			ConnectionMode: "gateway", State: integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "profile-route-guild", ProviderAccountRef: "profile-route-bot",
		},
	)
	if err != nil {
		t.Fatalf("create profile route installation: %v", err)
	}
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "profile-route", HandlerKey: testChannelHandler, HandlerVersion: 1,
			Configuration: json.RawMessage(`{"launch":"per_message"}`), State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create profile route: %v", err)
	}
	routeCalls := 0
	service := integration.NewChannelService(
		store.Execution(),
		store.Integrations(),
		integration.ChannelRouteHandlers{
			integration.ChannelRouteHandlerKey(testChannelHandler, 1): integration.ChannelRouteHandlerFunc(
				func(
					_ context.Context,
					_ integration.ChannelRouteContext,
					envelope integration.ChannelInboundEnvelope,
				) (integration.ChannelRouteDecision, error) {
					routeCalls++
					return integration.ChannelRouteDecision{
						Accept: true, ProviderRef: envelope.Conversation.Ref,
						ProviderRefKind: "channel", DisplayName: envelope.Conversation.DisplayName,
						DeliveryMode: executionstore.DeliveryModeQueued,
						Attachments: []integration.ChannelAttachmentAction{{
							AgentProfileID: profile.ID, InstanceKey: envelope.ProviderEventID, SendAllowed: true,
						}},
					}, nil
				},
			),
		},
	)
	preflightErr := errors.New("invalid channel content")
	_, err = service.ProcessInbound(ctx, integration.ProcessChannelInboundInput{
		IntegrationAppID: app.ID, Capabilities: testChannelCapabilities(testChannelProvider),
		Envelope: integration.ChannelInboundEnvelope{
			Version: integration.ChannelEnvelopeVersionV1, ProviderEventID: "invalid-profile-event",
			ExternalTenantID: install.ProviderTenantID, ExternalAccountRef: install.ProviderAccountRef,
			EventType:    "message.created",
			Conversation: integration.ChannelConversation{Ref: "invalid-channel", Kind: "channel"},
			Actor:        integration.ChannelActor{Ref: "user-1"},
			ContentBlocks: json.RawMessage(
				`[{"type":"media","media_type":"image/png","data":"invalid"}]`,
			),
		},
		PrepareContent: func(
			context.Context,
			json.RawMessage,
		) (integration.MaterializeChannelInboundContentFunc, error) {
			return nil, preflightErr
		},
	})
	if !errors.Is(err, preflightErr) {
		t.Fatalf("invalid profile content error = %v, want preflight error", err)
	}
	var agents, targets, mutationBindings, inputs, artifacts int
	if err := pool.QueryRow(
		ctx,
		`SELECT
		   (SELECT count(*) FROM agents WHERE project_id = $1 AND agent_profile_id = $2),
		   (SELECT count(*) FROM integration_targets WHERE project_id = $1 AND integration_install_id = $3),
		   (SELECT count(*) FROM integration_target_bindings WHERE project_id = $1 AND integration_route_id = $4),
		   (SELECT count(*) FROM agent_inputs WHERE project_id = $1),
		   (SELECT count(*) FROM artifacts artifact
		      JOIN agents artifact_agent ON artifact_agent.id = artifact.agent_id
		      WHERE artifact_agent.project_id = $1)`,
		testProjectID,
		profile.ID,
		install.ID,
		route.ID,
	).Scan(&agents, &targets, &mutationBindings, &inputs, &artifacts); err != nil {
		t.Fatalf("count mutations after invalid profile content: %v", err)
	}
	if routeCalls != 0 || agents != 0 || targets != 0 || mutationBindings != 0 ||
		inputs != 0 || artifacts != 0 {
		t.Fatalf(
			"invalid profile content mutated route_calls=%d agents=%d targets=%d bindings=%d inputs=%d artifacts=%d",
			routeCalls,
			agents,
			targets,
			mutationBindings,
			inputs,
			artifacts,
		)
	}
	for name, envelope := range map[string]integration.ChannelInboundEnvelope{
		"nul conversation display": {
			Version: integration.ChannelEnvelopeVersionV1, ProviderEventID: "nul-display-event",
			ExternalTenantID: install.ProviderTenantID, ExternalAccountRef: install.ProviderAccountRef,
			EventType: "message.created",
			Conversation: integration.ChannelConversation{
				Ref: "nul-display-channel", Kind: "channel", DisplayName: "bad\x00display",
			},
			Actor:         integration.ChannelActor{Ref: "user-1"},
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
		},
		"nul metadata": {
			Version: integration.ChannelEnvelopeVersionV1, ProviderEventID: "nul-metadata-event",
			ExternalTenantID: install.ProviderTenantID, ExternalAccountRef: install.ProviderAccountRef,
			EventType:    "message.created",
			Conversation: integration.ChannelConversation{Ref: "nul-metadata-channel", Kind: "channel"},
			Actor:        integration.ChannelActor{Ref: "user-1"},
			ContentBlocks: json.RawMessage(
				`[{"type":"text","text":"hello"}]`,
			),
			Metadata: json.RawMessage(`{"bad":"\u0000"}`),
		},
		"nul content text": {
			Version: integration.ChannelEnvelopeVersionV1, ProviderEventID: "nul-content-event",
			ExternalTenantID: install.ProviderTenantID, ExternalAccountRef: install.ProviderAccountRef,
			EventType:    "message.created",
			Conversation: integration.ChannelConversation{Ref: "nul-content-channel", Kind: "channel"},
			Actor:        integration.ChannelActor{Ref: "user-1"},
			ContentBlocks: json.RawMessage(
				`[{"type":"text","text":"bad\u0000content"}]`,
			),
		},
	} {
		t.Run(name, func(t *testing.T) {
			prepareCalls := 0
			_, err := service.ProcessInbound(ctx, integration.ProcessChannelInboundInput{
				IntegrationAppID: app.ID,
				Capabilities:     testChannelCapabilities(testChannelProvider),
				Envelope:         envelope,
				PrepareContent: func(
					context.Context,
					json.RawMessage,
				) (integration.MaterializeChannelInboundContentFunc, error) {
					prepareCalls++
					return nil, errors.New("unexpected content preparation")
				},
			})
			if err == nil {
				t.Fatal("invalid external text was accepted")
			}
			if prepareCalls != 0 {
				t.Fatalf("content preparation calls = %d, want 0", prepareCalls)
			}
		})
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT
		   (SELECT count(*) FROM agents WHERE project_id = $1 AND agent_profile_id = $2),
		   (SELECT count(*) FROM integration_targets WHERE project_id = $1 AND integration_install_id = $3),
		   (SELECT count(*) FROM integration_target_bindings WHERE project_id = $1 AND integration_route_id = $4),
		   (SELECT count(*) FROM agent_inputs WHERE project_id = $1),
		   (SELECT count(*) FROM artifacts artifact
		      JOIN agents artifact_agent ON artifact_agent.id = artifact.agent_id
		      WHERE artifact_agent.project_id = $1)`,
		testProjectID,
		profile.ID,
		install.ID,
		route.ID,
	).Scan(&agents, &targets, &mutationBindings, &inputs, &artifacts); err != nil {
		t.Fatalf("count mutations after invalid external text: %v", err)
	}
	if routeCalls != 0 || agents != 0 || targets != 0 || mutationBindings != 0 ||
		inputs != 0 || artifacts != 0 {
		t.Fatalf(
			"invalid external text mutated route_calls=%d agents=%d targets=%d bindings=%d inputs=%d artifacts=%d",
			routeCalls,
			agents,
			targets,
			mutationBindings,
			inputs,
			artifacts,
		)
	}
	process := func(eventID string) integration.ChannelInboundAcceptance {
		t.Helper()
		result, err := service.ProcessInbound(ctx, integration.ProcessChannelInboundInput{
			IntegrationAppID: app.ID, Capabilities: testChannelCapabilities(testChannelProvider),
			Envelope: integration.ChannelInboundEnvelope{
				Version: integration.ChannelEnvelopeVersionV1, ProviderEventID: eventID,
				ExternalTenantID:   install.ProviderTenantID,
				ExternalAccountRef: install.ProviderAccountRef, EventType: "message.created",
				Conversation: integration.ChannelConversation{
					Ref: "shared-channel", Kind: "channel", DisplayName: "Shared channel",
				},
				Actor:         integration.ChannelActor{Ref: "user-1", DisplayName: "Customer"},
				ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
				OccurredAt:    time.Now().UTC(),
			},
			PrepareContent: passthroughChannelInboundContent,
		})
		if err != nil {
			t.Fatalf("process profile route event %q: %v", eventID, err)
		}
		if len(result.Accepted) != 1 || result.IgnoredRoutes != 0 {
			t.Fatalf("profile route event %q = %+v", eventID, result)
		}
		return result.Accepted[0]
	}
	first := process("profile-event-1")
	second := process("profile-event-2")
	if first.AgentID == second.AgentID || first.BindingID == second.BindingID {
		t.Fatalf("distinct route sessions collapsed: first=%+v second=%+v", first, second)
	}
	if first.TargetID != second.TargetID || first.RouteID != route.ID || second.RouteID != route.ID {
		t.Fatalf("shared provider target was not preserved: first=%+v second=%+v", first, second)
	}
	replayed := process("profile-event-1")
	if replayed.AgentID != first.AgentID || replayed.TargetID != first.TargetID ||
		replayed.BindingID != first.BindingID || replayed.AgentInputID != first.AgentInputID {
		t.Fatalf("profile route replay was not idempotent: first=%+v replay=%+v", first, replayed)
	}
	var bindings int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*)
		 FROM integration_target_bindings
		 WHERE project_id = $1 AND integration_target_id = $2
		   AND integration_route_id = $3 AND receive_allowed AND revoked_at IS NULL`,
		testProjectID,
		first.TargetID,
		route.ID,
	).Scan(&bindings); err != nil {
		t.Fatalf("count shared-target bindings: %v", err)
	}
	if bindings != 2 {
		t.Fatalf("shared-target receive bindings = %d, want 2", bindings)
	}
}

func TestChannelFoundationRetriesWholeEventAfterProfileLaunchCompletionRace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "channel-profile-retry@example.com")
	existingProfile := createIntegrationTestProfile(t, ctx, store, "channel-profile-retry-existing")
	existingAgent := createIntegrationBoundAgent(
		t,
		ctx,
		store,
		existingProfile,
		admin.ID,
		"channel-profile-retry-existing-agent",
	)
	spawnProfile := createIntegrationTestProfile(t, ctx, store, "channel-profile-retry-spawn")
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: testChannelProvider, ProviderAppRef: "channel-profile-retry-app",
			DisplayName: "Profile retry app", ConnectorKey: testChannelConnector,
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create profile retry app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
			InstalledByUserID: admin.ID, Provider: testChannelProvider,
			IntegrationKind: "profile_retry", ConnectionMode: "gateway",
			State:            integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "profile-retry-tenant", ProviderAccountRef: "profile-retry-account",
		},
	)
	if err != nil {
		t.Fatalf("create profile retry install: %v", err)
	}
	createRoute := func(deploymentKey, handlerKey string) integrationstore.IntegrationRouteRecord {
		t.Helper()
		route, err := store.Integrations().CreateIntegrationRoute(
			ctx,
			integrationstore.CreateIntegrationRouteInput{
				ProjectID:            testProjectID,
				IntegrationInstallID: install.ID, DeploymentKey: deploymentKey,
				HandlerKey: handlerKey, HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
			},
		)
		if err != nil {
			t.Fatalf("create profile retry route %q: %v", deploymentKey, err)
		}
		return route
	}
	existingRoute := createRoute("profile-retry-existing", "profile_retry_existing")
	time.Sleep(2 * time.Millisecond)
	spawnRoute := createRoute("profile-retry-spawn", "profile_retry_spawn")
	execution := &failOnceBoundChannelInputStore{Store: store.Execution(), failAt: 2}
	service := integration.NewChannelService(
		execution,
		store.Integrations(),
		integration.ChannelRouteHandlers{
			integration.ChannelRouteHandlerKey("profile_retry_existing", 1): integration.ChannelRouteHandlerFunc(
				func(
					context.Context,
					integration.ChannelRouteContext,
					integration.ChannelInboundEnvelope,
				) (integration.ChannelRouteDecision, error) {
					return integration.ChannelRouteDecision{
						Accept: true, ProviderRef: "profile-retry-thread", ProviderRefKind: "thread",
						DeliveryMode: executionstore.DeliveryModeQueued,
						Attachments: []integration.ChannelAttachmentAction{{
							AgentID: existingAgent.ID, SendAllowed: true,
						}},
					}, nil
				},
			),
			integration.ChannelRouteHandlerKey("profile_retry_spawn", 1): integration.ChannelRouteHandlerFunc(
				func(
					_ context.Context,
					_ integration.ChannelRouteContext,
					envelope integration.ChannelInboundEnvelope,
				) (integration.ChannelRouteDecision, error) {
					return integration.ChannelRouteDecision{
						Accept: true, ProviderRef: "profile-retry-thread", ProviderRefKind: "thread",
						DeliveryMode: executionstore.DeliveryModeQueued,
						Attachments: []integration.ChannelAttachmentAction{{
							AgentProfileID: spawnProfile.ID,
							InstanceKey:    envelope.ProviderEventID,
							SendAllowed:    true,
						}},
					}, nil
				},
			),
		},
	)
	input := integration.ProcessChannelInboundInput{
		IntegrationAppID: app.ID, Capabilities: testChannelCapabilities(testChannelProvider),
		Envelope: integration.ChannelInboundEnvelope{
			Version: integration.ChannelEnvelopeVersionV1, ProviderEventID: "profile-retry-event",
			ExternalTenantID: install.ProviderTenantID, ExternalAccountRef: install.ProviderAccountRef,
			EventType: "message.created",
			Conversation: integration.ChannelConversation{
				Ref: "profile-retry-thread", Kind: "thread", DisplayName: "Retry thread",
			},
			Actor:         integration.ChannelActor{Ref: "profile-retry-user"},
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"retry me"}]`),
		},
		PrepareContent: passthroughChannelInboundContent,
	}
	if _, err := service.ProcessInbound(ctx, input); !errors.Is(
		err,
		integration.ErrChannelInboundCompletionRetry,
	) || !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("first profile completion attempt error = %v", err)
	}
	if execution.createCalls != 2 {
		t.Fatalf("first profile completion create calls = %d, want 2", execution.createCalls)
	}

	var existingInputID, launchedAgentID uuid.UUID
	if err := pool.QueryRow(
		ctx,
		`SELECT input.id
		 FROM agent_inputs input
		 WHERE input.project_id = $1 AND input.agent_id = $2
		   AND input.input_idempotency_key = $3`,
		testProjectID,
		existingAgent.ID,
		input.Envelope.ProviderEventID,
	).Scan(&existingInputID); err != nil {
		t.Fatalf("load earlier committed route input: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT id FROM agents WHERE project_id = $1 AND agent_profile_id = $2`,
		testProjectID,
		spawnProfile.ID,
	).Scan(&launchedAgentID); err != nil {
		t.Fatalf("load profile agent committed before retry: %v", err)
	}
	var launchedInputs int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_inputs
		 WHERE project_id = $1 AND agent_id = $2 AND integration_target_id IS NOT NULL`,
		testProjectID,
		launchedAgentID,
	).Scan(&launchedInputs); err != nil {
		t.Fatalf("count profile inputs before retry: %v", err)
	}
	if launchedInputs != 0 {
		t.Fatalf("profile agent inputs before retry = %d, want 0", launchedInputs)
	}

	result, err := service.ProcessInbound(ctx, input)
	if err != nil {
		t.Fatalf("retry profile completion: %v", err)
	}
	if execution.createCalls != 4 || len(result.Accepted) != 2 || len(result.FailedRoutes) != 0 {
		t.Fatalf("profile completion retry result = %+v, create calls %d", result, execution.createCalls)
	}
	acceptedByRoute := make(map[integrationstore.ID]integration.ChannelInboundAcceptance, 2)
	for _, acceptance := range result.Accepted {
		acceptedByRoute[acceptance.RouteID] = acceptance
	}
	if acceptedByRoute[existingRoute.ID].AgentInputID != integrationstore.ID(existingInputID) {
		t.Fatalf("earlier route input was duplicated on replay: %+v", acceptedByRoute)
	}
	spawned := acceptedByRoute[spawnRoute.ID]
	if spawned.AgentID != integrationstore.ID(launchedAgentID) || spawned.Launch.Created {
		t.Fatalf("profile retry did not reuse launched agent: %+v", spawned)
	}

	var targets, bindings, inputs, spawnedAgents int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM integration_targets WHERE project_id = $1 AND integration_install_id = $2),
  (SELECT count(*) FROM integration_target_bindings
     WHERE project_id = $1 AND integration_install_id = $2 AND revoked_at IS NULL),
  (SELECT count(*) FROM agent_inputs
     WHERE project_id = $1 AND integration_target_id IN (
       SELECT id FROM integration_targets WHERE project_id = $1 AND integration_install_id = $2
     )),
  (SELECT count(*) FROM agents WHERE project_id = $1 AND agent_profile_id = $3)
`, testProjectID, install.ID, spawnProfile.ID).Scan(
		&targets,
		&bindings,
		&inputs,
		&spawnedAgents,
	); err != nil {
		t.Fatalf("count profile completion retry rows: %v", err)
	}
	if targets != 1 || bindings != 2 || inputs != 2 || spawnedAgents != 1 {
		t.Fatalf(
			"profile retry rows targets=%d bindings=%d inputs=%d spawned_agents=%d",
			targets,
			bindings,
			inputs,
			spawnedAgents,
		)
	}
}

func TestChannelFoundationStaleRuntimeCannotLaunchProfileAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "channel-stale-profile@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "channel-stale-profile")
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: testChannelProvider, ProviderAppRef: "stale-profile-app",
			DisplayName: "Stale profile app", ConnectorKey: testChannelConnector,
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create stale profile app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
			InstalledByUserID: admin.ID,
			Provider:          testChannelProvider, IntegrationKind: "new_agent_per_message",
			ConnectionMode: "gateway", State: integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "stale-profile-guild", ProviderAccountRef: "stale-profile-bot",
		},
	)
	if err != nil {
		t.Fatalf("create stale profile installation: %v", err)
	}
	if _, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "stale-profile", HandlerKey: testChannelHandler, HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	); err != nil {
		t.Fatalf("create stale profile route: %v", err)
	}
	unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: testOrgID, IntegrationAppID: app.ID,
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			UnitKey: "stale-profile", RuntimeKind: "provider_socket",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1,
		},
	)
	if err != nil {
		t.Fatalf("create stale profile runtime: %v", err)
	}
	claim := func(owner string) integrationstore.IntegrationRuntimeUnitRecord {
		t.Helper()
		claims, err := store.Integrations().ClaimIntegrationRuntimeUnits(
			ctx,
			integrationstore.ClaimIntegrationRuntimeUnitsInput{
				LeaseOwner: owner, LeaseDuration: time.Minute,
				Capability: testChannelCapability(testChannelProvider), Limit: 1,
			},
		)
		if err != nil || len(claims) != 1 || claims[0].ID != unit.ID {
			t.Fatalf("claim stale profile runtime as %q = %+v, %v", owner, claims, err)
		}
		return claims[0]
	}
	stale := claim("stale-profile-owner")
	execution := &expiringChannelLaunchStore{Store: store.Execution()}
	execution.beforeRuntimeLaunch = func() {
		if _, err := store.Integrations().ReleaseIntegrationRuntimeUnit(
			ctx,
			integrationstore.ReleaseIntegrationRuntimeUnitInput{
				ID: stale.ID, LeaseToken: stale.LeaseToken,
				LeaseGeneration: stale.LeaseGeneration, LastError: json.RawMessage(`{}`),
				Capabilities: testChannelCapabilities(testChannelProvider),
			},
		); err != nil {
			t.Fatalf("release runtime during profile launch: %v", err)
		}
		current := claim("current-profile-owner")
		if current.LeaseGeneration <= stale.LeaseGeneration {
			t.Fatalf("profile runtime generation did not advance: stale=%+v current=%+v", stale, current)
		}
	}
	service := integration.NewChannelService(
		execution,
		store.Integrations(),
		integration.ChannelRouteHandlers{
			integration.ChannelRouteHandlerKey(testChannelHandler, 1): integration.ChannelRouteHandlerFunc(
				func(
					_ context.Context,
					_ integration.ChannelRouteContext,
					envelope integration.ChannelInboundEnvelope,
				) (integration.ChannelRouteDecision, error) {
					return integration.ChannelRouteDecision{
						Accept: true, ProviderRef: envelope.Conversation.Ref,
						ProviderRefKind: "channel", DeliveryMode: executionstore.DeliveryModeQueued,
						Attachments: []integration.ChannelAttachmentAction{{
							AgentProfileID: profile.ID, InstanceKey: envelope.ProviderEventID, SendAllowed: true,
						}},
					}, nil
				},
			),
		},
	)
	prepareCalls := 0
	_, err = service.ProcessRuntimeInbound(
		ctx,
		integration.ProcessChannelInboundInput{
			IntegrationAppID: app.ID, Capabilities: testChannelCapabilities(testChannelProvider),
			Envelope: integration.ChannelInboundEnvelope{
				Version: integration.ChannelEnvelopeVersionV1, ProviderEventID: "stale-profile-event",
				ExternalTenantID: install.ProviderTenantID, ExternalAccountRef: install.ProviderAccountRef,
				EventType:    "message.created",
				Conversation: integration.ChannelConversation{Ref: "stale-profile-channel", Kind: "channel"},
				Actor:        integration.ChannelActor{Ref: "stale-profile-user"},
				ContentBlocks: json.RawMessage(`[{
					"type":"text","text":"must not launch"
				}]`),
			},
			PrepareContent: func(
				_ context.Context,
				content json.RawMessage,
			) (integration.MaterializeChannelInboundContentFunc, error) {
				return func(
					context.Context,
					integration.MaterializeChannelInboundContentInput,
				) (json.RawMessage, error) {
					prepareCalls++
					return content, nil
				}, nil
			},
		},
		integration.ChannelRuntimeLease{
			UnitID: stale.ID, Token: stale.LeaseToken, Generation: stale.LeaseGeneration,
		},
	)
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("stale profile launch error = %v, want state conflict", err)
	}
	var agents int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agents WHERE project_id = $1 AND agent_profile_id = $2`,
		testProjectID,
		profile.ID,
	).Scan(&agents); err != nil {
		t.Fatalf("count agents after stale profile launch: %v", err)
	}
	if agents != 0 || prepareCalls != 0 {
		t.Fatalf("stale profile launch created agents=%d prepare_calls=%d", agents, prepareCalls)
	}
}

func TestChannelFoundationRuntimeLeaseFencingAndCheckpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "channel-runtime@example.com")
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: testChannelProvider, ProviderAppRef: "discord-runtime-app",
			DisplayName: "Runtime app", ConnectorKey: testChannelConnector,
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create runtime app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
			InstalledByUserID: admin.ID,
			Provider:          testChannelProvider, IntegrationKind: "runtime_install",
			ConnectionMode: "gateway", State: integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "runtime-guild", ProviderAccountRef: "runtime-bot",
			ProviderAgentDisplayName: "Runtime bot",
		},
	)
	if err != nil {
		t.Fatalf("create runtime installation: %v", err)
	}
	unitInput := integrationstore.UpsertIntegrationRuntimeUnitInput{
		OrgID: testOrgID, IntegrationAppID: app.ID,
		UnitKey:     "gateway-shard-0",
		RuntimeKind: "provider_gateway", DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
		SpecRevision: 1, Configuration: json.RawMessage(`{"shard":0}`),
	}
	unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, unitInput)
	if err != nil {
		t.Fatalf("create runtime unit: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_installs SET provider_account_ref = 'moved-account' WHERE id = $1`,
		install.ID,
	); !isPgCode(err, "25006") {
		t.Fatalf("change integration install identity error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_apps
		 SET configuration_revision = configuration_revision - 1
		 WHERE id = $1`,
		app.ID,
	); !isPgCode(err, "25006") {
		t.Fatalf("decrease integration app revision error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_installs
		 SET configuration_revision = configuration_revision - 1
		 WHERE id = $1`,
		install.ID,
	); !isPgCode(err, "25006") {
		t.Fatalf("decrease integration install revision error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_runtime_units SET unit_key = 'moved-unit' WHERE id = $1`,
		unit.ID,
	); !isPgCode(err, "25006") {
		t.Fatalf("change integration runtime identity error = %v, want SQLSTATE 25006", err)
	}
	replayedUnit, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, unitInput)
	if err != nil || replayedUnit.ID != unit.ID || replayedUnit.SpecRevision != unit.SpecRevision {
		t.Fatalf("replay identical runtime specification = %+v, %v", replayedUnit, err)
	}
	changedSameRevision := unitInput
	changedSameRevision.Configuration = json.RawMessage(`{"shard":1}`)
	if _, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		changedSameRevision,
	); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("changed same-revision runtime specification error = %v, want conflict", err)
	}
	claim := func(owner string) integrationstore.IntegrationRuntimeUnitRecord {
		rows, err := store.Integrations().ClaimIntegrationRuntimeUnits(
			ctx,
			integrationstore.ClaimIntegrationRuntimeUnitsInput{
				LeaseOwner: owner, LeaseDuration: time.Minute,
				Capability: testChannelCapability(testChannelProvider),
				Limit:      1,
			},
		)
		if err != nil {
			t.Fatalf("claim runtime unit: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("runtime claims = %+v", rows)
		}
		return rows[0]
	}
	first := claim("gateway-a")
	if first.ID != unit.ID || first.LeaseToken == NilID || first.LeaseGeneration != 1 ||
		first.LeaseSpecRevision != unit.SpecRevision ||
		first.LeaseAppConfigurationRevision != app.ConfigurationRevision ||
		first.LeaseInstallConfigRevision != 0 {
		t.Fatalf("first runtime lease = %+v", first)
	}
	current, err := store.Integrations().IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		first.ID,
		install.ID,
		first.LeaseToken,
		first.LeaseGeneration,
	)
	if err != nil || !current {
		t.Fatalf("current runtime lease = %v, %v", current, err)
	}
	current, err = store.Integrations().IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		first.ID,
		uuid.New(),
		first.LeaseToken,
		first.LeaseGeneration,
	)
	if err != nil || current {
		t.Fatalf("app runtime lease accepted an unrelated installation = %v, %v", current, err)
	}
	heartbeat, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: first.ID, LeaseToken: first.LeaseToken, LeaseGeneration: first.LeaseGeneration,
			LeaseDuration: time.Minute, WriteCheckpoint: true, CheckpointVersion: 1,
			Checkpoint:   json.RawMessage(`{"sequence":42}`),
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	)
	if err != nil || heartbeat.CheckpointRevision != 1 {
		t.Fatalf("heartbeat runtime unit = %+v, %v", heartbeat, err)
	}
	if _, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: first.ID, LeaseToken: uuid.New(), LeaseGeneration: first.LeaseGeneration,
			LeaseDuration: time.Minute, Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("wrong-token heartbeat error = %v", err)
	}
	released, err := store.Integrations().ReleaseIntegrationRuntimeUnit(
		ctx,
		integrationstore.ReleaseIntegrationRuntimeUnitInput{
			ID: first.ID, LeaseToken: first.LeaseToken, LeaseGeneration: first.LeaseGeneration,
			WriteCheckpoint: true, CheckpointVersion: 1,
			Checkpoint:   json.RawMessage(`{"sequence":43}`),
			LastError:    json.RawMessage(`{"code":"connection_failed"}`),
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	)
	if err != nil || released.Status != integrationstore.IntegrationRuntimeStatusError {
		t.Fatalf("release failed runtime = %+v, %v", released, err)
	}
	if released.CheckpointRevision != 2 ||
		!sameJSON(released.Checkpoint, json.RawMessage(`{"sequence":43}`)) {
		t.Fatalf("release did not flush final runtime checkpoint: %+v", released)
	}
	current, err = store.Integrations().IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		first.ID,
		install.ID,
		first.LeaseToken,
		first.LeaseGeneration,
	)
	if err != nil || current {
		t.Fatalf("released runtime lease = %v, %v", current, err)
	}

	unitInput.SpecRevision = 2
	unitInput.Configuration = json.RawMessage(`{"shard":0,"resume":true}`)
	updated, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, unitInput)
	if err != nil {
		t.Fatalf("update runtime configuration: %v", err)
	}
	if updated.CheckpointRevision != 2 {
		t.Fatalf("configuration update discarded checkpoint: %+v", updated)
	}
	lowerRevision := unitInput
	lowerRevision.SpecRevision = 1
	if _, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		lowerRevision,
	); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("lower runtime specification revision error = %v, want conflict", err)
	}
	second := claim("gateway-b")
	if second.LeaseGeneration != first.LeaseGeneration+1 || second.CheckpointRevision != 2 {
		t.Fatalf("reclaimed runtime unit = %+v", second)
	}
	if _, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: first.ID, LeaseToken: first.LeaseToken, LeaseGeneration: first.LeaseGeneration,
			LeaseDuration: time.Minute, Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("superseded runtime heartbeat error = %v", err)
	}

	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_apps SET provider_config = '{"revision":2}'::jsonb WHERE id = $1`,
		app.ID,
	); err != nil {
		t.Fatalf("rotate runtime app configuration: %v", err)
	}
	current, err = store.Integrations().IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		second.ID,
		install.ID,
		second.LeaseToken,
		second.LeaseGeneration,
	)
	if err != nil || current {
		t.Fatalf("app-revision-stale runtime lease = %v, %v", current, err)
	}
	if _, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: second.ID, LeaseToken: second.LeaseToken,
			LeaseGeneration: second.LeaseGeneration, LeaseDuration: time.Minute,
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("app-revision-stale heartbeat error = %v", err)
	}
	if _, err := store.Integrations().ReleaseIntegrationRuntimeUnit(
		ctx,
		integrationstore.ReleaseIntegrationRuntimeUnitInput{
			ID: second.ID, LeaseToken: second.LeaseToken,
			LeaseGeneration: second.LeaseGeneration, LastError: json.RawMessage(`{}`),
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); err != nil {
		t.Fatalf("release app-revision-stale runtime: %v", err)
	}
	third := claim("gateway-c")
	if third.LeaseAppConfigurationRevision != app.ConfigurationRevision+1 {
		t.Fatalf("refreshed app runtime lease = %+v", third)
	}
	unitInput.SpecRevision = 3
	unitInput.DesiredState = integrationstore.IntegrationRuntimeDesiredStateStopped
	if _, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, unitInput); err != nil {
		t.Fatalf("stop app-wide runtime unit: %v", err)
	}
	if _, err := store.Integrations().ReleaseIntegrationRuntimeUnit(
		ctx,
		integrationstore.ReleaseIntegrationRuntimeUnitInput{
			ID: third.ID, LeaseToken: third.LeaseToken,
			LeaseGeneration: third.LeaseGeneration, LastError: json.RawMessage(`{}`),
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("release fenced stopped app-wide runtime error = %v, want conflict", err)
	}

	installUnit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: testOrgID, IntegrationAppID: app.ID,
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			UnitKey: "installation-runtime", RuntimeKind: "provider_socket",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1, Configuration: json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatalf("create installation runtime unit: %v", err)
	}
	installLease := claim("gateway-install")
	if installLease.ID != installUnit.ID ||
		installLease.LeaseInstallConfigRevision != install.ConfigurationRevision {
		t.Fatalf("installation runtime lease = %+v", installLease)
	}
	current, err = store.Integrations().IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		installLease.ID,
		install.ID,
		installLease.LeaseToken,
		installLease.LeaseGeneration,
	)
	if err != nil || !current {
		t.Fatalf("installation runtime lease = %v, %v", current, err)
	}
	current, err = store.Integrations().IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		installLease.ID,
		uuid.New(),
		installLease.LeaseToken,
		installLease.LeaseGeneration,
	)
	if err != nil || current {
		t.Fatalf("cross-install runtime lease = %v, %v", current, err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_installs SET provider_config = '{"revision":2}'::jsonb WHERE id = $1`,
		install.ID,
	); err != nil {
		t.Fatalf("rotate runtime installation configuration: %v", err)
	}
	current, err = store.Integrations().IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		installLease.ID,
		install.ID,
		installLease.LeaseToken,
		installLease.LeaseGeneration,
	)
	if err != nil || current {
		t.Fatalf("install-revision-stale runtime lease = %v, %v", current, err)
	}
	if _, err := store.Integrations().HeartbeatIntegrationRuntimeUnit(
		ctx,
		integrationstore.HeartbeatIntegrationRuntimeUnitInput{
			ID: installLease.ID, LeaseToken: installLease.LeaseToken,
			LeaseGeneration: installLease.LeaseGeneration, LeaseDuration: time.Minute,
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("install-revision-stale heartbeat error = %v", err)
	}
	if err := store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, install.ID); err != nil {
		t.Fatalf("delete leased runtime installation: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_apps
		 SET state = 'disabled', deleted_at = statement_timestamp()
		 WHERE id = $1`,
		app.ID,
	); err != nil {
		t.Fatalf("delete leased runtime app: %v", err)
	}
	if _, err := store.Integrations().ReleaseIntegrationRuntimeUnit(
		ctx,
		integrationstore.ReleaseIntegrationRuntimeUnitInput{
			ID: installLease.ID, LeaseToken: installLease.LeaseToken,
			LeaseGeneration: installLease.LeaseGeneration, LastError: json.RawMessage(`{}`),
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("release fenced runtime after app deletion error = %v, want conflict", err)
	}
}

func TestChannelFoundationStaleRuntimeCannotReplaceBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, app, install := createChannelLifecycleFixture(t, ctx, store, "stale-binding-runtime")
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "stale-runtime", HandlerKey: testChannelHandler, HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create stale runtime route: %v", err)
	}
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "stale-runtime-thread",
			ProviderRefKind: "thread", DisplayName: "Original thread",
			ProviderMetadata: json.RawMessage(`{"revision":1}`),
		},
	)
	if err != nil {
		t.Fatalf("create stale runtime target: %v", err)
	}
	binding, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: true,
			Source: "channel_route", Metadata: json.RawMessage(`{"policy":"reply"}`),
		},
	)
	if err != nil {
		t.Fatalf("create stale runtime binding: %v", err)
	}
	unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: testOrgID, IntegrationAppID: app.ID,
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			UnitKey: "stale-binding", RuntimeKind: "provider_socket",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1,
		},
	)
	if err != nil {
		t.Fatalf("create stale binding runtime unit: %v", err)
	}
	claim := func(owner string) integrationstore.IntegrationRuntimeUnitRecord {
		t.Helper()
		claims, err := store.Integrations().ClaimIntegrationRuntimeUnits(
			ctx,
			integrationstore.ClaimIntegrationRuntimeUnitsInput{
				LeaseOwner: owner, LeaseDuration: time.Minute,
				Capability: testChannelCapability(testChannelProvider), Limit: 1,
			},
		)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claim stale binding runtime as %q = %+v, %v", owner, claims, err)
		}
		return claims[0]
	}
	stale := claim("stale-owner")
	if stale.ID != unit.ID {
		t.Fatalf("claimed stale runtime = %+v, want %s", stale, unit.ID)
	}
	if _, err := store.Integrations().ReleaseIntegrationRuntimeUnit(
		ctx,
		integrationstore.ReleaseIntegrationRuntimeUnitInput{
			ID: stale.ID, LeaseToken: stale.LeaseToken,
			LeaseGeneration: stale.LeaseGeneration, LastError: json.RawMessage(`{}`),
			Capabilities: testChannelCapabilities(testChannelProvider),
		},
	); err != nil {
		t.Fatalf("release stale binding runtime: %v", err)
	}
	current := claim("current-owner")
	if current.LeaseGeneration <= stale.LeaseGeneration {
		t.Fatalf("runtime generation did not advance: stale=%+v current=%+v", stale, current)
	}
	_, err = store.Execution().CreateBoundIntegrationTargetContentInput(
		ctx,
		executionstore.CreateBoundIntegrationTargetContentInput{
			Target: integrationstore.CreateIntegrationTargetInput{
				ProjectID: testProjectID, AgentID: agent.ID,
				IntegrationInstallID: install.ID, ProviderRef: target.ProviderRef,
				ProviderRefKind: target.ProviderRefKind, DisplayName: "Mutated thread",
				ProviderMetadata: json.RawMessage(`{"revision":2}`),
			},
			IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: false,
			BindingSource:    "channel_route",
			BindingMetadata:  json.RawMessage(`{"policy":"receive_only"}`),
			ProviderTenantID: install.ProviderTenantID, ProviderUserID: "stale-user",
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"stale"}]`),
			Metadata:      json.RawMessage(`{}`), DeliveryMode: executionstore.DeliveryModeQueued,
			IdempotencyKey: "stale-runtime-event",
			RuntimeLease: &executionstore.IntegrationRuntimeLeaseProof{
				IntegrationAppID: app.ID, UnitID: stale.ID, LeaseToken: stale.LeaseToken,
				LeaseGeneration: stale.LeaseGeneration,
			},
		},
	)
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("stale runtime binding replacement error = %v, want state conflict", err)
	}
	var displayName string
	var providerMetadata json.RawMessage
	var activeBinding uuid.UUID
	var staleInputs int
	if err := pool.QueryRow(
		ctx,
		`SELECT target.display_name, target.provider_metadata,
		        (SELECT active.id
		         FROM integration_target_bindings active
		         WHERE active.project_id = target.project_id
		           AND active.agent_id = $2
		           AND active.integration_target_id = target.id
		           AND active.integration_route_id = $3
		           AND active.revoked_at IS NULL),
		        (SELECT count(*) FROM agent_inputs input
		         WHERE input.project_id = target.project_id
		           AND input.agent_id = $2
		           AND input.input_idempotency_key = 'stale-runtime-event')
		 FROM integration_targets target
		 WHERE target.id = $1`,
		target.ID,
		agent.ID,
		route.ID,
	).Scan(&displayName, &providerMetadata, &activeBinding, &staleInputs); err != nil {
		t.Fatalf("load state after stale runtime rejection: %v", err)
	}
	if displayName != "Original thread" ||
		!sameJSON(providerMetadata, json.RawMessage(`{"revision":1}`)) ||
		activeBinding != uuid.UUID(binding.ID) || staleInputs != 0 {
		t.Fatalf(
			"stale runtime mutated state display=%q metadata=%s binding=%s inputs=%d",
			displayName,
			providerMetadata,
			activeBinding,
			staleInputs,
		)
	}
}

func TestChannelFoundationRetriesWhenAHandlerVersionIsUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, app, install := createChannelLifecycleFixture(t, ctx, store, "route-preflight")

	for _, handler := range []string{testChannelHandler, "missing_handler"} {
		if _, err := store.Integrations().CreateIntegrationRoute(
			ctx,
			integrationstore.CreateIntegrationRouteInput{
				ProjectID: testProjectID, IntegrationInstallID: install.ID,
				DeploymentKey: "preflight-" + handler, HandlerKey: handler, HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
			},
		); err != nil {
			t.Fatalf("create %s route: %v", handler, err)
		}
	}
	service := integration.NewChannelService(
		store.Execution(),
		store.Integrations(),
		integration.ChannelRouteHandlers{
			integration.ChannelRouteHandlerKey(testChannelHandler, 1): integration.ChannelRouteHandlerFunc(
				func(
					context.Context,
					integration.ChannelRouteContext,
					integration.ChannelInboundEnvelope,
				) (integration.ChannelRouteDecision, error) {
					return integration.ChannelRouteDecision{
						Accept: true, ProviderRef: "preflight-thread", ProviderRefKind: "thread",
						DeliveryMode: executionstore.DeliveryModeQueued,
						Attachments: []integration.ChannelAttachmentAction{{
							AgentID: agent.ID, SendAllowed: true,
						}},
					}, nil
				},
			),
		},
	)
	prepareCalls := 0
	_, err := service.ProcessInbound(ctx, integration.ProcessChannelInboundInput{
		IntegrationAppID: app.ID, Capabilities: testChannelCapabilities(testChannelProvider),
		Envelope: integration.ChannelInboundEnvelope{
			Version: integration.ChannelEnvelopeVersionV1, ProviderEventID: "preflight-event",
			ExternalTenantID: install.ProviderTenantID, ExternalAccountRef: install.ProviderAccountRef,
			EventType: "message.created", Conversation: integration.ChannelConversation{
				Ref: "preflight-thread", Kind: "thread",
			},
			Actor:         integration.ChannelActor{Ref: "preflight-user"},
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
		},
		PrepareContent: func(
			_ context.Context,
			content json.RawMessage,
		) (integration.MaterializeChannelInboundContentFunc, error) {
			return func(
				context.Context,
				integration.MaterializeChannelInboundContentInput,
			) (json.RawMessage, error) {
				prepareCalls++
				return content, nil
			}, nil
		},
	})
	if !errors.Is(err, integration.ErrChannelRouteHandlerUnavailable) {
		t.Fatalf("process error = %v, want unavailable handler", err)
	}
	if prepareCalls != 0 {
		t.Fatalf("content materialization calls = %d, want 0", prepareCalls)
	}
	var targets, bindings, inputs int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM integration_targets WHERE integration_install_id = $1),
  (SELECT count(*) FROM integration_target_bindings WHERE integration_install_id = $1),
  (SELECT count(*) FROM agent_inputs WHERE agent_id = $2 AND input_idempotency_key = 'preflight-event')
`, install.ID, agent.ID).Scan(&targets, &bindings, &inputs); err != nil {
		t.Fatalf("count route preflight mutations: %v", err)
	}
	if targets != 0 || bindings != 0 || inputs != 0 {
		t.Fatalf("unavailable handler mutations targets=%d bindings=%d inputs=%d", targets, bindings, inputs)
	}
}

func TestChannelFoundationPreflightsAggregateInputMetadata(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, app, install := createChannelLifecycleFixture(t, ctx, store, "metadata-preflight")

	if _, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "metadata-preflight", HandlerKey: testChannelHandler, HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	); err != nil {
		t.Fatalf("create metadata preflight route: %v", err)
	}
	largeObject := json.RawMessage(`{"value":"` + strings.Repeat("x", 150*1024) + `"}`)
	service := integration.NewChannelService(
		store.Execution(),
		store.Integrations(),
		integration.ChannelRouteHandlers{
			integration.ChannelRouteHandlerKey(testChannelHandler, 1): integration.ChannelRouteHandlerFunc(
				func(
					context.Context,
					integration.ChannelRouteContext,
					integration.ChannelInboundEnvelope,
				) (integration.ChannelRouteDecision, error) {
					return integration.ChannelRouteDecision{
						Accept: true, ProviderRef: "metadata-preflight-thread", ProviderRefKind: "thread",
						DeliveryMode: executionstore.DeliveryModeQueued,
						Attachments: []integration.ChannelAttachmentAction{{
							AgentID: agent.ID, SendAllowed: true, Metadata: largeObject,
						}},
					}, nil
				},
			),
		},
	)
	prepareCalls := 0
	result, err := service.ProcessInbound(ctx, integration.ProcessChannelInboundInput{
		IntegrationAppID: app.ID, Capabilities: testChannelCapabilities(testChannelProvider),
		Envelope: integration.ChannelInboundEnvelope{
			Version: integration.ChannelEnvelopeVersionV1, ProviderEventID: "metadata-preflight-event",
			ExternalTenantID: install.ProviderTenantID, ExternalAccountRef: install.ProviderAccountRef,
			EventType: "message.created", Conversation: integration.ChannelConversation{
				Ref: "metadata-preflight-thread", Kind: "thread",
			},
			Actor:         integration.ChannelActor{Ref: "metadata-preflight-user"},
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
			Metadata:      largeObject,
		},
		PrepareContent: func(
			_ context.Context,
			content json.RawMessage,
		) (integration.MaterializeChannelInboundContentFunc, error) {
			return func(
				context.Context,
				integration.MaterializeChannelInboundContentInput,
			) (json.RawMessage, error) {
				prepareCalls++
				return content, nil
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("process invalid route metadata: %v", err)
	}
	if len(result.FailedRoutes) != 1 ||
		!strings.Contains(result.FailedRoutes[0].Err.Error(), "channel input metadata exceeds") {
		t.Fatalf("aggregate metadata route failure = %+v", result.FailedRoutes)
	}
	if prepareCalls != 0 {
		t.Fatalf("content preparation calls = %d, want 0", prepareCalls)
	}
	var targets, bindings, inputs int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM integration_targets WHERE integration_install_id = $1),
  (SELECT count(*) FROM integration_target_bindings WHERE integration_install_id = $1),
  (SELECT count(*) FROM agent_inputs WHERE agent_id = $2 AND input_idempotency_key = 'metadata-preflight-event')
`, install.ID, agent.ID).Scan(&targets, &bindings, &inputs); err != nil {
		t.Fatalf("count metadata preflight mutations: %v", err)
	}
	if targets != 0 || bindings != 0 || inputs != 0 {
		t.Fatalf("metadata preflight mutations targets=%d bindings=%d inputs=%d", targets, bindings, inputs)
	}
}

func TestChannelFoundationLifecycleDeletion(t *testing.T) {
	t.Run("install after app disable", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		seedMigratedDB(t, ctx, pool)
		store := newSecretIntegrationStore(pool)
		_, _, app, install := createChannelLifecycleFixture(t, ctx, store, "install-delete")

		if _, err := pool.Exec(
			ctx,
			`UPDATE integration_apps SET state = 'disabled' WHERE id = $1`,
			app.ID,
		); err != nil {
			t.Fatalf("disable integration app: %v", err)
		}
		if err := store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, install.ID); err != nil {
			t.Fatalf("delete install after parent app disable: %v", err)
		}
		var deleted bool
		if err := pool.QueryRow(
			ctx,
			`SELECT deleted_at IS NOT NULL FROM integration_installs WHERE id = $1`,
			install.ID,
		).Scan(&deleted); err != nil {
			t.Fatalf("load deleted integration install: %v", err)
		}
		if !deleted {
			t.Fatal("integration install was not soft deleted")
		}
	})

	t.Run("project with active integration", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		seedMigratedDB(t, ctx, pool)
		store := newSecretIntegrationStore(pool)
		admin, _, app, install := createChannelLifecycleFixture(t, ctx, store, "project-delete")
		unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
			ctx,
			integrationstore.UpsertIntegrationRuntimeUnitInput{
				OrgID: testOrgID, IntegrationAppID: app.ID,
				ProjectID: testProjectID, IntegrationInstallID: install.ID,
				UnitKey: "project-runtime", RuntimeKind: "provider_socket",
				DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
				SpecRevision: 1,
			},
		)
		if err != nil {
			t.Fatalf("create project runtime unit: %v", err)
		}
		appUnit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
			ctx,
			integrationstore.UpsertIntegrationRuntimeUnitInput{
				OrgID: testOrgID, IntegrationAppID: app.ID,
				UnitKey: "project-owned-app-runtime", RuntimeKind: "provider_gateway",
				DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
				SpecRevision: 1,
			},
		)
		if err != nil {
			t.Fatalf("create project-owned app runtime unit: %v", err)
		}
		otherProject, err := store.Identity().CreateProjectForPrincipal(
			ctx,
			identitystore.CreateProjectForPrincipalInput{
				OrgID: testOrgID, Creator: identitystore.NewUserPrincipal(admin.ID),
				Name: "Other integration project", IdempotencyKey: "other-integration-project",
			},
		)
		if err != nil {
			t.Fatalf("create other project for project deletion: %v", err)
		}
		otherApp, err := store.Integrations().CreateIntegrationApp(
			ctx,
			integrationstore.CreateIntegrationAppInput{
				OrgID: testOrgID, OwnerProjectID: otherProject.ID, Provider: testChannelProvider,
				ProviderAppRef: "other-project-deletion-app", DisplayName: "Other project app",
				ConnectorKey: testChannelConnector, State: integrationstore.IntegrationAppStateActive,
			},
		)
		if err != nil {
			t.Fatalf("create other-project app for project deletion: %v", err)
		}
		otherUnit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
			ctx,
			integrationstore.UpsertIntegrationRuntimeUnitInput{
				OrgID: testOrgID, IntegrationAppID: otherApp.ID,
				UnitKey: "other-project-app-runtime", RuntimeKind: "provider_gateway",
				DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
				SpecRevision: 1,
			},
		)
		if err != nil {
			t.Fatalf("create other-project app runtime unit: %v", err)
		}

		if _, err := store.Organizations().DeleteProject(
			ctx,
			testOrgID,
			testProjectID,
			identitystore.NewUserPrincipal(admin.ID),
		); err != nil {
			t.Fatalf("delete project with active integration: %v", err)
		}
		assertChannelLifecycleRowsDeleted(t, ctx, pool, app.ID, install.ID)
		assertChannelRuntimeUnitDeleted(t, ctx, pool, unit.ID)
		assertChannelRuntimeUnitDeleted(t, ctx, pool, appUnit.ID)
		var otherDeleted bool
		if err := pool.QueryRow(
			ctx,
			`SELECT deleted_at IS NOT NULL FROM integration_runtime_units WHERE id = $1`,
			otherUnit.ID,
		).Scan(&otherDeleted); err != nil {
			t.Fatalf("load other-project integration runtime after project deletion: %v", err)
		}
		if otherDeleted {
			t.Fatal("project deletion removed another project's integration runtime")
		}
	})

	t.Run("organization with active integration", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		seedMigratedDB(t, ctx, pool)
		store := newSecretIntegrationStore(pool)
		admin, _, app, install := createChannelLifecycleFixture(t, ctx, store, "org-delete")
		unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
			ctx,
			integrationstore.UpsertIntegrationRuntimeUnitInput{
				OrgID: testOrgID, IntegrationAppID: app.ID,
				UnitKey: "org-runtime", RuntimeKind: "provider_gateway",
				DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
				SpecRevision: 1,
			},
		)
		if err != nil {
			t.Fatalf("create organization runtime unit: %v", err)
		}

		if _, err := store.Organizations().DeleteOrganization(
			ctx,
			testOrgID,
			identitystore.NewUserPrincipal(admin.ID),
		); err != nil {
			t.Fatalf("delete organization with active integration: %v", err)
		}
		assertChannelLifecycleRowsDeleted(t, ctx, pool, app.ID, install.ID)
		assertChannelRuntimeUnitDeleted(t, ctx, pool, unit.ID)
	})
}

func TestChannelFoundationInstallDeletionRevokesConcurrentBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "delete-binding-race")
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "delete-binding-race", HandlerKey: testChannelHandler, HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create deletion-race route: %v", err)
	}
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "delete-binding-race-target",
			ProviderRefKind: "thread", DisplayName: "Deletion race",
		},
	)
	if err != nil {
		t.Fatalf("create deletion-race target: %v", err)
	}

	creatorTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin concurrent binding create: %v", err)
	}
	t.Cleanup(func() { _ = creatorTx.Rollback(ctx) })
	binding, err := store.Integrations().CreateIntegrationTargetBindingTx(
		ctx,
		creatorTx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: true,
			Source: "concurrent-create",
		},
	)
	if err != nil {
		t.Fatalf("create uncommitted concurrent binding: %v", err)
	}

	access := &blockingDeleteInstallAccess{
		reachedClear:  make(chan struct{}),
		continueClear: make(chan struct{}),
	}
	deletingStore := integrationstore.New(pool, access)
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- deletingStore.DeleteIntegrationInstall(ctx, testProjectID, install.ID)
	}()

	select {
	case <-access.reachedClear:
		close(access.continueClear)
	case <-time.After(500 * time.Millisecond):
		// Older target-before-agent ordering blocks before the observer. Releasing
		// the creator lets deletion finish so the final assertion exposes an
		// active orphan instead of hanging the test.
		close(access.continueClear)
	}
	if err := creatorTx.Commit(ctx); err != nil {
		t.Fatalf("commit concurrent binding create: %v", err)
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("delete install racing with binding create: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete install did not finish after concurrent binding commit")
	}

	var revoked, targetDeleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT binding.revoked_at IS NOT NULL, target.deleted_at IS NOT NULL
		 FROM integration_target_bindings binding
		 JOIN integration_targets target ON target.id = binding.integration_target_id
		 WHERE binding.id = $1`,
		binding.ID,
	).Scan(&revoked, &targetDeleted); err != nil {
		t.Fatalf("load concurrent binding deletion state: %v", err)
	}
	if !revoked || !targetDeleted {
		t.Fatalf("concurrent binding deletion revoked=%t target_deleted=%t", revoked, targetDeleted)
	}
}

func TestChannelFoundationInstallDeletionFencesConcurrentTargetCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "delete-target-race")

	creatorTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin concurrent target create: %v", err)
	}
	t.Cleanup(func() { _ = creatorTx.Rollback(ctx) })
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBindingTx(
		ctx,
		creatorTx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "delete-target-race-target",
			ProviderRefKind: "thread", DisplayName: "Deletion target race",
		},
	)
	if err != nil {
		t.Fatalf("create uncommitted concurrent target: %v", err)
	}

	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, install.ID)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked bool
		if err := pool.QueryRow(
			ctx,
			`SELECT EXISTS (
			   SELECT 1
			   FROM pg_stat_activity
			   WHERE datname = current_database()
			     AND pid <> pg_backend_pid()
			     AND wait_event_type = 'Lock'
			     AND query LIKE '%UPDATE integration_installs%credential_secret_id = NULL%'
			 )`,
		).Scan(&blocked); err != nil {
			t.Fatalf("observe blocked integration install deletion: %v", err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("integration install deletion did not wait for target creator authority")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := creatorTx.Commit(ctx); err != nil {
		t.Fatalf("commit concurrent target create: %v", err)
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("delete install racing with target create: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete install did not finish after concurrent target commit")
	}

	var deleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT deleted_at IS NOT NULL FROM integration_targets WHERE id = $1`,
		target.ID,
	).Scan(&deleted); err != nil {
		t.Fatalf("load concurrent target deletion state: %v", err)
	}
	if !deleted {
		t.Fatal("target committed before install deletion remained active")
	}
}

func TestChannelFoundationAppCredentialContractIsImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, app, _ := createChannelLifecycleFixture(t, ctx, store, "immutable-app-contract")

	_, err := pool.Exec(
		ctx,
		`UPDATE integration_apps SET installation_credential_kind = 'generic' WHERE id = $1`,
		app.ID,
	)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatalf("change integration app credential contract error = %v, want SQLSTATE 25006", err)
	}
}

func TestChannelFoundationRouteDefinitionIsImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, _, install := createChannelLifecycleFixture(t, ctx, store, "immutable-route")
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "immutable-route", HandlerKey: testChannelHandler, HandlerVersion: 1,
			Configuration: json.RawMessage(`{"mode":"mentions"}`), State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create immutable route: %v", err)
	}
	replayed, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "immutable-route", HandlerKey: testChannelHandler, HandlerVersion: 1,
			Configuration: json.RawMessage(`{ "mode": "mentions" }`), State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil || replayed.ID != route.ID {
		t.Fatalf("canonical route create replay = %+v, %v", replayed, err)
	}
	if _, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "immutable-route", HandlerKey: testChannelHandler, HandlerVersion: 2,
			Configuration: json.RawMessage(`{"mode":"mentions"}`), State: integrationstore.IntegrationRouteStateActive,
		},
	); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("changed route create replay error = %v, want idempotency conflict", err)
	}
	_, err = pool.Exec(
		ctx,
		`UPDATE integration_routes SET configuration = '{"mode":"all"}'::jsonb WHERE id = $1`,
		route.ID,
	)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatalf("change integration route definition error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_routes SET state = 'disabled' WHERE id = $1`,
		route.ID,
	); err != nil {
		t.Fatalf("disable immutable route: %v", err)
	}
	disabledReplay, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "immutable-route", HandlerKey: testChannelHandler, HandlerVersion: 1,
			Configuration: json.RawMessage(`{"mode":"mentions"}`), State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil || disabledReplay.ID != route.ID ||
		disabledReplay.State != integrationstore.IntegrationRouteStateDisabled {
		t.Fatalf("disabled route create replay changed lifecycle = %+v, %v", disabledReplay, err)
	}

}

func TestChannelFoundationTargetAndBindingDefinitionsAreImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "immutable-address")

	createRoute := func(handler string) integrationstore.IntegrationRouteRecord {
		t.Helper()
		route, err := store.Integrations().CreateIntegrationRoute(
			ctx,
			integrationstore.CreateIntegrationRouteInput{
				ProjectID: testProjectID, IntegrationInstallID: install.ID,
				DeploymentKey: "route-" + handler, HandlerKey: handler, HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
			},
		)
		if err != nil {
			t.Fatalf("create %s route: %v", handler, err)
		}
		return route
	}
	route := createRoute("immutable_address_primary")
	alternateRoute := createRoute("immutable_address_alternate")
	if _, err := store.Integrations().CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "legacy-shaped-connector-target",
			ProviderRefKind: "thread",
		},
	); err == nil {
		t.Fatal("legacy target creation accepted a connector installation")
	}
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "immutable-address-thread",
			ProviderRefKind: "thread", DisplayName: "Original address",
		},
	)
	if err != nil {
		t.Fatalf("create immutable target: %v", err)
	}
	if !isNilID(target.AgentID) {
		t.Fatalf("connector target retained creator agent %s", target.AgentID)
	}
	binding, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: true,
			Source: "test", Metadata: json.RawMessage(`{"scope":"thread"}`),
		},
	)
	if err != nil {
		t.Fatalf("create immutable binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_inputs(
  project_id, agent_id, state, input_kind, delivery_mode,
  integration_target_id, idempotency_scope, input_idempotency_key,
  queued_at, metadata
)
VALUES (
  $1, $2, 'received', 'content', 'queued', $3,
  'missing-binding-provenance', 'missing-binding-provenance',
  statement_timestamp(), '{}'::jsonb
)
`, testProjectID, agent.ID, target.ID); !isPgCode(err, "23514") {
		t.Fatalf("connector input without binding error = %v, want SQLSTATE 23514", err)
	}
	var targetXmin, bindingXmin string
	var targetUpdatedAt, bindingUpdatedAt time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT xmin::text, updated_at FROM integration_targets WHERE id = $1`,
		target.ID,
	).Scan(&targetXmin, &targetUpdatedAt); err != nil {
		t.Fatalf("load target replay markers: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT xmin::text, updated_at FROM integration_target_bindings WHERE id = $1`,
		binding.ID,
	).Scan(&bindingXmin, &bindingUpdatedAt); err != nil {
		t.Fatalf("load binding replay markers: %v", err)
	}
	replayedTarget, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "immutable-address-thread",
			ProviderRefKind: "thread", DisplayName: "Original address",
		},
	)
	if err != nil || replayedTarget.ID != target.ID {
		t.Fatalf("replay immutable target = %+v, %v", replayedTarget, err)
	}
	replayedBinding, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: true,
			Source: "test", Metadata: json.RawMessage(`{"scope":"thread"}`),
		},
	)
	if err != nil || replayedBinding.ID != binding.ID {
		t.Fatalf("replay immutable binding = %+v, %v", replayedBinding, err)
	}
	var replayedTargetXmin, replayedBindingXmin string
	var replayedTargetUpdatedAt, replayedBindingUpdatedAt time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT xmin::text, updated_at FROM integration_targets WHERE id = $1`,
		target.ID,
	).Scan(&replayedTargetXmin, &replayedTargetUpdatedAt); err != nil {
		t.Fatalf("reload target replay markers: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT xmin::text, updated_at FROM integration_target_bindings WHERE id = $1`,
		binding.ID,
	).Scan(&replayedBindingXmin, &replayedBindingUpdatedAt); err != nil {
		t.Fatalf("reload binding replay markers: %v", err)
	}
	if replayedTargetXmin != targetXmin || !replayedTargetUpdatedAt.Equal(targetUpdatedAt) {
		t.Fatalf("exact target replay wrote the row: xmin %s -> %s, updated %s -> %s",
			targetXmin, replayedTargetXmin, targetUpdatedAt, replayedTargetUpdatedAt)
	}
	if replayedBindingXmin != bindingXmin || !replayedBindingUpdatedAt.Equal(bindingUpdatedAt) {
		t.Fatalf("exact binding replay wrote the row: xmin %s -> %s, updated %s -> %s",
			bindingXmin, replayedBindingXmin, bindingUpdatedAt, replayedBindingUpdatedAt)
	}
	metadataRefresh, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "immutable-address-thread",
			ProviderRefKind: "thread", ProviderMetadata: json.RawMessage(`{"fresh":true}`),
		},
	)
	if err != nil {
		t.Fatalf("refresh target provider metadata without a display name: %v", err)
	}
	if metadataRefresh.DisplayName != "Original address" ||
		!sameJSON(metadataRefresh.ProviderMetadata, json.RawMessage(`{"fresh":true}`)) {
		t.Fatalf("metadata-only target refresh = %+v", metadataRefresh)
	}
	omittedMetadata, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "immutable-address-thread",
			ProviderRefKind: "thread",
		},
	)
	if err != nil || !sameJSON(
		omittedMetadata.ProviderMetadata,
		json.RawMessage(`{"fresh":true}`),
	) {
		t.Fatalf("omitted target metadata replay = %+v, %v", omittedMetadata, err)
	}
	clearedMetadata, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "immutable-address-thread",
			ProviderRefKind: "thread", ProviderMetadata: json.RawMessage(`{}`),
		},
	)
	if err != nil || !sameJSON(clearedMetadata.ProviderMetadata, json.RawMessage(`{}`)) {
		t.Fatalf("explicit target metadata clear = %+v, %v", clearedMetadata, err)
	}
	replacement, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: false,
			Source: "test", Metadata: json.RawMessage(`{"scope":"read_only"}`),
		},
	)
	if err != nil || replacement.ID == binding.ID {
		t.Fatalf("replace immutable binding = %+v, %v", replacement, err)
	}
	var oldRevoked, replacementRevoked bool
	if err := pool.QueryRow(
		ctx,
		`SELECT old_binding.revoked_at IS NOT NULL, new_binding.revoked_at IS NOT NULL
		 FROM integration_target_bindings old_binding
		 JOIN integration_target_bindings new_binding ON new_binding.id = $2
		 WHERE old_binding.id = $1`,
		binding.ID,
		replacement.ID,
	).Scan(&oldRevoked, &replacementRevoked); err != nil {
		t.Fatalf("load binding replacement lifecycle: %v", err)
	}
	if !oldRevoked || replacementRevoked {
		t.Fatalf("binding replacement lifecycle old_revoked=%v new_revoked=%v", oldRevoked, replacementRevoked)
	}

	_, err = pool.Exec(
		ctx,
		`UPDATE integration_targets SET provider_ref = 'rewritten-address' WHERE id = $1`,
		target.ID,
	)
	if !isPgCode(err, "25006") {
		t.Fatalf("change integration target identity error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_targets
		 SET display_name = 'Refreshed address', provider_metadata = '{"fresh":true}'::jsonb,
		     updated_at = statement_timestamp()
		 WHERE id = $1`,
		target.ID,
	); err != nil {
		t.Fatalf("refresh mutable integration target metadata: %v", err)
	}

	_, err = pool.Exec(
		ctx,
		`UPDATE integration_target_bindings SET integration_route_id = $2 WHERE id = $1`,
		replacement.ID,
		alternateRoute.ID,
	)
	if !isPgCode(err, "25006") {
		t.Fatalf("change integration binding route error = %v, want SQLSTATE 25006", err)
	}
	_, err = pool.Exec(
		ctx,
		`UPDATE integration_target_bindings SET send_allowed = true WHERE id = $1`,
		replacement.ID,
	)
	if !isPgCode(err, "25006") {
		t.Fatalf("change integration binding permission error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_target_bindings
		 SET revoked_at = statement_timestamp(), updated_at = statement_timestamp()
		 WHERE id = $1`,
		replacement.ID,
	); err != nil {
		t.Fatalf("revoke immutable integration binding: %v", err)
	}
	_, err = pool.Exec(
		ctx,
		`UPDATE integration_target_bindings
		 SET revoked_at = NULL, updated_at = statement_timestamp()
		 WHERE id = $1`,
		replacement.ID,
	)
	if !isPgCode(err, "25006") {
		t.Fatalf("reopen revoked integration binding error = %v, want SQLSTATE 25006", err)
	}
}

func TestDeleteIntegrationRouteRevokesOnlyItsBindings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "delete-single-route")
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "delete-single-route-channel",
			ProviderRefKind: "channel",
		},
	)
	if err != nil {
		t.Fatalf("create route lifecycle target: %v", err)
	}
	createRouteAndBinding := func(label string) (
		integrationstore.IntegrationRouteRecord,
		integrationstore.IntegrationTargetBindingRecord,
	) {
		t.Helper()
		route, err := store.Integrations().CreateIntegrationRoute(
			ctx,
			integrationstore.CreateIntegrationRouteInput{
				ProjectID: testProjectID, IntegrationInstallID: install.ID,
				DeploymentKey: "delete-single-route-" + label,
				HandlerKey:    "delete_single_route", HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
			},
		)
		if err != nil {
			t.Fatalf("create %s route: %v", label, err)
		}
		binding, err := store.Integrations().CreateIntegrationTargetBinding(
			ctx,
			integrationstore.CreateIntegrationTargetBindingInput{
				ProjectID: testProjectID, AgentID: agent.ID,
				IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
				IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: true,
				Source: "test",
			},
		)
		if err != nil {
			t.Fatalf("create %s binding: %v", label, err)
		}
		return route, binding
	}
	deletedRoute, deletedBinding := createRouteAndBinding("deleted")
	siblingRoute, siblingBinding := createRouteAndBinding("sibling")

	if err := store.Integrations().DeleteIntegrationRoute(
		ctx,
		testProjectID,
		install.ID,
		deletedRoute.ID,
	); err != nil {
		t.Fatalf("delete one integration route: %v", err)
	}
	if err := store.Integrations().DeleteIntegrationRoute(
		ctx,
		testProjectID,
		install.ID,
		deletedRoute.ID,
	); err != nil {
		t.Fatalf("replay integration route delete: %v", err)
	}

	var deletedBindingRevoked bool
	if err := pool.QueryRow(
		ctx,
		`SELECT revoked_at IS NOT NULL FROM integration_target_bindings
WHERE project_id = $1 AND id = $2`,
		testProjectID,
		deletedBinding.ID,
	).Scan(&deletedBindingRevoked); err != nil {
		t.Fatalf("load deleted route binding history: %v", err)
	}
	if !deletedBindingRevoked {
		t.Fatal("deleted route binding remains active")
	}
	if _, err := store.Integrations().GetActiveSendBinding(
		ctx,
		testProjectID,
		agent.ID,
		deletedBinding.ID,
	); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("deleted route send binding error = %v, want not found", err)
	}
	if active, err := store.Integrations().GetActiveSendBinding(
		ctx,
		testProjectID,
		agent.ID,
		siblingBinding.ID,
	); err != nil || active.ID != siblingBinding.ID {
		t.Fatalf("sibling route send binding = %+v, %v", active, err)
	}
	routes, err := store.Integrations().ListActiveIntegrationRoutes(
		ctx,
		testProjectID,
		install.ID,
	)
	if err != nil || len(routes) != 1 || routes[0].ID != siblingRoute.ID {
		t.Fatalf("active routes after single delete = %+v, %v", routes, err)
	}
	channels, err := store.Integrations().ListAgentChannelTargets(
		ctx,
		testProjectID,
		agent.ID,
		integrationstore.ListAgentChannelTargetsInput{Limit: 10},
	)
	if err != nil || len(channels.Targets) != 1 || channels.Targets[0].ID != target.ID ||
		!channels.Targets[0].ReceiveAllowed || !channels.Targets[0].SendAllowed {
		t.Fatalf("sibling route channel authority = %+v, %v", channels, err)
	}
	var deletedState, siblingState string
	var deletedAt, siblingDeleted bool
	if err := pool.QueryRow(ctx, `
SELECT deleted.state, deleted.deleted_at IS NOT NULL,
       sibling.state, sibling.deleted_at IS NOT NULL
FROM integration_routes deleted
JOIN integration_routes sibling ON sibling.id = $2
WHERE deleted.id = $1
`, deletedRoute.ID, siblingRoute.ID).Scan(
		&deletedState, &deletedAt, &siblingState, &siblingDeleted,
	); err != nil {
		t.Fatalf("load route deletion lifecycle: %v", err)
	}
	if deletedState != "disabled" || !deletedAt || siblingState != "active" || siblingDeleted {
		t.Fatalf(
			"route lifecycle deleted=%q/%t sibling=%q/%t",
			deletedState, deletedAt, siblingState, siblingDeleted,
		)
	}
}

func TestChannelFoundationReceiveBindingLimitIsWriteSafe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "binding-limit@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "binding-limit-profile")

	agents := make([]executionstore.AgentRecord, integrationstore.MaxActiveReceiveBindingsPerTargetRoute+3)
	for index := range agents {
		agents[index] = createIntegrationBoundAgent(
			t,
			ctx,
			store,
			profile,
			admin.ID,
			fmt.Sprintf("binding-limit-agent-%03d", index),
		)
	}
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: testChannelProvider, ProviderAppRef: "binding-limit-app",
			DisplayName: "Binding limit", ConnectorKey: testChannelConnector,
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create binding-limit app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
			InstalledByUserID: admin.ID,
			Provider:          testChannelProvider, IntegrationKind: "binding_limit",
			ConnectionMode: "gateway", State: integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "binding-limit-tenant", ProviderAccountRef: "binding-limit-account",
			ProviderAgentDisplayName: "Binding limit bot",
		},
	)
	if err != nil {
		t.Fatalf("create binding-limit install: %v", err)
	}
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: "binding-limit", HandlerKey: testChannelHandler, HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create binding-limit route: %v", err)
	}
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agents[0].ID,
			IntegrationInstallID: install.ID, ProviderRef: "binding-limit-channel",
			ProviderRefKind: "channel", DisplayName: "Binding limit channel",
		},
	)
	if err != nil {
		t.Fatalf("create binding-limit target: %v", err)
	}
	bindingInput := func(
		agentID ID,
		receiveAllowed, sendAllowed bool,
		source string,
	) integrationstore.CreateIntegrationTargetBindingInput {
		return integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agentID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: receiveAllowed,
			SendAllowed: sendAllowed, Source: source,
		}
	}

	sendOnly, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		bindingInput(agents[0].ID, false, true, "send-only"),
	)
	if err != nil {
		t.Fatalf("create send-only capacity candidate: %v", err)
	}
	for index := 1; index <= integrationstore.MaxActiveReceiveBindingsPerTargetRoute-1; index++ {
		if _, err := store.Integrations().CreateIntegrationTargetBinding(
			ctx,
			bindingInput(agents[index].ID, true, true, "initial-receive"),
		); err != nil {
			t.Fatalf("create receive binding %d: %v", index, err)
		}
	}

	type createResult struct {
		agentID ID
		binding integrationstore.IntegrationTargetBindingRecord
		err     error
	}
	results := make(chan createResult, 2)
	for _, agent := range agents[integrationstore.MaxActiveReceiveBindingsPerTargetRoute : integrationstore.MaxActiveReceiveBindingsPerTargetRoute+2] {
		agent := agent
		go func() {
			binding, createErr := store.Integrations().CreateIntegrationTargetBinding(
				ctx,
				bindingInput(agent.ID, true, true, "concurrent-receive"),
			)
			results <- createResult{agentID: agent.ID, binding: binding, err: createErr}
		}()
	}
	var winner createResult
	var successes, capacityFailures int
	for range 2 {
		result := <-results
		if result.err == nil {
			winner = result
			successes++
		} else if errors.Is(result.err, storeerr.ErrInvalidRequest) {
			capacityFailures++
		} else {
			t.Fatalf("concurrent receive binding failed unexpectedly: %v", result.err)
		}
	}
	if successes != 1 || capacityFailures != 1 {
		t.Fatalf("concurrent capacity boundary successes=%d failures=%d", successes, capacityFailures)
	}

	replayed, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		bindingInput(winner.agentID, true, true, "concurrent-receive"),
	)
	if err != nil || replayed.ID != winner.binding.ID {
		t.Fatalf("replay at receive-binding capacity = %+v, %v", replayed, err)
	}
	if _, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		bindingInput(
			agents[integrationstore.MaxActiveReceiveBindingsPerTargetRoute+2].ID,
			true,
			true,
			"sequential-overflow",
		),
	); !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("sequential receive binding over capacity error = %v", err)
	}
	if _, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		bindingInput(agents[0].ID, true, true, "send-to-receive-at-capacity"),
	); !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("send-only to receive replacement at capacity error = %v", err)
	}
	stillSendOnly, err := store.Integrations().GetIntegrationTargetBinding(
		ctx,
		testProjectID,
		sendOnly.ID,
	)
	if err != nil || stillSendOnly.ReceiveAllowed || !stillSendOnly.SendAllowed {
		t.Fatalf("failed capacity replacement changed send-only binding = %+v, %v", stillSendOnly, err)
	}

	receiveReplacement, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		bindingInput(winner.agentID, true, false, "receive-to-receive"),
	)
	if err != nil || receiveReplacement.ID == winner.binding.ID {
		t.Fatalf("receive-to-receive replacement at capacity = %+v, %v", receiveReplacement, err)
	}
	if _, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		bindingInput(winner.agentID, false, true, "receive-to-send"),
	); err != nil {
		t.Fatalf("replace receive binding with send-only binding: %v", err)
	}
	if _, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		bindingInput(agents[0].ID, true, true, "send-to-receive-after-space"),
	); err != nil {
		t.Fatalf("replace send-only binding after capacity freed: %v", err)
	}

	var activeReceiveBindings int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*)
		 FROM integration_target_bindings
		 WHERE project_id = $1
		   AND integration_target_id = $2
		   AND integration_route_id = $3
		   AND receive_allowed
		   AND revoked_at IS NULL`,
		testProjectID,
		target.ID,
		route.ID,
	).Scan(&activeReceiveBindings); err != nil {
		t.Fatalf("count final active receive bindings: %v", err)
	}
	if activeReceiveBindings != integrationstore.MaxActiveReceiveBindingsPerTargetRoute {
		t.Fatalf("final active receive bindings = %d", activeReceiveBindings)
	}
}

func createChannelLifecycleFixture(
	t *testing.T,
	ctx context.Context,
	store *Store,
	suffix string,
) (
	identitystore.UserRecord,
	executionstore.AgentRecord,
	integrationstore.IntegrationAppRecord,
	integrationstore.IntegrationInstallRecord,
) {
	t.Helper()
	admin := createIntegrationProjectAdmin(t, ctx, store, suffix+"@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, suffix+"-profile")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, suffix+"-agent")
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: testChannelProvider, ProviderAppRef: suffix + "-app",
			DisplayName: suffix, ConnectorKey: testChannelConnector,
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create lifecycle integration app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
			InstalledByUserID: admin.ID,
			Provider:          testChannelProvider, IntegrationKind: "lifecycle_test",
			ConnectionMode: "gateway", State: integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: suffix + "-tenant", ProviderAccountRef: suffix + "-account",
			ProviderAgentDisplayName: suffix,
		},
	)
	if err != nil {
		t.Fatalf("create lifecycle integration install: %v", err)
	}
	return admin, agent, app, install
}

func assertChannelLifecycleRowsDeleted(
	t *testing.T,
	ctx context.Context,
	pool interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	appID, installID integrationstore.ID,
) {
	t.Helper()
	var appDeleted, installDeleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT app.deleted_at IS NOT NULL, install.deleted_at IS NOT NULL
		 FROM integration_apps app
		 JOIN integration_installs install ON install.integration_app_id = app.id
		 WHERE app.id = $1 AND install.id = $2`,
		appID,
		installID,
	).Scan(&appDeleted, &installDeleted); err != nil {
		t.Fatalf("load integration lifecycle rows: %v", err)
	}
	if !appDeleted || !installDeleted {
		t.Fatalf("integration lifecycle deletion app=%t install=%t, want both true", appDeleted, installDeleted)
	}
}

func assertChannelRuntimeUnitDeleted(
	t *testing.T,
	ctx context.Context,
	pool interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	unitID integrationstore.ID,
) {
	t.Helper()
	var desiredState, status string
	var deleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT desired_state, status, deleted_at IS NOT NULL
		 FROM integration_runtime_units WHERE id = $1`,
		unitID,
	).Scan(&desiredState, &status, &deleted); err != nil {
		t.Fatalf("load integration runtime lifecycle row: %v", err)
	}
	if desiredState != "stopped" || status != "stopped" || !deleted {
		t.Fatalf("runtime lifecycle state = %s/%s deleted=%t", desiredState, status, deleted)
	}
}
