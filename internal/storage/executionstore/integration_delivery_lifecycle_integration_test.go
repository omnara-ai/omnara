//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestIntegrationDeliveryRetryFailureAndExpiryLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "delivery-lifecycle")
	binding := createDeliveryLifecycleBinding(t, ctx, store, agent.ID, install.ID, "delivery-lifecycle")

	createDelivery := func(key string, notifyRef ID) integrationstore.IntegrationDeliveryRecord {
		t.Helper()
		delivery, err := store.Integrations().CreateIntegrationDelivery(
			ctx,
			integrationstore.CreateIntegrationDeliveryInput{
				ProjectID: testProjectID, AgentID: agent.ID,
				IntegrationTargetBindingID: binding.ID,
				Transport:                  integrationstore.IntegrationDeliveryTransportConnector,
				DeliveryKind:               "message", PayloadVersion: "channel-message.v1",
				Payload:          json.RawMessage(`{"message":{"text":"hello"}}`),
				IdempotencyScope: "delivery-lifecycle", IdempotencyKey: key,
				NotifyRef: notifyRef,
			},
		)
		if err != nil {
			t.Fatalf("create delivery %q: %v", key, err)
		}
		return delivery
	}
	claim := func(owner string, limit int) []integrationstore.IntegrationDeliveryRecord {
		t.Helper()
		claims, err := store.Integrations().ClaimIntegrationDeliveries(
			ctx,
			integrationstore.ClaimIntegrationDeliveriesInput{
				ClaimedBy: owner, LeaseDuration: time.Minute,
				Capability: testChannelCapability(testChannelProvider), Limit: limit,
			},
		)
		if err != nil {
			t.Fatalf("claim deliveries as %q: %v", owner, err)
		}
		return claims
	}

	retryDelivery := createDelivery("retry-then-fail", testID("delivery-retry-notify"))
	firstClaim := claim("gateway-retry-1", 1)
	if len(firstClaim) != 1 || firstClaim[0].ID != retryDelivery.ID ||
		firstClaim[0].AttemptCount != 1 || firstClaim[0].ClaimGeneration != 1 {
		t.Fatalf("first delivery claim = %+v", firstClaim)
	}
	retryStarted := time.Now()
	retrying, err := store.Integrations().CompleteIntegrationDelivery(
		ctx,
		integrationstore.CompleteIntegrationDeliveryInput{
			ID: retryDelivery.ID, ClaimToken: firstClaim[0].ClaimToken,
			ClaimGeneration: firstClaim[0].ClaimGeneration,
			State:           integrationstore.IntegrationDeliveryStateRetryWait,
			RetryAfter:      time.Hour,
			LastError:       json.RawMessage(`{"code":"rate_limited"}`),
			Capabilities:    testChannelCapabilities(testChannelProvider),
		},
	)
	if err != nil {
		t.Fatalf("schedule delivery retry: %v", err)
	}
	if retrying.State != integrationstore.IntegrationDeliveryStateRetryWait ||
		retrying.ClaimToken != NilID || retrying.CompletedAt != nil ||
		retrying.AvailableAt.Before(retryStarted.Add(59*time.Minute)) {
		t.Fatalf("retry-wait delivery = %+v", retrying)
	}
	if claims := claim("gateway-too-early", 1); len(claims) != 0 {
		t.Fatalf("early retry claims = %+v", claims)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_deliveries SET available_at = statement_timestamp() - interval '1 second'
		 WHERE id = $1`,
		retryDelivery.ID,
	); err != nil {
		t.Fatalf("make delivery retry due: %v", err)
	}
	secondClaim := claim("gateway-retry-2", 1)
	if len(secondClaim) != 1 || secondClaim[0].ID != retryDelivery.ID ||
		secondClaim[0].AttemptCount != 2 || secondClaim[0].ClaimGeneration != 2 ||
		secondClaim[0].ClaimToken == firstClaim[0].ClaimToken {
		t.Fatalf("second delivery claim = %+v", secondClaim)
	}
	failed, err := store.Integrations().CompleteIntegrationDelivery(
		ctx,
		integrationstore.CompleteIntegrationDeliveryInput{
			ID: retryDelivery.ID, ClaimToken: secondClaim[0].ClaimToken,
			ClaimGeneration: secondClaim[0].ClaimGeneration,
			State:           integrationstore.IntegrationDeliveryStateFailed,
			LastError:       json.RawMessage(`{"code":"provider_rejected"}`),
			Capabilities:    testChannelCapabilities(testChannelProvider),
		},
	)
	if err != nil || failed.State != integrationstore.IntegrationDeliveryStateFailed ||
		failed.CompletedAt == nil || failed.ClaimToken != NilID {
		t.Fatalf("failed delivery = %+v, %v", failed, err)
	}
	if claims := claim("gateway-after-terminal", 1); len(claims) != 0 {
		t.Fatalf("terminal delivery was reclaimed: %+v", claims)
	}

	expiryNotifyRef := testID("delivery-expiry-notify")
	expiring := createDelivery("expire-ambiguous", expiryNotifyRef)
	expiringClaim := claim("gateway-expiring", 1)
	if len(expiringClaim) != 1 || expiringClaim[0].ID != expiring.ID {
		t.Fatalf("expiring delivery claim = %+v", expiringClaim)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_deliveries
		 SET claimed_at = statement_timestamp() - interval '2 minutes',
		     claim_expires_at = statement_timestamp() - interval '1 minute'
		 WHERE id = $1`,
		expiring.ID,
	); err != nil {
		t.Fatalf("expire claimed delivery: %v", err)
	}
	if claims := claim("gateway-before-expiry-sweep", 1); len(claims) != 0 {
		t.Fatalf("expired claimed delivery was reclaimed before its sweep: %+v", claims)
	}
	if _, err := store.Integrations().CompleteIntegrationDelivery(
		ctx,
		integrationstore.CompleteIntegrationDeliveryInput{
			ID: expiring.ID, ClaimToken: expiringClaim[0].ClaimToken,
			ClaimGeneration: expiringClaim[0].ClaimGeneration,
			State:           integrationstore.IntegrationDeliveryStateDelivered,
			Capabilities:    testChannelCapabilities(testChannelProvider),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("late completion after delivery lease expiry = %v, want state transition conflict", err)
	}
	expired, err := store.Integrations().ExpireIntegrationDeliveryClaims(ctx, 10)
	if err != nil || len(expired) != 1 || expired[0].ID != expiring.ID ||
		expired[0].ProjectID != testProjectID || expired[0].NotifyRef != expiryNotifyRef {
		t.Fatalf("expired delivery updates = %+v, %v", expired, err)
	}
	stored, err := store.Integrations().GetIntegrationDelivery(ctx, testProjectID, expiring.ID)
	if err != nil || stored.State != integrationstore.IntegrationDeliveryStateUnknown ||
		stored.CompletedAt == nil || stored.ClaimToken != NilID {
		t.Fatalf("expired delivery = %+v, %v", stored, err)
	}

	budgeted := createDelivery("safe-retry-budget", testID("delivery-budget-notify"))
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_deliveries SET attempt_count = $2 WHERE id = $1`,
		budgeted.ID,
		integrationstore.MaxIntegrationDeliveryClaims-2,
	); err != nil {
		t.Fatalf("prepare delivery retry budget: %v", err)
	}
	penultimate := claim("gateway-budget-penultimate", 1)
	if len(penultimate) != 1 ||
		penultimate[0].AttemptCount != integrationstore.MaxIntegrationDeliveryClaims-1 {
		t.Fatalf("penultimate delivery claim = %+v", penultimate)
	}
	penultimateRetry, err := store.Integrations().CompleteIntegrationDelivery(
		ctx,
		integrationstore.CompleteIntegrationDeliveryInput{
			ID: penultimate[0].ID, ClaimToken: penultimate[0].ClaimToken,
			ClaimGeneration: penultimate[0].ClaimGeneration,
			State:           integrationstore.IntegrationDeliveryStateRetryWait,
			RetryAfter:      time.Second,
			LastError:       json.RawMessage(`{"code":"safe_to_retry"}`),
			Capabilities:    testChannelCapabilities(testChannelProvider),
		},
	)
	if err != nil || penultimateRetry.State != integrationstore.IntegrationDeliveryStateRetryWait {
		t.Fatalf("penultimate safe retry = %+v, %v", penultimateRetry, err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_deliveries SET available_at = statement_timestamp() - interval '1 second'
		 WHERE id = $1`,
		budgeted.ID,
	); err != nil {
		t.Fatalf("make final delivery retry due: %v", err)
	}
	finalClaim := claim("gateway-budget-final", 1)
	if len(finalClaim) != 1 ||
		finalClaim[0].AttemptCount != integrationstore.MaxIntegrationDeliveryClaims {
		t.Fatalf("final delivery claim = %+v", finalClaim)
	}
	exhausted, err := store.Integrations().CompleteIntegrationDelivery(
		ctx,
		integrationstore.CompleteIntegrationDeliveryInput{
			ID: finalClaim[0].ID, ClaimToken: finalClaim[0].ClaimToken,
			ClaimGeneration: finalClaim[0].ClaimGeneration,
			State:           integrationstore.IntegrationDeliveryStateRetryWait,
			RetryAfter:      time.Second,
			LastError:       json.RawMessage(`{"code":"safe_to_retry"}`),
			Capabilities:    testChannelCapabilities(testChannelProvider),
		},
	)
	if err != nil {
		t.Fatalf("exhaust delivery retry budget: %v", err)
	}
	var exhaustedError map[string]any
	if decodeErr := json.Unmarshal(exhausted.LastError, &exhaustedError); decodeErr != nil {
		t.Fatalf("decode exhausted delivery error: %v", decodeErr)
	}
	if exhausted.State != integrationstore.IntegrationDeliveryStateFailed ||
		exhausted.CompletedAt == nil ||
		exhaustedError["code"] != "retry_budget_exhausted" {
		t.Fatalf("exhausted delivery retry budget = %+v, %v", exhausted, err)
	}
}

func TestIntegrationDeliveryClaimsAreDisjointAcrossGateways(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "delivery-concurrency")
	binding := createDeliveryLifecycleBinding(t, ctx, store, agent.ID, install.ID, "delivery-concurrency")

	const deliveryCount = 12
	for index := range deliveryCount {
		if _, err := store.Integrations().CreateIntegrationDelivery(
			ctx,
			integrationstore.CreateIntegrationDeliveryInput{
				ProjectID: testProjectID, AgentID: agent.ID,
				IntegrationTargetBindingID: binding.ID,
				Transport:                  integrationstore.IntegrationDeliveryTransportConnector,
				DeliveryKind:               "message", PayloadVersion: "channel-message.v1",
				Payload:          json.RawMessage(`{"message":{"text":"hello"}}`),
				IdempotencyScope: "delivery-concurrency",
				IdempotencyKey:   testID("delivery-concurrency-" + string(rune('a'+index))).String(),
			},
		); err != nil {
			t.Fatalf("create concurrent delivery %d: %v", index, err)
		}
	}

	start := make(chan struct{})
	type claimResult struct {
		owner  string
		claims []integrationstore.IntegrationDeliveryRecord
		err    error
	}
	results := make(chan claimResult, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, owner := range []string{"gateway-a", "gateway-b"} {
		go func(owner string) {
			ready.Done()
			<-start
			claims, err := store.Integrations().ClaimIntegrationDeliveries(
				ctx,
				integrationstore.ClaimIntegrationDeliveriesInput{
					ClaimedBy: owner, LeaseDuration: time.Minute,
					Capability: testChannelCapability(testChannelProvider), Limit: deliveryCount / 2,
				},
			)
			results <- claimResult{owner: owner, claims: claims, err: err}
		}(owner)
	}
	ready.Wait()
	close(start)

	seen := make(map[ID]string, deliveryCount)
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("claim deliveries as %s: %v", result.owner, result.err)
		}
		if len(result.claims) != deliveryCount/2 {
			t.Fatalf("%s claimed %d deliveries, want %d", result.owner, len(result.claims), deliveryCount/2)
		}
		for _, delivery := range result.claims {
			if previous, duplicated := seen[delivery.ID]; duplicated {
				t.Fatalf("delivery %s claimed by both %s and %s", delivery.ID, previous, result.owner)
			}
			if delivery.ClaimedBy != result.owner || delivery.AttemptCount != 1 {
				t.Fatalf("%s claim = %+v", result.owner, delivery)
			}
			seen[delivery.ID] = result.owner
		}
	}
	if len(seen) != deliveryCount {
		t.Fatalf("unique claimed deliveries = %d, want %d", len(seen), deliveryCount)
	}
}

func createDeliveryLifecycleBinding(
	t *testing.T,
	ctx context.Context,
	store *Store,
	agentID, installID ID,
	suffix string,
) integrationstore.IntegrationTargetBindingRecord {
	t.Helper()
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID: testProjectID, IntegrationInstallID: installID,
			DeploymentKey: suffix, HandlerKey: testChannelHandler, HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create delivery lifecycle route: %v", err)
	}
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agentID, IntegrationInstallID: installID,
			ProviderRef: suffix + "-channel", ProviderRefKind: "channel",
		},
	)
	if err != nil {
		t.Fatalf("create delivery lifecycle target: %v", err)
	}
	binding, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agentID, IntegrationInstallID: installID,
			IntegrationTargetID: target.ID, IntegrationRouteID: route.ID,
			ReceiveAllowed: true, SendAllowed: true, Source: suffix,
		},
	)
	if err != nil {
		t.Fatalf("create delivery lifecycle binding: %v", err)
	}
	return binding
}
