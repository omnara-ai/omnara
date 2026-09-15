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
