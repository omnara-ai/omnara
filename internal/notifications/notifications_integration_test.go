//go:build integration

package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
)

var (
	testReplicaA       = uuid.MustParse("10000000-0000-0000-0000-000000000001")
	testReplicaB       = uuid.MustParse("10000000-0000-0000-0000-000000000002")
	testReplicaMissing = uuid.MustParse("10000000-0000-0000-0000-000000000003")
)

func TestRedisPresenceStoreOwnerCAS(t *testing.T) {
	ctx := context.Background()
	client := integrationredis.OpenClient(t)
	store, err := NewRedisPresenceStore(client)
	if err != nil {
		t.Fatalf("create presence store: %v", err)
	}

	machineID := uuid.New()
	runtimeID := uuid.New()
	connectionID := uuid.New()
	if err := store.PutIfRuntime(
		ctx,
		machineID,
		DaemonPresence{
			PresenceOwner: PresenceOwner{ReplicaID: testReplicaA, RuntimeID: runtimeID, ConnectionID: connectionID},
		},
		time.Minute,
	); err != nil {
		t.Fatalf("put presence: %v", err)
	}
	if err := store.DeleteIfOwned(
		ctx,
		uuid.Nil,
		PresenceOwner{RuntimeID: runtimeID, ReplicaID: testReplicaA, ConnectionID: connectionID},
	); err == nil {
		t.Fatal("DeleteIfOwned accepted nil machine id")
	}
	if err := store.Refresh(
		ctx,
		machineID,
		PresenceOwner{RuntimeID: uuid.New(), ReplicaID: testReplicaA, ConnectionID: connectionID},
		time.Minute,
	); err != ErrPresenceNotOwned {
		t.Fatalf("refresh wrong runtime error = %v, want ErrPresenceNotOwned", err)
	}
	if err := store.Refresh(
		ctx,
		machineID,
		PresenceOwner{RuntimeID: runtimeID, ReplicaID: testReplicaB, ConnectionID: connectionID},
		time.Minute,
	); err != ErrPresenceNotOwned {
		t.Fatalf("refresh wrong replica error = %v, want ErrPresenceNotOwned", err)
	}
	if err := store.Refresh(
		ctx,
		machineID,
		PresenceOwner{RuntimeID: runtimeID, ReplicaID: testReplicaA, ConnectionID: uuid.New()},
		time.Minute,
	); err != ErrPresenceNotOwned {
		t.Fatalf("refresh wrong connection error = %v, want ErrPresenceNotOwned", err)
	}
	if err := store.Refresh(
		ctx,
		machineID,
		PresenceOwner{RuntimeID: runtimeID, ReplicaID: testReplicaA, ConnectionID: connectionID},
		time.Minute,
	); err != nil {
		t.Fatalf("refresh owner: %v", err)
	}
	if err := store.DeleteIfOwned(
		ctx,
		machineID,
		PresenceOwner{RuntimeID: uuid.New(), ReplicaID: testReplicaA, ConnectionID: connectionID},
	); err != nil {
		t.Fatalf("delete wrong owner: %v", err)
	}
	if _, ok, err := store.Get(ctx, machineID); err != nil || !ok {
		t.Fatalf("presence after wrong-owner delete ok=%v err=%v", ok, err)
	}
	if err := store.DeleteIfOwned(
		ctx,
		machineID,
		PresenceOwner{RuntimeID: runtimeID, ReplicaID: testReplicaA, ConnectionID: uuid.New()},
	); err != nil {
		t.Fatalf("delete wrong connection: %v", err)
	}
	if _, ok, err := store.Get(ctx, machineID); err != nil || !ok {
		t.Fatalf("presence after wrong-connection delete ok=%v err=%v", ok, err)
	}
	if err := store.DeleteIfOwned(
		ctx,
		machineID,
		PresenceOwner{RuntimeID: runtimeID, ReplicaID: testReplicaA, ConnectionID: connectionID},
	); err != nil {
		t.Fatalf("delete owner: %v", err)
	}
	if _, ok, err := store.Get(ctx, machineID); err != nil || ok {
		t.Fatalf("presence after owner delete ok=%v err=%v", ok, err)
	}
	if err := store.Refresh(
		ctx,
		machineID,
		PresenceOwner{RuntimeID: runtimeID, ReplicaID: testReplicaA, ConnectionID: connectionID},
		time.Minute,
	); err != ErrPresenceNotOwned {
		t.Fatalf("refresh missing presence error = %v, want ErrPresenceNotOwned", err)
	}
	if err := store.PutIfRuntime(
		ctx,
		machineID,
		DaemonPresence{
			PresenceOwner: PresenceOwner{ReplicaID: testReplicaA, RuntimeID: runtimeID, ConnectionID: connectionID},
		},
		time.Minute,
	); err != nil {
		t.Fatalf("restore presence: %v", err)
	}
	if err := store.PutIfMissing(
		ctx,
		machineID,
		DaemonPresence{
			PresenceOwner: PresenceOwner{ReplicaID: testReplicaMissing, RuntimeID: runtimeID, ConnectionID: uuid.New()},
		},
		time.Minute,
	); err != ErrPresenceNotOwned {
		t.Fatalf("put-if-missing existing presence error = %v, want ErrPresenceNotOwned", err)
	}
	if err := store.PutIfRuntime(
		ctx,
		machineID,
		DaemonPresence{
			PresenceOwner: PresenceOwner{ReplicaID: testReplicaB, RuntimeID: uuid.New(), ConnectionID: uuid.New()},
		},
		time.Minute,
	); err != ErrPresenceNotOwned {
		t.Fatalf("put-if-runtime wrong runtime error = %v, want ErrPresenceNotOwned", err)
	}
	unchanged, ok, err := store.Get(ctx, machineID)
	if err != nil || !ok {
		t.Fatalf("presence after wrong-runtime put-if ok=%v err=%v", ok, err)
	}
	if unchanged.RuntimeID != runtimeID || unchanged.ReplicaID != testReplicaA || unchanged.ConnectionID != connectionID {
		t.Fatalf("wrong-runtime put-if changed presence to %+v", unchanged)
	}
	replacementConnectionID := uuid.New()
	if err := store.PutIfRuntime(
		ctx,
		machineID,
		DaemonPresence{
			PresenceOwner: PresenceOwner{ReplicaID: testReplicaB, RuntimeID: runtimeID, ConnectionID: replacementConnectionID},
		},
		time.Minute,
	); err != nil {
		t.Fatalf("put-if-runtime same runtime: %v", err)
	}
	replaced, ok, err := store.Get(ctx, machineID)
	if err != nil || !ok {
		t.Fatalf("presence after same-runtime put-if ok=%v err=%v", ok, err)
	}
	if replaced.RuntimeID != runtimeID || replaced.ReplicaID != testReplicaB ||
		replaced.ConnectionID != replacementConnectionID {
		t.Fatalf("same-runtime put-if presence = %+v, want replacement connection", replaced)
	}
	if err := store.DeleteIfOwned(
		ctx,
		machineID,
		PresenceOwner{RuntimeID: runtimeID, ReplicaID: testReplicaB, ConnectionID: replacementConnectionID},
	); err != nil {
		t.Fatalf("delete replaced presence: %v", err)
	}
	missingPresence := DaemonPresence{
		PresenceOwner: PresenceOwner{ReplicaID: testReplicaMissing, RuntimeID: runtimeID, ConnectionID: uuid.New()},
	}
	if err := store.PutIfMissing(ctx, machineID, missingPresence, time.Minute); err != nil {
		t.Fatalf("put-if-missing absent presence: %v", err)
	}
	storedMissing, ok, err := store.Get(ctx, machineID)
	if err != nil || !ok {
		t.Fatalf("presence after put-if-missing ok=%v err=%v", ok, err)
	}
	if storedMissing.RuntimeID != runtimeID || storedMissing.ReplicaID != missingPresence.ReplicaID ||
		storedMissing.ConnectionID != missingPresence.ConnectionID {
		t.Fatalf("put-if-missing presence = %+v, want %+v", storedMissing, missingPresence)
	}
}

func TestRedisPresenceStoreRejectsMismatchedRuntimePresenceOwner(t *testing.T) {
	ctx := context.Background()
	client := integrationredis.OpenClient(t)
	store, err := NewRedisPresenceStore(client)
	if err != nil {
		t.Fatalf("create presence store: %v", err)
	}

	runtimeID := uuid.New()
	otherRuntimeID := uuid.New()
	presence := DaemonPresence{
		PresenceOwner: PresenceOwner{ReplicaID: testReplicaA, RuntimeID: otherRuntimeID, ConnectionID: uuid.New()},
	}
	if err := store.PutRuntime(ctx, runtimeID, presence, time.Minute); err == nil {
		t.Fatal("PutRuntime accepted mismatched runtime owner")
	}
	if err := store.PutRuntimeIfMissing(ctx, runtimeID, presence, time.Minute); err == nil {
		t.Fatal("PutRuntimeIfMissing accepted mismatched runtime owner")
	}
	if err := store.RefreshRuntime(ctx, runtimeID, presence.PresenceOwner, time.Minute); err == nil {
		t.Fatal("RefreshRuntime accepted mismatched runtime owner")
	}
	if err := store.DeleteRuntimeIfOwned(ctx, runtimeID, presence.PresenceOwner); err == nil {
		t.Fatal("DeleteRuntimeIfOwned accepted mismatched runtime owner")
	}
}

func TestRedisPresenceStoreRejectsInvalidIdentity(t *testing.T) {
	ctx := context.Background()
	client := integrationredis.OpenClient(t)
	store, err := NewRedisPresenceStore(client)
	if err != nil {
		t.Fatalf("create presence store: %v", err)
	}

	for _, tc := range []struct {
		name  string
		field string
		value string
	}{
		{name: "invalid runtime", field: "runtime_id", value: "not-a-uuid"},
		{name: "nil replica", field: "replica_id", value: uuid.Nil.String()},
		{
			name:  "uppercase runtime",
			field: "runtime_id",
			value: "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA",
		},
		{
			name:  "compact replica",
			field: "replica_id",
			value: "aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa",
		},
		{
			name:  "urn connection",
			field: "connection_id",
			value: "urn:uuid:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			machineID := uuid.New()
			record := map[string]any{
				"runtime_id":    uuid.New(),
				"replica_id":    uuid.New(),
				"connection_id": uuid.New(),
			}
			record[tc.field] = tc.value
			body, err := json.Marshal(record)
			if err != nil {
				t.Fatalf("marshal invalid presence: %v", err)
			}
			if err := client.Set(ctx, daemonPresenceKey(machineID), body, time.Minute); err != nil {
				t.Fatalf("seed invalid presence: %v", err)
			}
			if _, ok, err := store.Get(ctx, machineID); ok || !errors.Is(err, errInvalidPresence) {
				t.Fatalf("get invalid presence ok=%v err=%v, want invalid presence error", ok, err)
			}
		})
	}
}

func TestRedisPresenceStoreRejectsRuntimeIdentityMismatchedWithKey(t *testing.T) {
	ctx := context.Background()
	client := integrationredis.OpenClient(t)
	store, err := NewRedisPresenceStore(client)
	if err != nil {
		t.Fatalf("create presence store: %v", err)
	}

	runtimeID := uuid.New()
	body, err := json.Marshal(DaemonPresence{PresenceOwner: PresenceOwner{
		RuntimeID:    uuid.New(),
		ReplicaID:    uuid.New(),
		ConnectionID: uuid.New(),
	}})
	if err != nil {
		t.Fatalf("marshal mismatched runtime presence: %v", err)
	}
	if err := client.Set(ctx, daemonRuntimePresenceKey(runtimeID), body, time.Minute); err != nil {
		t.Fatalf("seed mismatched runtime presence: %v", err)
	}
	if _, ok, err := store.GetRuntime(ctx, runtimeID); ok || !errors.Is(err, errInvalidPresence) {
		t.Fatalf("get mismatched runtime presence ok=%v err=%v, want invalid presence error", ok, err)
	}
}

func TestRedisPresenceStoreRejectsMalformedRecord(t *testing.T) {
	ctx := context.Background()
	client := integrationredis.OpenClient(t)
	store, err := NewRedisPresenceStore(client)
	if err != nil {
		t.Fatalf("create presence store: %v", err)
	}
	machineID := uuid.New()
	if err := client.Set(ctx, daemonPresenceKey(machineID), []byte(`{"runtime_id":`), time.Minute); err != nil {
		t.Fatalf("seed malformed presence: %v", err)
	}
	if _, ok, err := store.Get(ctx, machineID); ok || !errors.Is(err, errInvalidPresence) {
		t.Fatalf("get malformed presence ok=%v err=%v, want invalid presence error", ok, err)
	}
}

func TestRedisPresenceStorePutRuntimeIfMissingDoesNotReplaceOwner(t *testing.T) {
	ctx := context.Background()
	client := integrationredis.OpenClient(t)
	store, err := NewRedisPresenceStore(client)
	if err != nil {
		t.Fatalf("create presence store: %v", err)
	}

	runtimeID := uuid.New()
	first := DaemonPresence{
		PresenceOwner: PresenceOwner{ReplicaID: testReplicaA, RuntimeID: runtimeID, ConnectionID: uuid.New()},
	}
	second := DaemonPresence{
		PresenceOwner: PresenceOwner{ReplicaID: testReplicaB, RuntimeID: runtimeID, ConnectionID: uuid.New()},
	}
	if err := store.PutRuntimeIfMissing(ctx, runtimeID, first, time.Minute); err != nil {
		t.Fatalf("put first runtime presence: %v", err)
	}
	if err := store.PutRuntimeIfMissing(ctx, runtimeID, second, time.Minute); err != ErrPresenceNotOwned {
		t.Fatalf("put second runtime presence error = %v, want ErrPresenceNotOwned", err)
	}
	stored, ok, err := store.GetRuntime(ctx, runtimeID)
	if err != nil || !ok {
		t.Fatalf("get runtime presence ok=%v err=%v", ok, err)
	}
	if stored.ReplicaID != first.ReplicaID || stored.ConnectionID != first.ConnectionID {
		t.Fatalf("runtime presence was replaced by SetNX path: got %+v want %+v", stored, first)
	}
}

func TestRoutedPublisherRoutesRuntimeEndedByRuntimePresence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := integrationredis.OpenClient(t)
	presence, err := NewRedisPresenceStore(client)
	if err != nil {
		t.Fatalf("create presence store: %v", err)
	}
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	machineID := uuid.New()
	oldRuntimeID := uuid.New()
	newRuntimeID := uuid.New()
	oldReplicaID := uuid.New()
	newReplicaID := uuid.New()
	if err := presence.PutRuntime(
		ctx,
		oldRuntimeID,
		DaemonPresence{
			PresenceOwner: PresenceOwner{ReplicaID: oldReplicaID, RuntimeID: oldRuntimeID, ConnectionID: uuid.New()},
		},
		time.Minute,
	); err != nil {
		t.Fatalf("put old runtime presence: %v", err)
	}
	if err := presence.PutIfRuntime(
		ctx,
		machineID,
		DaemonPresence{
			PresenceOwner: PresenceOwner{ReplicaID: newReplicaID, RuntimeID: newRuntimeID, ConnectionID: uuid.New()},
		},
		time.Minute,
	); err != nil {
		t.Fatalf("put current machine presence: %v", err)
	}

	received := make(chan WakeupMessage, 1)
	sub, err := bus.SubscribeDaemonReplicaWakeups(ctx, oldReplicaID, func(_ context.Context, body WakeupMessage) {
		received <- body
	})
	if err != nil {
		t.Fatalf("subscribe old replica: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	newReplicaReceived := make(chan WakeupMessage, 1)
	wrongSub, err := bus.SubscribeDaemonReplicaWakeups(ctx, newReplicaID, func(_ context.Context, body WakeupMessage) {
		newReplicaReceived <- body
	})
	if err != nil {
		t.Fatalf("subscribe new replica: %v", err)
	}
	t.Cleanup(func() { _ = wrongSub.Unsubscribe() })

	publisher := newTestRoutedPublisher(t, RoutedPublisherPorts{DaemonWakeups: bus}, presence, nil)
	publisher.PublishPostCommit(ctx, DaemonRuntimeEndedCommitted{
		MachineID: machineID,
		RuntimeID: oldRuntimeID,
		Cause:     DaemonRuntimeEndReconnect,
	})

	select {
	case body := <-received:
		if body.Type != WakeupTypeDaemonRuntimeEnded ||
			body.RuntimeID == nil ||
			*body.RuntimeID != oldRuntimeID ||
			body.RuntimeEndCause != DaemonRuntimeEndReconnect {
			t.Fatalf("received wakeup = %+v, want runtime-ended for old runtime", body)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for runtime-routed wakeup: %v", ctx.Err())
	}
	select {
	case body := <-newReplicaReceived:
		t.Fatalf("current machine replica received old runtime-ended wakeup %+v", body)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRoutedPublisherUsesRedisPresenceAndReplicaWakeupChannel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := integrationredis.OpenClient(t)
	presence, err := NewRedisPresenceStore(client)
	if err != nil {
		t.Fatalf("create presence store: %v", err)
	}
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	replicaID := uuid.New()
	machineID := uuid.New()
	runtimeID := uuid.New()
	if err := presence.PutIfRuntime(
		ctx,
		machineID,
		DaemonPresence{
			PresenceOwner: PresenceOwner{ReplicaID: replicaID, RuntimeID: runtimeID, ConnectionID: uuid.New()},
		},
		time.Minute,
	); err != nil {
		t.Fatalf("put presence: %v", err)
	}

	received := make(chan WakeupMessage, 1)
	sub, err := bus.SubscribeDaemonReplicaWakeups(ctx, replicaID, func(_ context.Context, body WakeupMessage) {
		received <- body
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	wrongReplicaReceived := make(chan WakeupMessage, 1)
	wrongReplicaID := uuid.New()
	wrongSub, err := bus.SubscribeDaemonReplicaWakeups(ctx, wrongReplicaID, func(_ context.Context, body WakeupMessage) {
		wrongReplicaReceived <- body
	})
	if err != nil {
		t.Fatalf("subscribe wrong replica: %v", err)
	}
	t.Cleanup(func() { _ = wrongSub.Unsubscribe() })

	publisher := newTestRoutedPublisher(t, RoutedPublisherPorts{DaemonWakeups: bus}, presence, nil)
	publisher.PublishPostCommit(ctx, DaemonWorkCommitted{MachineID: machineID})

	select {
	case body := <-received:
		if body.Type != WakeupTypeDaemonWork || body.MachineID != machineID {
			t.Fatalf("received wakeup = %+v, want daemon work for machine %s", body, machineID)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for routed redis wakeup: %v", ctx.Err())
	}
	select {
	case body := <-wrongReplicaReceived:
		t.Fatalf("wrong replica received wakeup %+v", body)
	case <-time.After(100 * time.Millisecond):
	}
}
