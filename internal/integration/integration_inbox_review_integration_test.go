//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestIntegrationInboxObserverFollowupWaitsForLaunch(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name         string
		profiles     int
		freezeChoice bool
		terminal     bool
	}{
		{name: "direct launch", profiles: 1},
		{name: "accepted choice", profiles: 2},
		{name: "frozen choice", profiles: 2, freezeChoice: true},
		{name: "failed direct launch", profiles: 1, terminal: true},
		{name: "failed accepted choice", profiles: 2, terminal: true},
		{name: "failed frozen choice", profiles: 2, freezeChoice: true, terminal: true},
	} {
		for _, mentioned := range []bool{false, true} {
			if scenario.terminal && mentioned {
				continue // A fresh mention after failure may legitimately request a replacement launch.
			}
			t.Run(fmt.Sprintf("%s/mentioned=%t", scenario.name, mentioned), func(t *testing.T) {
				t.Parallel()
				ctx := t.Context()
				f := newChoiceJourney(t, scenario.profiles)
				inbox := f.store.Integrations()
				observer := launcherObserver(t, f)
				removeTestAgentSubscriptions(t, f.store, f.integration, observer.ID)
				createTestIntegrationSubscription(t, f.store, f.integration, observer.ID, `{"channel_id":"C123"}`)
				accept := func(key string) integrationstore.IntegrationInboxRecord {
					t.Helper()
					_, _, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
						ProjectID: f.ids.ProjectID, IntegrationID: f.integration.ID,
						ReceiptKey: key, Payload: []byte(`{}`),
					})
					require.NoError(t, err)
					return f.claim()
				}
				var owner integrationstore.IntegrationInboxRecord
				if scenario.profiles == 1 {
					owner = accept("mention")
					event, _, err := f.consumer.launchers.Decide(ctx, owner.Lease(), owner, f.integration, f.event)
					require.NoError(t, err)
					plan, _, err := f.consumer.router.Freeze(ctx, owner.Lease(), event, nil)
					require.NoError(t, err)
					require.Len(t, plan.Recipients, 2)
				} else {
					results := f.receive("mention", f.event)
					require.Len(t, results, 1)
					require.Equal(t, observer.ID, results[0].Input.AgentInput.AgentID)
					f.choose(f.provider.menus[0], "heavy")
					owner = f.claim()
					if scenario.freezeChoice {
						choice, err := inbox.GetIntegrationProfileChoice(ctx, owner.ProjectID, owner.IntegrationID, owner.SourceStateID)
						require.NoError(t, err)
						event, err := selectedIntegrationEvent(choice)
						require.NoError(t, err)
						_, _, err = f.consumer.router.Freeze(ctx, owner.Lease(), event, nil)
						require.NoError(t, err)
					}
				}
				reply := accept("early-reply")
				event := f.event
				event.SemanticKey, event.Event.Mentioned = "early-reply", mentioned
				f.provider.event = &event
				_, err := f.consumer.Consume(ctx, reply.Lease())
				require.ErrorIs(t, err, integrationstore.ErrIntegrationLaunchReserved)
				unplanned, err := inbox.GetIntegrationInbox(ctx, f.ids.ProjectID, reply.ID)
				require.NoError(t, err)
				require.Empty(t, unplanned.Plan, "an observer must not freeze out the pending launch recipient")
				var launched uuid.UUID
				if scenario.terminal {
					require.NoError(t, inbox.WithIntegrationInboxLease(ctx, owner.Lease(),
						func(work *integrationstore.IntegrationInboxLeaseTx) error {
							return work.Fail(ctx, "launch failed permanently")
						}))
				} else {
					results, err := f.consumer.Consume(ctx, owner.Lease())
					require.NoError(t, err)
					for _, result := range results {
						if result.Launch != nil {
							launched = result.Launch.Agent.ID
						}
					}
					require.NotEqual(t, uuid.Nil, launched)
				}
				f.restart()
				results, err := f.consumer.Consume(ctx, reply.Lease())
				require.NoError(t, err)
				want := 2
				if scenario.terminal {
					want = 1
				}
				require.Len(t, results, want)
				inputs := map[uuid.UUID]uuid.UUID{}
				for _, result := range results {
					require.NotNil(t, result.Input)
					require.True(t, result.Input.Created)
					inputs[result.Input.AgentInput.AgentID] = result.Input.AgentInput.ID
				}
				require.Contains(t, inputs, observer.ID)
				if !scenario.terminal {
					require.Contains(t, inputs, launched)
				}
				replayed, err := f.consumer.Consume(ctx, reply.Lease())
				require.NoError(t, err)
				require.Len(t, replayed, want)
				for _, result := range replayed {
					require.False(t, result.Input.Created)
					require.Equal(t, inputs[result.Input.AgentInput.AgentID], result.Input.AgentInput.ID)
				}
			})
		}
	}
}

func TestIntegrationInboxCommittedLaunchDoesNotWaitForRevokedObserver(t *testing.T) {
	t.Parallel()
	for _, mentioned := range []bool{false, true} {
		t.Run(fmt.Sprintf("mentioned=%t", mentioned), func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newChoiceJourney(t, 1)
			observer := launcherObserver(t, f)
			inbox := f.store.Integrations()
			_, _, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: f.ids.ProjectID, IntegrationID: f.integration.ID, ReceiptKey: "parent", Payload: []byte(`{}`),
			})
			require.NoError(t, err)
			parent := f.claim()
			plan, err := freezeTestIntegrationEvent(ctx, f.consumer.router, parent.Lease(), &f.event)
			require.NoError(t, err)
			require.Len(t, plan.Recipients, 2)
			var launched uuid.UUID
			var launchKey, observerKey string
			for key, recipient := range plan.Recipients {
				if recipient.Launch != nil {
					launched, launchKey = recipient.AgentID, key
				} else {
					require.Equal(t, observer.ID, recipient.AgentID)
					observerKey = key
				}
			}
			require.NotEqual(t, uuid.Nil, launched)
			removeTestAgentSubscriptions(t, f.store, f.integration, observer.ID)
			worker := NewIntegrationInboxWorker(inbox, f.consumer, IntegrationInboxWorkerOptions{})
			require.ErrorIs(t, worker.consume(ctx, parent), storeerr.ErrUnauthorized)
			_, err = f.pool.Exec(ctx, `UPDATE integration_inbox SET next_attempt_at=now()+interval '1 hour' WHERE id=$1`,
				parent.ID)
			require.NoError(t, err)
			pending, err := inbox.GetIntegrationInbox(ctx, f.ids.ProjectID, parent.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxQueued, pending.State)
			outcomes, err := f.store.Execution().GetIntegrationInboxOutcomes(ctx, pending)
			require.NoError(t, err)
			require.Equal(t, executionstore.InboxRecipientDelivered, outcomes[launchKey])
			require.Equal(t, executionstore.InboxRecipientPending, outcomes[observerKey])
			readInputs := func(semanticKey string) map[uuid.UUID]uuid.UUID {
				t.Helper()
				rows, err := f.pool.Query(ctx,
					`SELECT agent_id,id FROM agent_inputs WHERE project_id=$1 AND input_idempotency_key=$2`,
					f.ids.ProjectID, semanticKey)
				require.NoError(t, err)
				defer rows.Close()
				inputs := map[uuid.UUID]uuid.UUID{}
				for rows.Next() {
					var agentID, inputID uuid.UUID
					require.NoError(t, rows.Scan(&agentID, &inputID))
					require.NotContains(t, inputs, agentID)
					inputs[agentID] = inputID
				}
				require.NoError(t, rows.Err())
				return inputs
			}
			initial := readInputs(f.event.SemanticKey)
			require.Len(t, initial, 1)
			require.Contains(t, initial, launched)
			replyEvent := f.event
			replyEvent.SemanticKey, replyEvent.Event.Mentioned = "follow-up", mentioned
			f.provider.event = &replyEvent
			reply, _, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: f.ids.ProjectID, IntegrationID: f.integration.ID, ReceiptKey: "reply", Payload: []byte(`{}`),
			})
			require.NoError(t, err)
			claimedReply := f.claim()
			require.Equal(t, reply.ID, claimedReply.ID)
			err = worker.consume(ctx, claimedReply)
			require.NoError(t, err, "a committed owner must receive replies while another parent recipient retries")
			completedReply, err := inbox.GetIntegrationInbox(ctx, f.ids.ProjectID, reply.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxCompleted, completedReply.State)
			require.Equal(t, 1, completedReply.AttemptCount)
			replies := readInputs(replyEvent.SemanticKey)
			require.Len(t, replies, 1)
			require.Contains(t, replies, launched)
			unchanged, err := inbox.GetIntegrationInbox(ctx, f.ids.ProjectID, parent.ID)
			require.NoError(t, err)
			require.Equal(t, pending, unchanged, "follow-up admission must not settle or rewrite the parent")

			createTestIntegrationSubscription(t, f.store, f.integration, observer.ID,
				`{"channel_id":"C123","thread_ts":"1.2"}`)
			reattachedReply := replyEvent
			reattachedReply.SemanticKey = "follow-up-reattached"
			require.Len(t, f.receive("reply-reattached", reattachedReply), 2)
			both := readInputs(reattachedReply.SemanticKey)
			require.Len(t, both, 2)
			require.Contains(t, both, launched)
			require.Contains(t, both, observer.ID)
			unchanged, err = inbox.GetIntegrationInbox(ctx, f.ids.ProjectID, parent.ID)
			require.NoError(t, err)
			require.Equal(t, pending, unchanged)
			_, err = f.pool.Exec(ctx, `UPDATE integration_inbox SET next_attempt_at=now() WHERE id=$1`, parent.ID)
			require.NoError(t, err)
			f.restart()
			worker = NewIntegrationInboxWorker(inbox, f.consumer, IntegrationInboxWorkerOptions{})
			worked, err := worker.RunOnce(ctx)
			require.True(t, worked)
			require.NoError(t, err)
			completedParent, err := inbox.GetIntegrationInbox(ctx, f.ids.ProjectID, parent.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxCompleted, completedParent.State)
			require.Equal(t, 2, completedParent.AttemptCount)
			require.JSONEq(t, string(pending.Plan), string(completedParent.Plan))
			after := readInputs(f.event.SemanticKey)
			require.Len(t, after, 2)
			require.Equal(t, initial[launched], after[launched], "retry must replay the committed launch")
			require.Contains(t, after, observer.ID, "reattachment restores delivery of the frozen pending input")
			replayed, err := f.consumer.Consume(ctx, claimedReply.Lease())
			require.NoError(t, err)
			require.Len(t, replayed, 1)
			require.False(t, replayed[0].Input.Created)
			require.Equal(t, replies[launched], replayed[0].Input.AgentInput.ID)
			require.Equal(t, replies, readInputs(replyEvent.SemanticKey))
		})
	}
}

func TestIntegrationInboxObserverWaiterRetainsBoundedRetryPolicy(t *testing.T) {
	ctx := t.Context()
	f := newChoiceJourney(t, 1)
	launcherObserver(t, f)
	inbox := f.store.Integrations()
	_, _, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.ids.ProjectID, IntegrationID: f.integration.ID, ReceiptKey: "parent", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	owner := f.claim()
	plan, err := freezeTestIntegrationEvent(ctx, f.consumer.router, owner.Lease(), &f.event)
	require.NoError(t, err)
	require.Len(t, plan.Recipients, 2)
	require.NoError(t, inbox.WithIntegrationInboxLease(ctx, owner.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.Retry(ctx, time.Hour, "provider Retry-After")
		}))
	f.event.Event.Mentioned, f.event.SemanticKey = false, "early-reply"
	f.provider.event = &f.event
	input := integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.ids.ProjectID, IntegrationID: f.integration.ID, ReceiptKey: "waiter", Payload: []byte(`{}`),
	}
	waiter, _, err := inbox.AcceptIntegrationReceipt(ctx, input)
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `UPDATE integration_inbox SET attempt_count=$2 WHERE id=$1`,
		waiter.ID, integrationstore.IntegrationInboxMaxAttempts-1)
	require.NoError(t, err)
	worker := NewIntegrationInboxWorker(inbox, f.consumer, IntegrationInboxWorkerOptions{})
	worked, err := worker.RunOnce(ctx)
	require.True(t, worked)
	require.ErrorIs(t, err, integrationstore.ErrIntegrationLaunchReserved)
	failed, err := inbox.GetIntegrationInbox(ctx, f.ids.ProjectID, waiter.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxFailed, failed.State)
	require.Equal(t, integrationstore.IntegrationInboxMaxAttempts, failed.AttemptCount)
	require.NotNil(t, failed.CompletedAt)
	require.Empty(t, failed.Plan)
	require.Contains(t, failed.LastError, integrationstore.ErrIntegrationLaunchReserved.Error())
	_, err = f.pool.Exec(ctx, `UPDATE integration_inbox SET next_attempt_at=now() WHERE id=$1`, owner.ID)
	require.NoError(t, err)
	worked, err = worker.RunOnce(ctx)
	require.True(t, worked)
	require.NoError(t, err)
	completed, err := inbox.GetIntegrationInbox(ctx, f.ids.ProjectID, owner.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxCompleted, completed.State)
	outcomes, err := f.store.Execution().GetIntegrationInboxOutcomes(ctx, completed)
	require.NoError(t, err)
	require.Len(t, outcomes, 2)
	var replies int
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_idempotency_key=$2`,
		f.ids.ProjectID, "early-reply").Scan(&replies))
	require.Zero(t, replies, "an exhausted waiter stays terminal after the parent later succeeds")
	duplicate, created, err := inbox.AcceptIntegrationReceipt(ctx, input)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, failed, duplicate)
	worked, err = worker.RunOnce(ctx)
	require.NoError(t, err)
	require.False(t, worked, "duplicate intake cannot revive an exhausted waiter")
}

func TestIntegrationInboxWorkerInvalidFrozenPlanIsTerminal(t *testing.T) {
	for _, normalized := range []bool{false, true} {
		t.Run(fmt.Sprintf("jsonb_expansion=%t", normalized), func(t *testing.T) {
			ctx := t.Context()
			f := newChoiceJourney(t, 1)
			launcherObserver(t, f)
			f.event.Event.Mentioned = false
			metadataSize := 1
			if normalized {
				// JSONB adds one space per element, taking a valid raw plan over the durable bound.
				metadataSize += 8192
			}
			metadata := make([]string, metadataSize)
			empty, err := json.Marshal(metadata)
			require.NoError(t, err)
			padding := integrationstore.IntegrationInboxMaxPlanBytes
			if normalized {
				padding -= len(empty) + 4096
			}
			metadata[len(metadata)-1] = strings.Repeat("x", padding)
			f.event.Metadata, err = json.Marshal(metadata)
			require.NoError(t, err)
			f.provider.event = &f.event
			inbox := f.store.Integrations()
			input := integrationstore.VerifiedIntegrationReceipt{
				ProjectID: f.ids.ProjectID, IntegrationID: f.integration.ID,
				ReceiptKey: "invalid-plan", Payload: []byte(`{}`),
			}
			receipt, _, err := inbox.AcceptIntegrationReceipt(ctx, input)
			require.NoError(t, err)
			worker := NewIntegrationInboxWorker(inbox, f.consumer, IntegrationInboxWorkerOptions{})
			worked, err := worker.RunOnce(ctx)
			require.True(t, worked)
			require.ErrorIs(t, err, ErrIntegrationInboundPermanent)
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			if normalized {
				require.ErrorContains(t, err, "normalized inbox JSON exceeds durable bounds")
			} else {
				require.ErrorContains(t, err, "inbox JSON exceeds bounds")
			}
			failed, err := inbox.GetIntegrationInbox(ctx, f.ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxFailed, failed.State)
			require.Equal(t, 1, failed.AttemptCount)
			require.NotNil(t, failed.CompletedAt)
			require.Empty(t, failed.Plan)
			require.Equal(t, []string{"Request failed"}, f.provider.notices)
			var count int
			require.NoError(t, f.pool.QueryRow(ctx,
				`SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_kind='content'`, f.ids.ProjectID).Scan(&count))
			require.Zero(t, count)
			duplicate, created, err := inbox.AcceptIntegrationReceipt(ctx, input)
			require.NoError(t, err)
			require.False(t, created)
			require.Equal(t, receipt.ID, duplicate.ID)
			worked, err = worker.RunOnce(ctx)
			require.NoError(t, err)
			require.False(t, worked)
			require.Equal(t, 1, f.provider.expansions)
			require.Len(t, f.provider.notices, 1)
		})
	}
}
