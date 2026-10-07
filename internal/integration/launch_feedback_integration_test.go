//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type committedLaunchFeedbackProvider struct {
	*choiceTestProvider
	check func(context.Context, IntegrationLaunchContext)
	err   error
}

func (p *committedLaunchFeedbackProvider) NotifyLaunchUnavailable(
	ctx context.Context, input IntegrationLaunchContext, message string,
) error {
	p.check(ctx, input)
	_ = p.choiceTestProvider.NotifyLaunchUnavailable(ctx, input, message)
	return p.err
}

func acceptLaunchFeedbackReceipt(t *testing.T, f *choiceJourney, key string) integrationstore.IntegrationInboxRecord {
	t.Helper()
	_, _, err := f.store.Integrations().AcceptIntegrationReceipt(t.Context(), integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.ids.ProjectID, IntegrationID: f.integration.ID, ReceiptKey: key, Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	return f.claim()
}

func TestLaunchFeedbackWaitsForCommittedFreezeAndPreservesObserver(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 1)
	ctx := t.Context()
	observer := launcherObserver(t, f)
	owner := acceptLaunchFeedbackReceipt(t, f, "reserved-launch")
	_, err := freezeTestIntegrationEvent(ctx, f.consumer.router, owner.Lease(), &f.event)
	require.NoError(t, err)
	require.NoError(t, f.store.Execution().DeleteAgentProfile(ctx, f.ids.ProjectID, f.profiles[0].ID))
	receipt := acceptLaunchFeedbackReceipt(t, f, "unavailable-profile")
	f.event.SemanticKey = "unavailable-profile"
	f.provider.event = &f.event
	provider := &committedLaunchFeedbackProvider{
		choiceTestProvider: f.provider,
		err:                errors.New("feedback unavailable"),
		check: func(noticeCtx context.Context, input IntegrationLaunchContext) {
			deadline, ok := noticeCtx.Deadline()
			require.True(t, ok)
			require.LessOrEqual(t, time.Until(deadline), 3*time.Second)
			saved, err := f.store.Integrations().GetIntegrationInbox(noticeCtx, f.ids.ProjectID, input.Receipt.ID)
			require.NoError(t, err)
			plan, err := decodeIntegrationInboxPlan(saved.Plan)
			require.NoError(t, err, "feedback must observe a committed plan from another DB connection")
			require.Equal(t, f.event.SemanticKey, plan.Message.SemanticKey)
			require.Len(t, plan.Recipients, 1)
		},
	}
	f.consumer.launchers.providers["slack"] = provider
	for range 2 {
		_, err := f.consumer.Consume(ctx, receipt.Lease())
		require.ErrorIs(t, err, integrationstore.ErrIntegrationLaunchReserved)
		require.Empty(t, f.provider.notices, "reservation waits must not publish an unavailable decision")
	}
	require.NoError(t, f.store.Integrations().WithIntegrationInboxLease(ctx, owner.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.Fail(ctx, "release test reservation")
		}))
	results, err := f.consumer.Consume(ctx, receipt.Lease())
	require.NoError(t, err, "feedback failure must not block observer delivery")
	require.Len(t, results, 1)
	require.Equal(t, observer.ID, results[0].Input.AgentInput.AgentID)
	require.True(t, results[0].Input.Created)
	require.Equal(t, []string{launchUnavailableMessage}, f.provider.notices)
	f.restart()
	results, err = f.consumer.Consume(ctx, receipt.Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].Input.Created)
	require.Equal(t, []string{launchUnavailableMessage}, f.provider.notices, "replay must not resend feedback")
}

type launchFeedbackFreezeFailure struct {
	IntegrationRoutingStore
	rollback bool
	err      error
}

func (s *launchFeedbackFreezeFailure) WithIntegrationInboxLease(
	ctx context.Context, lease integrationstore.IntegrationInboxLease,
	apply func(*integrationstore.IntegrationInboxLeaseTx) error,
) error {
	created := false
	err := s.IntegrationRoutingStore.WithIntegrationInboxLease(ctx, lease,
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			unplanned := len(work.Receipt().Plan) == 0
			if err := apply(work); err != nil {
				return err
			}
			created = unplanned && len(work.Receipt().Plan) != 0
			if created && s.rollback {
				return s.err
			}
			return nil
		})
	if err == nil && created {
		return s.err
	}
	return err
}

func TestLaunchFeedbackDoesNotSendOnFailedFreeze(t *testing.T) {
	t.Parallel()
	for _, rollback := range []bool{true, false} {
		name := "lost commit response"
		if rollback {
			name = "rolled back"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newChoiceJourney(t, 1)
			ctx := t.Context()
			observer := launcherObserver(t, f)
			require.NoError(t, f.store.Execution().DeleteAgentProfile(ctx, f.ids.ProjectID, f.profiles[0].ID))
			receipt := acceptLaunchFeedbackReceipt(t, f, "unavailable-profile")
			f.provider.event = &f.event
			failure := &launchFeedbackFreezeFailure{
				IntegrationRoutingStore: f.store.Integrations(), rollback: rollback, err: errors.New("freeze failed"),
			}
			f.consumer.router.integrations = failure
			_, err := f.consumer.Consume(ctx, receipt.Lease())
			require.ErrorIs(t, err, failure.err)
			require.Empty(t, f.provider.notices)
			saved, err := f.store.Integrations().GetIntegrationInbox(ctx, f.ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, rollback, len(saved.Plan) == 0)
			f.restart()
			results, err := f.consumer.Consume(ctx, receipt.Lease())
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.Equal(t, observer.ID, results[0].Input.AgentInput.AgentID)
			if rollback {
				require.Equal(t, []string{launchUnavailableMessage}, f.provider.notices)
			} else {
				require.Empty(t, f.provider.notices, "an uncertain commit may lose feedback but must not resend on recovery")
			}
		})
	}
}

func TestLaunchFeedbackRejectsStaleDecision(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 1)
	ctx := t.Context()
	observer := launcherObserver(t, f)
	require.NoError(t, f.store.Execution().DeleteAgentProfile(ctx, f.ids.ProjectID, f.profiles[0].ID))
	receipt := acceptLaunchFeedbackReceipt(t, f, "stale-decision")
	f.provider.event = &f.event
	launcher := f.consumer.launchers.launchers[integrationdefinition.SlackThread]
	f.consumer.launchers.launchers[integrationdefinition.SlackThread] = func(
		ctx context.Context, input IntegrationLaunchContext,
	) ([]IntegrationLaunchIntent, error) {
		intents, err := launcher(ctx, input)
		_, updateErr := f.store.Integrations().UpdateIntegration(ctx, f.integration.ID, integrationstore.SaveIntegrationInput{
			OrgID: f.ids.OrgID, ProjectID: f.ids.ProjectID, Name: f.integration.Name,
			IntegrationKind: f.integration.IntegrationKind, Settings: integrationstore.IntegrationSettings(`{}`),
		})
		require.NoError(t, updateErr)
		return intents, err
	}
	_, err := f.consumer.Consume(ctx, receipt.Lease())
	require.ErrorIs(t, err, ErrIntegrationRoutingChanged)
	require.Empty(t, f.provider.notices)
	saved, err := f.store.Integrations().GetIntegrationInbox(ctx, f.ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Empty(t, saved.Plan)
	f.restart()
	results, err := f.consumer.Consume(ctx, receipt.Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, observer.ID, results[0].Input.AgentInput.AgentID)
	require.Empty(t, f.provider.notices, "the disabled launcher must not inherit stale feedback")
}

func TestLaunchFeedbackDoesNotSendForCompetingFreeze(t *testing.T) {
	t.Parallel()
	f := newChoiceJourney(t, 1)
	ctx := t.Context()
	observer := launcherObserver(t, f)
	require.NoError(t, f.store.Execution().DeleteAgentProfile(ctx, f.ids.ProjectID, f.profiles[0].ID))
	receipt := acceptLaunchFeedbackReceipt(t, f, "competing-freeze")
	f.provider.event = &f.event
	launcher := f.consumer.launchers.launchers[integrationdefinition.SlackThread]
	f.consumer.launchers.launchers[integrationdefinition.SlackThread] = func(
		ctx context.Context, input IntegrationLaunchContext,
	) ([]IntegrationLaunchIntent, error) {
		intents, err := launcher(ctx, input)
		other := f.event
		other.SemanticKey = "already-frozen-decision"
		_, _, freezeErr := f.consumer.router.Freeze(ctx, receipt.Lease(), &other, nil)
		require.NoError(t, freezeErr)
		return intents, err
	}
	results, err := f.consumer.Consume(ctx, receipt.Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, observer.ID, results[0].Input.AgentInput.AgentID)
	require.Empty(t, f.provider.notices)
	saved, err := f.store.Integrations().GetIntegrationInbox(ctx, f.ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	plan, err := decodeIntegrationInboxPlan(saved.Plan)
	require.NoError(t, err)
	require.Equal(t, "already-frozen-decision", plan.Message.SemanticKey)
}

func TestIntegrationFailureDetachedSubscriptionDoesNotSuggestResending(t *testing.T) {
	t.Parallel()
	for _, partial := range []bool{false, true} {
		name := "all detached"
		if partial {
			name = "partially delivered"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newChoiceJourney(t, 1)
			ctx := t.Context()
			observer := launcherObserver(t, f)
			if partial {
				launcherObserver(t, f)
			}
			f.event.Event.Mentioned = false
			receipt := acceptLaunchFeedbackReceipt(t, f, "subscription-delivery")
			_, _, err := f.consumer.router.Freeze(ctx, receipt.Lease(), &f.event, nil)
			require.NoError(t, err)
			removeTestAgentSubscriptions(t, f.store, f.integration, observer.ID)
			_, cause := f.consumer.Consume(ctx, receipt.Lease())
			require.ErrorIs(t, cause, storeerr.ErrUnauthorized)
			require.NoError(t, f.store.Integrations().WithIntegrationInboxLease(ctx, receipt.Lease(),
				func(work *integrationstore.IntegrationInboxLeaseTx) error {
					return work.Fail(ctx, "retry budget exhausted")
				}))
			provider := &failedInboxProvider{}
			f.consumer.providers["slack"] = provider
			require.NoError(t, f.consumer.FinalizeFailure(ctx, f.ids.ProjectID, receipt.ID, cause))
			require.Equal(t, 1, provider.notices)
			if partial {
				require.Equal(t,
					"I couldn't deliver this request to every agent. Some agents have already received it.", provider.message)
			} else {
				require.Equal(t, inboxDeliveryUnavailableMessage, provider.message)
			}
		})
	}
}
