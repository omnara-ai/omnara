package executionstore

import (
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func ScheduledInboxActor(
	integration integrationstore.ProjectIntegrationRecord,
	launch integrationstore.ScheduledIntegrationEvent,
) (*ActorParams, error) {
	return CronTriggerActor(integration.OrgID, launch.TriggerID, launch.Occurrence.Name)
}

func validateScheduledInboxLaunch(
	receipt integrationstore.IntegrationInboxRecord,
	integration integrationstore.ProjectIntegrationRecord,
	slot InboxLaunchSlot,
) error {
	launch, err := receipt.ScheduledEvent()
	if err != nil {
		return err
	}
	if err := receipt.ValidateScheduledPlan(integration, receipt.Plan); err != nil {
		return err
	}
	input := slot.Launch.InitialInput
	if integration.ID != receipt.IntegrationID || integration.ProjectID != receipt.ProjectID ||
		slot.Selection.IntegrationID != receipt.IntegrationID ||
		slot.Launch.LaunchedBy.Type != identitystore.PrincipalTypeSystem || slot.Launch.LaunchedBy.ID != launch.TriggerID ||
		input == nil || input.Actor == nil || input.SemanticEventKey != receipt.ReceiptKey {
		return storeerr.ErrUnauthorized
	}
	actor, err := ScheduledInboxActor(integration, launch)
	if err != nil {
		return err
	}
	if input.Actor.Provider != actor.Provider || input.Actor.ProviderTenantID != actor.ProviderTenantID ||
		input.Actor.ProviderUserID != actor.ProviderUserID {
		return storeerr.ErrUnauthorized
	}
	return nil
}
