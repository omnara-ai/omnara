package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
)

func App(ctx context.Context, install appstore.ProjectAppRecord) {
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

func AppEvent(
	ctx context.Context,
	install appstore.ProjectAppRecord,
	classification string,
	eventType string,
) {
	App(ctx, install)
	log.Attach(ctx, log.Fields{
		"app_event.classification": classification,
		"app_event.type":           eventType,
	})
}
