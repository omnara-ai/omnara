package integration

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (r *IntegrationRouter) freezeEmptyIfUnrouted(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	integrationSetup integrationstore.IntegrationRecord,
	event IntegrationEvent,
) (bool, error) {
	request, err := prepareIntegrationEvent(event, integrationSetup)
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
			candidates, err := r.candidatesForEvent(ctx, work, request)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(
				candidates.Subscriptions,
				func(subscription integrationstore.IntegrationSubscriptionRecord) bool {
					return request.matchesSubscriptionAddress(subscription.Address)
				},
			) {
				return nil
			}
			if integration := candidates.Launcher; integration != nil &&
				integration.State == integrationstore.IntegrationStateActive {
				definition, _ := integrationdefinition.Lookup(integration.IntegrationKind)
				if definition.MatchesLaunch(integration.Settings, event.Event) {
					return nil
				}
			}
			if err := work.CheckNoUnsettledIntegrationSelection(ctx, request.address); err != nil {
				return err
			}
			if err := work.FreezePlan(ctx, json.RawMessage(`{"recipients":{}}`)); err != nil {
				return err
			}
			frozen = true
			return nil
		},
	)
	return frozen, err
}
