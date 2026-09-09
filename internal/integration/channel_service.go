package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const ChannelEnvelopeVersionV1 = "v1"

const (
	maxChannelContentBlocksBytes = 48 * 1024 * 1024
	maxChannelRouteAttachments   = 64
	maxChannelRouteSelections    = 256
	maxChannelInboundDeliveries  = 256
	maxChannelInboundRecipients  = 64
)

var (
	ErrChannelInboundCompletionRetry  = errors.New("channel inbound completion requires retry")
	ErrChannelRouteHandlerUnavailable = errors.New("channel route handler unavailable")
)

type ChannelInboundEnvelope struct {
	Version            string
	ProviderEventID    string
	ExternalTenantID   string
	ExternalAccountRef string
	EventType          string
	Conversation       ChannelConversation
	Actor              ChannelActor
	ContentBlocks      json.RawMessage
	OccurredAt         time.Time
	Metadata           json.RawMessage
}

type ChannelConversation struct {
	Ref         string          `json:"ref"`
	Kind        string          `json:"kind"`
	DisplayName string          `json:"display_name,omitempty"`
	ParentRef   string          `json:"parent_ref,omitempty"`
	ReplyToRef  string          `json:"reply_to_ref,omitempty"`
	Mentioned   bool            `json:"mentioned"`
	Direct      bool            `json:"direct"`
	Metadata    json.RawMessage `json:"metadata"`
}

type ChannelActor struct {
	Ref         string          `json:"ref"`
	DisplayName string          `json:"display_name,omitempty"`
	Metadata    json.RawMessage `json:"metadata"`
}

type ChannelRuntimeLease struct {
	UnitID     integrationstore.ID
	Token      integrationstore.ID
	Generation int64
}

type ChannelRouteContext struct {
	App      integrationstore.IntegrationAppRecord
	Install  integrationstore.IntegrationInstallRecord
	Route    integrationstore.IntegrationRouteRecord
	Bindings ChannelRouteBindingLookup
}

type ChannelRouteDecision struct {
	Accept                 bool
	ProviderRef            string
	ProviderRefKind        string
	DisplayName            string
	ExistingBindingIDs     []integrationstore.ID
	DeliverToAllExisting   bool
	Attachments            []ChannelAttachmentAction
	DeliveryMode           executionstore.AgentInputDeliveryMode
	CancelOpenInteractions bool
	TargetMetadata         json.RawMessage
}

// ChannelAttachmentAction authorizes one agent to receive the current route's
// events at the exact provider address selected by the handler. Existing agents
// use AgentID. Profile launches use AgentProfileID plus a stable, handler-owned
// InstanceKey; the same key on the same immutable route always resolves to the
// same agent, even when the route attaches it to several channel targets.
type ChannelAttachmentAction struct {
	AgentID        integrationstore.ID
	AgentProfileID integrationstore.ID
	InstanceKey    string
	SendAllowed    bool
	Metadata       json.RawMessage
}

// ChannelRouteBindingLookup gives a route implementation a bounded, read-only
// view of the current receive bindings for one exact external address. It does
// not grant mutation authority; the returned IDs must still be selected in the
// route decision and are revalidated before delivery.
type ChannelRouteBindingLookup interface {
	ListActiveForTarget(
		context.Context,
		string,
		string,
	) ([]integrationstore.IntegrationTargetBindingRecord, error)
}

type ChannelRouteHandler interface {
	// Route must return the same decision for the same immutable route and
	// provider envelope. Inbound completion retries replay the whole event.
	Route(context.Context, ChannelRouteContext, ChannelInboundEnvelope) (ChannelRouteDecision, error)
}

type ChannelRouteHandlerFunc func(
	context.Context,
	ChannelRouteContext,
	ChannelInboundEnvelope,
) (ChannelRouteDecision, error)

func (f ChannelRouteHandlerFunc) Route(
	ctx context.Context,
	routeContext ChannelRouteContext,
	envelope ChannelInboundEnvelope,
) (ChannelRouteDecision, error) {
	return f(ctx, routeContext, envelope)
}

type ChannelRouteHandlers map[string]ChannelRouteHandler

func ChannelRouteHandlerKey(handlerKey string, handlerVersion int) string {
	return fmt.Sprintf("%s@%d", strings.TrimSpace(handlerKey), handlerVersion)
}

type channelExecutionStore interface {
	GetAgentInProject(
		context.Context,
		executionstore.ID,
		executionstore.ID,
	) (executionstore.AgentRecord, error)
	GetAgentProfile(context.Context, executionstore.ID, executionstore.ID) (executionstore.AgentProfileRecord, error)
	LaunchAgent(context.Context, executionstore.LaunchAgentInput) (executionstore.LaunchAgentResult, error)
	LaunchAgentWithIntegrationRuntimeLease(
		context.Context,
		executionstore.LaunchAgentInput,
		executionstore.ID,
		*executionstore.IntegrationRuntimeLeaseProof,
	) (executionstore.LaunchAgentResult, error)
	CreateBoundIntegrationTargetContentInput(
		context.Context,
		executionstore.CreateBoundIntegrationTargetContentInput,
	) (executionstore.CreateBoundIntegrationTargetContentResult, error)
	CreateIntegrationTargetContentInput(
		context.Context,
		executionstore.CreateIntegrationTargetContentInput,
	) (executionstore.AgentInputRecord, []executionstore.ID, error)
}

type channelIntegrationStore interface {
	GetConnectorIntegrationApp(
		context.Context,
		integrationstore.ID,
		[]channelconnector.Capability,
	) (integrationstore.IntegrationAppRecord, error)
	GetConnectorIntegrationInstall(
		context.Context,
		integrationstore.ID,
		string,
		string,
	) (integrationstore.IntegrationInstallRecord, error)
	ListActiveIntegrationRoutes(
		context.Context,
		integrationstore.ID,
		integrationstore.ID,
	) ([]integrationstore.IntegrationRouteRecord, error)
	GetIntegrationTargetByProviderRef(
		context.Context,
		integrationstore.ID,
		integrationstore.ID,
		string,
	) (integrationstore.IntegrationTargetRecord, error)
	ListActiveReceiveBindingsForTargetRoute(
		context.Context,
		integrationstore.ID,
		integrationstore.ID,
		integrationstore.ID,
		integrationstore.ID,
	) ([]integrationstore.IntegrationTargetBindingRecord, error)
	IntegrationRuntimeLeaseIsCurrent(
		context.Context,
		integrationstore.ID,
		integrationstore.ID,
		integrationstore.ID,
		integrationstore.ID,
		int64,
	) (bool, error)
}

type ChannelService struct {
	execution    channelExecutionStore
	integrations channelIntegrationStore
	handlers     ChannelRouteHandlers
}

func NewChannelService(
	execution channelExecutionStore,
	integrations channelIntegrationStore,
	handlers ChannelRouteHandlers,
) *ChannelService {
	registered := make(ChannelRouteHandlers, len(handlers))
	for key, handler := range handlers {
		if handler != nil {
			registered[key] = handler
		}
	}
	return &ChannelService{execution: execution, integrations: integrations, handlers: registered}
}

type MaterializeChannelInboundContentInput struct {
	ProjectID            integrationstore.ID
	AgentID              integrationstore.ID
	IntegrationInstallID integrationstore.ID
	IdempotencyKey       string
	RuntimeLease         *executionstore.IntegrationRuntimeLeaseProof
}

type MaterializeChannelInboundContentFunc func(
	context.Context,
	MaterializeChannelInboundContentInput,
) (json.RawMessage, error)

// PrepareChannelInboundContentFunc validates and decodes an event once, before
// route execution, and returns the only function allowed to materialize that
// exact content for an agent.
type PrepareChannelInboundContentFunc func(
	context.Context,
	json.RawMessage,
) (MaterializeChannelInboundContentFunc, error)

type ProcessChannelInboundInput struct {
	IntegrationAppID integrationstore.ID
	Capabilities     []channelconnector.Capability
	Envelope         ChannelInboundEnvelope
	PrepareContent   PrepareChannelInboundContentFunc
}

type ChannelInboundAcceptance struct {
	RouteID      integrationstore.ID
	AgentID      executionstore.ID
	TargetID     integrationstore.ID
	BindingID    integrationstore.ID
	AgentInputID executionstore.ID
	Launch       executionstore.LaunchAgentResult
}

type ProcessChannelInboundResult struct {
	Accepted      []ChannelInboundAcceptance
	IgnoredRoutes int
	FailedRoutes  []ChannelInboundRouteFailure
}

type ChannelInboundRouteFailure struct {
	RouteID integrationstore.ID
	Err     error
}

type plannedChannelRoute struct {
	route       integrationstore.IntegrationRouteRecord
	decision    ChannelRouteDecision
	target      integrationstore.IntegrationTargetRecord
	bindings    []plannedChannelBindingDelivery
	attachments []plannedChannelAttachment
}

type plannedChannelBindingDelivery struct {
	binding  integrationstore.IntegrationTargetBindingRecord
	metadata json.RawMessage
}

type plannedChannelAttachment struct {
	action   ChannelAttachmentAction
	agent    executionstore.AgentRecord
	profile  executionstore.AgentProfileRecord
	metadata json.RawMessage
}

func (s *ChannelService) ProcessInbound(
	ctx context.Context,
	input ProcessChannelInboundInput,
) (ProcessChannelInboundResult, error) {
	return s.processInbound(ctx, input, nil)
}

func (s *ChannelService) ProcessRuntimeInbound(
	ctx context.Context,
	input ProcessChannelInboundInput,
	lease ChannelRuntimeLease,
) (ProcessChannelInboundResult, error) {
	if lease.UnitID == integrationstore.NilID || lease.Token == integrationstore.NilID ||
		lease.Generation <= 0 {
		return ProcessChannelInboundResult{}, storeerr.InvalidRequest(errors.New(
			"runtime unit, lease token, and positive lease generation are required",
		))
	}
	return s.processInbound(ctx, input, &lease)
}

func (s *ChannelService) processInbound(
	ctx context.Context,
	input ProcessChannelInboundInput,
	lease *ChannelRuntimeLease,
) (ProcessChannelInboundResult, error) {
	if err := validateProcessChannelInboundInput(input); err != nil {
		return ProcessChannelInboundResult{}, storeerr.InvalidRequest(err)
	}
	app, err := s.integrations.GetConnectorIntegrationApp(
		ctx,
		input.IntegrationAppID,
		input.Capabilities,
	)
	if err != nil {
		return ProcessChannelInboundResult{}, err
	}
	if strings.HasPrefix(app.ConnectorKey, "native_") {
		return ProcessChannelInboundResult{}, storeerr.ErrUnauthorized
	}
	install, err := s.integrations.GetConnectorIntegrationInstall(
		ctx,
		app.ID,
		input.Envelope.ExternalTenantID,
		input.Envelope.ExternalAccountRef,
	)
	if err != nil {
		return ProcessChannelInboundResult{}, err
	}
	if lease != nil {
		current, err := s.integrations.IntegrationRuntimeLeaseIsCurrent(
			ctx,
			app.ID,
			lease.UnitID,
			install.ID,
			lease.Token,
			lease.Generation,
		)
		if err != nil {
			return ProcessChannelInboundResult{}, err
		}
		if !current {
			return ProcessChannelInboundResult{}, storeerr.ErrStateTransitionConflict
		}
	}
	materializeContent, err := input.PrepareContent(ctx, input.Envelope.ContentBlocks)
	if err != nil {
		return ProcessChannelInboundResult{}, err
	}
	if materializeContent == nil {
		return ProcessChannelInboundResult{}, errors.New("inbound content materializer is required")
	}
	routes, err := s.integrations.ListActiveIntegrationRoutes(ctx, install.ProjectID, install.ID)
	if err != nil {
		return ProcessChannelInboundResult{}, err
	}
	result := ProcessChannelInboundResult{Accepted: make([]ChannelInboundAcceptance, 0, len(routes))}
	plans, ignoredRoutes, failures, err := s.planInboundRoutes(
		ctx,
		app,
		install,
		routes,
		input.Envelope,
	)
	if err != nil {
		return ProcessChannelInboundResult{}, err
	}
	result.IgnoredRoutes = ignoredRoutes
	result.FailedRoutes = append(result.FailedRoutes, failures...)
	preparedContent := make(map[integrationstore.ID]json.RawMessage)
	acceptedInputs := make(map[executionstore.ID]struct{})
	for _, plan := range plans {
		acceptances, err := s.executeInboundRoute(
			ctx, app, install, plan, input, lease, materializeContent, preparedContent,
		)
		if err != nil {
			if errors.Is(err, ErrChannelInboundCompletionRetry) {
				return ProcessChannelInboundResult{}, err
			}
			var routeErr permanentChannelRouteError
			if errors.As(err, &routeErr) {
				result.FailedRoutes = append(result.FailedRoutes, ChannelInboundRouteFailure{
					RouteID: plan.route.ID,
					Err:     routeErr.err,
				})
				continue
			}
			isolated, classifyErr := s.isolateChannelRouteExecutionError(
				ctx,
				app,
				install,
				lease,
				err,
			)
			if classifyErr != nil {
				return ProcessChannelInboundResult{}, classifyErr
			}
			if isolated {
				result.FailedRoutes = append(result.FailedRoutes, ChannelInboundRouteFailure{
					RouteID: plan.route.ID,
					Err:     err,
				})
				continue
			}
			return ProcessChannelInboundResult{}, err
		}
		for _, acceptance := range acceptances {
			if _, duplicate := acceptedInputs[acceptance.AgentInputID]; duplicate {
				continue
			}
			acceptedInputs[acceptance.AgentInputID] = struct{}{}
			result.Accepted = append(result.Accepted, acceptance)
		}
	}
	return result, nil
}

// Route dependencies can disappear after the mutation-free planning pass.
// Those state races are final for this route and event, but must not erase
// acceptances already committed for independent routes. Unknown storage errors
// remain global so the provider can retry them. A runtime lease conflict is
// global when it means this worker lost ownership of the installation.
func (s *ChannelService) isolateChannelRouteExecutionError(
	ctx context.Context,
	app integrationstore.IntegrationAppRecord,
	install integrationstore.IntegrationInstallRecord,
	lease *ChannelRuntimeLease,
	err error,
) (bool, error) {
	if !storeerr.IsNotFound(err) &&
		!errors.Is(err, storeerr.ErrInvalidRequest) &&
		!errors.Is(err, storeerr.ErrStateTransitionConflict) &&
		!errors.Is(err, storeerr.ErrUnauthorized) &&
		!errors.Is(err, storeerr.ErrConflict) &&
		!errors.Is(err, storeerr.ErrIdempotencyConflict) {
		return false, nil
	}
	if lease == nil || !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		return true, nil
	}
	current, checkErr := s.integrations.IntegrationRuntimeLeaseIsCurrent(
		ctx,
		app.ID,
		lease.UnitID,
		install.ID,
		lease.Token,
		lease.Generation,
	)
	if checkErr != nil {
		return false, checkErr
	}
	return current, nil
}

// planInboundRoutes is deliberately mutation-free. Every registered handler,
// decision, target requirement, and profile dependency is validated before a
// fanout starts creating agents, bindings, media, or inputs.
func (s *ChannelService) planInboundRoutes(
	ctx context.Context,
	app integrationstore.IntegrationAppRecord,
	install integrationstore.IntegrationInstallRecord,
	routes []integrationstore.IntegrationRouteRecord,
	envelope ChannelInboundEnvelope,
) ([]plannedChannelRoute, int, []ChannelInboundRouteFailure, error) {
	plans := make([]plannedChannelRoute, 0, len(routes))
	failures := make([]ChannelInboundRouteFailure, 0)
	ignored := 0
	for _, route := range routes {
		handler, ok := s.handlers[ChannelRouteHandlerKey(route.HandlerKey, route.HandlerVersion)]
		if !ok {
			return nil, 0, nil, fmt.Errorf(
				"%w: %s",
				ErrChannelRouteHandlerUnavailable,
				ChannelRouteHandlerKey(route.HandlerKey, route.HandlerVersion),
			)
		}
		lookup := newChannelRouteBindingLookup(s.integrations, install, route)
		decision, err := handler.Route(
			ctx,
			ChannelRouteContext{App: app, Install: install, Route: route, Bindings: lookup},
			envelope,
		)
		if err != nil {
			return nil, 0, nil, err
		}
		if !decision.Accept {
			ignored++
			continue
		}
		decision, err = normalizeChannelRouteDecision(decision)
		if err != nil {
			failures = append(failures, ChannelInboundRouteFailure{RouteID: route.ID, Err: err})
			continue
		}
		plan := plannedChannelRoute{route: route, decision: decision}
		var currentBindings []integrationstore.IntegrationTargetBindingRecord
		plan.target, currentBindings, err = lookup.resolve(
			ctx,
			decision.ProviderRef,
			decision.ProviderRefKind,
		)
		if err != nil {
			if errors.Is(err, storeerr.ErrConflict) {
				failures = append(failures, ChannelInboundRouteFailure{RouteID: route.ID, Err: err})
				continue
			}
			return nil, 0, nil, err
		}
		plan.bindings, err = planChannelBindingDeliveries(
			envelope,
			route,
			decision,
			currentBindings,
		)
		if err != nil {
			failures = append(failures, ChannelInboundRouteFailure{RouteID: route.ID, Err: err})
			continue
		}
		plan.attachments, err = s.planChannelAttachments(ctx, envelope, route, decision.Attachments)
		if err != nil {
			var routeErr permanentChannelRouteError
			if errors.As(err, &routeErr) {
				failures = append(failures, ChannelInboundRouteFailure{
					RouteID: route.ID,
					Err:     routeErr.err,
				})
				continue
			}
			if storeerr.IsNotFound(err) || errors.Is(err, storeerr.ErrStateTransitionConflict) {
				failures = append(failures, ChannelInboundRouteFailure{RouteID: route.ID, Err: err})
				continue
			}
			return nil, 0, nil, err
		}
		if len(plan.bindings) == 0 && len(plan.attachments) == 0 {
			ignored++
			continue
		}
		plans = append(plans, plan)
	}
	plans, kindFailures := rejectConflictingNewTargetKinds(plans)
	failures = append(failures, kindFailures...)
	if err := validateChannelInboundFanout(plans); err != nil {
		for _, plan := range plans {
			failures = append(failures, ChannelInboundRouteFailure{RouteID: plan.route.ID, Err: err})
		}
		plans = nil
	}
	return plans, ignored, failures, nil
}

// A provider ref is the immutable external identity of a target. Separate
// route handlers may interpret an event differently, but they cannot create
// that same identity with conflicting kinds. Reject every conflicting plan
// before fanout so route ordering cannot leave a partially processed event.
func rejectConflictingNewTargetKinds(
	plans []plannedChannelRoute,
) ([]plannedChannelRoute, []ChannelInboundRouteFailure) {
	kindByProviderRef := make(map[string]string, len(plans))
	conflicts := make(map[string]struct{})
	for _, plan := range plans {
		if plan.target.ID != integrationstore.NilID {
			continue
		}
		providerRef := plan.decision.ProviderRef
		kind, seen := kindByProviderRef[providerRef]
		if seen && kind != plan.decision.ProviderRefKind {
			conflicts[providerRef] = struct{}{}
			continue
		}
		kindByProviderRef[providerRef] = plan.decision.ProviderRefKind
	}
	if len(conflicts) == 0 {
		return plans, nil
	}

	valid := make([]plannedChannelRoute, 0, len(plans))
	failures := make([]ChannelInboundRouteFailure, 0, len(plans))
	for _, plan := range plans {
		if _, conflict := conflicts[plan.decision.ProviderRef]; !conflict ||
			plan.target.ID != integrationstore.NilID {
			valid = append(valid, plan)
			continue
		}
		failures = append(failures, ChannelInboundRouteFailure{
			RouteID: plan.route.ID,
			Err: fmt.Errorf(
				"channel routes disagree on the kind of provider ref %q",
				plan.decision.ProviderRef,
			),
		})
	}
	return valid, failures
}

func validateChannelInboundFanout(plans []plannedChannelRoute) error {
	deliveries := 0
	recipients := make(map[string]struct{})
	for _, plan := range plans {
		attachedAgents := make(map[integrationstore.ID]struct{}, len(plan.attachments))
		for _, attachment := range plan.attachments {
			deliveries++
			if attachment.action.AgentID != integrationstore.NilID {
				attachedAgents[attachment.action.AgentID] = struct{}{}
				recipients["agent:"+attachment.action.AgentID.String()] = struct{}{}
			} else {
				recipients["profile-instance:"+plan.route.ID.String()+":"+attachment.action.InstanceKey] =
					struct{}{}
			}
		}
		for _, delivery := range plan.bindings {
			if _, attached := attachedAgents[delivery.binding.AgentID]; attached {
				continue
			}
			deliveries++
			recipients["agent:"+delivery.binding.AgentID.String()] = struct{}{}
		}
		if deliveries > maxChannelInboundDeliveries || len(recipients) > maxChannelInboundRecipients {
			return fmt.Errorf(
				"channel event exceeds its total fanout limit (%d deliveries, %d recipients)",
				maxChannelInboundDeliveries,
				maxChannelInboundRecipients,
			)
		}
	}
	return nil
}

func planChannelBindingDeliveries(
	envelope ChannelInboundEnvelope,
	route integrationstore.IntegrationRouteRecord,
	decision ChannelRouteDecision,
	bindings []integrationstore.IntegrationTargetBindingRecord,
) ([]plannedChannelBindingDelivery, error) {
	byID := make(map[integrationstore.ID]integrationstore.IntegrationTargetBindingRecord, len(bindings))
	for _, binding := range bindings {
		byID[binding.ID] = binding
	}
	selected := bindings
	if !decision.DeliverToAllExisting {
		selected = make([]integrationstore.IntegrationTargetBindingRecord, 0, len(decision.ExistingBindingIDs))
		for _, bindingID := range decision.ExistingBindingIDs {
			binding, ok := byID[bindingID]
			if !ok {
				return nil, fmt.Errorf("selected channel binding %s is not active for the route target", bindingID)
			}
			selected = append(selected, binding)
		}
	}
	out := make([]plannedChannelBindingDelivery, 0, len(selected))
	for _, binding := range selected {
		metadata, err := channelInputMetadata(envelope, route, binding.Metadata)
		if err != nil {
			return nil, err
		}
		out = append(out, plannedChannelBindingDelivery{binding: binding, metadata: metadata})
	}
	return out, nil
}

func (s *ChannelService) planChannelAttachments(
	ctx context.Context,
	envelope ChannelInboundEnvelope,
	route integrationstore.IntegrationRouteRecord,
	actions []ChannelAttachmentAction,
) ([]plannedChannelAttachment, error) {
	out := make([]plannedChannelAttachment, 0, len(actions))
	for _, action := range actions {
		metadata, err := channelInputMetadata(envelope, route, action.Metadata)
		if err != nil {
			return nil, permanentChannelRouteError{err: err}
		}
		planned := plannedChannelAttachment{action: action, metadata: metadata}
		if action.AgentID != integrationstore.NilID {
			planned.agent, err = s.execution.GetAgentInProject(ctx, route.ProjectID, action.AgentID)
			if err != nil {
				return nil, err
			}
			if planned.agent.State != executionstore.AgentStateActive {
				return nil, storeerr.ErrStateTransitionConflict
			}
		} else {
			planned.profile, err = s.execution.GetAgentProfile(
				ctx,
				route.ProjectID,
				action.AgentProfileID,
			)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, planned)
	}
	return out, nil
}

type channelRouteBindingLookup struct {
	store   channelIntegrationStore
	install integrationstore.IntegrationInstallRecord
	route   integrationstore.IntegrationRouteRecord
	cache   map[string]channelRouteBindingLookupResult
}

type channelRouteBindingLookupResult struct {
	target   integrationstore.IntegrationTargetRecord
	bindings []integrationstore.IntegrationTargetBindingRecord
}

func newChannelRouteBindingLookup(
	store channelIntegrationStore,
	install integrationstore.IntegrationInstallRecord,
	route integrationstore.IntegrationRouteRecord,
) *channelRouteBindingLookup {
	return &channelRouteBindingLookup{
		store: store, install: install, route: route,
		cache: make(map[string]channelRouteBindingLookupResult),
	}
}

func (l *channelRouteBindingLookup) ListActiveForTarget(
	ctx context.Context,
	providerRef, providerRefKind string,
) ([]integrationstore.IntegrationTargetBindingRecord, error) {
	_, bindings, err := l.resolve(ctx, providerRef, providerRefKind)
	if err != nil {
		return nil, err
	}
	return append([]integrationstore.IntegrationTargetBindingRecord(nil), bindings...), nil
}

func (l *channelRouteBindingLookup) resolve(
	ctx context.Context,
	providerRef, providerRefKind string,
) (
	integrationstore.IntegrationTargetRecord,
	[]integrationstore.IntegrationTargetBindingRecord,
	error,
) {
	providerRef = strings.TrimSpace(providerRef)
	providerRefKind = strings.TrimSpace(providerRefKind)
	if providerRef == "" || providerRefKind == "" {
		return integrationstore.IntegrationTargetRecord{}, nil, errors.New(
			"channel binding lookup requires provider ref and kind",
		)
	}
	if len(providerRef) > 2048 || len(providerRefKind) > 128 {
		return integrationstore.IntegrationTargetRecord{}, nil, errors.New(
			"channel binding lookup exceeds its size limit",
		)
	}
	cacheKey := providerRef + "\x00" + providerRefKind
	if cached, ok := l.cache[cacheKey]; ok {
		return cached.target,
			append([]integrationstore.IntegrationTargetBindingRecord(nil), cached.bindings...),
			nil
	}
	target, err := l.store.GetIntegrationTargetByProviderRef(
		ctx,
		l.install.ProjectID,
		l.install.ID,
		providerRef,
	)
	if storeerr.IsNotFound(err) {
		l.cache[cacheKey] = channelRouteBindingLookupResult{}
		return integrationstore.IntegrationTargetRecord{}, nil, nil
	}
	if err != nil {
		return integrationstore.IntegrationTargetRecord{}, nil, err
	}
	if target.ProviderRefKind != providerRefKind {
		return integrationstore.IntegrationTargetRecord{}, nil, storeerr.ErrConflict
	}
	bindings, err := l.store.ListActiveReceiveBindingsForTargetRoute(
		ctx,
		l.install.ProjectID,
		l.install.ID,
		l.route.ID,
		target.ID,
	)
	if err != nil {
		return integrationstore.IntegrationTargetRecord{}, nil, err
	}
	result := channelRouteBindingLookupResult{target: target, bindings: bindings}
	l.cache[cacheKey] = result
	return target, append([]integrationstore.IntegrationTargetBindingRecord(nil), bindings...), nil
}

func (s *ChannelService) executeInboundRoute(
	ctx context.Context,
	app integrationstore.IntegrationAppRecord,
	install integrationstore.IntegrationInstallRecord,
	plan plannedChannelRoute,
	input ProcessChannelInboundInput,
	lease *ChannelRuntimeLease,
	materializeContent MaterializeChannelInboundContentFunc,
	preparedContent map[integrationstore.ID]json.RawMessage,
) (acceptances []ChannelInboundAcceptance, err error) {
	route := plan.route
	decision := plan.decision
	runtimeLease := channelRuntimeLeaseProof(app.ID, lease)
	profileAgentResolved := false
	defer func() {
		var permanent permanentChannelRouteError
		if err != nil && profileAgentResolved &&
			!errors.Is(err, ErrChannelInboundCompletionRetry) &&
			!errors.Is(err, storeerr.ErrInvalidRequest) &&
			!errors.Is(err, storeerr.ErrIdempotencyConflict) &&
			!errors.As(err, &permanent) {
			err = fmt.Errorf("%w: %w", ErrChannelInboundCompletionRetry, err)
		}
	}()

	type resolvedAttachment struct {
		planned plannedChannelAttachment
		agentID integrationstore.ID
		launch  executionstore.LaunchAgentResult
	}
	resolved := make([]resolvedAttachment, 0, len(plan.attachments))
	handledAgents := make(map[integrationstore.ID]struct{}, len(plan.attachments))
	for _, attachment := range plan.attachments {
		item := resolvedAttachment{planned: attachment, agentID: attachment.action.AgentID}
		if item.agentID == integrationstore.NilID {
			launchInput := executionstore.LaunchAgentInput{
				ProjectID:     route.ProjectID,
				ProfileID:     attachment.action.AgentProfileID,
				AgentConfigID: attachment.profile.CurrentConfigID,
				LaunchedBy:    identitystore.NewUserPrincipal(install.InstalledByUserID),
				IdempotencyKey: channelLaunchIdempotencyKey(
					route.ID,
					attachment.action.InstanceKey,
				),
			}
			var err error
			if runtimeLease == nil {
				item.launch, err = s.execution.LaunchAgent(ctx, launchInput)
			} else {
				item.launch, err = s.execution.LaunchAgentWithIntegrationRuntimeLease(
					ctx,
					launchInput,
					install.ID,
					runtimeLease,
				)
			}
			if err != nil {
				if errors.Is(err, storeerr.ErrInvalidRequest) ||
					errors.Is(err, storeerr.ErrModelGrantUnavailable) ||
					errors.Is(err, storeerr.ErrManagedWorkAdmissionDenied) {
					return nil, permanentChannelRouteError{err: fmt.Errorf(
						"%w: %w",
						storeerr.ErrAgentLaunchFailed,
						err,
					)}
				}
				return nil, fmt.Errorf("%w: %w", storeerr.ErrAgentLaunchFailed, err)
			}
			if item.launch.Agent.State != executionstore.AgentStateActive ||
				item.launch.Agent.AgentProfileID != attachment.action.AgentProfileID {
				return nil, permanentChannelRouteError{err: errors.New(
					"channel profile instance resolves to an unavailable or incompatible agent",
				)}
			}
			profileAgentResolved = true
			item.agentID = item.launch.Agent.ID
		}
		if _, duplicate := handledAgents[item.agentID]; duplicate {
			return nil, permanentChannelRouteError{err: fmt.Errorf(
				"channel route attaches agent %s more than once",
				item.agentID,
			)}
		}
		handledAgents[item.agentID] = struct{}{}
		resolved = append(resolved, item)
	}

	acceptances = make([]ChannelInboundAcceptance, 0, len(resolved)+len(plan.bindings))
	for _, attachment := range resolved {
		inputMetadata := attachment.planned.metadata
		contentBlocks, err := prepareChannelContent(
			ctx,
			input,
			app.ID,
			install,
			attachment.agentID,
			runtimeLease,
			materializeContent,
			preparedContent,
		)
		if err != nil {
			return nil, err
		}
		created, err := s.execution.CreateBoundIntegrationTargetContentInput(
			ctx,
			executionstore.CreateBoundIntegrationTargetContentInput{
				Target: integrationstore.CreateIntegrationTargetInput{
					ProjectID: install.ProjectID, AgentID: attachment.agentID,
					IntegrationInstallID: install.ID, ProviderRef: decision.ProviderRef,
					ProviderRefKind: decision.ProviderRefKind, DisplayName: decision.DisplayName,
					ProviderMetadata: decision.TargetMetadata,
				},
				IntegrationRouteID: route.ID, ReceiveAllowed: true,
				SendAllowed:   attachment.planned.action.SendAllowed,
				BindingSource: "channel_route", BindingMetadata: attachment.planned.action.Metadata,
				ProviderTenantID: install.ProviderTenantID,
				ProviderUserID:   input.Envelope.Actor.Ref,
				ActorDisplayName: input.Envelope.Actor.DisplayName,
				ContentBlocks:    contentBlocks, Metadata: inputMetadata,
				DeliveryMode: decision.DeliveryMode, IdempotencyKey: input.Envelope.ProviderEventID,
				CancelOpenInteractions: decision.CancelOpenInteractions,
				RuntimeLease:           runtimeLease,
			},
		)
		if err != nil {
			return nil, err
		}
		acceptances = append(acceptances, ChannelInboundAcceptance{
			RouteID: route.ID, AgentID: attachment.agentID,
			TargetID: created.IntegrationTargetID, BindingID: created.IntegrationTargetBindingID,
			AgentInputID: created.AgentInput.ID, Launch: attachment.launch,
		})
	}

	for _, delivery := range plan.bindings {
		if _, attached := handledAgents[delivery.binding.AgentID]; attached {
			continue
		}
		contentBlocks, err := prepareChannelContent(
			ctx,
			input,
			app.ID,
			install,
			delivery.binding.AgentID,
			runtimeLease,
			materializeContent,
			preparedContent,
		)
		if err != nil {
			return nil, err
		}
		agentInput, _, err := s.execution.CreateIntegrationTargetContentInput(
			ctx,
			executionstore.CreateIntegrationTargetContentInput{
				IntegrationInstallID: install.ID, IntegrationTargetID: plan.target.ID,
				IntegrationTargetBindingID: delivery.binding.ID,
				AgentID:                    delivery.binding.AgentID,
				RefreshTarget:              true,
				TargetDisplayName:          decision.DisplayName,
				TargetProviderMetadata:     decision.TargetMetadata,
				ProviderTenantID:           install.ProviderTenantID,
				ProviderUserID:             input.Envelope.Actor.Ref,
				ActorDisplayName:           input.Envelope.Actor.DisplayName,
				ContentBlocks:              contentBlocks,
				Metadata:                   delivery.metadata,
				DeliveryMode:               decision.DeliveryMode,
				IdempotencyKey:             input.Envelope.ProviderEventID,
				CancelOpenInteractions:     decision.CancelOpenInteractions,
				RuntimeLease:               runtimeLease,
			},
		)
		if err != nil {
			return nil, err
		}
		acceptances = append(acceptances, ChannelInboundAcceptance{
			RouteID: route.ID, AgentID: delivery.binding.AgentID, TargetID: plan.target.ID,
			BindingID: delivery.binding.ID, AgentInputID: agentInput.ID,
		})
	}
	return acceptances, nil
}

type permanentChannelRouteError struct{ err error }

func (e permanentChannelRouteError) Error() string { return e.err.Error() }
func (e permanentChannelRouteError) Unwrap() error { return e.err }

func prepareChannelContent(
	ctx context.Context,
	input ProcessChannelInboundInput,
	appID integrationstore.ID,
	install integrationstore.IntegrationInstallRecord,
	agentID integrationstore.ID,
	runtimeLease *executionstore.IntegrationRuntimeLeaseProof,
	materializeContent MaterializeChannelInboundContentFunc,
	preparedContent map[integrationstore.ID]json.RawMessage,
) (json.RawMessage, error) {
	if contentBlocks, ok := preparedContent[agentID]; ok {
		return contentBlocks, nil
	}
	contentBlocks, err := materializeContent(
		ctx,
		MaterializeChannelInboundContentInput{
			ProjectID: install.ProjectID, AgentID: agentID, IntegrationInstallID: install.ID,
			IdempotencyKey: channelMediaIdempotencyKey(
				appID,
				install.ID,
				agentID,
				input.Envelope.ProviderEventID,
			),
			RuntimeLease: runtimeLease,
		},
	)
	if err != nil {
		return nil, err
	}
	preparedContent[agentID] = contentBlocks
	return contentBlocks, nil
}

func channelRuntimeLeaseProof(
	appID integrationstore.ID,
	lease *ChannelRuntimeLease,
) *executionstore.IntegrationRuntimeLeaseProof {
	if lease == nil {
		return nil
	}
	return &executionstore.IntegrationRuntimeLeaseProof{
		IntegrationAppID: appID,
		UnitID:           lease.UnitID,
		LeaseToken:       lease.Token,
		LeaseGeneration:  lease.Generation,
	}
}

func validateProcessChannelInboundInput(input ProcessChannelInboundInput) error {
	if input.IntegrationAppID == integrationstore.NilID {
		return errors.New("integration app is required")
	}
	if input.PrepareContent == nil {
		return errors.New("inbound content preparer is required")
	}
	envelope := input.Envelope
	if envelope.Version != ChannelEnvelopeVersionV1 {
		return fmt.Errorf("unsupported channel envelope version %q", envelope.Version)
	}
	if strings.TrimSpace(envelope.ProviderEventID) == "" ||
		strings.TrimSpace(envelope.ExternalAccountRef) == "" ||
		strings.TrimSpace(envelope.Actor.Ref) == "" {
		return errors.New("provider event, external account, and actor refs are required")
	}
	if len(envelope.ProviderEventID) > 512 || len(envelope.ExternalTenantID) > 512 ||
		len(envelope.ExternalAccountRef) > 512 || len(envelope.Actor.Ref) > 512 ||
		len(envelope.EventType) > 256 {
		return errors.New("channel envelope identifier exceeds its size limit")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"provider event ID", envelope.ProviderEventID},
		{"external tenant ID", envelope.ExternalTenantID},
		{"external account ref", envelope.ExternalAccountRef},
		{"event type", envelope.EventType},
		{"conversation ref", envelope.Conversation.Ref},
		{"conversation kind", envelope.Conversation.Kind},
		{"conversation display name", envelope.Conversation.DisplayName},
		{"conversation parent ref", envelope.Conversation.ParentRef},
		{"conversation reply-to ref", envelope.Conversation.ReplyToRef},
		{"actor ref", envelope.Actor.Ref},
		{"actor display name", envelope.Actor.DisplayName},
	} {
		if err := dbsafe.Text(field.value); err != nil {
			return fmt.Errorf("channel %s %w", field.name, err)
		}
	}
	if utf8.RuneCountInString(envelope.Actor.DisplayName) > executionstore.MaxActorDisplayNameLength {
		return fmt.Errorf(
			"channel actor display name exceeds %d characters",
			executionstore.MaxActorDisplayNameLength,
		)
	}
	if len(envelope.ContentBlocks) == 0 {
		return errors.New("channel content blocks are required")
	}
	if len(envelope.ContentBlocks) > maxChannelContentBlocksBytes {
		return errors.New("channel content blocks exceed their size limit")
	}
	if err := dbsafe.JSONStrings(envelope.ContentBlocks); err != nil {
		return fmt.Errorf("channel content blocks JSON string %w", err)
	}
	if _, err := channelconnector.NormalizeOpaqueObject(envelope.Metadata); err != nil {
		return fmt.Errorf("channel envelope metadata: %w", err)
	}
	if _, err := channelconnector.NormalizeOpaqueObject(envelope.Conversation.Metadata); err != nil {
		return fmt.Errorf("channel conversation metadata: %w", err)
	}
	if _, err := channelconnector.NormalizeOpaqueObject(envelope.Actor.Metadata); err != nil {
		return fmt.Errorf("channel actor metadata: %w", err)
	}
	if _, err := channelInputMetadata(
		envelope,
		integrationstore.IntegrationRouteRecord{},
		json.RawMessage(`{}`),
	); err != nil {
		return fmt.Errorf("channel aggregate metadata: %w", err)
	}
	return nil
}

func normalizeChannelRouteDecision(decision ChannelRouteDecision) (ChannelRouteDecision, error) {
	decision.ProviderRef = strings.TrimSpace(decision.ProviderRef)
	decision.ProviderRefKind = strings.TrimSpace(decision.ProviderRefKind)
	decision.DisplayName = strings.TrimSpace(decision.DisplayName)
	if decision.ProviderRef == "" || decision.ProviderRefKind == "" {
		return ChannelRouteDecision{}, errors.New("accepted channel route requires provider ref and kind")
	}
	if len(decision.ProviderRef) > 2048 || len(decision.ProviderRefKind) > 128 ||
		len(decision.DisplayName) > 512 {
		return ChannelRouteDecision{}, errors.New("channel route decision exceeds its size limit")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"provider ref", decision.ProviderRef},
		{"provider ref kind", decision.ProviderRefKind},
		{"display name", decision.DisplayName},
	} {
		if err := dbsafe.Text(field.value); err != nil {
			return ChannelRouteDecision{}, fmt.Errorf("channel route %s %w", field.name, err)
		}
	}
	if len(decision.ExistingBindingIDs) > maxChannelRouteSelections ||
		len(decision.Attachments) > maxChannelRouteAttachments {
		return ChannelRouteDecision{}, errors.New("channel route decision exceeds its fanout limit")
	}
	if decision.DeliverToAllExisting && len(decision.ExistingBindingIDs) != 0 {
		return ChannelRouteDecision{}, errors.New(
			"channel route cannot select bindings while delivering to all existing bindings",
		)
	}
	if decision.DeliveryMode != executionstore.DeliveryModeQueued &&
		decision.DeliveryMode != executionstore.DeliveryModeSteering {
		return ChannelRouteDecision{}, errors.New("channel route requires queued or steering delivery")
	}
	if decision.CancelOpenInteractions && decision.DeliveryMode != executionstore.DeliveryModeSteering {
		return ChannelRouteDecision{}, errors.New(
			"canceling open interactions requires steering delivery",
		)
	}
	if len(decision.TargetMetadata) != 0 {
		targetMetadata, err := channelconnector.NormalizeOpaqueObject(decision.TargetMetadata)
		if err != nil {
			return ChannelRouteDecision{}, fmt.Errorf("channel target metadata: %w", err)
		}
		decision.TargetMetadata = targetMetadata
	}
	bindingIDs := make(map[integrationstore.ID]struct{}, len(decision.ExistingBindingIDs))
	for _, bindingID := range decision.ExistingBindingIDs {
		if bindingID == integrationstore.NilID {
			return ChannelRouteDecision{}, errors.New("channel route selected a nil binding")
		}
		if _, duplicate := bindingIDs[bindingID]; duplicate {
			return ChannelRouteDecision{}, errors.New("channel route selected a binding more than once")
		}
		bindingIDs[bindingID] = struct{}{}
	}
	agentIDs := make(map[integrationstore.ID]struct{}, len(decision.Attachments))
	instanceKeys := make(map[string]struct{}, len(decision.Attachments))
	for index := range decision.Attachments {
		action := &decision.Attachments[index]
		action.InstanceKey = strings.TrimSpace(action.InstanceKey)
		hasAgent := action.AgentID != integrationstore.NilID
		hasProfile := action.AgentProfileID != integrationstore.NilID
		if hasAgent == hasProfile {
			return ChannelRouteDecision{}, errors.New(
				"channel attachment requires exactly one agent or agent profile",
			)
		}
		if hasAgent && action.InstanceKey != "" {
			return ChannelRouteDecision{}, errors.New(
				"existing-agent channel attachment cannot have an instance key",
			)
		}
		if hasProfile && action.InstanceKey == "" {
			return ChannelRouteDecision{}, errors.New(
				"profile channel attachment requires a stable instance key",
			)
		}
		if len(action.InstanceKey) > 2048 {
			return ChannelRouteDecision{}, errors.New("channel attachment instance key exceeds its size limit")
		}
		if err := dbsafe.Text(action.InstanceKey); err != nil {
			return ChannelRouteDecision{}, fmt.Errorf("channel attachment instance key %w", err)
		}
		if hasAgent {
			if _, duplicate := agentIDs[action.AgentID]; duplicate {
				return ChannelRouteDecision{}, errors.New("channel route attaches an agent more than once")
			}
			agentIDs[action.AgentID] = struct{}{}
		} else {
			if _, duplicate := instanceKeys[action.InstanceKey]; duplicate {
				return ChannelRouteDecision{}, errors.New(
					"channel route uses a profile instance key more than once",
				)
			}
			instanceKeys[action.InstanceKey] = struct{}{}
		}
		metadata, err := channelconnector.NormalizeOpaqueObject(action.Metadata)
		if err != nil {
			return ChannelRouteDecision{}, fmt.Errorf("channel attachment metadata: %w", err)
		}
		action.Metadata = metadata
	}
	return decision, nil
}

func channelLaunchIdempotencyKey(
	routeID integrationstore.ID,
	instanceKey string,
) string {
	instanceKeyHash := sha256.Sum256([]byte(instanceKey))
	return fmt.Sprintf("channel-route:%s:instance:%x", routeID, instanceKeyHash)
}

func channelMediaIdempotencyKey(
	appID, installID, agentID integrationstore.ID,
	providerEventID string,
) string {
	return "channel-app:" + appID.String() + ":install:" + installID.String() +
		":agent:" + agentID.String() +
		":event:" + providerEventID
}

func channelInputMetadata(
	envelope ChannelInboundEnvelope,
	route integrationstore.IntegrationRouteRecord,
	bindingMetadata json.RawMessage,
) (json.RawMessage, error) {
	metadata, err := json.Marshal(map[string]any{
		"channel": map[string]any{
			"version": envelope.Version, "provider_event_id": envelope.ProviderEventID,
			"event_type": envelope.EventType, "occurred_at": envelope.OccurredAt,
			"conversation": envelope.Conversation, "actor_metadata": envelope.Actor.Metadata,
			"metadata": envelope.Metadata,
		},
		"integration_route_id": route.ID.String(),
		"binding_metadata":     bindingMetadata,
	})
	if err != nil {
		return nil, err
	}
	if len(metadata) > channelconnector.MaxMetadataBytes {
		return nil, fmt.Errorf(
			"channel input metadata exceeds the %d-byte limit",
			channelconnector.MaxMetadataBytes,
		)
	}
	if err := dbsafe.JSONB(metadata, channelconnector.MaxMetadataBytes); err != nil {
		return nil, fmt.Errorf("channel input metadata PostgreSQL-safe JSON: %w", err)
	}
	return metadata, nil
}
