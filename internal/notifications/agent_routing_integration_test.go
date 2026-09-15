//go:build integration

package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
	"github.com/redis/go-redis/v9"
)

func TestRedisAgentUpdatesShareSubscriptionAndPreserveOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner, parent, destination := uuid.New(), uuid.New(), uuid.New()
	options, err := redis.ParseURL(integrationredis.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	observer := redis.NewClient(options)
	t.Cleanup(func() { _ = observer.Close() })
	channel := agentUpdateChannel(destination)
	assertSubscribers := func(want int64) {
		t.Helper()
		counts, err := observer.PubSubNumSub(ctx, channel).Result()
		if err != nil {
			t.Fatal(err)
		}
		if counts[channel] != want {
			t.Fatalf("Redis subscriptions = %d, want %d", counts[channel], want)
		}
	}
	a, b := make(chan AgentUpdate, 4), make(chan AgentUpdate, 4)
	subA, err := bus.SubscribeAgentUpdates(ctx, destination, func(_ context.Context, update AgentUpdate) { a <- update })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = subA.Unsubscribe() })
	subB, err := bus.SubscribeAgentUpdates(ctx, destination, func(_ context.Context, update AgentUpdate) { b <- update })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = subB.Unsubscribe() })
	assertSubscribers(1)
	tool := AgentUpdate{ToolCallUpdate: &ToolCallUpdatedCommitted{
		AgentID:    owner,
		ToolCallID: uuid.New(),
		State:      "ready",
	}}
	change := AgentUpdate{Change: &AgentChangeCommitted{
		AgentID:       owner,
		ParentAgentID: &parent,
		Changes:       []AgentChangeKind{AgentChangeInteractions},
	}}
	for _, update := range []AgentUpdate{tool, change} {
		if err := bus.PublishAgentUpdate(ctx, destination, update); err != nil {
			t.Fatal(err)
		}
		assertAgentUpdateReceived(t, ctx, a, update)
		assertAgentUpdateReceived(t, ctx, b, update)
	}
	if err := subA.Unsubscribe(); err != nil {
		t.Fatal(err)
	}
	assertSubscribers(1)
	if err := bus.PublishAgentUpdate(ctx, destination, change); err != nil {
		t.Fatal(err)
	}
	assertAgentUpdateReceived(t, ctx, b, change)
	select {
	case update := <-a:
		t.Fatalf("unsubscribed handler received %+v", update)
	default:
	}
	if err := subB.Unsubscribe(); err != nil {
		t.Fatal(err)
	}
	assertSubscribers(0)
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if len(bus.agentUpdateFanouts) != 0 {
		t.Fatal("agent update fanout remains after final unsubscribe")
	}
}

func TestRedisAgentUpdatesRejectInvalidPayloadAndRouting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := integrationredis.OpenClient(t)
	bus, err := NewRedisBus(client, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner, destination := uuid.New(), uuid.New()
	valid := AgentUpdate{Change: &AgentChangeCommitted{
		AgentID: owner,
		Changes: []AgentChangeKind{AgentChangeAgent},
	}}
	if err := bus.PublishAgentUpdate(ctx, uuid.Nil, valid); err == nil {
		t.Fatal("accepted nil destination")
	}
	if _, err := bus.SubscribeAgentUpdates(ctx, uuid.Nil, func(context.Context, AgentUpdate) {}); err == nil {
		t.Fatal("accepted nil subscription agent")
	}
	if _, err := bus.SubscribeAgentUpdates(ctx, destination, nil); err == nil {
		t.Fatal("accepted nil handler")
	}
	received := make(chan AgentUpdate, 8)
	sub, err := bus.SubscribeAgentUpdates(ctx, destination, func(_ context.Context, update AgentUpdate) {
		received <- update
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	for _, payload := range []string{
		`{"change":`, `{}`, `{"tool_call_update":{}}`,
		`{"tool_call_update":{},"change":{}}`,
		`{"change":{"agent_id":"` + owner.String() + `","changes":["unknown"]}}`,
		`{"tool_call_id":"` + uuid.New().String() + `","state":"ready"}`,
	} {
		if err := client.Publish(ctx, agentUpdateChannel(destination), []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	// The valid frame is a barrier on the same ordered channel: anything earlier
	// reaching the handler is a malformed payload that should have been dropped.
	if err := bus.PublishAgentUpdate(ctx, destination, valid); err != nil {
		t.Fatal(err)
	}
	assertAgentUpdateReceived(t, ctx, received, valid)
}

func TestRedisRoutedAgentUpdatesFilterAncestorTraffic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bus, err := NewRedisBus(integrationredis.OpenClient(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	owner, parent, grandparent, unrelated := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	received := map[uuid.UUID]chan AgentUpdate{}
	for _, id := range []uuid.UUID{owner, parent, grandparent, unrelated} {
		ch := make(chan AgentUpdate, 12)
		received[id] = ch
		sub, err := bus.SubscribeAgentUpdates(ctx, id, func(_ context.Context, update AgentUpdate) { ch <- update })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sub.Unsubscribe() })
	}
	lookups := 0
	p := &RoutedPublisher{
		agentUpdatePublisher: bus,
		agentAncestry: agentAncestryReaderFunc(func(_ context.Context, projectID, agentID uuid.UUID) ([]uuid.UUID, error) {
			lookups++
			if projectID != testProjectID || agentID != owner {
				t.Fatalf("wrong lookup %s/%s", projectID, agentID)
			}
			return []uuid.UUID{parent, grandparent}, nil
		}),
	}
	var expected []AgentUpdate
	for _, toolType := range []string{"built_in", "mcp", "custom"} {
		intent := ToolCallUpdatedCommitted{
			ProjectID:  testProjectID,
			AgentID:    owner,
			ToolCallID: uuid.New(),
			ToolType:   toolType,
			State:      "ready",
		}
		p.publishWithTimeout(ctx, intent, time.Second)
		expected = append(expected, wireAgentUpdate(t, AgentUpdate{ToolCallUpdate: &intent}))
	}
	for _, kind := range []AgentChangeKind{AgentChangeAgent, AgentChangeInteractions} {
		intent := AgentChangeCommitted{ProjectID: testProjectID, AgentID: owner, Changes: []AgentChangeKind{kind}}
		p.publishWithTimeout(ctx, intent, time.Second)
		intent.ParentAgentID = &parent
		expected = append(expected, wireAgentUpdate(t, AgentUpdate{Change: &intent}))
	}
	for _, update := range expected {
		assertAgentUpdateReceived(t, ctx, received[owner], update)
	}
	for _, update := range expected[2:] {
		assertAgentUpdateReceived(t, ctx, received[parent], update)
	}
	for _, update := range []AgentUpdate{expected[2], expected[4]} {
		assertAgentUpdateReceived(t, ctx, received[grandparent], update)
	}
	if lookups != 3 {
		t.Fatalf("ancestry reads = %d, want only 3 routable intents", lookups)
	}
	// A direct barrier also checks that an unrelated root received no routed traffic.
	barrier := AgentUpdate{Change: &AgentChangeCommitted{
		AgentID: unrelated,
		Changes: []AgentChangeKind{AgentChangeAgent},
	}}
	if err := bus.PublishAgentUpdate(ctx, unrelated, barrier); err != nil {
		t.Fatal(err)
	}
	assertAgentUpdateReceived(t, ctx, received[unrelated], barrier)
}

func TestRedisRoutedAgentChangeLookupFailureStillDeliversOwner(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "error"
		if timeout {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			bus, err := NewRedisBus(integrationredis.OpenClient(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			received := make(chan AgentUpdate, 1)
			sub, err := bus.SubscribeAgentUpdates(ctx, owner, func(_ context.Context, update AgentUpdate) {
				received <- update
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sub.Unsubscribe() })
			p := &RoutedPublisher{
				agentUpdatePublisher: bus,
				agentAncestry: agentAncestryReaderFunc(func(ctx context.Context, _, _ uuid.UUID) ([]uuid.UUID, error) {
					if timeout {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return nil, errors.New("lookup failed")
				}),
			}
			intent := AgentChangeCommitted{
				ProjectID: testProjectID,
				AgentID:   owner,
				Changes:   []AgentChangeKind{AgentChangeInteractions},
			}
			p.publishWithTimeout(ctx, intent, 200*time.Millisecond)
			assertAgentUpdateReceived(t, ctx, received, wireAgentUpdate(t, AgentUpdate{Change: &intent}))
		})
	}
}

func wireAgentUpdate(t *testing.T, update AgentUpdate) AgentUpdate {
	t.Helper()
	payload, err := json.Marshal(update)
	if err != nil {
		t.Fatal(err)
	}
	var decoded AgentUpdate
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func assertAgentUpdateReceived(t *testing.T, ctx context.Context, received <-chan AgentUpdate, want AgentUpdate) {
	t.Helper()
	select {
	case got := <-received:
		if !reflect.DeepEqual(got, want) {
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			t.Fatalf("agent update = %s, want %s", gotJSON, wantJSON)
		}
	case <-ctx.Done():
		t.Fatalf("waiting for agent update: %v", ctx.Err())
	}
}

func TestRedisAgentUpdateSubscribersSurviveFirstCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bus, err := NewRedisBus(integrationredis.OpenClient(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	agentID := uuid.New()
	firstCtx, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	first, err := bus.SubscribeAgentUpdates(firstCtx, agentID, func(context.Context, AgentUpdate) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Unsubscribe() })
	received := make(chan AgentUpdate, 1)
	second, err := bus.SubscribeAgentUpdates(ctx, agentID, func(_ context.Context, update AgentUpdate) {
		received <- update
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Unsubscribe() })
	cancelFirst()
	select {
	case <-testutil.RequireType[*fanoutSubscription[AgentUpdate]](t, first).Done():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	update := AgentUpdate{Change: &AgentChangeCommitted{
		AgentID: agentID,
		Changes: []AgentChangeKind{AgentChangeAgent},
	}}
	if err := bus.PublishAgentUpdate(ctx, agentID, update); err != nil {
		t.Fatal(err)
	}
	assertAgentUpdateReceived(t, ctx, received, update)
}
