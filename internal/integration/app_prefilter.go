package integration

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

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
			if slices.ContainsFunc(candidates.Subscriptions, func(subscription integrationstore.AppSubscriptionRecord) bool {
				return request.matchesSubscriptionAddress(subscription.Address)
			}) {
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
