//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func TestIntegrationDeliveryRetentionSkipsRowsLockedByAnotherWorker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "retention-skip-locked")
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, ProviderRef: "retention-channel",
			ProviderRefKind: "channel",
		},
	)
	if err != nil {
		t.Fatalf("create retention target: %v", err)
	}
	binding, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			SendAllowed: true, Source: "retention-skip-locked",
		},
	)
	if err != nil {
		t.Fatalf("create retention binding: %v", err)
	}

	completeDelivery := func(key string) integrationstore.ID {
		t.Helper()
		delivery, err := store.Integrations().CreateIntegrationDelivery(
			ctx,
			integrationstore.CreateIntegrationDeliveryInput{
				ProjectID: testProjectID, AgentID: agent.ID,
				IntegrationTargetBindingID: binding.ID,
				Transport:                  integrationstore.IntegrationDeliveryTransportConnector,
				DeliveryKind:               "message",
				PayloadVersion:             "channel-message.v1",
				Payload:                    json.RawMessage(`{"message":{"text":"hello"}}`),
				IdempotencyScope:           "retention-skip-locked",
				IdempotencyKey:             key,
			},
		)
		if err != nil {
			t.Fatalf("create delivery %q: %v", key, err)
		}
		claims, err := store.Integrations().ClaimIntegrationDeliveries(
			ctx,
			integrationstore.ClaimIntegrationDeliveriesInput{
				ClaimedBy: "retention-worker", LeaseDuration: time.Minute,
				Capability: testChannelCapability(testChannelProvider), Limit: 1,
			},
		)
		if err != nil || len(claims) != 1 || claims[0].ID != delivery.ID {
			t.Fatalf("claim delivery %q = %+v, %v", key, claims, err)
		}
		if _, err := store.Integrations().CompleteIntegrationDelivery(
			ctx,
			integrationstore.CompleteIntegrationDeliveryInput{
				ID: delivery.ID, ClaimToken: claims[0].ClaimToken,
				ClaimGeneration: claims[0].ClaimGeneration,
				State:           integrationstore.IntegrationDeliveryStateDelivered,
				LastError:       json.RawMessage(`{}`),
				Capabilities:    testChannelCapabilities(testChannelProvider),
			},
		); err != nil {
			t.Fatalf("complete delivery %q: %v", key, err)
		}
		return delivery.ID
	}

	lockedID := completeDelivery("locked")
	time.Sleep(2 * time.Millisecond)
	deletableID := completeDelivery("deletable")
	time.Sleep(2 * time.Millisecond)

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin retained delivery holder: %v", err)
	}
	t.Cleanup(func() { _ = holder.Rollback(ctx) })
	if _, err := holder.Exec(
		ctx,
		`SELECT id FROM integration_deliveries WHERE id = $1 FOR UPDATE`,
		lockedID,
	); err != nil {
		t.Fatalf("lock retained delivery: %v", err)
	}

	deleted, err := store.Integrations().DeleteRetainedIntegrationDeliveries(
		ctx,
		integrationstore.DeleteRetainedIntegrationDeliveriesInput{
			Retention: time.Microsecond,
			Limit:     1,
		},
	)
	if err != nil || deleted != 1 {
		t.Fatalf("delete unlocked retained delivery = %d, %v", deleted, err)
	}
	var lockedExists, deletableExists bool
	if err := pool.QueryRow(
		ctx,
		`SELECT
		   EXISTS (SELECT 1 FROM integration_deliveries WHERE id = $1),
		   EXISTS (SELECT 1 FROM integration_deliveries WHERE id = $2)`,
		lockedID,
		deletableID,
	).Scan(&lockedExists, &deletableExists); err != nil {
		t.Fatalf("load retained delivery states: %v", err)
	}
	if !lockedExists || deletableExists {
		t.Fatalf(
			"retention lock handling locked_exists=%t deletable_exists=%t",
			lockedExists,
			deletableExists,
		)
	}
}
