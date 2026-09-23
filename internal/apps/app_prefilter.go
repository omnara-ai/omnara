package apps

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
)

func (r *AppRouter) freezeEmptyIfUnrouted(
	ctx context.Context,
	lease appstore.AppInboxLease,
	appSetup appstore.ProjectAppRecord,
	event AppEvent,
) (bool, error) {
	requests, err := prepareAppEvents([]AppEvent{event}, appSetup)
	if err != nil {
		return false, err
	}
	frozen := false
	err = r.apps.WithAppInboxLease(
		ctx,
		lease,
		func(work *appstore.AppInboxLeaseTx) error {
			if len(work.Receipt().Plan) != 0 {
				return nil
			}
			request := requests[0]
			candidates, err := r.apps.AppRoutingCandidatesForInbox(
				ctx,
				work,
				request.address,
				request.scopes,
				event.Event.Kind,
			)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(candidates.Subscriptions, func(subscription appstore.AppSubscriptionRecord) bool {
				return request.matchesSubscriptionAddress(subscription.Address)
			}) {
				return nil
			}
			if app := candidates.Launcher; app != nil && app.State == appstore.ProjectAppStateActive &&
				app.Settings.Launcher != nil {
				definition, _ := appdefinition.Lookup(app.AppType)
				if definition.MatchesLauncher(event.Event, app.Settings.Launcher.Trigger) {
					return nil
				}
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
