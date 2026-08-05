package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func IntegrationInstall(ctx context.Context, install integrationstore.IntegrationInstallRecord) {
	log.Attach(ctx, log.Fields{
		"org.id":                                   install.OrgID,
		"project.id":                               install.ProjectID,
		"integration_install.id":                   install.ID,
		"integration_install.provider":             install.Provider,
		"integration_install.state":                string(install.State),
		"integration_install.agent_profile_id":     install.AgentProfileID,
		"integration_install.agent_id":             install.AgentID,
		"integration_install.integration_kind":     install.IntegrationKind,
		"integration_install.connection_mode":      install.ConnectionMode,
		"integration_install.provider_tenant_id":   install.ProviderTenantID,
		"integration_install.provider_account_ref": install.ProviderAccountRef,
		"integration_install.installed_by_user_id": install.InstalledByUserID,
	})
}

func IntegrationEvent(
	ctx context.Context,
	install integrationstore.IntegrationInstallRecord,
	classification string,
	eventType string,
) {
	IntegrationInstall(ctx, install)
	log.Attach(ctx, log.Fields{
		"integration_event.classification": classification,
		"integration_event.type":           eventType,
	})
}
