package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

// DaemonInboxMessage is the Redis-relayed envelope for one daemon socket message.
type DaemonInboxMessage struct {
	MachineID    uuid.UUID                  `json:"machine_id"`
	Kind         daemonprotocol.MessageType `json:"kind"`
	Payload      json.RawMessage            `json:"payload"`
	ReplyChannel string                     `json:"reply_channel,omitempty"`
}

var ErrDaemonOffline = errors.New("daemon is offline")

type inboxPublisher interface {
	PublishDaemonReplicaInbox(ctx context.Context, replicaID uuid.UUID, msg DaemonInboxMessage) error
}

// PublishOptions tweaks a single Publish call.
type PublishOptions struct {
	ReplyChannel string
}

// DaemonInbox routes inbox messages to the API replica owning daemon presence.
type DaemonInbox struct {
	bus      inboxPublisher
	presence DaemonPresenceStore
}

func NewDaemonInbox(bus inboxPublisher, presence DaemonPresenceStore) *DaemonInbox {
	if bus == nil || presence == nil {
		return nil
	}
	return &DaemonInbox{bus: bus, presence: presence}
}

// Publish sends an inbox message to the API replica that owns machineID.
func (d *DaemonInbox) Publish(
	ctx context.Context,
	machineID uuid.UUID,
	kind daemonprotocol.MessageType,
	payload json.RawMessage,
	opts PublishOptions,
) error {
	if d == nil || d.bus == nil || d.presence == nil {
		return errors.New("daemon inbox is not configured")
	}
	if machineID == uuid.Nil {
		return errors.New("daemon inbox machine id is required")
	}
	if kind == "" {
		return errors.New("daemon inbox kind is required")
	}
	presence, ok, err := d.presence.Get(ctx, machineID)
	if err != nil {
		return fmt.Errorf("lookup daemon presence: %w", err)
	}
	if !ok {
		return ErrDaemonOffline
	}
	return d.bus.PublishDaemonReplicaInbox(ctx, presence.ReplicaID, DaemonInboxMessage{
		MachineID:    machineID,
		Kind:         kind,
		Payload:      payload,
		ReplyChannel: opts.ReplyChannel,
	})
}

func daemonReplicaInboxChannel(replicaID uuid.UUID) string {
	return "omnara:inbox:api:" + replicaID.String()
}
