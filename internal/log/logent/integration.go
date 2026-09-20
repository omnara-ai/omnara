package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func App(ctx context.Context, install integrationstore.ProjectAppRecord) {
	log.Attach(ctx, log.Fields{
		"org.id":                   install.OrgID,
		"project.id":               install.ProjectID,
		"app.id":                   install.ID,
		"app.provider":             install.Provider,
		"app.state":                string(install.State),
		"app.provider_tenant_id":   install.ProviderTenantID,
		"app.provider_account_ref": install.ProviderAccountRef,
		"app.installed_by_user_id": install.InstalledByUserID,
	})
}

func IntegrationEvent(
	ctx context.Context,
	install integrationstore.ProjectAppRecord,
	classification string,
	eventType string,
) {
	App(ctx, install)
	log.Attach(ctx, log.Fields{
		"integration_event.classification": classification,
		"integration_event.type":           eventType,
	})
}
