package executionstore

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
)

// CronTriggerActor attributes both direct cron inputs and scheduled app tasks to
// the same Omnara identity, regardless of their delivery path.
func CronTriggerActor(orgID, triggerID uuid.UUID, name string) (*ActorParams, error) {
	tenantID, err := publicid.Encode(publicid.KindOrganization, orgID)
	if err != nil {
		return nil, fmt.Errorf("encode cron trigger actor tenant: %w", err)
	}
	providerUserID, err := publicid.Encode(publicid.KindCronTrigger, triggerID)
	if err != nil {
		return nil, fmt.Errorf("encode cron trigger actor: %w", err)
	}
	return &ActorParams{
		Provider:         ActorProviderOmnara,
		ProviderTenantID: tenantID,
		ProviderUserID:   providerUserID,
		DisplayName:      &name,
	}, nil
}
