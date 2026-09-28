package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

var (
	ErrIntegrationRoutingChanged    = errors.New("integration routing changed while planning; retry before preparation")
	ErrIntegrationLaunchUnavailable = errors.New("integration launch choice is no longer available")
)

type integrationEventCandidates struct {
	forwards   bool
	event      IntegrationEvent
	address    integrationstore.ConversationAddress
	scopes     []integrationstore.ConversationAddress
	candidates integrationstore.IntegrationRoutingCandidates
}

func (r *IntegrationRouter) Freeze(
	ctx context.Context, lease integrationstore.IntegrationInboxLease, event *IntegrationEvent,
) (IntegrationInboxPlan, error) {
	receipt, err := r.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return IntegrationInboxPlan{}, err
	}
	if len(receipt.Plan) != 0 {
		return decodeIntegrationInboxPlan(receipt.Plan)
	}
	integrationSetup, err := r.integrations.GetIntegrationByID(ctx, receipt.IntegrationID)
	if err != nil {
		return IntegrationInboxPlan{}, err
	}
	if integrationSetup.ProjectID != lease.ProjectID || integrationSetup.State != integrationstore.IntegrationStateActive {
		return IntegrationInboxPlan{}, storeerr.ErrUnauthorized
	}
	var request integrationEventCandidates
	if event != nil {
		request, err = prepareIntegrationEvent(*event, integrationSetup)
		if err != nil {
			return IntegrationInboxPlan{}, err
		}
	}
	var frozen IntegrationInboxPlan
	err = r.integrations.WithIntegrationInboxLease(ctx, lease, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		if raw := work.Receipt().Plan; len(raw) != 0 {
			frozen, err = decodeIntegrationInboxPlan(raw)
			return err
		}
		if event != nil {
			request.candidates, err = r.candidatesForEvent(ctx, work, request)
		}
		return err
	})
	if err != nil || frozen.Recipients != nil {
		return frozen, err
	}
	plan := IntegrationInboxPlan{Recipients: map[string]IntegrationInboxSlot{}}
	if event != nil {
		plan, err = r.buildIntegrationPlan(ctx, receipt, integrationSetup, request)
		if err != nil {
			return IntegrationInboxPlan{}, err
		}
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return IntegrationInboxPlan{}, err
	}
	err = r.integrations.WithIntegrationInboxLease(ctx, lease, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		if existing := work.Receipt().Plan; len(existing) != 0 {
			frozen, err = decodeIntegrationInboxPlan(existing)
			return err
		}
		if event != nil {
			current, err := r.candidatesForEvent(ctx, work, request)
			if err != nil {
				return err
			}
			for _, intent := range event.Launches {
				if _, _, err := resolveIntegrationLaunchIntent(receipt.ProjectID, event.Event, intent, current); err != nil {
					return err
				}
			}
			if !sameIntegrationCandidates(current, request.candidates) {
				return ErrIntegrationRoutingChanged
			}
			hasSelection := false
			for _, recipient := range plan.Recipients {
				if recipient.Selection != nil {
					hasSelection = true
					break
				}
			}
			// A reserved agent may not have a subscription yet, even when observers already match.
			if !hasSelection {
				if err := work.CheckNoUnsettledIntegrationSelection(ctx, request.address); err != nil {
					return err
				}
			}
		}
		if err := work.FreezePlan(ctx, raw); err != nil {
			// FreezePlan rejects immutable plan shape/size; admission capacity remains retryable.
			if errors.Is(err, storeerr.ErrInvalidRequest) {
				return fmt.Errorf("%w: freeze inbox plan: %w", ErrIntegrationInboundPermanent, err)
			}
			return err
		}
		frozen = plan
		return nil
	})
	return frozen, err
}

func prepareIntegrationEvent(
	event IntegrationEvent, integrationSetup integrationstore.IntegrationRecord,
) (integrationEventCandidates, error) {
	fail := func(err error) (integrationEventCandidates, error) { return integrationEventCandidates{}, err }
	definition, found := integrationdefinition.Lookup(integrationSetup.IntegrationKind)
	if !found {
		return fail(fmt.Errorf("unknown integration type %q", integrationSetup.IntegrationKind))
	}
	if event.SemanticKey == "" || len(event.SemanticKey) > 512 {
		return fail(fmt.Errorf("event requires a bounded semantic key"))
	}
	if event.Event.Scope.Provider() != integrationSetup.Provider || event.Actor.ProviderUserID == "" {
		return fail(storeerr.ErrUnauthorized)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return fail(fmt.Errorf("%w: encode event: %w", ErrIntegrationInboundPermanent, err))
	}
	if err := dbsafe.JSONStrings(raw); err != nil {
		return fail(fmt.Errorf("%w: event %w", ErrIntegrationInboundPermanent, err))
	}
	addresses, err := event.Event.RoutingAddresses()
	if err != nil {
		return fail(err)
	}
	if event.DeliveryMode != "" && event.DeliveryMode != executionstore.DeliveryModeQueued &&
		event.DeliveryMode != executionstore.DeliveryModeSteering {
		return fail(fmt.Errorf("invalid delivery mode"))
	}
	if event.CancelOpenInteractions && event.DeliveryMode != executionstore.DeliveryModeSteering {
		return fail(fmt.Errorf("canceling interactions requires steering delivery"))
	}
	message := executionstore.InboxMessage{ContentBlocks: event.ContentBlocks, Files: event.Files}
	ids, err := integrationRecipientArtifactIDs(event.Files)
	if err != nil {
		return fail(err)
	}
	if _, _, err := message.RecipientContent(ids); err != nil {
		return fail(err)
	}
	request := integrationEventCandidates{event: event, forwards: definition.Forwards(event.Event.Kind)}
	for _, address := range addresses {
		request.scopes = append(request.scopes, integrationstore.ConversationAddress{Kind: address.Kind, Ref: address.Ref})
	}
	request.address = request.scopes[0]
	return request, nil
}

func (r *IntegrationRouter) candidatesForEvent(
	ctx context.Context,
	work *integrationstore.IntegrationInboxLeaseTx,
	request integrationEventCandidates,
) (integrationstore.IntegrationRoutingCandidates, error) {
	candidates, err := r.integrations.IntegrationRoutingCandidatesForInbox(ctx, work, request.address, request.scopes)
	if !request.forwards {
		candidates.Subscriptions = nil
	}
	return candidates, err
}

func (r integrationEventCandidates) matchesSubscriptionAddress(address integrationstore.ConversationAddress) bool {
	if !r.forwards || !slices.Contains(r.scopes, address) {
		return false
	}
	scope := r.event.Event.Scope.Discord
	if scope == nil || address == r.address {
		return true
	}
	if !r.event.Event.Mentioned || address.Kind != "channel" || address.Ref != scope.ChannelID {
		return false
	}
	var metadata DiscordEventMetadata
	return json.Unmarshal(r.event.Metadata, &metadata) == nil && metadata.ThreadStarter &&
		metadata.SourceChannelID == scope.ChannelID && metadata.ChannelID == scope.ChannelID &&
		metadata.MessageID == scope.ThreadID && metadata.ThreadID == scope.ThreadID && metadata.GuildID == scope.GuildID
}

func sameIntegrationCandidates(a, b integrationstore.IntegrationRoutingCandidates) bool {
	canonical := func(c integrationstore.IntegrationRoutingCandidates) integrationstore.IntegrationRoutingCandidates {
		c.Subscriptions, c.Selections = slices.Clone(c.Subscriptions), slices.Clone(c.Selections)
		slices.SortFunc(
			c.Subscriptions,
			func(a, b integrationstore.IntegrationSubscriptionRecord) int { return bytes.Compare(a.ID[:], b.ID[:]) },
		)
		slices.SortFunc(
			c.Selections,
			func(a, b integrationstore.IntegrationTargetRecord) int { return bytes.Compare(a.ID[:], b.ID[:]) },
		)
		return c
	}
	return reflect.DeepEqual(canonical(a), canonical(b))
}

func (r *IntegrationRouter) buildIntegrationPlan(
	ctx context.Context, receipt integrationstore.IntegrationInboxRecord,
	integrationSetup integrationstore.IntegrationRecord, request integrationEventCandidates,
) (IntegrationInboxPlan, error) {
	fail := func(err error) (IntegrationInboxPlan, error) { return IntegrationInboxPlan{}, err }
	event, candidates := request.event, request.candidates
	content, err := integrationdefinition.AppendInputContext(integrationSetup.Name, event.Event.Scope, event.ContentBlocks)
	if err != nil {
		return fail(err)
	}
	plan := IntegrationInboxPlan{
		Message: &executionstore.InboxMessage{
			Scope: event.Event.Scope, ContentBlocks: content, Metadata: event.Metadata, Actor: &event.Actor,
			Origin: &executionstore.AgentInputOrigin{
				IntegrationID: integrationSetup.ID, Address: request.address, DisplayName: event.DisplayName,
			},
			SemanticKey: event.SemanticKey, DeliveryMode: event.DeliveryMode,
			CancelOpenInteractions: event.CancelOpenInteractions,
			Sibling:                event.Sibling, Files: event.Files,
		},
		Recipients: map[string]IntegrationInboxSlot{},
	}
	recipients := map[uuid.UUID]*executionstore.InboxSubscriptionAuthority{}
	for _, subscription := range candidates.Subscriptions {
		if event.Directed || !request.matchesSubscriptionAddress(subscription.Address) {
			continue
		}
		authority := recipients[subscription.AgentID]
		if authority == nil {
			authority = &executionstore.InboxSubscriptionAuthority{}
			recipients[subscription.AgentID] = authority
		}
		if !slices.Contains(authority.Alternatives, subscription.Address) {
			authority.Alternatives = append(authority.Alternatives, subscription.Address)
		}
	}
	for _, intent := range event.Launches {
		integration, settledAgent, err := resolveIntegrationLaunchIntent(receipt.ProjectID, event.Event, intent, candidates)
		if err != nil {
			return fail(err)
		}
		if settledAgent != uuid.Nil {
			agent, err := r.execution.GetAgentInProject(ctx, receipt.ProjectID, settledAgent)
			if err != nil {
				return fail(err)
			}
			if agent.AgentProfileID != intent.ProfileID {
				return fail(fmt.Errorf("%w: settled agent uses another profile", ErrIntegrationLaunchUnavailable))
			}
			recipients[settledAgent] = nil
			continue
		}
		key := integrationPlanKey(event.SemanticKey, "profile", integration.ID.String(), intent.LaunchKey)
		if _, exists := plan.Recipients[key]; exists {
			continue
		}
		profile, err := r.execution.GetAgentProfile(ctx, receipt.ProjectID, intent.ProfileID)
		if storeerr.IsNotFound(err) {
			return fail(fmt.Errorf("configured launcher profile %s is unavailable: %w",
				intent.ProfileID, ErrIntegrationLaunchUnavailable))
		}
		if err != nil {
			return fail(err)
		}
		derived, subscriptions, err := deriveIntegrationLaunch(profile.CurrentConfig, integration, event.Event.Scope)
		if err != nil {
			return fail(err)
		}
		savedConfig, err := r.execution.CreateAgentConfig(ctx, derived)
		if err != nil {
			return fail(err)
		}
		agentID, err := uuid.NewV7()
		if err != nil {
			return fail(err)
		}
		ids, err := integrationRecipientArtifactIDs(event.Files)
		if err != nil {
			return fail(err)
		}
		plan.Recipients[key] = IntegrationInboxSlot{
			Selection: &integrationstore.InboxIntegrationSelection{
				IntegrationID: integration.ID, Address: request.address, LaunchKey: intent.LaunchKey,
			},
			AgentID: agentID, ArtifactIDs: ids,
			Launch: &executionstore.InboxLaunchPlan{
				ProfileID: intent.ProfileID, AgentConfigID: savedConfig.ID, DerivedBaseConfigID: profile.CurrentConfig.ID,
				LaunchedBy: executionstore.InboxLaunchPrincipal{
					Type: identitystore.PrincipalTypeUser, ID: integrationSetup.InstalledByUserID,
				},
				Subscriptions: subscriptions, IdempotencyKey: "integration:" + receipt.ID.String() + ":" + key,
			},
		}
	}
	for agentID, subscription := range recipients {
		ids, err := integrationRecipientArtifactIDs(event.Files)
		if err != nil {
			return fail(err)
		}
		key := integrationPlanKey(event.SemanticKey, "input", agentID.String())
		plan.Recipients[key] = IntegrationInboxSlot{AgentID: agentID, Subscription: subscription, ArtifactIDs: ids}
	}
	return plan, nil
}

func resolveIntegrationLaunchIntent(
	projectID uuid.UUID,
	event integrationdefinition.Event,
	intent IntegrationLaunchIntent,
	candidates integrationstore.IntegrationRoutingCandidates,
) (integrationstore.IntegrationRecord, uuid.UUID, error) {
	unavailable := func() (integrationstore.IntegrationRecord, uuid.UUID, error) {
		return integrationstore.IntegrationRecord{}, uuid.Nil,
			fmt.Errorf("%w: integration %s launch key %q",
				ErrIntegrationLaunchUnavailable, intent.IntegrationID, intent.LaunchKey)
	}
	if intent.IntegrationID == uuid.Nil || intent.LaunchKey == "" || intent.ProfileID == uuid.Nil {
		return unavailable()
	}
	integration := candidates.Launcher
	if integration == nil || integration.ID != intent.IntegrationID ||
		integration.ProjectID != projectID || integration.State != integrationstore.IntegrationStateActive {
		return unavailable()
	}
	definition, ok := integrationdefinition.Lookup(integration.IntegrationKind)
	if !ok || definition.AuthorizeLaunch(integration.Settings, event, integrationdefinition.LaunchIntent{
		LaunchKey: intent.LaunchKey, ProfileID: intent.ProfileID,
	}) != nil {
		return unavailable()
	}
	for _, target := range candidates.Selections {
		if target.IntegrationID != integration.ID || target.LaunchKey == "" {
			continue
		}
		if target.DeletedAt != nil || target.AgentID == uuid.Nil {
			return unavailable()
		}
		return *integration, target.AgentID, nil
	}
	return *integration, uuid.Nil, nil
}

func deriveIntegrationLaunch(
	base executionstore.AgentConfigRecord,
	integration integrationstore.IntegrationRecord,
	scope integrationdefinition.Scope,
) (executionstore.CreateAgentConfigInput, []integrationstore.IntegrationSubscriptionAttachment, error) {
	derived, err := deriveIntegrationLaunchConfig(base, integration)
	if err != nil {
		return executionstore.CreateAgentConfigInput{}, nil, err
	}
	definition, _ := integrationdefinition.Lookup(integration.IntegrationKind)
	subscriptions, err := integrationLaunchSubscriptions(integration.ID, definition, scope)
	return derived, subscriptions, err
}

func deriveIntegrationLaunchConfig(
	base executionstore.AgentConfigRecord, integration integrationstore.IntegrationRecord,
) (executionstore.CreateAgentConfigInput, error) {
	if base.ProjectID != integration.ProjectID || integration.State != integrationstore.IntegrationStateActive {
		return executionstore.CreateAgentConfigInput{}, storeerr.ErrUnauthorized
	}
	definition, found := integrationdefinition.Lookup(integration.IntegrationKind)
	if !found {
		return executionstore.CreateAgentConfigInput{}, fmt.Errorf("invalid launcher integration definition")
	}
	additions := agentconfig.IntegrationCapabilitiesSource{Tools: map[string]agentconfig.AgentConfigToolSource{}}
	for _, operation := range definition.Tools {
		additions.Tools[toolcatalog.IntegrationToolName(integration.Name, operation)] = agentconfig.AgentConfigToolSource{}
	}
	if definition.InteractionHandler != nil {
		additions.InteractionHandlers = map[string]agentconfig.AgentConfigIntegrationCapabilitySource{integration.Name: {}}
		for _, name := range toolcatalog.InteractionHandlerToolNames() {
			additions.Tools[name] = agentconfig.AgentConfigToolSource{}
		}
	}
	return DeriveIntegrationProfileConfig(base, additions, agentconfig.CompileOptions{
		ResolveIntegrationName: func(name string) (agentconfig.IntegrationResolution, error) {
			if name != integration.Name {
				return agentconfig.IntegrationResolution{}, storeerr.ErrUnauthorized
			}
			return agentconfig.IntegrationResolution{
				IntegrationID:   integration.ID,
				IntegrationKind: integration.IntegrationKind,
			}, nil
		},
	})
}

func integrationLaunchSubscriptions(
	integrationID uuid.UUID, definition integrationdefinition.Definition, scope integrationdefinition.Scope,
) ([]integrationstore.IntegrationSubscriptionAttachment, error) {
	if err := scope.Validate(definition.Provider); err != nil {
		return nil, err
	}
	if !definition.SubscribeOnLaunch {
		return nil, nil
	}
	if definition.Subscription == nil {
		return nil, fmt.Errorf("integration cannot subscribe on launch without a subscription capability")
	}
	conversation, err := scope.ConversationJSON()
	if err != nil {
		return nil, err
	}
	return []integrationstore.IntegrationSubscriptionAttachment{{
		IntegrationID: integrationID,
		Conversation:  conversation,
	}}, nil
}

func integrationPlanKey(parts ...string) string {
	var value strings.Builder
	for _, part := range parts {
		value.WriteString(strconv.Itoa(len(part)))
		value.WriteByte(':')
		value.WriteString(part)
	}
	sum := sha256.Sum256([]byte(value.String()))
	return hex.EncodeToString(sum[:])
}

func integrationRecipientArtifactIDs(files []executionstore.InboxPlannedFile) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, len(files))
	for i := range ids {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	return ids, nil
}
