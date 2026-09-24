package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func Integration(ctx context.Context, install integrationstore.ProjectIntegrationRecord) {
	log.Attach(ctx, log.Fields{
		"org.id":                           install.OrgID,
		"project.id":                       install.ProjectID,
		"integration.id":                   install.ID,
		"integration.provider":             install.Provider,
		"integration.state":                string(install.State),
		"integration.provider_tenant_id":   install.ProviderTenantID,
		"integration.provider_account_ref": install.ProviderAccountRef,
		"integration.installed_by_user_id": install.InstalledByUserID,
	})
}

func IntegrationEvent(
	ctx context.Context,
	install integrationstore.ProjectIntegrationRecord,
	classification string,
	eventType string,
) {
	Integration(ctx, install)
	log.Attach(ctx, log.Fields{
		"integration_event.classification": classification,
		"integration_event.type":           eventType,
	})
}
