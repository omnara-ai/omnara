package integration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func passthroughChannelInboundContent(
	_ context.Context,
	content json.RawMessage,
) (MaterializeChannelInboundContentFunc, error) {
	return func(
		context.Context,
		MaterializeChannelInboundContentInput,
	) (json.RawMessage, error) {
		return content, nil
	}, nil
}

type integrationServiceExecutionStub struct{}

func (integrationServiceExecutionStub) GetAgentProfile(
	context.Context,
	executionstore.ID,
	executionstore.ID,
) (executionstore.AgentProfileRecord, error) {
	return executionstore.AgentProfileRecord{}, errors.New("unexpected GetAgentProfile call")
}

func (integrationServiceExecutionStub) LaunchAgent(
	context.Context,
	executionstore.LaunchAgentInput,
) (executionstore.LaunchAgentResult, error) {
	return executionstore.LaunchAgentResult{}, errors.New("unexpected LaunchAgent call")
}

type integrationServiceLaunchFailureStub struct {
	profile   executionstore.AgentProfileRecord
	launchErr error
}

func (s integrationServiceLaunchFailureStub) GetAgentProfile(
	context.Context,
	executionstore.ID,
	executionstore.ID,
) (executionstore.AgentProfileRecord, error) {
	return s.profile, nil
}

func (s integrationServiceLaunchFailureStub) LaunchAgent(
	context.Context,
	executionstore.LaunchAgentInput,
) (executionstore.LaunchAgentResult, error) {
	return executionstore.LaunchAgentResult{}, s.launchErr
}

type integrationServiceStoreStub struct {
	install       integrationstore.IntegrationInstallRecord
	lookupErr     error
	createdInput  integrationstore.CreateIntegrationTargetInput
	createdTarget integrationstore.IntegrationTargetRecord
}

type channelRouteRaceExecutionStub struct {
	createCalls int
	failAt      int
	failErr     error
}

type channelProfileCompletionExecutionStub struct {
	boundErr     error
	launchCalls  int
	launchErrors []error
	launches     []executionstore.LaunchAgentResult
}

func (s *channelProfileCompletionExecutionStub) GetAgentInProject(
	context.Context,
	executionstore.ID,
	executionstore.ID,
) (executionstore.AgentRecord, error) {
	return executionstore.AgentRecord{}, errors.New("unexpected GetAgentInProject call")
}

func (s *channelProfileCompletionExecutionStub) GetAgentProfile(
	context.Context,
	executionstore.ID,
	executionstore.ID,
) (executionstore.AgentProfileRecord, error) {
	return executionstore.AgentProfileRecord{}, errors.New("unexpected GetAgentProfile call")
}

func (s *channelProfileCompletionExecutionStub) LaunchAgent(
	context.Context,
	executionstore.LaunchAgentInput,
) (executionstore.LaunchAgentResult, error) {
	index := s.launchCalls
	s.launchCalls++
	if index < len(s.launchErrors) && s.launchErrors[index] != nil {
		return executionstore.LaunchAgentResult{}, s.launchErrors[index]
	}
	if index >= len(s.launches) {
		return executionstore.LaunchAgentResult{}, errors.New("unexpected LaunchAgent call")
	}
	return s.launches[index], nil
}

func (s *channelProfileCompletionExecutionStub) LaunchAgentWithIntegrationRuntimeLease(
	ctx context.Context,
	input executionstore.LaunchAgentInput,
	_ executionstore.ID,
	_ *executionstore.IntegrationRuntimeLeaseProof,
) (executionstore.LaunchAgentResult, error) {
	return s.LaunchAgent(ctx, input)
}

func (s *channelProfileCompletionExecutionStub) CreateBoundIntegrationTargetContentInput(
	context.Context,
	executionstore.CreateBoundIntegrationTargetContentInput,
) (executionstore.CreateBoundIntegrationTargetContentResult, error) {
	if s.boundErr != nil {
		return executionstore.CreateBoundIntegrationTargetContentResult{}, s.boundErr
	}
	return executionstore.CreateBoundIntegrationTargetContentResult{}, nil
}

func (s *channelProfileCompletionExecutionStub) CreateIntegrationTargetContentInput(
	context.Context,
	executionstore.CreateIntegrationTargetContentInput,
) (executionstore.AgentInputRecord, []executionstore.ID, error) {
	return executionstore.AgentInputRecord{}, nil, errors.New(
		"unexpected CreateIntegrationTargetContentInput call",
	)
}

func (s *channelRouteRaceExecutionStub) GetAgentInProject(
	context.Context,
	executionstore.ID,
	executionstore.ID,
) (executionstore.AgentRecord, error) {
	return executionstore.AgentRecord{}, errors.New("unexpected GetAgentInProject call")
}

func (s *channelRouteRaceExecutionStub) GetAgentProfile(
	context.Context,
	executionstore.ID,
	executionstore.ID,
) (executionstore.AgentProfileRecord, error) {
	return executionstore.AgentProfileRecord{}, errors.New("unexpected GetAgentProfile call")
}

func (s *channelRouteRaceExecutionStub) LaunchAgent(
	context.Context,
	executionstore.LaunchAgentInput,
) (executionstore.LaunchAgentResult, error) {
	return executionstore.LaunchAgentResult{}, errors.New("unexpected LaunchAgent call")
}

func (s *channelRouteRaceExecutionStub) LaunchAgentWithIntegrationRuntimeLease(
	context.Context,
	executionstore.LaunchAgentInput,
	executionstore.ID,
	*executionstore.IntegrationRuntimeLeaseProof,
) (executionstore.LaunchAgentResult, error) {
	return executionstore.LaunchAgentResult{}, errors.New(
		"unexpected LaunchAgentWithIntegrationRuntimeLease call",
	)
}

func (s *channelRouteRaceExecutionStub) CreateBoundIntegrationTargetContentInput(
	context.Context,
	executionstore.CreateBoundIntegrationTargetContentInput,
) (executionstore.CreateBoundIntegrationTargetContentResult, error) {
	return executionstore.CreateBoundIntegrationTargetContentResult{}, errors.New(
		"unexpected CreateBoundIntegrationTargetContentInput call",
	)
}

func (s *channelRouteRaceExecutionStub) CreateIntegrationTargetContentInput(
	context.Context,
	executionstore.CreateIntegrationTargetContentInput,
) (executionstore.AgentInputRecord, []executionstore.ID, error) {
	s.createCalls++
	if s.createCalls == s.failAt {
		if s.failErr != nil {
			return executionstore.AgentInputRecord{}, nil, s.failErr
		}
		return executionstore.AgentInputRecord{}, nil, storeerr.ErrStateTransitionConflict
	}
	return executionstore.AgentInputRecord{ID: uuid.New()}, nil, nil
}

type channelRouteRaceIntegrationStub struct {
	app                 integrationstore.IntegrationAppRecord
	install             integrationstore.IntegrationInstallRecord
	routes              []integrationstore.IntegrationRouteRecord
	target              integrationstore.IntegrationTargetRecord
	bindings            map[integrationstore.ID]integrationstore.IntegrationTargetBindingRecord
	leaseCurrentResults []bool
	leaseChecks         int
}

func (s *channelRouteRaceIntegrationStub) GetConnectorIntegrationApp(
	context.Context,
	integrationstore.ID,
	[]channelconnector.Capability,
) (integrationstore.IntegrationAppRecord, error) {
	return s.app, nil
}

func (s *channelRouteRaceIntegrationStub) GetConnectorIntegrationInstall(
	context.Context,
	integrationstore.ID,
	string,
	string,
) (integrationstore.IntegrationInstallRecord, error) {
	return s.install, nil
}

func (s *channelRouteRaceIntegrationStub) ListActiveIntegrationRoutes(
	context.Context,
	integrationstore.ID,
	integrationstore.ID,
) ([]integrationstore.IntegrationRouteRecord, error) {
	return append([]integrationstore.IntegrationRouteRecord(nil), s.routes...), nil
}

func (s *channelRouteRaceIntegrationStub) GetIntegrationTargetByProviderRef(
	context.Context,
	integrationstore.ID,
	integrationstore.ID,
	string,
) (integrationstore.IntegrationTargetRecord, error) {
	return s.target, nil
}

func (s *channelRouteRaceIntegrationStub) ListActiveReceiveBindingsForTargetRoute(
	_ context.Context,
	_, _, routeID, _ integrationstore.ID,
) ([]integrationstore.IntegrationTargetBindingRecord, error) {
	return []integrationstore.IntegrationTargetBindingRecord{s.bindings[routeID]}, nil
}

func (s *channelRouteRaceIntegrationStub) IntegrationRuntimeLeaseIsCurrent(
	context.Context,
	integrationstore.ID,
	integrationstore.ID,
	integrationstore.ID,
	integrationstore.ID,
	int64,
) (bool, error) {
	if s.leaseChecks >= len(s.leaseCurrentResults) {
		return true, nil
	}
	current := s.leaseCurrentResults[s.leaseChecks]
	s.leaseChecks++
	return current, nil
}

func (s *integrationServiceStoreStub) GetIntegrationInstallByID(
	context.Context,
	integrationstore.ID,
) (integrationstore.IntegrationInstallRecord, error) {
	return s.install, nil
}

func (s *integrationServiceStoreStub) GetIntegrationTargetByProviderRef(
	context.Context,
	integrationstore.ID,
	integrationstore.ID,
	string,
) (integrationstore.IntegrationTargetRecord, error) {
	return integrationstore.IntegrationTargetRecord{}, s.lookupErr
}

func (s *integrationServiceStoreStub) CreateIntegrationTarget(
	_ context.Context,
	input integrationstore.CreateIntegrationTargetInput,
) (integrationstore.IntegrationTargetRecord, error) {
	s.createdInput = input
	return s.createdTarget, nil
}

func TestGetOrCreateTargetTreatsDatabaseNoRowsAsNotFound(t *testing.T) {
	installID := uuid.New()
	projectID := uuid.New()
	agentID := uuid.New()
	target := integrationstore.IntegrationTargetRecord{ID: uuid.New()}
	store := &integrationServiceStoreStub{
		install: integrationstore.IntegrationInstallRecord{
			ID:        installID,
			ProjectID: projectID,
			AgentID:   agentID,
			State:     integrationstore.IntegrationInstallStateActive,
		},
		lookupErr:     pgx.ErrNoRows,
		createdTarget: target,
	}

	got, launch, err := New(integrationServiceExecutionStub{}, store).GetOrCreateTarget(
		context.Background(),
		GetOrCreateTargetInput{
			IntegrationInstallID: installID,
			ProviderRef:          "provider-ref",
			ProviderRefKind:      "channel",
			DisplayName:          "  Release channel  ",
		},
	)
	if err != nil {
		t.Fatalf("get or create target: %v", err)
	}
	if got.ID != target.ID || launch.Created {
		t.Fatalf("result = (%+v, %+v), want existing agent target creation", got, launch)
	}
	wantInput := integrationstore.CreateIntegrationTargetInput{
		ProjectID:            projectID,
		AgentID:              agentID,
		IntegrationInstallID: installID,
		ProviderRef:          "provider-ref",
		ProviderRefKind:      "channel",
		DisplayName:          "Release channel",
	}
	if !reflect.DeepEqual(store.createdInput, wantInput) {
		t.Fatalf("create input = %+v, want %+v", store.createdInput, wantInput)
	}
}

func TestGetOrCreateTargetTagsAgentLaunchFailures(t *testing.T) {
	installID := uuid.New()
	projectID := uuid.New()
	profileID := uuid.New()
	configID := uuid.New()
	store := &integrationServiceStoreStub{
		install: integrationstore.IntegrationInstallRecord{
			ID:                installID,
			ProjectID:         projectID,
			AgentProfileID:    profileID,
			InstalledByUserID: uuid.New(),
			State:             integrationstore.IntegrationInstallStateActive,
		},
		lookupErr: pgx.ErrNoRows,
	}
	execution := integrationServiceLaunchFailureStub{
		profile:   executionstore.AgentProfileRecord{CurrentConfigID: configID},
		launchErr: storeerr.ErrStateTransitionConflict,
	}

	_, _, err := New(execution, store).GetOrCreateTarget(
		context.Background(),
		GetOrCreateTargetInput{
			IntegrationInstallID: installID,
			ProviderRef:          "C123:111.222",
			ProviderRefKind:      "thread",
		},
	)
	if !errors.Is(err, storeerr.ErrAgentLaunchFailed) {
		t.Fatalf("error = %v, want ErrAgentLaunchFailed", err)
	}
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("error = %v, want underlying state transition conflict", err)
	}
	if !reflect.DeepEqual(store.createdInput, integrationstore.CreateIntegrationTargetInput{}) {
		t.Fatalf("launch failure created integration target with input %+v", store.createdInput)
	}
}

func TestChannelInboundActorDisplayNameLimitMatchesActorStorage(t *testing.T) {
	input := ProcessChannelInboundInput{
		IntegrationAppID: uuid.New(),
		Envelope: ChannelInboundEnvelope{
			Version:            ChannelEnvelopeVersionV1,
			ProviderEventID:    "event",
			ExternalAccountRef: "account",
			EventType:          "message.created",
			Actor: ChannelActor{
				Ref:         "actor",
				DisplayName: strings.Repeat("界", executionstore.MaxActorDisplayNameLength),
			},
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
		},
		PrepareContent: passthroughChannelInboundContent,
	}
	if err := validateProcessChannelInboundInput(input); err != nil {
		t.Fatalf("validate actor display name at limit: %v", err)
	}
	input.Envelope.Actor.DisplayName += "界"
	if err := validateProcessChannelInboundInput(input); err == nil {
		t.Fatal("actor display name above storage limit was accepted")
	}
}

func TestChannelInboundRejectsMetadataWhoseCombinedInputExceedsStorageLimit(t *testing.T) {
	metadata, err := json.Marshal(map[string]string{"value": strings.Repeat("a", 130*1024)})
	if err != nil {
		t.Fatal(err)
	}
	input := ProcessChannelInboundInput{
		IntegrationAppID: uuid.New(),
		Envelope: ChannelInboundEnvelope{
			Version:            ChannelEnvelopeVersionV1,
			ProviderEventID:    "event",
			ExternalAccountRef: "account",
			EventType:          "message.created",
			Actor: ChannelActor{
				Ref:      "actor",
				Metadata: metadata,
			},
			Metadata:      metadata,
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
		},
		PrepareContent: passthroughChannelInboundContent,
	}

	if err := validateProcessChannelInboundInput(input); err == nil ||
		!strings.Contains(err.Error(), "aggregate metadata") {
		t.Fatalf("aggregate metadata validation error = %v", err)
	}
}

func TestChannelInboundRejectsCombinedPostgreSQLTextExpansion(t *testing.T) {
	t.Parallel()

	expandedNumber := json.RawMessage(`{"value":1e131071}`)
	input := ProcessChannelInboundInput{
		IntegrationAppID: uuid.New(),
		Envelope: ChannelInboundEnvelope{
			Version:            ChannelEnvelopeVersionV1,
			ProviderEventID:    "event",
			ExternalAccountRef: "account",
			EventType:          "message.created",
			Actor: ChannelActor{
				Ref:      "actor",
				Metadata: expandedNumber,
			},
			Metadata:      expandedNumber,
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
		},
		PrepareContent: passthroughChannelInboundContent,
	}

	if err := validateProcessChannelInboundInput(input); err == nil ||
		!strings.Contains(err.Error(), "aggregate metadata") {
		t.Fatalf("aggregate PostgreSQL text-expansion error = %v", err)
	}
}

func TestChannelInputMetadataRejectsBindingPostgreSQLTextExpansion(t *testing.T) {
	t.Parallel()

	expandedNumber := json.RawMessage(`{"value":1e131071}`)
	_, err := channelInputMetadata(
		ChannelInboundEnvelope{
			Version:         ChannelEnvelopeVersionV1,
			ProviderEventID: "event",
			EventType:       "message.created",
			Metadata:        expandedNumber,
		},
		integrationstore.IntegrationRouteRecord{ID: uuid.New()},
		expandedNumber,
	)
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL JSON text exceeds") {
		t.Fatalf("binding PostgreSQL text-expansion error = %v", err)
	}
}

func TestChannelInboundFanoutLimitAppliesAcrossRoutes(t *testing.T) {
	plans := make([]plannedChannelRoute, 2)
	for routeIndex := range plans {
		plans[routeIndex].route.ID = uuid.New()
		plans[routeIndex].bindings = make([]plannedChannelBindingDelivery, 33)
		for bindingIndex := range plans[routeIndex].bindings {
			plans[routeIndex].bindings[bindingIndex].binding.AgentID = uuid.New()
		}
	}
	if err := validateChannelInboundFanout(plans); err == nil {
		t.Fatal("event-wide fanout above the recipient limit was accepted")
	}

	plans[1].bindings = plans[1].bindings[:31]
	if err := validateChannelInboundFanout(plans); err != nil {
		t.Fatalf("event-wide fanout at the recipient limit: %v", err)
	}
}

func TestChannelInboundFanoutLimitsRepeatedDeliveriesToOneRecipient(t *testing.T) {
	agentID := uuid.New()
	plans := make([]plannedChannelRoute, maxChannelInboundDeliveries+1)
	for routeIndex := range plans {
		plans[routeIndex].route.ID = uuid.New()
		plans[routeIndex].bindings = []plannedChannelBindingDelivery{{
			binding: integrationstore.IntegrationTargetBindingRecord{AgentID: agentID},
		}}
	}
	if err := validateChannelInboundFanout(plans); err == nil {
		t.Fatal("event-wide fanout above the delivery limit was accepted")
	}

	if err := validateChannelInboundFanout(plans[:maxChannelInboundDeliveries]); err != nil {
		t.Fatalf("event-wide fanout at the delivery limit: %v", err)
	}
}

func TestChannelProfileResolutionMakesLaterFailuresGloballyRetryable(t *testing.T) {
	projectID := uuid.New()
	profileID := uuid.New()
	configID := uuid.New()
	app := integrationstore.IntegrationAppRecord{ID: uuid.New()}
	install := integrationstore.IntegrationInstallRecord{
		ID: uuid.New(), ProjectID: projectID, InstalledByUserID: uuid.New(),
	}
	route := integrationstore.IntegrationRouteRecord{ID: uuid.New(), ProjectID: projectID}
	profileAttachment := func(instance string) plannedChannelAttachment {
		return plannedChannelAttachment{
			action: ChannelAttachmentAction{
				AgentProfileID: profileID,
				InstanceKey:    instance,
			},
			profile: executionstore.AgentProfileRecord{
				ID: profileID, ProjectID: projectID, CurrentConfigID: configID,
			},
			metadata: json.RawMessage(`{}`),
		}
	}
	launch := func() executionstore.LaunchAgentResult {
		return executionstore.LaunchAgentResult{Agent: executionstore.AgentRecord{
			ID: uuid.New(), ProjectID: projectID, AgentProfileID: profileID,
			State: executionstore.AgentStateActive,
		}}
	}
	basePlan := plannedChannelRoute{
		route: route,
		decision: ChannelRouteDecision{
			ProviderRef: "thread-1", ProviderRefKind: "thread",
			DeliveryMode: executionstore.DeliveryModeQueued,
		},
		attachments: []plannedChannelAttachment{profileAttachment("first")},
	}
	input := ProcessChannelInboundInput{Envelope: ChannelInboundEnvelope{
		ProviderEventID: "event-1", Actor: ChannelActor{Ref: "actor-1"},
	}}
	isolationErr := storeerr.ErrStateTransitionConflict

	for _, test := range []struct {
		name        string
		plan        plannedChannelRoute
		execution   *channelProfileCompletionExecutionStub
		materialize MaterializeChannelInboundContentFunc
		wantErr     error
		wantRetry   bool
	}{
		{
			name: "materialization after profile launch",
			plan: basePlan,
			execution: &channelProfileCompletionExecutionStub{
				launches: []executionstore.LaunchAgentResult{launch()},
			},
			materialize: func(
				context.Context,
				MaterializeChannelInboundContentInput,
			) (json.RawMessage, error) {
				return nil, isolationErr
			},
			wantRetry: true,
		},
		{
			name: "invalid materialization after profile launch",
			plan: basePlan,
			execution: &channelProfileCompletionExecutionStub{
				launches: []executionstore.LaunchAgentResult{launch()},
			},
			materialize: func(
				context.Context,
				MaterializeChannelInboundContentInput,
			) (json.RawMessage, error) {
				return nil, storeerr.InvalidRequest(isolationErr)
			},
		},
		{
			name: "bound input after profile launch",
			plan: basePlan,
			execution: &channelProfileCompletionExecutionStub{
				boundErr: isolationErr,
				launches: []executionstore.LaunchAgentResult{launch()},
			},
			materialize: func(
				context.Context,
				MaterializeChannelInboundContentInput,
			) (json.RawMessage, error) {
				return json.RawMessage(`[]`), nil
			},
			wantRetry: true,
		},
		{
			name: "second profile launch after first resolves",
			plan: func() plannedChannelRoute {
				plan := basePlan
				plan.attachments = append(
					append([]plannedChannelAttachment(nil), basePlan.attachments...),
					profileAttachment("second"),
				)
				return plan
			}(),
			execution: &channelProfileCompletionExecutionStub{
				launchErrors: []error{nil, isolationErr},
				launches:     []executionstore.LaunchAgentResult{launch()},
			},
			materialize: func(
				context.Context,
				MaterializeChannelInboundContentInput,
			) (json.RawMessage, error) {
				return json.RawMessage(`[]`), nil
			},
			wantRetry: true,
		},
		{
			name: "idempotency conflict after first profile resolves",
			plan: func() plannedChannelRoute {
				plan := basePlan
				plan.attachments = append(
					append([]plannedChannelAttachment(nil), basePlan.attachments...),
					profileAttachment("second"),
				)
				return plan
			}(),
			execution: &channelProfileCompletionExecutionStub{
				launchErrors: []error{nil, storeerr.ErrIdempotencyConflict},
				launches:     []executionstore.LaunchAgentResult{launch()},
			},
			wantErr: storeerr.ErrIdempotencyConflict,
			materialize: func(
				context.Context,
				MaterializeChannelInboundContentInput,
			) (json.RawMessage, error) {
				return json.RawMessage(`[]`), nil
			},
		},
		{
			name: "initial profile launch",
			plan: basePlan,
			execution: &channelProfileCompletionExecutionStub{
				launchErrors: []error{isolationErr},
			},
			materialize: func(
				context.Context,
				MaterializeChannelInboundContentInput,
			) (json.RawMessage, error) {
				return json.RawMessage(`[]`), nil
			},
		},
		{
			name: "existing agent bound input",
			plan: plannedChannelRoute{
				route: route,
				decision: ChannelRouteDecision{
					ProviderRef: "thread-1", ProviderRefKind: "thread",
					DeliveryMode: executionstore.DeliveryModeQueued,
				},
				attachments: []plannedChannelAttachment{{
					action:   ChannelAttachmentAction{AgentID: uuid.New()},
					metadata: json.RawMessage(`{}`),
				}},
			},
			execution: &channelProfileCompletionExecutionStub{boundErr: isolationErr},
			materialize: func(
				context.Context,
				MaterializeChannelInboundContentInput,
			) (json.RawMessage, error) {
				return json.RawMessage(`[]`), nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := NewChannelService(test.execution, nil, nil)
			_, err := service.executeInboundRoute(
				context.Background(),
				app,
				install,
				test.plan,
				input,
				nil,
				test.materialize,
				make(map[integrationstore.ID]json.RawMessage),
			)
			wantErr := test.wantErr
			if wantErr == nil {
				wantErr = isolationErr
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("route execution error = %v, want %v", err, wantErr)
			}
			if errors.Is(err, ErrChannelInboundCompletionRetry) != test.wantRetry {
				t.Fatalf("route execution retry classification = %v, error %v", test.wantRetry, err)
			}
		})
	}
}

func TestChannelProfileLaunchClassifiesDeterministicFailuresAsPermanent(t *testing.T) {
	projectID := uuid.New()
	profileID := uuid.New()
	plan := plannedChannelRoute{
		route: integrationstore.IntegrationRouteRecord{ID: uuid.New(), ProjectID: projectID},
		decision: ChannelRouteDecision{
			ProviderRef: "thread-1", ProviderRefKind: "thread",
			DeliveryMode: executionstore.DeliveryModeQueued,
		},
		attachments: []plannedChannelAttachment{{
			action: ChannelAttachmentAction{
				AgentProfileID: profileID,
				InstanceKey:    "profile",
			},
			profile: executionstore.AgentProfileRecord{
				ID: profileID, ProjectID: projectID, CurrentConfigID: uuid.New(),
			},
			metadata: json.RawMessage(`{}`),
		}},
	}
	app := integrationstore.IntegrationAppRecord{ID: uuid.New()}
	install := integrationstore.IntegrationInstallRecord{
		ID: uuid.New(), ProjectID: projectID, InstalledByUserID: uuid.New(),
	}
	input := ProcessChannelInboundInput{Envelope: ChannelInboundEnvelope{
		ProviderEventID: "event-1", Actor: ChannelActor{Ref: "actor-1"},
	}}

	for _, launchErr := range []error{
		storeerr.InvalidRequest(errors.New("invalid profile configuration")),
		storeerr.ErrModelGrantUnavailable,
		storeerr.ErrManagedWorkAdmissionDenied,
	} {
		service := NewChannelService(
			&channelProfileCompletionExecutionStub{launchErrors: []error{launchErr}},
			nil,
			nil,
		)
		_, err := service.executeInboundRoute(
			context.Background(),
			app,
			install,
			plan,
			input,
			nil,
			func(context.Context, MaterializeChannelInboundContentInput) (json.RawMessage, error) {
				return json.RawMessage(`[]`), nil
			},
			make(map[integrationstore.ID]json.RawMessage),
		)
		var permanent permanentChannelRouteError
		if !errors.As(err, &permanent) || !errors.Is(err, launchErr) ||
			!errors.Is(err, storeerr.ErrAgentLaunchFailed) {
			t.Errorf("deterministic launch error %v classified as %v", launchErr, err)
		}
	}
}

func TestChannelProfileResolutionDoesNotRetryPermanentRouteErrors(t *testing.T) {
	projectID := uuid.New()
	profileID := uuid.New()
	app := integrationstore.IntegrationAppRecord{ID: uuid.New()}
	install := integrationstore.IntegrationInstallRecord{
		ID: uuid.New(), ProjectID: projectID, InstalledByUserID: uuid.New(),
	}
	route := integrationstore.IntegrationRouteRecord{ID: uuid.New(), ProjectID: projectID}
	profileAttachment := plannedChannelAttachment{
		action: ChannelAttachmentAction{AgentProfileID: profileID, InstanceKey: "profile"},
		profile: executionstore.AgentProfileRecord{
			ID: profileID, ProjectID: projectID, CurrentConfigID: uuid.New(),
		},
		metadata: json.RawMessage(`{}`),
	}
	input := ProcessChannelInboundInput{Envelope: ChannelInboundEnvelope{
		ProviderEventID: "event-1", Actor: ChannelActor{Ref: "actor-1"},
	}}
	materialize := func(
		context.Context,
		MaterializeChannelInboundContentInput,
	) (json.RawMessage, error) {
		return json.RawMessage(`[]`), nil
	}

	for _, test := range []struct {
		name      string
		launch    executionstore.LaunchAgentResult
		duplicate bool
	}{
		{
			name: "incompatible profile instance",
			launch: executionstore.LaunchAgentResult{Agent: executionstore.AgentRecord{
				ID: uuid.New(), ProjectID: projectID, AgentProfileID: uuid.New(),
				State: executionstore.AgentStateActive,
			}},
		},
		{
			name:      "duplicate resolved agent",
			duplicate: true,
			launch: func() executionstore.LaunchAgentResult {
				agentID := uuid.New()
				return executionstore.LaunchAgentResult{Agent: executionstore.AgentRecord{
					ID: agentID, ProjectID: projectID, AgentProfileID: profileID,
					State: executionstore.AgentStateActive,
				}}
			}(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := plannedChannelRoute{
				route: route,
				decision: ChannelRouteDecision{
					ProviderRef: "thread-1", ProviderRefKind: "thread",
					DeliveryMode: executionstore.DeliveryModeQueued,
				},
				attachments: []plannedChannelAttachment{profileAttachment},
			}
			if test.duplicate {
				plan.attachments = append(plan.attachments, plannedChannelAttachment{
					action:   ChannelAttachmentAction{AgentID: test.launch.Agent.ID},
					metadata: json.RawMessage(`{}`),
				})
			}
			service := NewChannelService(
				&channelProfileCompletionExecutionStub{
					launches: []executionstore.LaunchAgentResult{test.launch},
				},
				nil,
				nil,
			)
			_, err := service.executeInboundRoute(
				context.Background(), app, install, plan, input, nil, materialize,
				make(map[integrationstore.ID]json.RawMessage),
			)
			var permanent permanentChannelRouteError
			if !errors.As(err, &permanent) {
				t.Fatalf("route execution error = %v, want permanent route error", err)
			}
			if errors.Is(err, ErrChannelInboundCompletionRetry) {
				t.Fatalf("permanent route error was marked retryable: %v", err)
			}
		})
	}
}

func TestChannelInboundPreparesContentOnceAndMaterializesOncePerAgent(t *testing.T) {
	for _, test := range []struct {
		name              string
		repeatAgent       bool
		wantMaterializers int
	}{
		{name: "distinct agents", wantMaterializers: 2},
		{name: "same agent across routes", repeatAgent: true, wantMaterializers: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, input, execution, integrations := newChannelRouteRaceFixture()
			execution.failAt = 0
			if test.repeatAgent {
				firstAgentID := integrations.bindings[integrations.routes[0].ID].AgentID
				second := integrations.bindings[integrations.routes[1].ID]
				second.AgentID = firstAgentID
				integrations.bindings[integrations.routes[1].ID] = second
			}

			prepareCalls := 0
			materializeCalls := make(map[integrationstore.ID]int)
			input.PrepareContent = func(
				_ context.Context,
				content json.RawMessage,
			) (MaterializeChannelInboundContentFunc, error) {
				prepareCalls++
				return func(
					_ context.Context,
					materialize MaterializeChannelInboundContentInput,
				) (json.RawMessage, error) {
					materializeCalls[materialize.AgentID]++
					return content, nil
				}, nil
			}

			result, err := service.ProcessInbound(context.Background(), input)
			if err != nil {
				t.Fatalf("process channel fanout: %v", err)
			}
			if prepareCalls != 1 {
				t.Fatalf("content preparation calls = %d, want 1", prepareCalls)
			}
			if len(materializeCalls) != test.wantMaterializers {
				t.Fatalf(
					"content materializers = %d, want %d",
					len(materializeCalls),
					test.wantMaterializers,
				)
			}
			for agentID, calls := range materializeCalls {
				if calls != 1 {
					t.Fatalf("agent %s content materialization calls = %d, want 1", agentID, calls)
				}
			}
			if len(result.Accepted) != 2 || len(result.FailedRoutes) != 0 {
				t.Fatalf("channel fanout result = %+v", result)
			}
		})
	}
}

func TestChannelInboundIsolatesDeterministicExecutionRaceAfterEarlierAcceptance(t *testing.T) {
	service, input, execution, integrations := newChannelRouteRaceFixture()

	result, err := service.ProcessInbound(context.Background(), input)
	if err != nil {
		t.Fatalf("process channel event: %v", err)
	}
	if execution.createCalls != 2 || len(result.Accepted) != 1 || len(result.FailedRoutes) != 1 {
		t.Fatalf(
			"route race result = calls %d, accepted %d, failed %d",
			execution.createCalls,
			len(result.Accepted),
			len(result.FailedRoutes),
		)
	}
	if result.FailedRoutes[0].RouteID != integrations.routes[1].ID {
		t.Fatalf(
			"failed route = %s, want %s",
			result.FailedRoutes[0].RouteID,
			integrations.routes[1].ID,
		)
	}
	if !errors.Is(result.FailedRoutes[0].Err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("route failure = %v", result.FailedRoutes[0].Err)
	}
}

func TestChannelInboundKeepsEarlierAcceptanceWhenLaterRouteInputIsInvalid(t *testing.T) {
	service, input, execution, integrations := newChannelRouteRaceFixture()
	execution.failErr = storeerr.InvalidRequest(errors.New("route input is invalid"))

	result, err := service.ProcessInbound(context.Background(), input)
	if err != nil {
		t.Fatalf("process channel event: %v", err)
	}
	if execution.createCalls != 2 || len(result.Accepted) != 1 || len(result.FailedRoutes) != 1 {
		t.Fatalf(
			"invalid route result = calls %d, accepted %d, failed %d",
			execution.createCalls,
			len(result.Accepted),
			len(result.FailedRoutes),
		)
	}
	if result.FailedRoutes[0].RouteID != integrations.routes[1].ID ||
		!errors.Is(result.FailedRoutes[0].Err, storeerr.ErrInvalidRequest) {
		t.Fatalf("invalid route failure = %+v", result.FailedRoutes[0])
	}
}

func TestChannelRuntimeInboundDoesNotIsolateLostLeaseAsRouteFailure(t *testing.T) {
	service, input, execution, integrations := newChannelRouteRaceFixture()
	integrations.leaseCurrentResults = []bool{true, false}
	lease := ChannelRuntimeLease{UnitID: uuid.New(), Token: uuid.New(), Generation: 1}

	_, err := service.ProcessRuntimeInbound(context.Background(), input, lease)
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("runtime route race error = %v, want state transition conflict", err)
	}
	if execution.createCalls != 2 || integrations.leaseChecks != 2 {
		t.Fatalf(
			"runtime route race = %d creates, %d lease checks, want 2 and 2",
			execution.createCalls,
			integrations.leaseChecks,
		)
	}
}

func TestChannelRuntimeInboundRejectsIncompleteLeaseAsInvalidRequest(t *testing.T) {
	service := NewChannelService(nil, nil, nil)
	_, err := service.ProcessRuntimeInbound(
		context.Background(),
		ProcessChannelInboundInput{},
		ChannelRuntimeLease{},
	)
	if !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("incomplete runtime lease error = %v, want invalid request", err)
	}
}

func newChannelRouteRaceFixture() (
	*ChannelService,
	ProcessChannelInboundInput,
	*channelRouteRaceExecutionStub,
	*channelRouteRaceIntegrationStub,
) {
	projectID := uuid.New()
	appID := uuid.New()
	installID := uuid.New()
	targetID := uuid.New()
	routes := []integrationstore.IntegrationRouteRecord{
		{
			ID: uuid.New(), ProjectID: projectID, IntegrationInstallID: installID,
			HandlerKey: "race", HandlerVersion: 1, Configuration: json.RawMessage(`{}`),
			State: integrationstore.IntegrationRouteStateActive,
		},
		{
			ID: uuid.New(), ProjectID: projectID, IntegrationInstallID: installID,
			HandlerKey: "race", HandlerVersion: 1, Configuration: json.RawMessage(`{}`),
			State: integrationstore.IntegrationRouteStateActive,
		},
	}
	bindings := make(map[integrationstore.ID]integrationstore.IntegrationTargetBindingRecord, 2)
	for _, route := range routes {
		bindings[route.ID] = integrationstore.IntegrationTargetBindingRecord{
			ID: uuid.New(), ProjectID: projectID, AgentID: uuid.New(),
			IntegrationInstallID: installID, IntegrationTargetID: targetID,
			IntegrationRouteID: route.ID, ReceiveAllowed: true,
			Source: "test", Metadata: json.RawMessage(`{}`),
		}
	}
	execution := &channelRouteRaceExecutionStub{failAt: 2}
	integrations := &channelRouteRaceIntegrationStub{
		app: integrationstore.IntegrationAppRecord{
			ID: appID, OwnerProjectID: projectID, ConnectorKey: "chat_sdk_v1",
			State: integrationstore.IntegrationAppStateActive,
		},
		install: integrationstore.IntegrationInstallRecord{
			ID: installID, ProjectID: projectID, IntegrationAppID: appID,
			Provider: "discord", ProviderTenantID: "tenant", ProviderAccountRef: "account",
			State: integrationstore.IntegrationInstallStateActive,
		},
		routes: routes,
		target: integrationstore.IntegrationTargetRecord{
			ID: targetID, ProjectID: projectID, IntegrationInstallID: installID,
			ProviderRef: "channel", ProviderRefKind: "channel",
		},
		bindings: bindings,
	}
	handler := ChannelRouteHandlerFunc(func(
		context.Context,
		ChannelRouteContext,
		ChannelInboundEnvelope,
	) (ChannelRouteDecision, error) {
		return ChannelRouteDecision{
			Accept: true, ProviderRef: "channel", ProviderRefKind: "channel",
			DeliverToAllExisting: true, DeliveryMode: executionstore.DeliveryModeQueued,
		}, nil
	})
	service := NewChannelService(
		execution,
		integrations,
		ChannelRouteHandlers{ChannelRouteHandlerKey("race", 1): handler},
	)
	input := ProcessChannelInboundInput{
		IntegrationAppID: appID,
		Envelope: ChannelInboundEnvelope{
			Version: ChannelEnvelopeVersionV1, ProviderEventID: "event",
			ExternalTenantID: "tenant", ExternalAccountRef: "account",
			Actor:         ChannelActor{Ref: "actor"},
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
		},
		PrepareContent: passthroughChannelInboundContent,
	}
	return service, input, execution, integrations
}
