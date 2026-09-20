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

// Publication and thread preparation run outside transactions. A confirmed root
// is recorded with the frozen plan before EnsureScheduledThread or admission.
type scheduledConversationProvider interface {
	PublishScheduledRoot(
		context.Context,
		integrationstore.ProjectAppRecord,
		integrationstore.ScheduledAppLaunch,
		uuid.UUID,
		integrationstore.ScheduledLaunchPreparation,
		bool,
		func(context.Context) error,
	) (appdefinition.Scope, bool, error)
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
		var preparation integrationstore.ScheduledLaunchPreparation
		var fresh bool
		err = c.inbox.WithIntegrationInboxLease(ctx, lease, func(work *integrationstore.IntegrationInboxLeaseTx) error {
			var err error
			preparation, fresh, err = work.BeginScheduledPublication(ctx)
			return err
		})
		if err != nil {
			return nil, err
		}
		var root appdefinition.Scope
		if preparation.Root != nil {
			root = *preparation.Root
		} else {
			var notSent bool
			root, notSent, err = provider.PublishScheduledRoot(ctx, app, launch, receipt.ID, preparation, fresh, authority)
			if err != nil {
				if fresh && notSent {
					recordErr := c.inbox.WithIntegrationInboxLease(
						ctx,
						lease,
						func(work *integrationstore.IntegrationInboxLeaseTx) error {
							return work.RecordScheduledNonDelivery(ctx)
						},
					)
					err = errors.Join(err, recordErr)
				}
				return nil, err
			}
		}
		if _, err := c.router.FreezeScheduledLaunch(ctx, lease, root); err != nil {
			// Preserve confirmed publication even when derivation/planning fails. If the
			// lease was lost, the next owner must reconcile, never publish blindly.
			recordErr := c.inbox.WithIntegrationInboxLease(
				ctx,
				lease,
				func(work *integrationstore.IntegrationInboxLeaseTx) error {
					return work.RecordScheduledRoot(ctx, root)
				},
			)
			return nil, errors.Join(err, recordErr)
		}
		receipt, err = c.inbox.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
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
) (AppInboxPlan, error) {
	receipt, err := r.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return nil, err
	}
	if len(receipt.Plan) != 0 {
		return decodeAppInboxPlan(receipt.Plan)
	}
	launch, err := receipt.ScheduledLaunch()
	if err != nil {
		return nil, err
	}
	app, err := r.integrations.GetProjectAppByID(ctx, receipt.AppID)
	if err != nil {
		return nil, err
	}
	if app.ProjectID != receipt.ProjectID || app.State != integrationstore.ProjectAppStateActive {
		return nil, storeerr.ErrUnauthorized
	}
	if _, err := r.execution.GetAgentProfile(ctx, receipt.ProjectID, launch.ProfileID); err != nil {
		return nil, err
	}
	base, found, err := r.execution.GetAgentConfig(ctx, receipt.ProjectID, launch.ConfigID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, storeerr.ErrNotFound
	}
	derived, listener, err := deriveAppLaunch(base, app, root)
	if err != nil {
		return nil, err
	}
	actor, err := executionstore.ScheduledInboxActor(app, launch)
	if err != nil {
		return nil, err
	}
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": launch.Message}})
	if err != nil {
		return nil, err
	}
	content, err = appdefinition.AppendInputContext(app.Name, root, content)
	if err != nil {
		return nil, err
	}
	kind, ref, err := root.Conversation()
	if err != nil {
		return nil, err
	}
	agentID, err := uuid.NewV7()
	if err != nil {
		return nil, err
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
		return nil, err
	}
	err = r.integrations.WithIntegrationInboxLease(ctx, lease, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		if len(work.Receipt().Plan) != 0 {
			var err error
			plan, err = decodeAppInboxPlan(work.Receipt().Plan)
			return err
		}
		if err := work.RecordScheduledRoot(ctx, root); err != nil {
			return err
		}
		return work.FreezePlan(ctx, raw)
	})
	return plan, err
}
