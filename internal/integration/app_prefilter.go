package integration

import (
	"context"
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

// freezeEmptyIfUnrouted lets a pure normalized message avoid file downloads when
// nobody can receive it. Empty completion is decided under the same conversation
// gate/reservation check as full planning; possible recipients use normal Freeze.
func (r *AppRouter) freezeEmptyIfUnrouted(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	appSetup integrationstore.ProjectAppRecord,
	event AppEvent,
) (bool, error) {
	requests, err := prepareAppEvents([]AppEvent{event}, appSetup)
	if err != nil {
		return false, err
	}
	frozen := false
	err = r.integrations.WithIntegrationInboxLease(
		ctx,
		lease,
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			if len(work.Receipt().Plan) != 0 {
				return nil
			}
			request := requests[0]
			candidates, err := r.integrations.AppRoutingCandidatesForInbox(
				ctx,
				work,
				request.address,
				request.scopes,
				event.Event.Kind,
			)
			if err != nil {
				return err
			}
			if len(candidates.Listeners) > 0 {
				return nil
			}
			if app := candidates.Launcher; app != nil && app.State == integrationstore.ProjectAppStateActive &&
				app.Settings.Launcher != nil && event.Event.MatchesLauncher(app.Settings.Launcher.Trigger) {
				return nil
			}
			if err := work.CheckNoUnsettledAppSelection(ctx, request.address); err != nil {
				return err
			}
			if err := work.FreezePlan(ctx, json.RawMessage(`{}`)); err != nil {
				return err
			}
			frozen = true
			return nil
		},
	)
	return frozen, err
}
