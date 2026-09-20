package executionstore

import (
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// ScheduledInboxActor keeps scheduled tasks attributable to Omnara's cron actor.
// The provider thread is a destination, not a claim that a human wrote the task.
func ScheduledInboxActor(
	app integrationstore.ProjectAppRecord,
	launch integrationstore.ScheduledAppLaunch,
) (*ActorParams, error) {
	return CronTriggerActor(app.OrgID, launch.TriggerID, launch.TriggerName)
}

// The private admission flag is derived only from the fenced trusted receipt.
// Neither a provider event nor a caller-supplied plan can grant this alternative.
func validateScheduledInboxLaunch(
	receipt integrationstore.IntegrationInboxRecord,
	app integrationstore.ProjectAppRecord,
	slot InboxLaunchSlot,
) error {
	launch, err := receipt.ScheduledLaunch()
	if err != nil {
		return err
	}
	preparation, err := receipt.ScheduledPreparation()
	if err != nil {
		return err
	}
	if preparation.Root == nil {
		return storeerr.ErrUnauthorized
	}
	kind, ref, err := preparation.Root.Conversation()
	if err != nil {
		return err
	}
	input := slot.Launch.InitialInput
	if app.ID != receipt.AppID || app.ProjectID != receipt.ProjectID ||
		slot.Selection.AppID != receipt.AppID || slot.Selection.Slot != "scheduled" ||
		slot.Selection.Address != (integrationstore.ConversationAddress{Kind: kind, Ref: ref}) ||
		slot.Launch.ProfileID != launch.ProfileID || slot.Launch.DerivedBaseConfigID != launch.ConfigID ||
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
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": launch.Message}})
	if err != nil {
		return err
	}
	content, err = appdefinition.AppendInputContext(app.Name, *preparation.Root, content)
	if err != nil {
		return err
	}
	if !jsoncanonical.Equal(content, input.ContentBlocks) {
		return storeerr.ErrUnauthorized
	}
	return nil
}
