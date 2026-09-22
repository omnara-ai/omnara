package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrScheduledActionFailed = errors.New("scheduled app action failed")

type ScheduledThreadProvider interface {
	PublishScheduledRoot(
		context.Context,
		integrationstore.ProjectAppRecord,
		appdefinition.ScheduledThreadLaunch,
		uuid.UUID,
		func(context.Context) error,
	) (appdefinition.Scope, error)
	EnsureScheduledThread(
		context.Context,
		integrationstore.ProjectAppRecord,
		appdefinition.Scope,
		func(context.Context) error,
	) error
}

type ThreadAppScheduledHandler struct {
	router   *AppRouter
	inbox    AppRoutingStore
	provider ScheduledThreadProvider
}

func NewThreadAppScheduledHandler(
	router *AppRouter,
	inbox AppRoutingStore,
	provider ScheduledThreadProvider,
) *ThreadAppScheduledHandler {
	return &ThreadAppScheduledHandler{router: router, inbox: inbox, provider: provider}
}

func (h *ThreadAppScheduledHandler) Handle(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	receipt integrationstore.IntegrationInboxRecord,
	app integrationstore.ProjectAppRecord,
) ([]AppSlotAdmission, error) {
	event, err := receipt.ScheduledEvent()
	if err != nil {
		return nil, err
	}
	launch, err := appdefinition.PrepareThreadSchedule(app.AppType, event.Settings, event.Occurrence)
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
		root, err := h.provider.PublishScheduledRoot(ctx, app, launch, receipt.ID, authority)
		if err != nil {
			return nil, err
		}
		freezeErr := h.router.FreezeScheduledLaunch(ctx, lease, root)
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
	plan, err := decodeAppInboxPlan(receipt.Plan)
	if err != nil {
		return nil, err
	}
	if len(plan) != 1 {
		return nil, fmt.Errorf("%w: invalid scheduled launch plan", ErrScheduledActionFailed)
	}
	var progress map[string]struct {
		Committed json.RawMessage `json:"committed"`
	}
	if err := json.Unmarshal(receipt.Progress, &progress); err != nil {
		return nil, err
	}
	for key, slot := range plan {
		if len(progress[key].Committed) != 0 {
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
		if err := h.provider.EnsureScheduledThread(ctx, app, slot.Scope, check); err != nil {
			return nil, err
		}
	}
	return h.router.Admit(ctx, lease)
}

func (r *AppRouter) FreezeScheduledLaunch(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	root appdefinition.Scope,
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
	app, err := r.integrations.GetProjectAppByID(ctx, receipt.AppID)
	if err != nil {
		return err
	}
	if app.ProjectID != receipt.ProjectID || app.State != integrationstore.ProjectAppStateActive {
		return storeerr.ErrUnauthorized
	}
	launch, err := appdefinition.PrepareThreadSchedule(app.AppType, event.Settings, event.Occurrence)
	if err != nil {
		return err
	}
	profile, err := r.execution.GetAgentProfile(ctx, receipt.ProjectID, launch.ProfileID)
	if err != nil {
		return err
	}
	base, found, err := r.execution.GetAgentConfig(ctx, receipt.ProjectID, profile.CurrentConfigID)
	if err != nil {
		return err
	}
	if !found {
		return storeerr.ErrNotFound
	}
	derived, subscription, err := deriveAppLaunch(base, app, root)
	if err != nil {
		return err
	}
	actor, err := executionstore.ScheduledInboxActor(app, event)
	if err != nil {
		return err
	}
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": launch.Message}})
	if err != nil {
		return err
	}
	content, err = appdefinition.AppendInputContext(app.Name, root, content)
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
	plan := AppInboxPlan{key: {
		Scope: root, AgentID: agentID,
		Selection:    &integrationstore.InboxAppSelection{AppID: app.ID, Address: address, Slot: key},
		BaseConfigID: base.ID,
		Launch: &executionstore.LaunchAgentInput{
			ProjectID: receipt.ProjectID, ProfileID: launch.ProfileID,
			DerivedConfig: &derived, DerivedBaseConfigID: base.ID,
			Subscriptions:  []integrationstore.AppSubscriptionAttachment{subscription},
			LaunchedBy:     identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeSystem, ID: event.TriggerID},
			IdempotencyKey: "app:" + receipt.ID.String() + ":" + key,
			InitialInput: &executionstore.LaunchInitialInput{
				ContentBlocks: content, Actor: actor, SemanticEventKey: receipt.ReceiptKey,
				Origin: &executionstore.LaunchInputOrigin{AppID: app.ID, Address: address, DisplayName: event.Occurrence.Name},
			},
		},
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
