//go:build integration

package notifications

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
)

func TestAgentEventWakeupBusPublishSubscribeRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	agentID := uuid.New()
	otherAgentID := uuid.New()

	received := make(chan struct{}, 4)
	sub, err := bus.SubscribeAgentEventWakeups(ctx, agentID, func(context.Context) {
		received <- struct{}{}
	})
	if err != nil {
		t.Fatalf("subscribe agent events: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	wrong := make(chan struct{}, 4)
	wrongSub, err := bus.SubscribeAgentEventWakeups(ctx, otherAgentID, func(context.Context) {
		wrong <- struct{}{}
	})
	if err != nil {
		t.Fatalf("subscribe other agent events: %v", err)
	}
	t.Cleanup(func() { _ = wrongSub.Unsubscribe() })

	if err := bus.PublishAgentEventWakeup(ctx, agentID); err != nil {
		t.Fatalf("publish agent event wakeup: %v", err)
	}

	select {
	case <-received:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for agent-event wakeup: %v", ctx.Err())
	}
	select {
	case <-wrong:
		t.Fatalf("wrong agent channel received wakeup")
	case <-time.After(100 * time.Millisecond):
	}

	streamReceived := make(chan json.RawMessage, 4)
	streamSub, err := bus.SubscribeAgentStreamDeltas(ctx, agentID, func(_ context.Context, payload json.RawMessage) {
		streamReceived <- payload
	})
	if err != nil {
		t.Fatalf("subscribe agent stream: %v", err)
	}
	t.Cleanup(func() { _ = streamSub.Unsubscribe() })
	streamPayload := json.RawMessage(`{"seq":1,"event":{"kind":"text_delta","delta":"hi"}}`)
	if err := bus.PublishAgentStreamDelta(ctx, agentID, streamPayload); err != nil {
		t.Fatalf("publish stream delta: %v", err)
	}
	select {
	case got := <-streamReceived:
		if string(got) != string(streamPayload) {
			t.Fatalf("stream payload = %s, want %s", got, streamPayload)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for stream delta: %v", ctx.Err())
	}
	select {
	case <-received:
		t.Fatalf("event subscriber received stream delta")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAgentEventWakeupBusMultiplexesSameAgentSubscriptions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	agentID := uuid.New()
	receivedA := make(chan struct{}, 2)
	subA, err := bus.SubscribeAgentEventWakeups(ctx, agentID, func(context.Context) {
		receivedA <- struct{}{}
	})
	if err != nil {
		t.Fatalf("subscribe agent events A: %v", err)
	}
	t.Cleanup(func() { _ = subA.Unsubscribe() })

	receivedB := make(chan struct{}, 2)
	subB, err := bus.SubscribeAgentEventWakeups(ctx, agentID, func(context.Context) {
		receivedB <- struct{}{}
	})
	if err != nil {
		t.Fatalf("subscribe agent events B: %v", err)
	}
	t.Cleanup(func() { _ = subB.Unsubscribe() })

	bus.mu.Lock()
	fanoutCount := len(bus.agentEventFanouts)
	fanout := bus.agentEventFanouts[agentID]
	bus.mu.Unlock()
	if fanoutCount != 1 || fanout == nil {
		t.Fatalf("agent fanouts = %d, fanout exists = %v; want one fanout for agent", fanoutCount, fanout != nil)
	}
	fanout.mu.Lock()
	subscriberCount := len(fanout.subscribers)
	fanout.mu.Unlock()
	if subscriberCount != 2 {
		t.Fatalf("fanout subscribers = %d, want 2", subscriberCount)
	}

	if err := bus.PublishAgentEventWakeup(ctx, agentID); err != nil {
		t.Fatalf("publish agent event wakeup: %v", err)
	}
	assertAgentEventWakeupReceived(t, ctx, receivedA)
	assertAgentEventWakeupReceived(t, ctx, receivedB)

	if err := subA.Unsubscribe(); err != nil {
		t.Fatalf("unsubscribe A: %v", err)
	}
	bus.mu.Lock()
	fanout = bus.agentEventFanouts[agentID]
	bus.mu.Unlock()
	if fanout == nil {
		t.Fatal("fanout removed while one subscriber remains")
	}
	fanout.mu.Lock()
	subscriberCount = len(fanout.subscribers)
	fanout.mu.Unlock()
	if subscriberCount != 1 {
		t.Fatalf("fanout subscribers after unsubscribe A = %d, want 1", subscriberCount)
	}

	if err := bus.PublishAgentEventWakeup(ctx, agentID); err != nil {
		t.Fatalf("publish second agent event wakeup: %v", err)
	}
	assertAgentEventWakeupReceived(t, ctx, receivedB)
	select {
	case <-receivedA:
		t.Fatalf("unsubscribed handler received message")
	case <-time.After(100 * time.Millisecond):
	}

	if err := subB.Unsubscribe(); err != nil {
		t.Fatalf("unsubscribe B: %v", err)
	}
	bus.mu.Lock()
	_, ok := bus.agentEventFanouts[agentID]
	bus.mu.Unlock()
	if ok {
		t.Fatal("fanout remains after final subscriber unsubscribed")
	}
}

func TestAgentStreamDeltaBusMultiplexesSameAgentSubscriptions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	agentID := uuid.New()
	receivedA := make(chan json.RawMessage, 2)
	subA, err := bus.SubscribeAgentStreamDeltas(ctx, agentID, func(_ context.Context, payload json.RawMessage) {
		receivedA <- payload
	})
	if err != nil {
		t.Fatalf("subscribe agent stream A: %v", err)
	}
	t.Cleanup(func() { _ = subA.Unsubscribe() })

	receivedB := make(chan json.RawMessage, 2)
	subB, err := bus.SubscribeAgentStreamDeltas(ctx, agentID, func(_ context.Context, payload json.RawMessage) {
		receivedB <- payload
	})
	if err != nil {
		t.Fatalf("subscribe agent stream B: %v", err)
	}
	t.Cleanup(func() { _ = subB.Unsubscribe() })

	bus.mu.Lock()
	fanoutCount := len(bus.streamDeltaFanouts)
	fanout := bus.streamDeltaFanouts[agentID]
	bus.mu.Unlock()
	if fanoutCount != 1 || fanout == nil {
		t.Fatalf("stream fanouts = %d, fanout exists = %v; want one fanout for agent", fanoutCount, fanout != nil)
	}
	fanout.mu.Lock()
	subscriberCount := len(fanout.subscribers)
	fanout.mu.Unlock()
	if subscriberCount != 2 {
		t.Fatalf("stream fanout subscribers = %d, want 2", subscriberCount)
	}

	firstPayload := json.RawMessage(`{"seq":1}`)
	if err := bus.PublishAgentStreamDelta(ctx, agentID, firstPayload); err != nil {
		t.Fatalf("publish first stream delta: %v", err)
	}
	assertAgentStreamDeltaReceived(t, ctx, receivedA, firstPayload)
	assertAgentStreamDeltaReceived(t, ctx, receivedB, firstPayload)

	if err := subA.Unsubscribe(); err != nil {
		t.Fatalf("unsubscribe stream A: %v", err)
	}
	bus.mu.Lock()
	fanout = bus.streamDeltaFanouts[agentID]
	bus.mu.Unlock()
	if fanout == nil {
		t.Fatal("stream fanout removed while one subscriber remains")
	}
	fanout.mu.Lock()
	subscriberCount = len(fanout.subscribers)
	fanout.mu.Unlock()
	if subscriberCount != 1 {
		t.Fatalf("stream fanout subscribers after unsubscribe A = %d, want 1", subscriberCount)
	}

	secondPayload := json.RawMessage(`{"seq":2}`)
	if err := bus.PublishAgentStreamDelta(ctx, agentID, secondPayload); err != nil {
		t.Fatalf("publish second stream delta: %v", err)
	}
	assertAgentStreamDeltaReceived(t, ctx, receivedB, secondPayload)
	select {
	case payload := <-receivedA:
		t.Fatalf("unsubscribed stream handler received payload %s", payload)
	case <-time.After(100 * time.Millisecond):
	}

	if err := subB.Unsubscribe(); err != nil {
		t.Fatalf("unsubscribe stream B: %v", err)
	}
	bus.mu.Lock()
	_, ok := bus.streamDeltaFanouts[agentID]
	bus.mu.Unlock()
	if ok {
		t.Fatal("stream fanout remains after final subscriber unsubscribed")
	}
}

func TestRedisBusRejectsNilRoutingIDs(t *testing.T) {
	ctx := context.Background()
	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	checks := []struct {
		name string
		call func() error
	}{
		{
			name: "publish daemon wakeup",
			call: func() error {
				return bus.PublishDaemonReplicaWakeup(
					ctx,
					uuid.Nil,
					WakeupMessage{Type: WakeupTypeDaemonWork, MachineID: uuid.New()},
				)
			},
		},
		{
			name: "subscribe daemon wakeup",
			call: func() error {
				_, err := bus.SubscribeDaemonReplicaWakeups(ctx, uuid.Nil, func(context.Context, WakeupMessage) {})
				return err
			},
		},
		{
			name: "publish agent event",
			call: func() error {
				return bus.PublishAgentEventWakeup(ctx, uuid.Nil)
			},
		},
		{
			name: "subscribe agent event",
			call: func() error {
				_, err := bus.SubscribeAgentEventWakeups(ctx, uuid.Nil, func(context.Context) {})
				return err
			},
		},
		{
			name: "publish agent stream delta",
			call: func() error {
				return bus.PublishAgentStreamDelta(ctx, uuid.Nil, json.RawMessage(`{"seq":1}`))
			},
		},
		{
			name: "subscribe agent stream delta",
			call: func() error {
				_, err := bus.SubscribeAgentStreamDeltas(ctx, uuid.Nil, func(context.Context, json.RawMessage) {})
				return err
			},
		},
		{
			name: "publish worker control",
			call: func() error {
				return bus.PublishWorkerControl(ctx, uuid.Nil, NewWorkerControlCancel(uuid.New(), uuid.New()))
			},
		},
		{
			name: "subscribe worker control",
			call: func() error {
				_, err := bus.SubscribeWorkerControl(ctx, uuid.Nil, func(context.Context, WorkerControl) {})
				return err
			},
		},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err == nil {
				t.Fatal("Redis bus accepted a nil routing id")
			}
		})
	}
}

func TestRedisBusRejectsNilHandlers(t *testing.T) {
	ctx := context.Background()
	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	checks := []struct {
		name string
		call func() error
	}{
		{
			name: "daemon wakeup",
			call: func() error {
				_, err := bus.SubscribeDaemonReplicaWakeups(ctx, uuid.New(), nil)
				return err
			},
		},
		{
			name: "agent event",
			call: func() error {
				_, err := bus.SubscribeAgentEventWakeups(ctx, uuid.New(), nil)
				return err
			},
		},
		{
			name: "agent stream delta",
			call: func() error {
				_, err := bus.SubscribeAgentStreamDeltas(ctx, uuid.New(), nil)
				return err
			},
		},
		{
			name: "worker control",
			call: func() error {
				_, err := bus.SubscribeWorkerControl(ctx, uuid.New(), nil)
				return err
			},
		},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err == nil {
				t.Fatal("Redis bus accepted a nil handler")
			}
		})
	}
}

func TestDaemonWakeupBusDropsInvalidPayloadFromRedis(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	replicaID := uuid.New()
	received := make(chan WakeupMessage, 1)
	sub, err := bus.SubscribeDaemonReplicaWakeups(ctx, replicaID, func(_ context.Context, msg WakeupMessage) {
		received <- msg
	})
	if err != nil {
		t.Fatalf("subscribe daemon wakeup: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	channel := daemonReplicaWakeupChannel(replicaID)
	for _, payload := range [][]byte{
		[]byte(`{"type":`),
		[]byte(`{"type":"daemon_work","machine_id":"00000000-0000-0000-0000-000000000000"}`),
	} {
		if err := client.Publish(ctx, channel, payload); err != nil {
			t.Fatalf("publish invalid daemon wakeup directly: %v", err)
		}
	}
	select {
	case msg := <-received:
		t.Fatalf("received invalid daemon wakeup %+v", msg)
	case <-time.After(150 * time.Millisecond):
	}

	valid := WakeupMessage{Type: WakeupTypeDaemonWork, MachineID: uuid.New()}
	payload, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal valid daemon wakeup: %v", err)
	}
	if err := client.Publish(ctx, channel, payload); err != nil {
		t.Fatalf("publish valid daemon wakeup directly: %v", err)
	}
	select {
	case got := <-received:
		if got.Type != valid.Type ||
			got.MachineID != valid.MachineID ||
			got.RuntimeID != nil ||
			len(got.ProcessIDs) != 0 {
			t.Fatalf("daemon wakeup = %+v, want %+v", got, valid)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for valid daemon wakeup: %v", ctx.Err())
	}
}

func TestAgentEventWakeupBusUnsubscribeWaitsForInFlightHandler(t *testing.T) {
	parentCtx, cancelParent := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelParent()

	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	agentID := uuid.New()
	handlerEntered := make(chan struct{})
	releaseHandler := make(chan struct{})
	var enteredOnce sync.Once
	sub, err := bus.SubscribeAgentEventWakeups(parentCtx, agentID, func(context.Context) {
		enteredOnce.Do(func() { close(handlerEntered) })
		<-releaseHandler
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := bus.PublishAgentEventWakeup(parentCtx, agentID); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-handlerEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler to start")
	}

	unsubDone := make(chan error, 1)
	go func() { unsubDone <- sub.Unsubscribe() }()

	select {
	case err := <-unsubDone:
		close(releaseHandler)
		t.Fatalf("Unsubscribe returned (%v) while handler was still in flight", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(releaseHandler)

	select {
	case err := <-unsubDone:
		if err != nil {
			t.Fatalf("Unsubscribe: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Unsubscribe did not return after handler released")
	}
}

func TestAgentStreamDeltaBusUnsubscribeWaitsForInFlightHandler(t *testing.T) {
	parentCtx, cancelParent := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelParent()

	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	agentID := uuid.New()
	handlerEntered := make(chan struct{})
	releaseHandler := make(chan struct{})
	var enteredOnce sync.Once
	sub, err := bus.SubscribeAgentStreamDeltas(parentCtx, agentID, func(_ context.Context, _ json.RawMessage) {
		enteredOnce.Do(func() { close(handlerEntered) })
		<-releaseHandler
	})
	if err != nil {
		t.Fatalf("subscribe stream: %v", err)
	}

	if err := bus.PublishAgentStreamDelta(parentCtx, agentID, json.RawMessage(`{"seq":1}`)); err != nil {
		t.Fatalf("publish stream: %v", err)
	}

	select {
	case <-handlerEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream handler to start")
	}

	unsubDone := make(chan error, 1)
	go func() { unsubDone <- sub.Unsubscribe() }()

	select {
	case err := <-unsubDone:
		close(releaseHandler)
		t.Fatalf("stream Unsubscribe returned (%v) while handler was still in flight", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(releaseHandler)

	select {
	case err := <-unsubDone:
		if err != nil {
			t.Fatalf("stream Unsubscribe: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream Unsubscribe did not return after handler released")
	}
}

func TestAgentEventWakeupBusSurvivesFirstSubscriberContextCancel(t *testing.T) {
	parentCtx, cancelParent := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelParent()

	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	agentID := uuid.New()

	ctxA, cancelA := context.WithCancel(parentCtx)
	defer cancelA()
	receivedA := make(chan struct{}, 4)
	subA, err := bus.SubscribeAgentEventWakeups(ctxA, agentID, func(context.Context) {
		receivedA <- struct{}{}
	})
	if err != nil {
		t.Fatalf("subscribe A: %v", err)
	}
	t.Cleanup(func() { _ = subA.Unsubscribe() })

	receivedB := make(chan struct{}, 4)
	subB, err := bus.SubscribeAgentEventWakeups(parentCtx, agentID, func(context.Context) {
		receivedB <- struct{}{}
	})
	if err != nil {
		t.Fatalf("subscribe B: %v", err)
	}
	t.Cleanup(func() { _ = subB.Unsubscribe() })

	cancelA()

	deadline := time.Now().Add(2 * time.Second)
	for {
		bus.mu.Lock()
		fanout := bus.agentEventFanouts[agentID]
		bus.mu.Unlock()
		if fanout == nil {
			t.Fatal("fanout removed while subscriber B is still active")
		}
		fanout.mu.Lock()
		count := len(fanout.subscribers)
		fanout.mu.Unlock()
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for A to drop out of fanout (count=%d)", count)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := bus.PublishAgentEventWakeup(parentCtx, agentID); err != nil {
		t.Fatalf("publish after A cancellation: %v", err)
	}
	assertAgentEventWakeupReceived(t, parentCtx, receivedB)
}

func TestAgentStreamDeltaBusSurvivesFirstSubscriberContextCancel(t *testing.T) {
	parentCtx, cancelParent := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelParent()

	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	agentID := uuid.New()

	ctxA, cancelA := context.WithCancel(parentCtx)
	defer cancelA()
	receivedA := make(chan json.RawMessage, 4)
	subA, err := bus.SubscribeAgentStreamDeltas(ctxA, agentID, func(_ context.Context, payload json.RawMessage) {
		receivedA <- payload
	})
	if err != nil {
		t.Fatalf("subscribe stream A: %v", err)
	}
	t.Cleanup(func() { _ = subA.Unsubscribe() })

	receivedB := make(chan json.RawMessage, 4)
	subB, err := bus.SubscribeAgentStreamDeltas(parentCtx, agentID, func(_ context.Context, payload json.RawMessage) {
		receivedB <- payload
	})
	if err != nil {
		t.Fatalf("subscribe stream B: %v", err)
	}
	t.Cleanup(func() { _ = subB.Unsubscribe() })

	cancelA()

	deadline := time.Now().Add(2 * time.Second)
	for {
		bus.mu.Lock()
		fanout := bus.streamDeltaFanouts[agentID]
		bus.mu.Unlock()
		if fanout == nil {
			t.Fatal("stream fanout removed while subscriber B is still active")
		}
		fanout.mu.Lock()
		count := len(fanout.subscribers)
		fanout.mu.Unlock()
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for stream A to drop out of fanout (count=%d)", count)
		}
		time.Sleep(10 * time.Millisecond)
	}

	payload := json.RawMessage(`{"seq":1}`)
	if err := bus.PublishAgentStreamDelta(parentCtx, agentID, payload); err != nil {
		t.Fatalf("publish stream after A cancellation: %v", err)
	}
	assertAgentStreamDeltaReceived(t, parentCtx, receivedB, payload)
	select {
	case got := <-receivedA:
		t.Fatalf("canceled stream subscriber received payload %s", got)
	default:
	}
}

func TestRoutedPublisherPublishesAgentEventEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	agentID := uuid.New()
	received := make(chan struct{}, 4)
	sub, err := bus.SubscribeAgentEventWakeups(ctx, agentID, func(context.Context) {
		received <- struct{}{}
	})
	if err != nil {
		t.Fatalf("subscribe agent events: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	publisher := newTestRoutedPublisher(t, RoutedPublisherPorts{AgentEventWakeups: bus}, nil, nil)
	publisher.PublishPostCommit(ctx, AgentEventCommitted{AgentID: agentID})

	select {
	case <-received:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for agent-event publish: %v", ctx.Err())
	}
}

func TestWorkerControlBusPublishSubscribeRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	workerID := uuid.New()
	agentID := uuid.New()
	runtimeID := uuid.New()
	received := make(chan WorkerControl, 1)
	sub, err := bus.SubscribeWorkerControl(ctx, workerID, func(_ context.Context, msg WorkerControl) {
		received <- msg
	})
	if err != nil {
		t.Fatalf("subscribe worker control: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	msg := NewWorkerControlCancel(agentID, runtimeID)
	if err := bus.PublishWorkerControl(ctx, workerID, msg); err != nil {
		t.Fatalf("publish worker control: %v", err)
	}

	select {
	case got := <-received:
		assertWorkerControlCancel(t, got, agentID, runtimeID)
	case <-ctx.Done():
		t.Fatalf("timed out waiting for worker-control message: %v", ctx.Err())
	}
}

func TestWorkerControlBusRejectsInvalidCancelPublish(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	workerID := uuid.New()
	agentID := uuid.New()
	runtimeID := uuid.New()
	cases := []struct {
		name     string
		workerID uuid.UUID
		msg      WorkerControl
	}{
		{
			name:     "missing worker",
			workerID: uuid.Nil,
			msg:      NewWorkerControlCancel(agentID, runtimeID),
		},
		{
			name:     "missing agent",
			workerID: workerID,
			msg:      NewWorkerControlCancel(uuid.Nil, runtimeID),
		},
		{
			name:     "missing runtime lock",
			workerID: workerID,
			msg:      NewWorkerControlCancel(agentID, uuid.Nil),
		},
		{
			name:     "missing kind",
			workerID: workerID,
			msg:      WorkerControl{},
		},
		{
			name:     "unknown kind",
			workerID: workerID,
			msg:      WorkerControl{Kind: WorkerControlKind("stop")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := bus.PublishWorkerControl(ctx, tc.workerID, tc.msg); err == nil {
				t.Fatalf("PublishWorkerControl returned nil error, want validation failure")
			}
		})
	}
}

func TestWorkerControlBusDropsInvalidCancelPayload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatalf("create redis bus: %v", err)
	}

	workerID := uuid.New()
	received := make(chan WorkerControl, 2)
	sub, err := bus.SubscribeWorkerControl(ctx, workerID, func(_ context.Context, msg WorkerControl) {
		received <- msg
	})
	if err != nil {
		t.Fatalf("subscribe worker control: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	invalidPayload, err := json.Marshal(WorkerControl{
		Kind:   WorkerControlKindCancel,
		Cancel: &WorkerControlCancel{RuntimeLockID: uuid.New()},
	})
	if err != nil {
		t.Fatalf("marshal invalid worker control cancel: %v", err)
	}
	if err := client.Publish(ctx, workerControlChannel(workerID), invalidPayload); err != nil {
		t.Fatalf("publish invalid worker control payload: %v", err)
	}
	select {
	case got := <-received:
		t.Fatalf("received invalid worker control payload: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}

	valid := NewWorkerControlCancel(uuid.New(), uuid.New())
	if err := bus.PublishWorkerControl(ctx, workerID, valid); err != nil {
		t.Fatalf("publish valid worker control cancel: %v", err)
	}
	select {
	case got := <-received:
		assertWorkerControlCancel(t, got, valid.Cancel.AgentID, valid.Cancel.RuntimeLockID)
	case <-ctx.Done():
		t.Fatalf("timed out waiting for valid worker-control cancel: %v", ctx.Err())
	}
}

func assertWorkerControlCancel(t *testing.T, got WorkerControl, agentID, runtimeID uuid.UUID) {
	t.Helper()
	if got.Kind != WorkerControlKindCancel || got.Cancel == nil {
		t.Fatalf("worker control = %+v, want cancel", got)
	}
	if got.Cancel.AgentID != agentID || got.Cancel.RuntimeLockID != runtimeID {
		t.Fatalf(
			"worker control cancel = %+v, want agent %s runtime %s",
			got.Cancel,
			agentID,
			runtimeID,
		)
	}
}

func assertAgentEventWakeupReceived(
	t *testing.T,
	ctx context.Context,
	ch <-chan struct{},
) {
	t.Helper()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for agent event: %v", ctx.Err())
	}
}

func assertAgentStreamDeltaReceived(
	t *testing.T,
	ctx context.Context,
	ch <-chan json.RawMessage,
	want json.RawMessage,
) {
	t.Helper()
	select {
	case got := <-ch:
		if string(got) != string(want) {
			t.Fatalf("agent stream payload = %s, want %s", got, want)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for agent stream payload: %v", ctx.Err())
	}
}
