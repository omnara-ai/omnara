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
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

var (
	ErrAppRoutingChanged    = errors.New("app routing changed while planning; retry before preparation")
	ErrAppLaunchUnavailable = errors.New("app launch choice is no longer available")
)

type appEventCandidates struct {
	order      int
	event      AppEvent
	address    integrationstore.ConversationAddress
	scopes     []integrationstore.ConversationAddress
	candidates integrationstore.AppRoutingCandidates
}

// Freeze pins the entire bounded expansion before media work. A retry with an
// existing plan ignores replacement events/configs. Reads/compilation between
// the two fenced transactions cannot hold conversation locks over pool reads.
// The second transaction checks routing again, then reserves all profile slots
// together. A competing unfinished reservation is returned intact to recovery.
func (r *AppRouter) Freeze(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	events []AppEvent,
) (AppInboxPlan, error) {
	receipt, err := r.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return nil, err
	}
	if len(receipt.Plan) != 0 {
		return decodeAppInboxPlan(receipt.Plan)
	}
	appSetup, err := r.integrations.GetProjectAppByID(ctx, receipt.AppID)
	if err != nil {
		return nil, err
	}
	if appSetup.ProjectID != lease.ProjectID ||
		appSetup.State != integrationstore.ProjectAppStateActive {
		return nil, storeerr.ErrUnauthorized
	}
	requests, err := prepareAppEvents(events, appSetup)
	if err != nil {
		return nil, err
	}
	var frozen AppInboxPlan
	err = r.integrations.WithIntegrationInboxLease(
		ctx,
		lease,
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			if raw := work.Receipt().Plan; len(raw) != 0 {
				var err error
				frozen, err = decodeAppInboxPlan(raw)
				return err
			}
			for i := range requests {
				request := &requests[i]
				var err error
				request.candidates, err = r.integrations.AppRoutingCandidatesForInbox(
					ctx,
					work,
					request.address,
					request.scopes,
					request.event.Event.Kind,
				)
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
	plan, err := r.buildAppPlan(ctx, receipt, appSetup, requests)
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
				frozen, err = decodeAppInboxPlan(existing)
				return err
			}
			for _, request := range requests {
				current, err := r.integrations.AppRoutingCandidatesForInbox(
					ctx,
					work,
					request.address,
					request.scopes,
					request.event.Event.Kind,
				)
				if err != nil {
					return err
				}
				for _, intent := range request.event.Launches {
					if _, _, err := resolveAppLaunchIntent(receipt.ProjectID, intent, current); err != nil {
						return err
					}
				}
				if !sameAppCandidates(current, request.candidates) {
					return ErrAppRoutingChanged
				}
			}
			// A plain follow-up may arrive before the first reserved agent has any
			// subscription. It must retry, not freeze empty and disappear. Check each
			// zero-recipient event, including within a multi-event expansion.
			for _, request := range requests {
				if !recipientEvents[request.order] {
					if err := work.CheckNoUnsettledAppSelection(ctx, request.address); err != nil {
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

func prepareAppEvents(
	events []AppEvent,
	appSetup integrationstore.ProjectAppRecord,
) ([]appEventCandidates, error) {
	if len(events) > 64 {
		return nil, fmt.Errorf("app expansion exceeds 64 events")
	}
	requests := make([]appEventCandidates, 0, len(events))
	seen := map[string]bool{}
	for order, event := range events {
		if event.SemanticKey == "" || len(event.SemanticKey) > 512 || seen[event.SemanticKey] {
			return nil, fmt.Errorf("events require distinct bounded semantic keys")
		}
		seen[event.SemanticKey] = true
		if event.Event.Scope.Provider() != appSetup.Provider || event.Actor.ProviderUserID == "" {
			return nil, storeerr.ErrUnauthorized
		}
		account := appSetup.ProviderTenantID
		if appSetup.Provider == appdefinition.ProviderGitHub {
			account = appSetup.ProviderAccountRef
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
		if _, _, err := appRecipientContent(event); err != nil {
			return nil, err
		}
		request := appEventCandidates{event: event, order: order}
		for _, address := range addresses {
			request.scopes = append(
				request.scopes,
				integrationstore.ConversationAddress{Kind: address.Kind, Ref: address.Ref},
			)
		}
		request.address = request.scopes[0]
		requests = append(requests, request)
	}
	// Every transaction takes the same sorted conversation union. FreezePlan
	// reenters these held gates when it checks the common selection envelopes.
	slices.SortFunc(requests, func(a, b appEventCandidates) int {
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

// Root Discord mentions normalize to their future thread address. Their verified
// source channel may authorize that one input; ordinary thread replies, including
// mentions within a thread, still require an exact thread subscription.
func (r appEventCandidates) matchesSubscriptionAddress(address integrationstore.ConversationAddress) bool {
	if !slices.Contains(r.scopes, address) {
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

func sameAppCandidates(a, b integrationstore.AppRoutingCandidates) bool {
	canonical := func(c integrationstore.AppRoutingCandidates) integrationstore.AppRoutingCandidates {
		c.Subscriptions, c.Selections = slices.Clone(c.Subscriptions), slices.Clone(c.Selections)
		slices.SortFunc(
			c.Subscriptions,
			func(a, b integrationstore.AppSubscriptionRecord) int { return bytes.Compare(a.ID[:], b.ID[:]) },
		)
		slices.SortFunc(
			c.Selections,
			func(a, b integrationstore.IntegrationTargetRecord) int { return bytes.Compare(a.ID[:], b.ID[:]) },
		)
		return c
	}
	return reflect.DeepEqual(canonical(a), canonical(b))
}

func (r *AppRouter) buildAppPlan(
	ctx context.Context,
	receipt integrationstore.IntegrationInboxRecord,
	appSetup integrationstore.ProjectAppRecord,
	requests []appEventCandidates,
) (AppInboxPlan, error) {
	plan := AppInboxPlan{}
	profiles := map[uuid.UUID]executionstore.AgentProfileRecord{}
	var planned []integrationstore.AppSubscriptionRecord
	requests = slices.Clone(requests)
	slices.SortFunc(requests, func(a, b appEventCandidates) int { return a.order - b.order })
	// Explicit decisions can repeat across an expansion, including late files.
	// Each app/slot/conversation gets one identity; another slot remains independent.
	type selectionKey struct {
		appID   uuid.UUID
		slot    string
		address integrationstore.ConversationAddress
	}
	type selectedRecipient struct {
		agentID uuid.UUID
		order   int
	}
	selected := map[selectionKey]selectedRecipient{}
	for _, request := range requests {
		event, candidates := request.event, request.candidates
		content, err := appdefinition.AppendInputContext(appSetup.Name, event.Event.Scope, event.ContentBlocks)
		if err != nil {
			return nil, err
		}
		event.ContentBlocks = content
		recipients := map[uuid.UUID]*executionstore.InboxSubscriptionAuthority{}
		addSubscription := func(agentID uuid.UUID, ref executionstore.InboxSubscriptionReference) {
			authority := recipients[agentID]
			if authority == nil {
				authority = &executionstore.InboxSubscriptionAuthority{Event: event.Event.Kind}
				recipients[agentID] = authority
			}
			authority.Alternatives = append(authority.Alternatives, ref)
		}
		for _, subscription := range candidates.Subscriptions {
			if event.Directed || !request.matchesSubscriptionAddress(subscription.Address) {
				continue
			}
			addSubscription(
				subscription.AgentID,
				executionstore.InboxSubscriptionReference{
					Type:    subscription.Type,
					Address: subscription.Address,
				},
			)
		}
		for _, subscription := range planned {
			if event.Directed {
				break
			}
			if subscription.AppID != appSetup.ID || !slices.Contains(subscription.Events, event.Event.Kind) ||
				!request.matchesSubscriptionAddress(subscription.Address) {
				continue
			}
			addSubscription(subscription.AgentID, executionstore.InboxSubscriptionReference{
				Type: subscription.Type, Address: subscription.Address,
			})
		}
		for _, intent := range event.Launches {
			app, settledAgent, err := resolveAppLaunchIntent(receipt.ProjectID, intent, candidates)
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
					return nil, fmt.Errorf("%w: settled agent uses another profile", ErrAppLaunchUnavailable)
				}
				// A chosen source or an intent racing another receipt's settlement
				// can reuse this recipient. It grants this input, not a subscription.
				recipients[settledAgent] = nil
				continue
			}
			identity := selectionKey{app.ID, intent.Slot, request.address}
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
			derived, subscription, err := deriveAppLaunch(profile.CurrentConfig, app, event.Event.Scope)
			if err != nil {
				return nil, err
			}
			agentID, err := uuid.NewV7()
			if err != nil {
				return nil, err
			}
			content, files, err := appRecipientContent(event)
			if err != nil {
				return nil, err
			}
			key := appPlanKey(event.SemanticKey, "profile", app.ID.String(), intent.Slot)
			selection := &integrationstore.InboxAppSelection{
				AppID:   app.ID,
				Address: request.address,
				Slot:    intent.Slot,
			}
			launch := &executionstore.LaunchAgentInput{
				ProjectID:           receipt.ProjectID,
				ProfileID:           profileID,
				LaunchedBy:          identitystore.NewUserPrincipal(appSetup.InstalledByUserID),
				DerivedConfig:       &derived,
				DerivedBaseConfigID: profile.CurrentConfig.ID,
				Subscriptions:       []integrationstore.AppSubscriptionAttachment{subscription},
				IdempotencyKey:      "app:" + receipt.ID.String() + ":" + key,
				InitialInput: &executionstore.LaunchInitialInput{
					ContentBlocks:          content,
					Metadata:               event.Metadata,
					Actor:                  &event.Actor,
					SemanticEventKey:       event.SemanticKey,
					DeliveryMode:           event.DeliveryMode,
					CancelOpenInteractions: event.CancelOpenInteractions,
					Origin: &executionstore.LaunchInputOrigin{
						AppID:       appSetup.ID,
						Address:     request.address,
						DisplayName: event.DisplayName,
					},
				},
			}
			plan[key] = AppInboxSlot{
				Sibling:      event.Sibling,
				Scope:        event.Event.Scope,
				EventOrder:   request.order,
				Selection:    selection,
				AgentID:      agentID,
				Launch:       launch,
				Files:        files,
				ArtifactIDs:  appArtifactIDs(files),
				BaseConfigID: profile.CurrentConfig.ID,
			}
			// This launch attaches exactly its source conversation. Keep a concrete
			// route for later events in this expansion, before admission persists it.
			planned = append(planned, integrationstore.AppSubscriptionRecord{
				AppID: subscription.AppID, AgentID: agentID, Type: subscription.Type,
				Address: request.address, Events: subscription.Events,
			})
			selected[identity] = selectedRecipient{agentID, request.order}
		}
		for agentID, subscription := range recipients {
			content, files, err := appRecipientContent(event)
			if err != nil {
				return nil, err
			}
			key := appPlanKey(event.SemanticKey, "input", agentID.String())
			plan[key] = AppInboxSlot{
				Sibling:      event.Sibling,
				Scope:        event.Event.Scope,
				EventOrder:   request.order,
				Subscription: subscription,
				AgentID:      agentID,
				Files:        files,
				ArtifactIDs:  appArtifactIDs(files),
				Input: &executionstore.CreateAgentContentInputInput{
					ProjectID:     receipt.ProjectID,
					AgentID:       agentID,
					ContentBlocks: content,
					Metadata:      event.Metadata,
					Actor:         &event.Actor,
					Origin: &executionstore.AgentInputOrigin{
						AppID:       appSetup.ID,
						Address:     request.address,
						DisplayName: event.DisplayName,
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

// resolveAppLaunchIntent validates the app-owned decision against live saved
// setup. Trigger and continuation policy belong to the app, never this planner.
func resolveAppLaunchIntent(
	projectID uuid.UUID,
	intent AppLaunchIntent,
	candidates integrationstore.AppRoutingCandidates,
) (integrationstore.ProjectAppRecord, uuid.UUID, error) {
	unavailable := func() (integrationstore.ProjectAppRecord, uuid.UUID, error) {
		return integrationstore.ProjectAppRecord{}, uuid.Nil,
			fmt.Errorf("%w: app %s slot %q", ErrAppLaunchUnavailable, intent.AppID, intent.Slot)
	}
	if intent.AppID == uuid.Nil || intent.Slot == "" || (intent.ProfileID == uuid.Nil) == (intent.AgentID == uuid.Nil) {
		return unavailable()
	}
	app := candidates.Launcher
	if app == nil || app.ID != intent.AppID || app.ProjectID != projectID ||
		app.State != integrationstore.ProjectAppStateActive || app.Settings.Launcher == nil {
		return unavailable()
	}
	for _, slot := range app.Settings.Launcher.Slots {
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
				if target.AppID != app.ID || target.SelectionSlot != slot.Key {
					continue
				}
				if target.DeletedAt != nil || target.AgentID == uuid.Nil {
					return unavailable()
				}
				return *app, target.AgentID, nil
			}
		}
		return *app, uuid.Nil, nil
	}
	return unavailable()
}

// deriveAppLaunch adds the hosted launcher's concrete capabilities without
// changing explicit profile entries. It resolves subscription defaults now so
// replay and later events use the exact attachment that atomic admission receives.
func deriveAppLaunch(
	base executionstore.AgentConfigRecord,
	app integrationstore.ProjectAppRecord,
	scope appdefinition.Scope,
) (executionstore.CreateAgentConfigInput, integrationstore.AppSubscriptionAttachment, error) {
	fail := func(err error) (executionstore.CreateAgentConfigInput, integrationstore.AppSubscriptionAttachment, error) {
		return executionstore.CreateAgentConfigInput{}, integrationstore.AppSubscriptionAttachment{}, err
	}
	if base.ProjectID != app.ProjectID || app.State != integrationstore.ProjectAppStateActive {
		return fail(storeerr.ErrUnauthorized)
	}
	definition, found := appdefinition.Lookup(app.AppType)
	if !found {
		return fail(fmt.Errorf("invalid launcher app definition"))
	}
	if err := scope.Validate(app.Provider); err != nil {
		return fail(err)
	}
	instance, err := publicid.Encode(publicid.KindProjectApp, app.ID)
	if err != nil {
		return fail(err)
	}
	subscriptionType := "thread_messages"
	if app.AppType == appdefinition.GitHubPR {
		subscriptionType = "pull_request"
	}
	subscription, exists := definition.Subscriptions[subscriptionType]
	if !exists {
		return fail(fmt.Errorf("app has no launch subscription"))
	}

	additions := agentconfig.AppCapabilitiesSource{Tools: map[string]agentconfig.AgentConfigToolSource{}}
	for _, operation := range definition.Tools {
		additions.Tools[toolcatalog.AppToolName(app.Name, operation)] = agentconfig.AgentConfigToolSource{}
	}
	if definition.InteractionHandler != nil {
		additions.InteractionHandlers = map[string]agentconfig.AgentConfigAppCapabilitySource{app.Name: {}}
	}
	derived, err := DeriveAppProfileConfig(base, additions, agentconfig.CompileOptions{
		ResolveAppName: func(name string) (agentconfig.AppResolution, error) {
			if name != app.Name {
				return agentconfig.AppResolution{}, storeerr.ErrUnauthorized
			}
			return agentconfig.AppResolution{AppID: instance, AppType: app.AppType}, nil
		},
	})
	if err != nil {
		return fail(err)
	}
	conversation, err := scope.ConversationJSON()
	if err != nil {
		return fail(err)
	}
	prepared, err := subscription.Prepare(conversation, nil)
	if err != nil {
		return fail(err)
	}
	return derived, integrationstore.AppSubscriptionAttachment{
		AppID: app.ID, Type: subscriptionType, Conversation: conversation, Events: slices.Clone(prepared.Events),
	}, nil
}

func appPlanKey(parts ...string) string {
	var value strings.Builder
	for _, part := range parts {
		value.WriteString(strconv.Itoa(len(part)))
		value.WriteByte(':')
		value.WriteString(part)
	}
	sum := sha256.Sum256([]byte(value.String()))
	return hex.EncodeToString(sum[:])
}

func appArtifactIDs(files []AppPlannedFile) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(files))
	for _, file := range files {
		ids = append(ids, file.ArtifactID)
	}
	return ids
}

func appRecipientContent(event AppEvent) (json.RawMessage, []AppPlannedFile, error) {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(event.ContentBlocks, &blocks) != nil || len(blocks) == 0 {
		return nil, nil, fmt.Errorf("event content blocks are required")
	}
	files := make([]AppPlannedFile, 0, len(event.Files))
	byID := map[uuid.UUID]AppPlannedFile{}
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
