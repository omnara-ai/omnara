package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/golang-lru/v2/simplelru"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

var testProjectID = uuid.MustParse("20202020-2020-2020-2020-202020202020")

type agentAncestryReaderFunc func(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error)

func (f agentAncestryReaderFunc) ListAgentAncestors(
	ctx context.Context,
	projectID, agentID uuid.UUID,
) ([]uuid.UUID, error) {
	return f(ctx, projectID, agentID)
}

type routedAgentUpdate struct {
	destinationID uuid.UUID
	update        AgentUpdate
}

func (b *fakeBus) updateSnapshot() []routedAgentUpdate {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.updates)
}

func TestAgentUpdateWireContract(t *testing.T) {
	agentID, parentID, toolID := uuid.New(), uuid.New(), uuid.New()
	for _, tc := range []struct {
		name   string
		update AgentUpdate
		want   map[string]any
	}{
		{"tool", AgentUpdate{ToolCallUpdate: &ToolCallUpdatedCommitted{
			ProjectID: testProjectID, AgentID: agentID, ToolCallID: toolID, ToolType: "custom", State: "ready",
		}}, map[string]any{"tool_call_update": map[string]any{
			"agent_id": agentID.String(), "tool_call_id": toolID.String(), "state": "ready",
		}}},
		{"root", AgentUpdate{Change: &AgentChangeCommitted{
			ProjectID: testProjectID, AgentID: agentID, Changes: []AgentChangeKind{AgentChangeAgent},
		}}, map[string]any{"change": map[string]any{
			"agent_id": agentID.String(), "parent_agent_id": nil, "changes": []any{"agent"},
		}}},
		{"child", AgentUpdate{Change: &AgentChangeCommitted{
			ProjectID: testProjectID, AgentID: agentID, ParentAgentID: &parentID,
			Changes: []AgentChangeKind{AgentChangeAgent, AgentChangeInteractions},
		}}, map[string]any{"change": map[string]any{
			"agent_id": agentID.String(), "parent_agent_id": parentID.String(),
			"changes": []any{"agent", "interactions"},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(tc.update)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(payload, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("payload = %s, want %+v", payload, tc.want)
			}
			var decoded AgentUpdate
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Fatal(err)
			}
			if err := decoded.Validate(); err != nil {
				t.Fatalf("wire payload invalid: %v", err)
			}
		})
	}
}

func TestAgentUpdateRejectsInvalidPayload(t *testing.T) {
	agentID := uuid.New()
	for _, update := range []AgentUpdate{
		{},
		{ToolCallUpdate: &ToolCallUpdatedCommitted{}, Change: &AgentChangeCommitted{}},
		{ToolCallUpdate: &ToolCallUpdatedCommitted{}},
		{Change: &AgentChangeCommitted{AgentID: agentID}},
		{Change: &AgentChangeCommitted{AgentID: agentID, Changes: []AgentChangeKind{"unknown"}}},
		{Change: &AgentChangeCommitted{
			AgentID:       agentID,
			ParentAgentID: &agentID,
			Changes:       []AgentChangeKind{AgentChangeAgent},
		}},
	} {
		if update.Validate() == nil {
			t.Fatalf("accepted invalid update %+v", update)
		}
	}
}

func TestTxNotificationsCoalescesChangesByProjectAndOwner(t *testing.T) {
	projectA, projectB, agentA, agentB := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tx := NewTxNotifications()
	tx.AddAgentChange(projectA, agentA, AgentChangeInteractions)
	tx.AddAgentChange(projectA, agentA, AgentChangeAgent)
	tx.AddAgentChange(projectA, agentA, AgentChangeInteractions)
	tx.AddAgentChange(projectB, agentA, AgentChangeAgent)
	tx.AddAgentChange(projectA, agentB, AgentChangeInteractions)
	tx.AddAgentChange(uuid.Nil, agentA, AgentChangeAgent)
	tx.AddAgentChange(projectA, uuid.Nil, AgentChangeAgent)
	tx.AddAgentChange(projectA, agentA, "unknown")
	var nilTx *TxNotifications
	nilTx.AddAgentChange(projectA, agentA, AgentChangeAgent)
	publisher := &capturingPublisher{}
	tx.Flush(context.Background(), publisher)
	got := map[agentNotificationKey][]AgentChangeKind{}
	for _, intent := range publisher.intents {
		change := testutil.RequireType[AgentChangeCommitted](t, intent)
		if change.ParentAgentID != nil {
			t.Fatal("transaction populated routing metadata")
		}
		got[agentNotificationKey{change.ProjectID, change.AgentID}] = change.Changes
	}
	want := map[agentNotificationKey][]AgentChangeKind{
		{projectA, agentA}: {AgentChangeAgent, AgentChangeInteractions},
		{projectB, agentA}: {AgentChangeAgent},
		{projectA, agentB}: {AgentChangeInteractions},
	}
	if len(publisher.intents) != 3 || !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %+v, want %+v", got, want)
	}
}

func TestRoutedPublisherAgentDestinations(t *testing.T) {
	owner, parent, grandparent := uuid.New(), uuid.New(), uuid.New()
	for _, tc := range []struct {
		name     string
		toolType string
		changes  []AgentChangeKind
		want     []uuid.UUID
		lookup   bool
	}{
		{"builtin", toolcatalog.ToolTypeBuiltIn, nil, []uuid.UUID{owner}, false},
		{"mcp", toolcatalog.ToolTypeMCP, nil, []uuid.UUID{owner}, false},
		{"custom", toolcatalog.ToolTypeCustom, nil, []uuid.UUID{owner, parent, grandparent}, true},
		{"agent", "", []AgentChangeKind{AgentChangeAgent}, []uuid.UUID{owner, parent}, true},
		{"interactions", "", []AgentChangeKind{AgentChangeInteractions}, []uuid.UUID{owner, parent, grandparent}, true},
		{"both", "", []AgentChangeKind{AgentChangeAgent, AgentChangeInteractions},
			[]uuid.UUID{owner, parent, grandparent}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := &fakeBus{}
			lookups := 0
			p := &RoutedPublisher{
				agentUpdatePublisher: bus,
				agentAncestry: agentAncestryReaderFunc(func(
					ctx context.Context, projectID, agentID uuid.UUID,
				) ([]uuid.UUID, error) {
					lookups++
					if projectID != testProjectID || agentID != owner {
						t.Fatalf("lookup scope = %s/%s", projectID, agentID)
					}
					if _, ok := ctx.Deadline(); !ok {
						t.Fatal("lookup lacks deadline")
					}
					return []uuid.UUID{parent, grandparent}, nil
				}),
			}
			var intent PostCommitIntent
			if tc.toolType != "" {
				intent = ToolCallUpdatedCommitted{
					ProjectID:  testProjectID,
					AgentID:    owner,
					ToolCallID: uuid.New(),
					ToolType:   tc.toolType,
					State:      "ready",
				}
			} else {
				intent = AgentChangeCommitted{ProjectID: testProjectID, AgentID: owner, Changes: tc.changes}
			}
			p.publishWithTimeout(context.Background(), intent, time.Second)
			var destinations []uuid.UUID
			for _, routed := range bus.updateSnapshot() {
				destinations = append(destinations, routed.destinationID)
				if change := routed.update.Change; change != nil {
					wantChanges := tc.changes
					if routed.destinationID == grandparent {
						wantChanges = []AgentChangeKind{AgentChangeInteractions}
					}
					if change.AgentID != owner || change.ParentAgentID == nil || *change.ParentAgentID != parent ||
						!slices.Equal(change.Changes, wantChanges) {
						t.Fatalf("routed change lost source identity: %+v", change)
					}
				} else if *routed.update.ToolCallUpdate != testutil.RequireType[ToolCallUpdatedCommitted](t, intent) {
					t.Fatalf("routed tool lost original payload: %+v", routed.update)
				}
			}
			if !slices.Equal(destinations, tc.want) || (lookups == 1) != tc.lookup {
				t.Fatalf("destinations = %v, lookups = %d; want %v, lookup %v", destinations, lookups, tc.want, tc.lookup)
			}
		})
	}
}

func TestRoutedPublisherLookupFailurePreservesOwner(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		for _, tool := range []bool{false, true} {
			t.Run(fmt.Sprintf("timeout=%t/tool=%t", timeout, tool), func(t *testing.T) {
				owner := uuid.New()
				bus := &fakeBus{}
				recorder := &recordingRecorder{}
				var logs bytes.Buffer
				p := &RoutedPublisher{agentUpdatePublisher: bus, recorder: recorder, log: slog.New(slog.NewTextHandler(&logs, nil))}
				p.agentAncestry = agentAncestryReaderFunc(func(ctx context.Context, _, _ uuid.UUID) ([]uuid.UUID, error) {
					if timeout {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return []uuid.UUID{uuid.New()}, errors.New("lookup failed")
				})
				var intent PostCommitIntent = AgentChangeCommitted{
					ProjectID: testProjectID,
					AgentID:   owner,
					Changes:   []AgentChangeKind{AgentChangeInteractions},
				}
				if tool {
					intent = ToolCallUpdatedCommitted{
						ProjectID:  testProjectID,
						AgentID:    owner,
						ToolCallID: uuid.New(),
						ToolType:   "custom",
						State:      "ready",
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				p.publishWithTimeout(ctx, intent, time.Second)
				if ctx.Err() != nil {
					t.Fatalf("lookup exhausted owner publish budget: %v", ctx.Err())
				}
				got := bus.updateSnapshot()
				if len(got) != 1 || got[0].destinationID != owner {
					t.Fatalf("failure destinations = %+v", got)
				}
				if change := got[0].update.Change; change != nil && change.ParentAgentID != nil {
					t.Fatal("failed lookup leaked partial ancestry")
				}
				if !bytes.Contains(logs.Bytes(), []byte("resolve agent notification ancestry failed")) {
					t.Fatalf("missing routing failure log: %s", &logs)
				}
				want := recordingEntry{Intent: notificationIntentLabel(intent), Result: "error", Reason: "routing_failed"}
				if !slices.Contains(recorder.snapshot(), want) {
					t.Fatalf("missing routing failure metric: %+v", recorder.snapshot())
				}
			})
		}
	}
}

func TestRoutedPublisherCoalescesAgentChangesAndCachesAncestryPerBatch(t *testing.T) {
	owner, parent, grandparent, otherProject := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	bus := &fakeBus{}
	lookups := map[agentNotificationKey]int{}
	p := &RoutedPublisher{
		agentUpdatePublisher: bus,
		queue:                make(chan PostCommitIntent, 8),
		agentAncestry: agentAncestryReaderFunc(func(_ context.Context, projectID, agentID uuid.UUID) ([]uuid.UUID, error) {
			lookups[agentNotificationKey{projectID, agentID}]++
			if projectID == otherProject {
				return nil, nil
			}
			return []uuid.UUID{parent, grandparent}, nil
		}),
	}
	first := AgentChangeCommitted{
		ProjectID: testProjectID,
		AgentID:   owner,
		Changes:   []AgentChangeKind{AgentChangeAgent},
	}
	p.queue <- AgentChangeCommitted{
		ProjectID: testProjectID,
		AgentID:   owner,
		Changes:   []AgentChangeKind{AgentChangeInteractions},
	}
	p.queue <- first
	p.queue <- ToolCallUpdatedCommitted{
		ProjectID:  testProjectID,
		AgentID:    owner,
		ToolCallID: uuid.New(),
		ToolType:   "custom",
		State:      "ready",
	}
	p.queue <- AgentChangeCommitted{
		ProjectID: otherProject,
		AgentID:   owner,
		Changes:   []AgentChangeKind{AgentChangeAgent},
	}
	p.publishCoalesced(context.Background(), first)
	if lookups[agentNotificationKey{testProjectID, owner}] != 1 ||
		lookups[agentNotificationKey{otherProject, owner}] != 1 {
		t.Fatalf("lookup counts = %+v", lookups)
	}
	got := bus.updateSnapshot()
	if len(got) != 7 {
		t.Fatalf("published %d updates, want 3 changes + 3 custom tools + 1 other-project root", len(got))
	}
	for _, routed := range got[:2] {
		if !slices.Equal(routed.update.Change.Changes, []AgentChangeKind{AgentChangeAgent, AgentChangeInteractions}) {
			t.Fatalf("changes not coalesced: %+v", routed)
		}
	}
	change := got[2].update.Change
	if got[2].destinationID != grandparent || !slices.Equal(change.Changes, []AgentChangeKind{AgentChangeInteractions}) {
		t.Fatalf("grandparent received agent flag: %+v", change)
	}
	p.publishCoalesced(context.Background(), first)
	if lookups[agentNotificationKey{testProjectID, owner}] != 2 {
		t.Fatalf("cache survived batch: %+v", lookups)
	}
}

func TestRoutedPublisherCachesImmutableAncestryAcrossBatches(t *testing.T) {
	for _, root := range []bool{false, true} {
		t.Run(fmt.Sprintf("root=%t", root), func(t *testing.T) {
			cache, err := simplelru.NewLRU[agentNotificationKey, []uuid.UUID](2, nil)
			if err != nil {
				t.Fatal(err)
			}
			owner, parent := uuid.New(), uuid.New()
			calls := 0
			fail := false
			p := &RoutedPublisher{
				agentUpdatePublisher: &fakeBus{}, ancestryCache: cache,
				queue: make(chan PostCommitIntent, 1),
				agentAncestry: agentAncestryReaderFunc(func(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
					calls++
					if fail {
						return nil, errors.New("temporary read failure")
					}
					if root {
						return nil, nil
					}
					return []uuid.UUID{parent}, nil
				}),
			}
			change := AgentChangeCommitted{
				ProjectID: testProjectID, AgentID: owner, Changes: []AgentChangeKind{AgentChangeInteractions},
			}
			p.publishCoalesced(context.Background(), change)
			p.publishCoalesced(context.Background(), change)
			if calls != 1 {
				t.Fatalf("repeated owner lookup count = %d, want 1", calls)
			}
			otherProject := change
			otherProject.ProjectID = uuid.New()
			p.publishCoalesced(context.Background(), otherProject)
			if calls != 2 {
				t.Fatalf("project-scoped lookup count = %d, want 2", calls)
			}
			otherOwner := change
			otherOwner.AgentID = uuid.New()
			p.publishCoalesced(context.Background(), otherOwner)
			if cache.Len() != 2 {
				t.Fatalf("cache size = %d, want 2", cache.Len())
			}
			p.publishCoalesced(context.Background(), change)
			if calls != 4 {
				t.Fatalf("evicted owner lookup count = %d, want 4", calls)
			}

			cache.Purge()
			fail = true
			p.queue <- change
			p.publishCoalesced(context.Background(), change)
			if cache.Len() != 0 {
				t.Fatal("failed lookup survived its batch")
			}
			fail = false
			p.publishCoalesced(context.Background(), change)
			if calls != 6 {
				t.Fatalf("lookup did not recover after failed batch: count=%d", calls)
			}
		})
	}
}
