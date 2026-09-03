//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestChannelRouteSelectsExistingBindingsWithoutOwningAnAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "channel-existing@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "channel-existing")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "channel-existing")
	app, install := createDestinationlessChannelInstall(t, ctx, store, admin.ID, "existing")
	route := createChannelAttachmentTestRoute(t, ctx, store, install, "existing")
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID, IntegrationInstallID: install.ID,
			ProviderRef: "known-thread", ProviderRefKind: "thread",
		},
	)
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	binding, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agent.ID, IntegrationInstallID: install.ID,
			IntegrationTargetID: target.ID, IntegrationRouteID: route.ID,
			ReceiveAllowed: true, SendAllowed: true, Source: "existing",
		},
	)
	if err != nil {
		t.Fatalf("create binding: %v", err)
	}

	service := newChannelAttachmentTestService(
		store,
		func(
			ctx context.Context,
			routeContext integration.ChannelRouteContext,
			envelope integration.ChannelInboundEnvelope,
		) (integration.ChannelRouteDecision, error) {
			bindings, err := routeContext.Bindings.ListActiveForTarget(
				ctx,
				envelope.Conversation.Ref,
				"thread",
			)
			if err != nil {
				return integration.ChannelRouteDecision{}, err
			}
			ids := make([]integrationstore.ID, 0, len(bindings))
			for _, candidate := range bindings {
				ids = append(ids, candidate.ID)
			}
			return integration.ChannelRouteDecision{
				Accept: true, ProviderRef: envelope.Conversation.Ref, ProviderRefKind: "thread",
				ExistingBindingIDs: ids, DeliveryMode: executionstore.DeliveryModeQueued,
			}, nil
		},
	)
	accepted := processChannelAttachmentTestEvent(
		t,
		ctx,
		service,
		app,
		install,
		"known-event",
		"known-thread",
	)
	if len(accepted.Accepted) != 1 || accepted.Accepted[0].AgentID != agent.ID ||
		accepted.Accepted[0].BindingID != binding.ID {
		t.Fatalf("existing-binding acceptance = %+v", accepted)
	}
	ignored := processChannelAttachmentTestEvent(
		t,
		ctx,
		service,
		app,
		install,
		"unknown-event",
		"unknown-thread",
	)
	if len(ignored.Accepted) != 0 || ignored.IgnoredRoutes != 1 {
		t.Fatalf("unbound target result = %+v", ignored)
	}
}

func TestChannelProfileInstanceAttachesOneAgentToMultipleTargets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "channel-multi-target@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "channel-multi-target")
	app, install := createDestinationlessChannelInstall(t, ctx, store, admin.ID, "multi-target")
	createChannelAttachmentTestRoute(t, ctx, store, install, "multi-target")
	service := newChannelAttachmentTestService(
		store,
		func(
			_ context.Context,
			_ integration.ChannelRouteContext,
			envelope integration.ChannelInboundEnvelope,
		) (integration.ChannelRouteDecision, error) {
			return integration.ChannelRouteDecision{
				Accept: true, ProviderRef: envelope.Conversation.Ref, ProviderRefKind: "thread",
				DeliveryMode: executionstore.DeliveryModeQueued,
				Attachments: []integration.ChannelAttachmentAction{{
					AgentProfileID: profile.ID, InstanceKey: "customer-42", SendAllowed: true,
				}},
			}, nil
		},
	)
	first := processChannelAttachmentTestEvent(t, ctx, service, app, install, "multi-a", "thread-a")
	second := processChannelAttachmentTestEvent(t, ctx, service, app, install, "multi-b", "thread-b")
	if len(first.Accepted) != 1 || len(second.Accepted) != 1 {
		t.Fatalf("multi-target results first=%+v second=%+v", first, second)
	}
	if first.Accepted[0].AgentID != second.Accepted[0].AgentID ||
		first.Accepted[0].TargetID == second.Accepted[0].TargetID ||
		first.Accepted[0].BindingID == second.Accepted[0].BindingID {
		t.Fatalf("stable profile instance was not attached across targets: first=%+v second=%+v", first, second)
	}
}

func TestChannelInboundReplayPreservesHistoricalBindingAfterReplacement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "channel-replay@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "channel-replay")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "channel-replay")
	app, install := createDestinationlessChannelInstall(t, ctx, store, admin.ID, "replay")
	createChannelAttachmentTestRoute(t, ctx, store, install, "replay")
	service := newChannelAttachmentTestService(
		store,
		func(
			_ context.Context,
			_ integration.ChannelRouteContext,
			envelope integration.ChannelInboundEnvelope,
		) (integration.ChannelRouteDecision, error) {
			return integration.ChannelRouteDecision{
				Accept: true, ProviderRef: envelope.Conversation.Ref, ProviderRefKind: "thread",
				DeliveryMode: executionstore.DeliveryModeQueued,
				Attachments: []integration.ChannelAttachmentAction{{
					AgentID: agent.ID, SendAllowed: envelope.ProviderEventID != "event-one",
					Metadata: json.RawMessage(`{"policy":"` + envelope.ProviderEventID + `"}`),
				}},
			}, nil
		},
	)
	first := processChannelAttachmentTestEvent(t, ctx, service, app, install, "event-one", "thread")
	second := processChannelAttachmentTestEvent(t, ctx, service, app, install, "event-two", "thread")
	replayed := processChannelAttachmentTestEvent(t, ctx, service, app, install, "event-one", "thread")
	if len(first.Accepted) != 1 || len(second.Accepted) != 1 || len(replayed.Accepted) != 1 {
		t.Fatalf("binding replay results first=%+v second=%+v replay=%+v", first, second, replayed)
	}
	if first.Accepted[0].BindingID == second.Accepted[0].BindingID ||
		replayed.Accepted[0].BindingID != first.Accepted[0].BindingID ||
		replayed.Accepted[0].AgentInputID != first.Accepted[0].AgentInputID {
		t.Fatalf("historical binding provenance changed: first=%+v second=%+v replay=%+v", first, second, replayed)
	}
	active, err := store.Integrations().GetActiveSendBindingForTarget(
		ctx,
		testProjectID,
		agent.ID,
		second.Accepted[0].TargetID,
	)
	if err != nil {
		t.Fatalf("load replacement binding after replay: %v", err)
	}
	if active.ID != second.Accepted[0].BindingID {
		t.Fatalf("replay replaced the current binding: got %s want %s", active.ID, second.Accepted[0].BindingID)
	}
	var firstRevoked, secondRevoked bool
	var firstInputBinding, secondInputBinding integrationstore.ID
	var secondInputMetadata json.RawMessage
	if err := pool.QueryRow(ctx, `
SELECT first_binding.revoked_at IS NOT NULL,
       second_binding.revoked_at IS NOT NULL,
       first_input.integration_target_binding_id,
       second_input.integration_target_binding_id,
       second_input.metadata
FROM integration_target_bindings first_binding
JOIN integration_target_bindings second_binding
  ON second_binding.project_id = first_binding.project_id AND second_binding.id = $2
JOIN agent_inputs first_input
  ON first_input.project_id = first_binding.project_id AND first_input.id = $3
JOIN agent_inputs second_input
  ON second_input.project_id = first_binding.project_id AND second_input.id = $4
WHERE first_binding.project_id = $5 AND first_binding.id = $1
`, first.Accepted[0].BindingID, second.Accepted[0].BindingID,
		first.Accepted[0].AgentInputID, second.Accepted[0].AgentInputID, testProjectID,
	).Scan(
		&firstRevoked,
		&secondRevoked,
		&firstInputBinding,
		&secondInputBinding,
		&secondInputMetadata,
	); err != nil {
		t.Fatalf("load replacement binding history: %v", err)
	}
	if !firstRevoked || secondRevoked ||
		firstInputBinding != first.Accepted[0].BindingID ||
		secondInputBinding != second.Accepted[0].BindingID {
		t.Fatalf(
			"replacement history first_revoked=%t second_revoked=%t first_input=%s second_input=%s",
			firstRevoked,
			secondRevoked,
			firstInputBinding,
			secondInputBinding,
		)
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(secondInputMetadata, &metadata); err != nil {
		t.Fatalf("decode replacement input metadata: %v", err)
	}
	if !sameJSON(metadata["binding_metadata"], json.RawMessage(`{"policy":"event-two"}`)) {
		t.Fatalf("replacement input metadata = %s", secondInputMetadata)
	}
}

func TestChannelInboundRejectsConflictingKindsBeforeCreatingTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "channel-kind-conflict@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "channel-kind-conflict")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "channel-kind-conflict")
	app, install := createDestinationlessChannelInstall(t, ctx, store, admin.ID, "kind-conflict")
	createChannelAttachmentTestRoute(t, ctx, store, install, "kind-conflict-channel")
	createChannelAttachmentTestRoute(t, ctx, store, install, "kind-conflict-thread")

	service := newChannelAttachmentTestService(
		store,
		func(
			_ context.Context,
			routeContext integration.ChannelRouteContext,
			envelope integration.ChannelInboundEnvelope,
		) (integration.ChannelRouteDecision, error) {
			kind := "channel"
			if routeContext.Route.DeploymentKey == "kind-conflict-thread" {
				kind = "thread"
			}
			return integration.ChannelRouteDecision{
				Accept: true, ProviderRef: envelope.Conversation.Ref, ProviderRefKind: kind,
				DeliveryMode: executionstore.DeliveryModeQueued,
				Attachments: []integration.ChannelAttachmentAction{{
					AgentID: agent.ID, SendAllowed: true,
				}},
			}, nil
		},
	)
	result := processChannelAttachmentTestEvent(
		t,
		ctx,
		service,
		app,
		install,
		"kind-conflict-event",
		"same-provider-ref",
	)
	if len(result.Accepted) != 0 || len(result.FailedRoutes) != 2 {
		t.Fatalf("conflicting-kind result = %+v, want two failures and no acceptance", result)
	}
	if _, err := store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		testProjectID,
		install.ID,
		"same-provider-ref",
	); !storeerr.IsNotFound(err) {
		t.Fatalf("conflicting routes created target, lookup error = %v", err)
	}
}

func createChannelAttachmentTestRoute(
	t *testing.T,
	ctx context.Context,
	store *Store,
	install integrationstore.IntegrationInstallRecord,
	key string,
) integrationstore.IntegrationRouteRecord {
	t.Helper()
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			DeploymentKey: key, HandlerKey: testChannelHandler, HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create channel route: %v", err)
	}
	return route
}

func createDestinationlessChannelInstall(
	t *testing.T,
	ctx context.Context,
	store *Store,
	installedBy integrationstore.ID,
	suffix string,
) (integrationstore.IntegrationAppRecord, integrationstore.IntegrationInstallRecord) {
	t.Helper()
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
		t.Fatalf("create destinationless app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
			InstalledByUserID: installedBy, Provider: testChannelProvider,
			IntegrationKind: "custom_route", ConnectionMode: "gateway",
			State:            integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: suffix + "-tenant", ProviderAccountRef: suffix + "-account",
		},
	)
	if err != nil {
		t.Fatalf("create destinationless install: %v", err)
	}
	return app, install
}

type channelAttachmentDecision func(
	context.Context,
	integration.ChannelRouteContext,
	integration.ChannelInboundEnvelope,
) (integration.ChannelRouteDecision, error)

func newChannelAttachmentTestService(
	store *Store,
	decide channelAttachmentDecision,
) *integration.ChannelService {
	return integration.NewChannelService(
		store.Execution(),
		store.Integrations(),
		integration.ChannelRouteHandlers{
			integration.ChannelRouteHandlerKey(testChannelHandler, 1): integration.ChannelRouteHandlerFunc(decide),
		},
	)
}

func processChannelAttachmentTestEvent(
	t *testing.T,
	ctx context.Context,
	service *integration.ChannelService,
	app integrationstore.IntegrationAppRecord,
	install integrationstore.IntegrationInstallRecord,
	eventID, providerRef string,
) integration.ProcessChannelInboundResult {
	t.Helper()
	result, err := service.ProcessInbound(ctx, integration.ProcessChannelInboundInput{
		IntegrationAppID: app.ID,
		Capabilities:     testChannelCapabilities(testChannelProvider),
		Envelope: integration.ChannelInboundEnvelope{
			Version: integration.ChannelEnvelopeVersionV1, ProviderEventID: eventID,
			ExternalTenantID: install.ProviderTenantID, ExternalAccountRef: install.ProviderAccountRef,
			EventType: "message.created", Conversation: integration.ChannelConversation{Ref: providerRef},
			Actor:         integration.ChannelActor{Ref: "attachment-test-user"},
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
		},
		PrepareContent: passthroughChannelInboundContent,
	})
	if err != nil {
		t.Fatalf("process channel event %q: %v", eventID, err)
	}
	return result
}
