//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationtest"
	"github.com/stretchr/testify/require"
)

type pendingLaunchProvider struct {
	*choiceTestProvider
	events      map[string]IntegrationEvent
	enrich      func(context.Context, string) error
	afterDecide func(context.Context, IntegrationLaunchContext)
}

func (p *pendingLaunchProvider) ExpandRouted(ctx context.Context, setup integrationstore.IntegrationRecord,
	payload []byte, route func(IntegrationEvent) (bool, error),
) (IntegrationInboxExpansion, error) {
	var key struct{ Key string }
	if err := json.Unmarshal(payload, &key); err != nil {
		return IntegrationInboxExpansion{}, err
	}
	event := p.events[key.Key]
	if route != nil {
		if proceed, err := route(event); err != nil || !proceed {
			return IntegrationInboxExpansion{}, err
		}
	}
	if p.enrich != nil {
		if err := p.enrich(ctx, key.Key); err != nil {
			return IntegrationInboxExpansion{}, err
		}
	}
	return IntegrationInboxExpansion{Event: &event}, nil
}

func newPendingLaunchJourney(t *testing.T, profiles int) (*choiceJourney, *pendingLaunchProvider) {
	t.Helper()
	f := newChoiceJourney(t, profiles)
	p := &pendingLaunchProvider{choiceTestProvider: f.provider, events: map[string]IntegrationEvent{}}
	providers := map[string]IntegrationInboxProvider{"slack": p}
	router := NewIntegrationRouter(f.store.Execution(), f.store.Integrations())
	launcher := NewChatIntegrationLauncher(f.store.Integrations(), f.store.Execution(), providers)
	workflow := NewIntegrationLaunchWorkflow(router, map[integrationdefinition.Kind]IntegrationLauncher{
		integrationdefinition.SlackThread: func(
			ctx context.Context, input IntegrationLaunchContext,
		) ([]IntegrationLaunchIntent, error) {
			intents, err := launcher.Decide(ctx, input)
			if err == nil && p.afterDecide != nil {
				p.afterDecide(ctx, input)
			}
			return intents, err
		},
	}, providers)
	f.consumer = NewIntegrationInboxConsumer(router, f.store.Integrations(), &integrationConsumerUploads{},
		providers, nil, workflow, WithIntegrationStateHandlers(map[integrationdefinition.Kind]IntegrationStateHandler{
			integrationdefinition.SlackThread: launcher.HandleState,
		}))
	return f, p
}

func (p *pendingLaunchProvider) accept(
	f *choiceJourney, key string, mentioned bool,
) integrationstore.IntegrationInboxRecord {
	f.t.Helper()
	event := f.event
	event.SemanticKey, event.Event.Mentioned = key, mentioned
	p.events[key] = event
	payload, err := json.Marshal(map[string]string{"key": key})
	require.NoError(f.t, err)
	_, _, err = f.store.Integrations().AcceptIntegrationReceipt(f.t.Context(), integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.ids.ProjectID, IntegrationID: f.integration.ID, ReceiptKey: key, Payload: payload,
	})
	require.NoError(f.t, err)
	return f.claim()
}

func TestSingleProfileLaunchHoldsReplyDuringEnrichment(t *testing.T) {
	t.Parallel()
	for _, observer := range []bool{false, true} {
		name := "no observer"
		if observer {
			name = "with observer"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, provider := newPendingLaunchJourney(t, 1)
			if observer {
				agent, err := f.store.Execution().LaunchAgent(t.Context(), executionstore.LaunchAgentInput{
					ProjectID: f.ids.ProjectID, AgentConfigID: f.profiles[0].CurrentConfigID,
					LaunchedBy: identitystore.NewUserPrincipal(f.ids.ProviderAdminUserID),
				})
				require.NoError(t, err)
				createTestIntegrationSubscription(t, f.store, f.integration, agent.Agent.ID, `{"channel_id":"C123"}`)
			}
			mention := provider.accept(f, "mention", true)
			var reply integrationstore.IntegrationInboxRecord
			var replyErr error
			provider.enrich = func(ctx context.Context, key string) error {
				if key == "mention" {
					reply = provider.accept(f, "reply", false)
					_, replyErr = f.consumer.Consume(ctx, reply.Lease())
				}
				return nil
			}
			launched, err := f.consumer.Consume(t.Context(), mention.Lease())
			require.NoError(t, err)
			var launchedAgent uuid.UUID
			for _, result := range launched {
				if result.Launch != nil {
					launchedAgent = result.Launch.Agent.ID
				}
			}
			require.NotEqual(t, uuid.Nil, launchedAgent)
			require.ErrorIs(t, replyErr, integrationstore.ErrIntegrationLaunchReserved)
			retried, err := f.consumer.Consume(t.Context(), reply.Lease())
			require.NoError(t, err)
			recipients := make([]uuid.UUID, 0, len(retried))
			for _, result := range retried {
				recipients = append(recipients, result.Input.AgentInput.AgentID)
			}
			require.Contains(t, recipients, launchedAgent)
			if observer {
				require.Len(t, recipients, 2)
			} else {
				require.Len(t, recipients, 1)
			}
		})
	}
}

func TestPendingLaunchRetainsRepliesAcrossRetryAndReleasesOnFailure(t *testing.T) {
	t.Parallel()
	for _, terminal := range []bool{false, true} {
		name := "recover"
		if terminal {
			name = "terminal"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, provider := newPendingLaunchJourney(t, 1)
			mention := provider.accept(f, "mention", true)
			transient := errors.New("provider temporarily unavailable")
			provider.enrich = func(context.Context, string) error { return transient }
			_, err := f.consumer.Consume(t.Context(), mention.Lease())
			require.ErrorIs(t, err, transient)
			require.NoError(t, f.store.Integrations().WithIntegrationInboxLease(t.Context(), mention.Lease(),
				func(work *integrationstore.IntegrationInboxLeaseTx) error {
					return work.Retry(t.Context(), time.Minute, "transient")
				}))
			provider.enrich = nil
			reply := provider.accept(f, "reply", false)
			_, err = f.consumer.Consume(t.Context(), reply.Lease())
			require.ErrorIs(t, err, integrationstore.ErrIntegrationLaunchReserved)
			_, err = f.pool.Exec(t.Context(),
				`UPDATE integration_inbox SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, mention.ID)
			require.NoError(t, err)
			retriedMention := f.claim()
			require.Equal(t, mention.ID, retriedMention.ID)
			if terminal {
				require.NoError(t, f.store.Integrations().WithIntegrationInboxLease(t.Context(), retriedMention.Lease(),
					func(work *integrationstore.IntegrationInboxLeaseTx) error {
						return work.Fail(t.Context(), "permanent failure")
					}))
				results, err := f.consumer.Consume(t.Context(), reply.Lease())
				require.NoError(t, err)
				require.Empty(t, results)
			} else {
				launched, err := f.consumer.Consume(t.Context(), retriedMention.Lease())
				require.NoError(t, err)
				require.Len(t, launched, 1)
				results, err := f.consumer.Consume(t.Context(), reply.Lease())
				require.NoError(t, err)
				require.Len(t, results, 1)
				require.Equal(t, launched[0].Launch.Agent.ID, results[0].Input.AgentInput.AgentID)
			}
		})
	}
}

func TestProfileMenuDiscardsRepliesBeforeSelection(t *testing.T) {
	t.Parallel()
	f, provider := newPendingLaunchJourney(t, 2)
	mention := provider.accept(f, "mention", true)
	provider.enrich = func(ctx context.Context, key string) error {
		if key == "mention" {
			reply := provider.accept(f, "unselected-reply", false)
			results, err := f.consumer.Consume(ctx, reply.Lease())
			require.NoError(t, err)
			require.Empty(t, results)
		}
		return nil
	}
	results, err := f.consumer.Consume(t.Context(), mention.Lease())
	require.NoError(t, err)
	require.Empty(t, results)
	require.Len(t, provider.menus, 1)
	f.choose(provider.menus[0], "heavy")
	results, err = f.consumer.Consume(t.Context(), f.claim().Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, f.profiles[1].ID, results[0].Launch.Agent.AgentProfileID)
}

func TestAcceptedProfileChoiceWinsBeforeEarlierMentionFreezes(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"enrichment", "decided"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			f, provider := newPendingLaunchJourney(t, 2)
			menu := provider.accept(f, "menu", true)
			_, err := f.consumer.Consume(t.Context(), menu.Lease())
			require.NoError(t, err)
			require.Len(t, provider.menus, 1)
			settings := integrationstore.SaveIntegrationInput{
				OrgID: f.ids.OrgID, ProjectID: f.ids.ProjectID, Name: f.integration.Name,
				IntegrationKind: f.integration.IntegrationKind,
				Settings:        integrationtest.ChatSettings("C123", f.profiles[0].ID),
			}
			_, err = f.store.Integrations().UpdateIntegration(t.Context(), f.integration.ID, settings)
			require.NoError(t, err)
			mention := provider.accept(f, "single-profile-mention", true)
			choose := func(ctx context.Context) {
				settings.Settings = integrationtest.ChatSettings("C123", f.profiles[0].ID, f.profiles[1].ID)
				_, err := f.store.Integrations().UpdateIntegration(ctx, f.integration.ID, settings)
				require.NoError(t, err)
				f.choose(provider.menus[0], "heavy")
			}
			if phase == "enrichment" {
				provider.enrich = func(ctx context.Context, key string) error {
					if key == "single-profile-mention" {
						choose(ctx)
					}
					return nil
				}
			} else {
				provider.afterDecide = func(ctx context.Context, input IntegrationLaunchContext) {
					if input.Receipt.ID == mention.ID {
						choose(ctx)
					}
				}
			}
			_, err = f.consumer.Consume(t.Context(), mention.Lease())
			require.ErrorIs(t, err, integrationstore.ErrIntegrationLaunchReserved)
			selected := f.claim()
			results, err := f.consumer.Consume(t.Context(), selected.Lease())
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.NotNil(t, results[0].Launch)
			require.Equal(t, f.profiles[1].ID, results[0].Launch.Agent.AgentProfileID)
			provider.enrich, provider.afterDecide = nil, nil
			followup, err := f.consumer.Consume(t.Context(), mention.Lease())
			require.NoError(t, err)
			require.Len(t, followup, 1)
			require.Equal(t, results[0].Launch.Agent.ID, followup[0].Input.AgentInput.AgentID)
		})
	}
}
