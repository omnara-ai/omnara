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
	order      int
	event      IntegrationEvent
	address    integrationstore.ConversationAddress
	scopes     []integrationstore.ConversationAddress
	candidates integrationstore.IntegrationRoutingCandidates
}

func (r *IntegrationRouter) Freeze(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	events []IntegrationEvent,
) (IntegrationInboxPlan, error) {
	receipt, err := r.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return nil, err
	}
	if len(receipt.Plan) != 0 {
		return decodeIntegrationInboxPlan(receipt.Plan)
	}
	integrationSetup, err := r.integrations.GetProjectIntegrationByID(ctx, receipt.IntegrationID)
	if err != nil {
		return nil, err
	}
	if integrationSetup.ProjectID != lease.ProjectID ||
		integrationSetup.State != integrationstore.ProjectIntegrationStateActive {
		return nil, storeerr.ErrUnauthorized
	}
	requests, err := prepareIntegrationEvents(events, integrationSetup)
	if err != nil {
		return nil, err
	}
	var frozen IntegrationInboxPlan
	err = r.integrations.WithIntegrationInboxLease(
		ctx,
		lease,
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			if raw := work.Receipt().Plan; len(raw) != 0 {
				var err error
				frozen, err = decodeIntegrationInboxPlan(raw)
				return err
			}
			for i := range requests {
				request := &requests[i]
				var err error
				request.candidates, err = r.candidatesForEvent(ctx, work, *request)
				if err != nil {
					return err
				}
			}
			return nil
		},
	)
	if err != nil || frozen != nil {
		return frozen, err
	}
	plan, err := r.buildIntegrationPlan(ctx, receipt, integrationSetup, requests)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	recipientEvents := make(map[int]bool, len(requests))
	for _, slot := range plan {
		recipientEvents[slot.EventOrder] = true
	}
	err = r.integrations.WithIntegrationInboxLease(
		ctx,
		lease,
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			if existing := work.Receipt().Plan; len(existing) != 0 {
				var err error
				frozen, err = decodeIntegrationInboxPlan(existing)
				return err
			}
			for _, request := range requests {
				current, err := r.candidatesForEvent(ctx, work, request)
				if err != nil {
					return err
				}
				for _, intent := range request.event.Launches {
					if _, _, err := resolveIntegrationLaunchIntent(receipt.ProjectID, intent, current); err != nil {
						return err
					}
				}
				if !sameIntegrationCandidates(current, request.candidates) {
					return ErrIntegrationRoutingChanged
				}
			}
			// A reserved agent may not have a subscription yet; freezing empty would lose its follow-up.
			for _, request := range requests {
				if !recipientEvents[request.order] {
					if err := work.CheckNoUnsettledIntegrationSelection(ctx, request.address); err != nil {
						return err
					}
				}
			}
			if err := work.FreezePlan(ctx, raw); err != nil {
				return err
			}
			frozen = plan
			return nil
		},
	)
	return frozen, err
}

func prepareIntegrationEvents(
	events []IntegrationEvent,
	integrationSetup integrationstore.ProjectIntegrationRecord,
) ([]integrationEventCandidates, error) {
	if len(events) > 64 {
		return nil, fmt.Errorf("integration expansion exceeds 64 events")
	}
	definition, found := integrationdefinition.Lookup(integrationSetup.IntegrationType)
	if !found {
		return nil, fmt.Errorf("unknown integration type %q", integrationSetup.IntegrationType)
	}
	requests := make([]integrationEventCandidates, 0, len(events))
	seen := map[string]bool{}
	for order, event := range events {
		if event.SemanticKey == "" || len(event.SemanticKey) > 512 || seen[event.SemanticKey] {
			return nil, fmt.Errorf("events require distinct bounded semantic keys")
		}
		seen[event.SemanticKey] = true
		if event.Event.Scope.Provider() != integrationSetup.Provider || event.Actor.ProviderUserID == "" {
			return nil, storeerr.ErrUnauthorized
		}
		account := integrationSetup.ProviderTenantID
		if integrationSetup.Provider == integrationdefinition.ProviderGitHub {
			account = integrationSetup.ProviderAccountRef
		}
		addresses, err := event.Event.RoutingAddresses(account)
		if err != nil {
			return nil, err
		}
		if event.DeliveryMode != "" && event.DeliveryMode != executionstore.DeliveryModeQueued &&
			event.DeliveryMode != executionstore.DeliveryModeSteering {
			return nil, fmt.Errorf("invalid delivery mode")
		}
		if event.CancelOpenInteractions && event.DeliveryMode != executionstore.DeliveryModeSteering {
			return nil, fmt.Errorf("canceling interactions requires steering delivery")
		}
		if _, _, err := integrationRecipientContent(event); err != nil {
			return nil, err
		}
		request := integrationEventCandidates{event: event, order: order, forwards: definition.Forwards(event.Event.Kind)}
		for _, address := range addresses {
			request.scopes = append(
				request.scopes,
				integrationstore.ConversationAddress{Kind: address.Kind, Ref: address.Ref},
			)
		}
		request.address = request.scopes[0]
		requests = append(requests, request)
	}
	slices.SortFunc(requests, func(a, b integrationEventCandidates) int {
		if n := strings.Compare(a.address.Kind, b.address.Kind); n != 0 {
			return n
		}
		if n := strings.Compare(a.address.Ref, b.address.Ref); n != 0 {
			return n
		}
		return strings.Compare(a.event.SemanticKey, b.event.SemanticKey)
	})
	return requests, nil
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
	ctx context.Context,
	receipt integrationstore.IntegrationInboxRecord,
	integrationSetup integrationstore.ProjectIntegrationRecord,
	requests []integrationEventCandidates,
) (IntegrationInboxPlan, error) {
	plan := IntegrationInboxPlan{}
	profiles := map[uuid.UUID]executionstore.AgentProfileRecord{}
	var planned []integrationstore.IntegrationSubscriptionRecord
	requests = slices.Clone(requests)
	slices.SortFunc(requests, func(a, b integrationEventCandidates) int { return a.order - b.order })
	type selectionKey struct {
		integrationID uuid.UUID
		slot          string
		address       integrationstore.ConversationAddress
	}
	type selectedRecipient struct {
		agentID uuid.UUID
		order   int
	}
	selected := map[selectionKey]selectedRecipient{}
	for _, request := range requests {
		event, candidates := request.event, request.candidates
		content, err := integrationdefinition.AppendInputContext(
			integrationSetup.Name,
			event.Event.Scope,
			event.ContentBlocks,
		)
		if err != nil {
			return nil, err
		}
		event.ContentBlocks = content
		recipients := map[uuid.UUID]*executionstore.InboxSubscriptionAuthority{}
		addSubscription := func(agentID uuid.UUID, address integrationstore.ConversationAddress) {
			authority := recipients[agentID]
			if authority == nil {
				authority = &executionstore.InboxSubscriptionAuthority{}
				recipients[agentID] = authority
			}
			authority.Alternatives = append(authority.Alternatives, address)
		}
		for _, subscription := range candidates.Subscriptions {
			if event.Directed || !request.matchesSubscriptionAddress(subscription.Address) {
				continue
			}
			addSubscription(subscription.AgentID, subscription.Address)
		}
		for _, subscription := range planned {
			if event.Directed {
				break
			}
			if subscription.IntegrationID != integrationSetup.ID ||
				!request.matchesSubscriptionAddress(subscription.Address) {
				continue
			}
			addSubscription(subscription.AgentID, subscription.Address)
		}
		for _, intent := range event.Launches {
			integration, settledAgent, err := resolveIntegrationLaunchIntent(receipt.ProjectID, intent, candidates)
			if err != nil {
				return nil, err
			}
			if intent.AgentID != uuid.Nil {
				recipients[intent.AgentID] = nil
				continue
			}
			if settledAgent != uuid.Nil {
				agent, err := r.execution.GetAgentInProject(ctx, receipt.ProjectID, settledAgent)
				if err != nil {
					return nil, err
				}
				if agent.AgentProfileID != intent.ProfileID {
					return nil, fmt.Errorf("%w: settled agent uses another profile", ErrIntegrationLaunchUnavailable)
				}
				recipients[settledAgent] = nil
				continue
			}
			identity := selectionKey{integration.ID, intent.Slot, request.address}
			if prior, found := selected[identity]; found {
				if prior.order != request.order {
					recipients[prior.agentID] = nil
				}
				continue
			}
			profileID := intent.ProfileID
			profile, found := profiles[profileID]
			if !found {
				var err error
				profile, err = r.execution.GetAgentProfile(ctx, receipt.ProjectID, profileID)
				if err != nil {
					return nil, err
				}
				profiles[profileID] = profile
			}
			derived, subscriptions, err := deriveIntegrationLaunch(profile.CurrentConfig, integration, event.Event.Scope)
			if err != nil {
				return nil, err
			}
			savedConfig, err := r.execution.CreateAgentConfig(ctx, derived)
			if err != nil {
				return nil, err
			}
			agentID, err := uuid.NewV7()
			if err != nil {
				return nil, err
			}
			content, files, err := integrationRecipientContent(event)
			if err != nil {
				return nil, err
			}
			key := integrationPlanKey(event.SemanticKey, "profile", integration.ID.String(), intent.Slot)
			selection := &integrationstore.InboxIntegrationSelection{
				IntegrationID: integration.ID,
				Address:       request.address,
				Slot:          intent.Slot,
			}
			launch := &executionstore.InboxLaunchPlan{
				ProfileID: profileID,
				LaunchedBy: executionstore.InboxLaunchPrincipal{
					Type: identitystore.PrincipalTypeUser, ID: integrationSetup.InstalledByUserID,
				},
				AgentConfigID:       savedConfig.ID,
				DerivedBaseConfigID: profile.CurrentConfig.ID,
				Subscriptions:       subscriptions,
				IdempotencyKey:      "integration:" + receipt.ID.String() + ":" + key,
				InitialInput: &executionstore.LaunchInitialInput{
					ContentBlocks:          content,
					Metadata:               event.Metadata,
					Actor:                  &event.Actor,
					SemanticEventKey:       event.SemanticKey,
					DeliveryMode:           event.DeliveryMode,
					CancelOpenInteractions: event.CancelOpenInteractions,
					Origin: &executionstore.LaunchInputOrigin{
						IntegrationID: integrationSetup.ID,
						Address:       request.address,
						DisplayName:   event.DisplayName,
					},
				},
			}
			plan[key] = IntegrationInboxSlot{
				Sibling:     event.Sibling,
				Scope:       event.Event.Scope,
				EventOrder:  request.order,
				Selection:   selection,
				AgentID:     agentID,
				Launch:      launch,
				Files:       files,
				ArtifactIDs: integrationArtifactIDs(files),
			}
			for _, subscription := range subscriptions {
				planned = append(planned, integrationstore.IntegrationSubscriptionRecord{
					IntegrationID: subscription.IntegrationID, AgentID: agentID,
					Address: request.address,
				})
			}
			selected[identity] = selectedRecipient{agentID, request.order}
		}
		for agentID, subscription := range recipients {
			content, files, err := integrationRecipientContent(event)
			if err != nil {
				return nil, err
			}
			key := integrationPlanKey(event.SemanticKey, "input", agentID.String())
			plan[key] = IntegrationInboxSlot{
				Sibling:      event.Sibling,
				Scope:        event.Event.Scope,
				EventOrder:   request.order,
				Subscription: subscription,
				AgentID:      agentID,
				Files:        files,
				ArtifactIDs:  integrationArtifactIDs(files),
				Input: &executionstore.CreateAgentContentInputInput{
					ProjectID:     receipt.ProjectID,
					AgentID:       agentID,
					ContentBlocks: content,
					Metadata:      event.Metadata,
					Actor:         &event.Actor,
					Origin: &executionstore.AgentInputOrigin{
						IntegrationID: integrationSetup.ID,
						Address:       request.address,
						DisplayName:   event.DisplayName,
					},
					IdempotencyKey:         event.SemanticKey,
					DeliveryMode:           event.DeliveryMode,
					CancelOpenInteractions: event.CancelOpenInteractions,
				},
			}
		}
	}
	return plan, nil
}

func resolveIntegrationLaunchIntent(
	projectID uuid.UUID,
	intent IntegrationLaunchIntent,
	candidates integrationstore.IntegrationRoutingCandidates,
) (integrationstore.ProjectIntegrationRecord, uuid.UUID, error) {
	unavailable := func() (integrationstore.ProjectIntegrationRecord, uuid.UUID, error) {
		return integrationstore.ProjectIntegrationRecord{}, uuid.Nil,
			fmt.Errorf("%w: integration %s slot %q", ErrIntegrationLaunchUnavailable, intent.IntegrationID, intent.Slot)
	}
	if intent.IntegrationID == uuid.Nil || intent.Slot == "" ||
		(intent.ProfileID == uuid.Nil) == (intent.AgentID == uuid.Nil) {
		return unavailable()
	}
	integration := candidates.Launcher
	if integration == nil || integration.ID != intent.IntegrationID || integration.ProjectID != projectID ||
		integration.State != integrationstore.ProjectIntegrationStateActive || integration.Settings.Launcher == nil {
		return unavailable()
	}
	for _, slot := range integration.Settings.Launcher.Slots {
		if slot.Key != intent.Slot {
			continue
		}
		var profileID, agentID uuid.UUID
		if slot.AgentProfileID != nil {
			profileID = *slot.AgentProfileID
		}
		if slot.AgentID != nil {
			agentID = *slot.AgentID
		}
		if profileID != intent.ProfileID || agentID != intent.AgentID {
			return unavailable()
		}
		if profileID != uuid.Nil {
			for _, target := range candidates.Selections {
				if target.IntegrationID != integration.ID || target.SelectionSlot != slot.Key {
					continue
				}
				if target.DeletedAt != nil || target.AgentID == uuid.Nil {
					return unavailable()
				}
				return *integration, target.AgentID, nil
			}
		}
		return *integration, uuid.Nil, nil
	}
	return unavailable()
}

func deriveIntegrationLaunch(
	base executionstore.AgentConfigRecord,
	integration integrationstore.ProjectIntegrationRecord,
	scope integrationdefinition.Scope,
) (executionstore.CreateAgentConfigInput, []integrationstore.IntegrationSubscriptionAttachment, error) {
	derived, err := deriveIntegrationLaunchConfig(base, integration)
	if err != nil {
		return executionstore.CreateAgentConfigInput{}, nil, err
	}
	definition, _ := integrationdefinition.Lookup(integration.IntegrationType)
	subscriptions, err := integrationLaunchSubscriptions(integration.ID, definition, scope)
	return derived, subscriptions, err
}

func deriveIntegrationLaunchConfig(
	base executionstore.AgentConfigRecord, integration integrationstore.ProjectIntegrationRecord,
) (executionstore.CreateAgentConfigInput, error) {
	if base.ProjectID != integration.ProjectID || integration.State != integrationstore.ProjectIntegrationStateActive {
		return executionstore.CreateAgentConfigInput{}, storeerr.ErrUnauthorized
	}
	definition, found := integrationdefinition.Lookup(integration.IntegrationType)
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
				IntegrationType: integration.IntegrationType,
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

func integrationArtifactIDs(files []IntegrationPlannedFile) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(files))
	for _, file := range files {
		ids = append(ids, file.ArtifactID)
	}
	return ids
}

func integrationRecipientContent(event IntegrationEvent) (json.RawMessage, []IntegrationPlannedFile, error) {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(event.ContentBlocks, &blocks) != nil || len(blocks) == 0 {
		return nil, nil, fmt.Errorf("event content blocks are required")
	}
	files := make([]IntegrationPlannedFile, 0, len(event.Files))
	byID := map[uuid.UUID]IntegrationPlannedFile{}
	for _, file := range event.Files {
		if file.ArtifactID == uuid.Nil || file.ProviderFileID == "" || len(file.ProviderFileID) > 512 ||
			byID[file.ArtifactID].ArtifactID != uuid.Nil {
			return nil, nil, fmt.Errorf("invalid event file identity")
		}
		byID[file.ArtifactID] = file
		if file.Expected != nil {
			if err := file.Expected.Validate(); err != nil {
				return nil, nil, err
			}
			if file.Expected.ID != file.ArtifactID {
				return nil, nil, fmt.Errorf("file expectation differs from placeholder identity")
			}
		}
	}
	replacements := map[uuid.UUID]uuid.UUID{}
	for _, block := range blocks {
		var kind string
		if json.Unmarshal(block["type"], &kind) != nil {
			return nil, nil, fmt.Errorf("invalid event content block")
		}
		switch kind {
		case "text":
			var text string
			if json.Unmarshal(block["text"], &text) != nil {
				return nil, nil, fmt.Errorf("invalid event text")
			}
		case "media_ref":
			var old uuid.UUID
			if json.Unmarshal(block["artifact_id"], &old) != nil || byID[old].ArtifactID == uuid.Nil {
				return nil, nil, fmt.Errorf("media reference has no provider file identity")
			}
			id := replacements[old]
			if id == uuid.Nil {
				var err error
				id, err = uuid.NewV7()
				if err != nil {
					return nil, nil, err
				}
				replacements[old] = id
				file := byID[old]
				file.ArtifactID = id
				if file.Expected != nil {
					expected := *file.Expected
					expected.ID = id
					file.Expected = &expected
				}
				files = append(files, file)
			}
			encoded, err := json.Marshal(id.String())
			if err != nil {
				return nil, nil, err
			}
			block["artifact_id"] = encoded
		default:
			return nil, nil, fmt.Errorf("unsupported event content block %q", kind)
		}
	}
	if len(replacements) != len(byID) {
		return nil, nil, fmt.Errorf("event file is not referenced by content")
	}
	raw, err := json.Marshal(blocks)
	return raw, files, err
}
