package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func Org(ctx context.Context, r identitystore.OrgRecord) {
	log.Attach(ctx, log.Fields{
		"org.id":   r.ID,
		"org.name": r.Name,
	})
}
func Project(ctx context.Context, r identitystore.ProjectRecord) {
	log.Attach(ctx, log.Fields{
		"org.id":       r.OrgID,
		"project.id":   r.ID,
		"project.name": r.Name,
	})
}

func OrgInvitationEmailFailed(ctx context.Context, err error) {
	fields := log.Fields{"org_invitation.email.result": "failed"}
	if err != nil {
		fields["org_invitation.email.error"] = err.Error()
	}
	log.Attach(ctx, fields)
	log.Level(ctx, log.WarnLevel)
}
