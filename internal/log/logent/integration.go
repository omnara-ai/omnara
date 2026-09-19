package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func IntegrationConnection(ctx context.Context, install integrationstore.IntegrationConnectionRecord) {
	log.Attach(ctx, log.Fields{
		"org.id":                                      install.OrgID,
		"project.id":                                  install.ProjectID,
		"integration_connection.id":                   install.ID,
		"integration_connection.provider":             install.Provider,
		"integration_connection.state":                string(install.State),
		"integration_connection.provider_tenant_id":   install.ProviderTenantID,
		"integration_connection.provider_account_ref": install.ProviderAccountRef,
		"integration_connection.installed_by_user_id": install.InstalledByUserID,
	})
}

func IntegrationEvent(
	ctx context.Context,
	install integrationstore.IntegrationConnectionRecord,
	classification string,
	eventType string,
) {
	IntegrationConnection(ctx, install)
	log.Attach(ctx, log.Fields{
		"integration_event.classification": classification,
		"integration_event.type":           eventType,
	})
}
