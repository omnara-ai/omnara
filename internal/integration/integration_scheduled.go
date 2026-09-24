package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrScheduledActionFailed = errors.New("scheduled integration action failed")

type ScheduledThreadProvider interface {
	PublishScheduledRoot(
		context.Context,
		integrationstore.ProjectIntegrationRecord,
		integrationdefinition.ScheduledThreadLaunch,
		uuid.UUID,
		func(context.Context) error,
	) (integrationdefinition.Scope, error)
	EnsureScheduledThread(
		context.Context,
		integrationstore.ProjectIntegrationRecord,
		integrationdefinition.Scope,
		func(context.Context) error,
	) error
}

type ThreadIntegrationScheduledHandler struct {
	router   *IntegrationRouter
	inbox    IntegrationRoutingStore
	provider ScheduledThreadProvider
}

func NewThreadIntegrationScheduledHandler(
	router *IntegrationRouter,
	inbox IntegrationRoutingStore,
	provider ScheduledThreadProvider,
) *ThreadIntegrationScheduledHandler {
	return &ThreadIntegrationScheduledHandler{router: router, inbox: inbox, provider: provider}
}

func (h *ThreadIntegrationScheduledHandler) Handle(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	receipt integrationstore.IntegrationInboxRecord,
	integration integrationstore.ProjectIntegrationRecord,
) ([]IntegrationSlotAdmission, error) {
	event, err := receipt.ScheduledEvent()
	if err != nil {
		return nil, err
	}
	launch, err := integrationdefinition.PrepareThreadSchedule(
		integration.IntegrationType,
		event.Settings,
		event.Occurrence,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: prepare scheduled thread: %w", ErrScheduledActionFailed, err)
	}
	authority := func(ctx context.Context) error {
		if _, err := h.router.execution.GetAgentProfile(ctx, receipt.ProjectID, launch.ProfileID); err != nil {
			if storeerr.IsNotFound(err) {
				return fmt.Errorf("%w: scheduled profile unavailable: %w", ErrScheduledActionFailed, err)
			}
			return err
		}
		return h.inbox.WithIntegrationInboxLease(ctx, lease, func(*integrationstore.IntegrationInboxLeaseTx) error {
			return nil
		})
	}
	if len(receipt.Plan) == 0 {
		if err := authority(ctx); err != nil {
			return nil, err
		}
		profile, err := h.router.execution.GetAgentProfile(ctx, receipt.ProjectID, launch.ProfileID)
		if err != nil {
			return nil, err
		}
		derived, err := deriveIntegrationLaunchConfig(profile.CurrentConfig, integration)
		if err != nil {
			return nil, err
		}
		saved, err := h.router.execution.CreateAgentConfig(ctx, derived)
		if err != nil {
			return nil, err
		}
		root, err := h.provider.PublishScheduledRoot(ctx, integration, launch, receipt.ID, authority)
		if err != nil {
			return nil, err
		}
		freezeErr := h.router.FreezeScheduledLaunch(ctx, lease, root, profile.ID, saved.ID, profile.CurrentConfigID)
		receipt, err = h.inbox.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
		if freezeErr != nil && (err != nil || len(receipt.Plan) == 0) {
			// Retrying without a saved plan can duplicate the published heading.
			// A crash before this terminal outcome is saved still leaves that recovery gap.
			return nil, fmt.Errorf("%w: save scheduled thread plan: %w", ErrScheduledActionFailed, errors.Join(freezeErr, err))
		}
		if err != nil {
			return nil, err
		}
	}
	plan, err := decodeIntegrationInboxPlan(receipt.Plan)
	if err != nil {
		return nil, err
	}
	if len(plan) != 1 {
		return nil, fmt.Errorf("%w: invalid scheduled launch plan", ErrScheduledActionFailed)
	}
	outcomes, err := h.router.execution.GetIntegrationInboxOutcomes(ctx, receipt)
	if err != nil {
		return nil, err
	}
	for key, slot := range plan {
		if outcomes[key] != executionstore.InboxSlotPending {
			continue
		}
		kind, ref, err := slot.Scope.Conversation()
		if err != nil {
			return nil, err
		}
		check := func(ctx context.Context) error {
			return h.router.execution.CheckInboxConversationAuthority(
				ctx, lease, key, integrationstore.ConversationAddress{Kind: kind, Ref: ref},
			)
		}
		if err := h.provider.EnsureScheduledThread(ctx, integration, slot.Scope, check); err != nil {
			return nil, err
		}
	}
	return h.router.Admit(ctx, lease, nil)
}

func (r *IntegrationRouter) FreezeScheduledLaunch(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	root integrationdefinition.Scope,
	profileID, configID, baseConfigID uuid.UUID,
) error {
	receipt, err := r.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return err
	}
	if len(receipt.Plan) != 0 {
		return nil
	}
	event, err := receipt.ScheduledEvent()
	if err != nil {
		return err
	}
	integration, err := r.integrations.GetProjectIntegrationByID(ctx, receipt.IntegrationID)
	if err != nil {
		return err
	}
	if integration.ProjectID != receipt.ProjectID || integration.State != integrationstore.ProjectIntegrationStateActive {
		return storeerr.ErrUnauthorized
	}
	launch, err := integrationdefinition.PrepareThreadSchedule(
		integration.IntegrationType,
		event.Settings,
		event.Occurrence,
	)
	if err != nil {
		return err
	}
	if _, err := r.execution.GetAgentProfile(ctx, receipt.ProjectID, launch.ProfileID); err != nil {
		return err
	}
	if profileID != launch.ProfileID {
		return storeerr.ErrUnauthorized
	}
	definition, _ := integrationdefinition.Lookup(integration.IntegrationType)
	subscriptions, err := integrationLaunchSubscriptions(integration.ID, definition, root)
	if err != nil {
		return err
	}
	actor, err := executionstore.ScheduledInboxActor(integration, event)
	if err != nil {
		return err
	}
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": launch.Message}})
	if err != nil {
		return err
	}
	content, err = integrationdefinition.AppendInputContext(integration.Name, root, content)
	if err != nil {
		return err
	}
	kind, ref, err := root.Conversation()
	if err != nil {
		return err
	}
	agentID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	key := "scheduled"
	address := integrationstore.ConversationAddress{Kind: kind, Ref: ref}
	frozenLaunch := executionstore.InboxLaunchPlan{
		ProfileID: profileID, AgentConfigID: configID, DerivedBaseConfigID: baseConfigID,
		Subscriptions: subscriptions,
	}
	frozenLaunch.LaunchedBy = executionstore.InboxLaunchPrincipal{
		Type: identitystore.PrincipalTypeSystem, ID: event.TriggerID,
	}
	frozenLaunch.IdempotencyKey = "integration:" + receipt.ID.String() + ":" + key
	frozenLaunch.InitialInput = &executionstore.LaunchInitialInput{
		ContentBlocks: content, Actor: actor, SemanticEventKey: receipt.ReceiptKey,
		Origin: &executionstore.LaunchInputOrigin{
			IntegrationID: integration.ID,
			Address:       address,
			DisplayName:   event.Occurrence.Name,
		},
	}
	plan := IntegrationInboxPlan{key: {
		Scope: root, AgentID: agentID,
		Selection: &integrationstore.InboxIntegrationSelection{IntegrationID: integration.ID, Address: address, Slot: key},
		Launch:    &frozenLaunch,
	}}
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	return r.integrations.WithIntegrationInboxLease(
		ctx, lease, func(work *integrationstore.IntegrationInboxLeaseTx) error {
			if len(work.Receipt().Plan) != 0 {
				return nil
			}
			return work.FreezePlan(ctx, raw)
		},
	)
}
