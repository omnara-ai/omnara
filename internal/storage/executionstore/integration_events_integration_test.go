//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestIntegrationEventReceiptClaimLeavesExcessWorkUnclaimed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, _, install := createChannelLifecycleFixture(t, ctx, store, "receipt-bounded-claim")
	for i := range 4 {
		_, err := store.Integrations().ReceiveIntegrationEvent(ctx, integrationstore.ReceiveIntegrationEventInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			EventID: fmt.Sprintf("event-%d", i), Payload: json.RawMessage(`{"message":"hello"}`),
			Capabilities: testChannelCapabilities(testChannelProvider),
		})
		require.NoError(t, err)
	}
	claim := integrationstore.ClaimNextIntegrationEventInput{
		Capability: testChannelCapability(testChannelProvider), LeaseDuration: time.Minute,
	}
	first, found, err := store.Integrations().ClaimNextIntegrationEvent(ctx, claim)
	require.NoError(t, err)
	require.True(t, found)
	var pending int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_event_receipts
WHERE state = 'pending' AND attempt_count = 0 AND lease_token IS NULL`).Scan(&pending))
	require.Equal(t, 3, pending, "one consumer reserves only the payload it can process")
	second, found, err := store.Integrations().ClaimNextIntegrationEvent(ctx, claim)
	require.NoError(t, err)
	require.True(t, found)
	require.NotEqual(t, first.ID, second.ID, "other consumers can immediately use the remaining capacity")
}

func TestIntegrationEventReceiptConcurrentReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, _, install := createChannelLifecycleFixture(t, ctx, store, "receipt-replay")
	input := integrationstore.ReceiveIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID,
		EventID: "event-1", Payload: json.RawMessage(`{"message":"hello","sequence":9007199254740993}`),
		Capabilities: testChannelCapabilities(testChannelProvider),
	}

	const copies = 8
	start := make(chan struct{})
	results := make([]integrationstore.IntegrationEventReceipt, copies)
	errors := make([]error, copies)
	var workers sync.WaitGroup
	for i := range copies {
		workers.Go(func() {
			<-start
			results[i], errors[i] = store.Integrations().ReceiveIntegrationEvent(ctx, input)
		})
	}
	close(start)
	workers.Wait()
	for i := range copies {
		require.NoError(t, errors[i])
		require.Equal(t, results[0].ID, results[i].ID)
		require.Equal(t, integrationstore.IntegrationEventPending, results[i].State)
		require.JSONEq(t, string(input.Payload), string(results[i].Payload))
	}
	var rows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_event_receipts`).Scan(&rows))
	require.Equal(t, 1, rows)

	input.Payload = json.RawMessage(`{"sequence":9007199254740993.0,"message":"hello"}`)
	replayed, err := store.Integrations().ReceiveIntegrationEvent(ctx, input)
	require.NoError(t, err, "equivalent JSON numeric values preserve event identity")
	require.Equal(t, results[0].ID, replayed.ID)
	input.Payload = json.RawMessage(`{"sequence":9007199254740992,"message":"hello"}`)
	_, err = store.Integrations().ReceiveIntegrationEvent(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	input.EventID = "event-2"
	second, err := store.Integrations().ReceiveIntegrationEvent(ctx, input)
	require.NoError(t, err)
	require.NotEqual(t, results[0].ID, second.ID, "new event identity must not be deduplicated by message content")
}

func TestIntegrationEventReceiptLeaseRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, _, install := createChannelLifecycleFixture(t, ctx, store, "receipt-lease")
	receipt, err := store.Integrations().ReceiveIntegrationEvent(ctx, integrationstore.ReceiveIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID,
		EventID: "event", Payload: json.RawMessage(`{"message":"hello"}`),
		Capabilities: testChannelCapabilities(testChannelProvider),
	})
	require.NoError(t, err)
	claim := integrationstore.ClaimNextIntegrationEventInput{
		Capability: testChannelCapability(testChannelProvider), LeaseDuration: time.Minute,
	}

	const consumers = 8
	start := make(chan struct{})
	results := make([]integrationstore.IntegrationEventReceipt, consumers)
	foundClaims := make([]bool, consumers)
	errors := make([]error, consumers)
	var workers sync.WaitGroup
	for i := range consumers {
		workers.Go(func() {
			<-start
			results[i], foundClaims[i], errors[i] = store.Integrations().ClaimNextIntegrationEvent(ctx, claim)
		})
	}
	close(start)
	workers.Wait()
	var leases []integrationstore.IntegrationEventReceipt
	for i := range consumers {
		require.NoError(t, errors[i])
		if foundClaims[i] {
			leases = append(leases, results[i])
		}
	}
	require.Len(t, leases, 1, "only one consumer may own the event")
	first := leases[0]
	require.Equal(t, receipt.ID, first.ID)
	require.Equal(t, 1, first.AttemptCount)
	finish := integrationstore.FinishIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, ID: first.ID,
		LeaseToken: first.LeaseToken, LeaseGeneration: first.LeaseGeneration,
		State:        integrationstore.IntegrationEventCompleted,
		Capabilities: testChannelCapabilities(testChannelProvider),
	}

	_, err = pool.Exec(ctx, `UPDATE integration_event_receipts
SET available_at = statement_timestamp() - interval '1 second',
    lease_expires_at = statement_timestamp() - interval '1 second'
WHERE id = $1`, first.ID)
	require.NoError(t, err)
	_, err = store.Integrations().FinishIntegrationEvent(ctx, finish)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict, "expiry fences completion before any replacement claims")
	second, found, err := store.Integrations().ClaimNextIntegrationEvent(ctx, claim)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, first.LeaseGeneration+1, second.LeaseGeneration)
	require.NotEqual(t, first.LeaseToken, second.LeaseToken)
	require.Equal(t, first.Payload, second.Payload, "crash recovery preserves the original payload")
	_, err = store.Integrations().FinishIntegrationEvent(ctx, finish)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict, "stale owner cannot complete a new claim")

	finish.LeaseToken, finish.LeaseGeneration = second.LeaseToken, second.LeaseGeneration
	finish.State, finish.LastError = integrationstore.IntegrationEventPending, json.RawMessage(`{"code":"temporary_provider_failure"}`)
	retry, err := store.Integrations().FinishIntegrationEvent(ctx, finish)
	require.NoError(t, err)
	require.Nil(t, retry.LeaseExpiresAt)
	require.Nil(t, retry.CompletedAt)
	require.Greater(t, retry.AvailableAt.Sub(second.AvailableAt.Add(-time.Minute)), time.Second)
	_, found, err = store.Integrations().ClaimNextIntegrationEvent(ctx, claim)
	require.NoError(t, err)
	require.False(t, found, "failed processing backs off instead of spinning")
	_, err = pool.Exec(ctx,
		`UPDATE integration_event_receipts SET available_at = statement_timestamp() - interval '1 second' WHERE id = $1`, first.ID)
	require.NoError(t, err)
	next, found, err := store.Integrations().ClaimNextIntegrationEvent(ctx, claim)
	require.NoError(t, err)
	require.True(t, found)
	finish.LeaseToken, finish.LeaseGeneration = next.LeaseToken, next.LeaseGeneration
	finish.State, finish.LastError = integrationstore.IntegrationEventCompleted, nil
	completed, err := store.Integrations().FinishIntegrationEvent(ctx, finish)
	require.NoError(t, err)
	require.NotNil(t, completed.CompletedAt)
	_, found, err = store.Integrations().ClaimNextIntegrationEvent(ctx, claim)
	require.NoError(t, err)
	require.False(t, found)
	_, err = store.Integrations().FinishIntegrationEvent(ctx, finish)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict, "terminal receipt cannot be reopened")
}

func TestIntegrationEventReceiptRetryAfter(t *testing.T) {
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, _, install := createChannelLifecycleFixture(t, ctx, store, "receipt-delay")
	receipt, err := store.Integrations().ReceiveIntegrationEvent(ctx, integrationstore.ReceiveIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, EventID: "provider-throttled",
		Payload: json.RawMessage(`{"message":"hello"}`), Capabilities: testChannelCapabilities(testChannelProvider),
	})
	require.NoError(t, err)
	claimInput := integrationstore.ClaimNextIntegrationEventInput{
		Capability: testChannelCapability(testChannelProvider), LeaseDuration: time.Minute,
	}
	claim, found, err := store.Integrations().ClaimNextIntegrationEvent(ctx, claimInput)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, receipt.ID, claim.ID)
	finish := integrationstore.FinishIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, ID: claim.ID,
		LeaseToken: claim.LeaseToken, LeaseGeneration: claim.LeaseGeneration,
		State: integrationstore.IntegrationEventPending, RetryAfter: time.Hour,
		LastError: json.RawMessage(`{"code":"rate_limited"}`), Capabilities: testChannelCapabilities(testChannelProvider),
	}
	readState := func(t *testing.T) string {
		t.Helper()
		var state string
		require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_build_array(
state, attempt_count, available_at, lease_token, lease_generation, lease_expires_at,
last_error, completed_at, updated_at)::text FROM integration_event_receipts WHERE id = $1`, receipt.ID).Scan(&state))
		return state
	}
	before := readState(t)
	for _, test := range []struct {
		name       string
		state      integrationstore.IntegrationEventState
		retryAfter time.Duration
	}{
		{"negative", integrationstore.IntegrationEventPending, -time.Nanosecond},
		{"over one day", integrationstore.IntegrationEventPending, 24*time.Hour + time.Nanosecond},
		{"completed with delay", integrationstore.IntegrationEventCompleted, time.Hour},
		{"failed with delay", integrationstore.IntegrationEventFailed, time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := finish
			invalid.State, invalid.RetryAfter = test.state, test.retryAfter
			if invalid.State == integrationstore.IntegrationEventCompleted {
				invalid.LastError = nil
			}
			_, err := store.Integrations().FinishIntegrationEvent(ctx, invalid)
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			require.Equal(t, before, readState(t), "invalid delay must not consume the lease or change retry state")
		})
	}
	retried, err := store.Integrations().FinishIntegrationEvent(ctx, finish)
	require.NoError(t, err, "the same lease remains usable after invalid requests")
	require.Equal(t, integrationstore.IntegrationEventPending, retried.State)
	require.Equal(t, 1, retried.AttemptCount)
	require.Nil(t, retried.LeaseExpiresAt)
	require.Nil(t, retried.CompletedAt)
	var delayed bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT available_at = updated_at + interval '1 hour'
FROM integration_event_receipts WHERE id = $1`, receipt.ID).Scan(&delayed))
	require.True(t, delayed, "provider delay is measured from the DB completion timestamp")
	_, found, err = store.Integrations().ClaimNextIntegrationEvent(ctx, claimInput)
	require.NoError(t, err)
	require.False(t, found, "an acknowledged one-hour retry is not immediately reclaimable")
	makeReady := func() {
		t.Helper()
		_, err := pool.Exec(ctx, `UPDATE integration_event_receipts
SET available_at = statement_timestamp() - interval '1 second' WHERE id = $1`, receipt.ID)
		require.NoError(t, err)
	}
	makeReady()
	next, found, err := store.Integrations().ClaimNextIntegrationEvent(ctx, claimInput)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, receipt.ID, next.ID)
	require.Equal(t, claim.LeaseGeneration+1, next.LeaseGeneration)
	require.NotEqual(t, claim.LeaseToken, next.LeaseToken)
	require.Equal(t, receipt.Payload, next.Payload)
	before = readState(t)
	_, err = store.Integrations().FinishIntegrationEvent(ctx, finish)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	require.Equal(t, before, readState(t), "stale proof cannot postpone the replacement consumer's work")

	finish.LeaseToken, finish.LeaseGeneration = next.LeaseToken, next.LeaseGeneration
	finish.RetryAfter = 0
	_, err = store.Integrations().FinishIntegrationEvent(ctx, finish)
	require.NoError(t, err)
	var coreBackoff bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT available_at - updated_at BETWEEN interval '3.2 seconds'
AND interval '4.8 seconds' FROM integration_event_receipts WHERE id = $1`, receipt.ID).Scan(&coreBackoff))
	require.True(t, coreBackoff, "a zero hint preserves the second attempt's existing jittered backoff")
	_, found, err = store.Integrations().ClaimNextIntegrationEvent(ctx, claimInput)
	require.NoError(t, err)
	require.False(t, found)
	makeReady()
	final, found, err := store.Integrations().ClaimNextIntegrationEvent(ctx, claimInput)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 3, final.AttemptCount)
	finish.LeaseToken, finish.LeaseGeneration = final.LeaseToken, final.LeaseGeneration
	finish.State, finish.LastError = integrationstore.IntegrationEventCompleted, nil
	completed, err := store.Integrations().FinishIntegrationEvent(ctx, finish)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationEventCompleted, completed.State)
	require.NotNil(t, completed.CompletedAt)
	_, found, err = store.Integrations().ClaimNextIntegrationEvent(ctx, claimInput)
	require.NoError(t, err)
	require.False(t, found)
}

func TestIntegrationEventReceiptAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, app, install := createChannelLifecycleFixture(t, ctx, store, "receipt-scope")
	input := integrationstore.ReceiveIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID,
		EventID: "event", Payload: json.RawMessage(`{"message":"hello"}`),
		Capabilities: testChannelCapabilities("another-provider"),
	}
	_, err := store.Integrations().ReceiveIntegrationEvent(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	input.Capabilities = []channelconnector.Capability{
		{ConnectorKey: testChannelConnector, Provider: "another-provider"},
		{ConnectorKey: "another-connector", Provider: testChannelProvider},
	}
	_, err = store.Integrations().ReceiveIntegrationEvent(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "capability pairs must not become a cross-product")
	input.Capabilities = testChannelCapabilities(testChannelProvider)
	input.ProjectID = testID("wrong-receipt-project")
	_, err = store.Integrations().ReceiveIntegrationEvent(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	input.ProjectID = testProjectID
	_, err = store.Integrations().ReceiveIntegrationEvent(ctx, input)
	require.NoError(t, err)
	claim := integrationstore.ClaimNextIntegrationEventInput{
		Capability: testChannelCapability(testChannelProvider), LeaseDuration: time.Minute,
	}
	next, found, err := store.Integrations().ClaimNextIntegrationEvent(ctx, claim)
	require.NoError(t, err)
	require.True(t, found)
	finish := integrationstore.FinishIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, ID: next.ID,
		LeaseToken: next.LeaseToken, LeaseGeneration: next.LeaseGeneration,
		State: integrationstore.IntegrationEventCompleted, Capabilities: testChannelCapabilities("another-provider"),
	}
	_, err = store.Integrations().FinishIntegrationEvent(ctx, finish)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = pool.Exec(ctx, `UPDATE integration_apps SET state = 'disabled' WHERE id = $1`, app.ID)
	require.NoError(t, err)
	finish.Capabilities = testChannelCapabilities(testChannelProvider)
	_, err = store.Integrations().FinishIntegrationEvent(ctx, finish)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = store.Integrations().ReceiveIntegrationEvent(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = pool.Exec(ctx, `UPDATE integration_event_receipts
SET available_at = statement_timestamp() - interval '1 second',
    lease_expires_at = statement_timestamp() - interval '1 second'
WHERE id = $1`, next.ID)
	require.NoError(t, err)
	_, found, err = store.Integrations().ClaimNextIntegrationEvent(ctx, claim)
	require.NoError(t, err)
	require.False(t, found, "disabled app must not hand more events to consumers")
}
