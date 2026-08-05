package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type stubPresenceStore struct {
	mu      sync.Mutex
	records map[uuid.UUID]DaemonPresence
}

func (s *stubPresenceStore) PutIfRuntime(context.Context, uuid.UUID, DaemonPresence, time.Duration) error {
	return errors.New("not implemented")
}
func (s *stubPresenceStore) PutIfMissing(context.Context, uuid.UUID, DaemonPresence, time.Duration) error {
	return errors.New("not implemented")
}
func (s *stubPresenceStore) Refresh(context.Context, uuid.UUID, PresenceOwner, time.Duration) error {
	return errors.New("not implemented")
}
func (s *stubPresenceStore) Get(_ context.Context, machineID uuid.UUID) (DaemonPresence, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.records[machineID]
	return p, ok, nil
}
func (s *stubPresenceStore) PutRuntime(context.Context, uuid.UUID, DaemonPresence, time.Duration) error {
	return errors.New("not implemented")
}
func (s *stubPresenceStore) PutRuntimeIfMissing(context.Context, uuid.UUID, DaemonPresence, time.Duration) error {
	return errors.New("not implemented")
}
func (s *stubPresenceStore) RefreshRuntime(context.Context, uuid.UUID, PresenceOwner, time.Duration) error {
	return errors.New("not implemented")
}
func (s *stubPresenceStore) GetRuntime(context.Context, uuid.UUID) (DaemonPresence, bool, error) {
	return DaemonPresence{}, false, nil
}
func (s *stubPresenceStore) DeleteIfOwned(context.Context, uuid.UUID, PresenceOwner) error {
	return nil
}
func (s *stubPresenceStore) DeleteRuntimeIfOwned(context.Context, uuid.UUID, PresenceOwner) error {
	return nil
}

type stubInboxBus struct {
	mu         sync.Mutex
	published  []DaemonInboxMessage
	replicaID  uuid.UUID
	publishErr error
}

func (b *stubInboxBus) PublishDaemonReplicaInbox(_ context.Context, replicaID uuid.UUID, msg DaemonInboxMessage) error {
	if b.publishErr != nil {
		return b.publishErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.replicaID = replicaID
	b.published = append(b.published, msg)
	return nil
}

func TestDaemonInboxPublishRoutesToOwningReplica(t *testing.T) {
	machineID := uuid.New()
	replicaID := uuid.New()
	presence := &stubPresenceStore{records: map[uuid.UUID]DaemonPresence{
		machineID: {PresenceOwner: PresenceOwner{
			RuntimeID:    uuid.New(),
			ReplicaID:    replicaID,
			ConnectionID: uuid.New(),
		}},
	}}
	bus := &stubInboxBus{}
	inbox := NewDaemonInbox(bus, presence)
	if inbox == nil {
		t.Fatalf("NewDaemonInbox returned nil")
	}
	payload := json.RawMessage(`{"hello":"world"}`)
	if err := inbox.Publish(
		context.Background(),
		machineID,
		"skill_offer",
		payload,
		PublishOptions{ReplyChannel: "omnara:skillreply:abc"},
	); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if bus.replicaID != replicaID {
		t.Fatalf("publish replica = %s, want %s", bus.replicaID, replicaID)
	}
	if got := len(bus.published); got != 1 {
		t.Fatalf("publish count = %d, want 1", got)
	}
	envelope := bus.published[0]
	if envelope.MachineID != machineID {
		t.Fatalf("envelope machineID mismatch")
	}
	if envelope.Kind != "skill_offer" {
		t.Fatalf("envelope kind = %q, want skill_offer", envelope.Kind)
	}
	if string(envelope.Payload) != string(payload) {
		t.Fatalf("envelope payload = %s, want %s", envelope.Payload, payload)
	}
	if envelope.ReplyChannel != "omnara:skillreply:abc" {
		t.Fatalf("envelope reply_channel = %q, want omnara:skillreply:abc", envelope.ReplyChannel)
	}
}

func TestDaemonInboxPublishReturnsErrDaemonOfflineOnMissingPresence(t *testing.T) {
	bus := &stubInboxBus{}
	presence := &stubPresenceStore{records: map[uuid.UUID]DaemonPresence{}}
	inbox := NewDaemonInbox(bus, presence)
	err := inbox.Publish(context.Background(), uuid.New(), "skill_offer", json.RawMessage(`{}`), PublishOptions{})
	if !errors.Is(err, ErrDaemonOffline) {
		t.Fatalf("error = %v, want ErrDaemonOffline", err)
	}
	if len(bus.published) != 0 {
		t.Fatalf("expected no publish on offline daemon, got %d", len(bus.published))
	}
}

func TestDaemonInboxPublishRejectsZeroMachineIDAndEmptyKind(t *testing.T) {
	inbox := NewDaemonInbox(&stubInboxBus{}, &stubPresenceStore{records: map[uuid.UUID]DaemonPresence{}})
	if err := inbox.Publish(context.Background(), uuid.Nil, "skill_offer", json.RawMessage(`{}`), PublishOptions{}); err == nil {
		t.Fatal("zero machineID should error")
	}
	if err := inbox.Publish(context.Background(), uuid.New(), "", json.RawMessage(`{}`), PublishOptions{}); err == nil {
		t.Fatal("empty kind should error")
	}
}
