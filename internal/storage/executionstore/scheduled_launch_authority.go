package executionstore

import (
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// ScheduledInboxActor keeps scheduled tasks attributable to Omnara's cron actor.
// The provider thread is a destination, not a claim that a human wrote the task.
func ScheduledInboxActor(
	app integrationstore.ProjectAppRecord,
	launch integrationstore.ScheduledAppEvent,
) (*ActorParams, error) {
	return CronTriggerActor(app.OrgID, launch.TriggerID, launch.Occurrence.Name)
}

// The private admission flag is derived only from the fenced trusted receipt.
// Neither a provider event nor a caller-supplied plan can grant this alternative.
func validateScheduledInboxLaunch(
	receipt integrationstore.IntegrationInboxRecord,
	app integrationstore.ProjectAppRecord,
	slot InboxLaunchSlot,
) error {
	launch, err := receipt.ScheduledEvent()
	if err != nil {
		return err
	}
	if err := receipt.ValidateScheduledPlan(app, receipt.Plan); err != nil {
		return err
	}
	input := slot.Launch.InitialInput
	if app.ID != receipt.AppID || app.ProjectID != receipt.ProjectID ||
		slot.Selection.AppID != receipt.AppID ||
		slot.Launch.LaunchedBy.Type != identitystore.PrincipalTypeSystem || slot.Launch.LaunchedBy.ID != launch.TriggerID ||
		input == nil || input.Actor == nil || input.SemanticEventKey != receipt.ReceiptKey {
		return storeerr.ErrUnauthorized
	}
	actor, err := ScheduledInboxActor(app, launch)
	if err != nil {
		return err
	}
	if input.Actor.Provider != actor.Provider || input.Actor.ProviderTenantID != actor.ProviderTenantID ||
		input.Actor.ProviderUserID != actor.ProviderUserID {
		return storeerr.ErrUnauthorized
	}
	return nil
}
