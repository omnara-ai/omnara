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

var ErrScheduledLaunchFailed = errors.New("scheduled app launch failed")

// Publication runs outside transactions. The confirmed root is saved only in
// the frozen plan, which later thread preparation and admission reuse.
type scheduledConversationProvider interface {
	PublishScheduledRoot(
		context.Context,
		integrationstore.ProjectAppRecord,
		integrationstore.ScheduledAppLaunch,
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

func (c *AppInboxConsumer) consumeScheduled(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	receipt integrationstore.IntegrationInboxRecord,
	app integrationstore.ProjectAppRecord,
) ([]AppSlotAdmission, error) {
	provider, ok := c.providers[app.Provider].(scheduledConversationProvider)
	if !ok {
		return nil, fmt.Errorf("%w: app does not support scheduled threads", ErrScheduledLaunchFailed)
	}
	launch, err := receipt.ScheduledLaunch()
	if err != nil {
		return nil, err
	}
	authority := func(ctx context.Context) error {
		if _, err := c.router.execution.GetAgentProfile(ctx, receipt.ProjectID, launch.ProfileID); err != nil {
			return err
		}
		return c.inbox.WithIntegrationInboxLease(ctx, lease, func(*integrationstore.IntegrationInboxLeaseTx) error {
			return nil
		})
	}
	if len(receipt.Plan) == 0 {
		// Reject an unavailable profile before posting. A later deletion is fenced
		// again during admission; it can leave a heading but never an orphan agent.
		if err := authority(ctx); err != nil {
			return nil, err
		}
		root, err := provider.PublishScheduledRoot(ctx, app, launch, receipt.ID, authority)
		if err != nil {
			return nil, err
		}
		freezeErr := c.router.FreezeScheduledLaunch(ctx, lease, root)
		receipt, err = c.inbox.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
		if freezeErr != nil && (err != nil || len(receipt.Plan) == 0) {
			// A commit may have succeeded despite a lost acknowledgement. Reuse
			// its plan if visible; otherwise stop so deterministic errors cannot
			// repost a heading on every retry. Losing the lease or outcome write
			// can still leave a stray heading on recovery.
			return nil, fmt.Errorf("%w: save scheduled thread plan: %w", ErrScheduledLaunchFailed, errors.Join(freezeErr, err))
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
		return nil, fmt.Errorf("%w: invalid scheduled launch plan", ErrScheduledLaunchFailed)
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
			return c.router.execution.CheckInboxConversationAuthority(
				ctx, lease, key, integrationstore.ConversationAddress{Kind: kind, Ref: ref},
			)
		}
		if err := provider.EnsureScheduledThread(ctx, app, slot.Scope, check); err != nil {
			return nil, err
		}
	}
	return c.router.Admit(ctx, lease)
}

// FreezeScheduledLaunch authorizes exactly the accepted occurrence, independently
// of mention scopes and profile-picker slots. Only the real root is used for
// derivation; no mutable per-agent app destination or provisional config exists.
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
	launch, err := receipt.ScheduledLaunch()
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
	if _, err := r.execution.GetAgentProfile(ctx, receipt.ProjectID, launch.ProfileID); err != nil {
		return err
	}
	base, found, err := r.execution.GetAgentConfig(ctx, receipt.ProjectID, launch.ConfigID)
	if err != nil {
		return err
	}
	if !found {
		return storeerr.ErrNotFound
	}
	derived, listener, err := deriveAppLaunch(base, app, root)
	if err != nil {
		return err
	}
	actor, err := executionstore.ScheduledInboxActor(app, launch)
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
		Scope: root, AgentID: agentID, ListenerKey: listener,
		Selection:    &integrationstore.InboxAppSelection{AppID: app.ID, Address: address, Slot: key},
		BaseConfigID: derived.BaseConfigID, BaseConfigHash: derived.BaseConfigHash,
		Launch: &executionstore.LaunchAgentInput{
			ProjectID: receipt.ProjectID, ProfileID: launch.ProfileID,
			DerivedConfig: &derived.Config, DerivedBaseConfigID: derived.BaseConfigID,
			LaunchedBy:     identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeSystem, ID: launch.TriggerID},
			IdempotencyKey: "app:" + receipt.ID.String() + ":" + key,
			InitialInput: &executionstore.LaunchInitialInput{
				ContentBlocks: content, Actor: actor, SemanticEventKey: receipt.ReceiptKey,
				Origin: &executionstore.LaunchInputOrigin{AppID: app.ID, Address: address, DisplayName: launch.TriggerName},
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
