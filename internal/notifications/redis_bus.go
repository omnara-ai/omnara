package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/redistore"
)

type RedisBus struct {
	client *redistore.Client
	log    *slog.Logger

	mu                 sync.Mutex
	agentEventFanouts  map[uuid.UUID]*agentFanout[struct{}]
	streamDeltaFanouts map[uuid.UUID]*agentFanout[json.RawMessage]
	toolCallFanouts    map[uuid.UUID]*agentFanout[ToolCallUpdatedCommitted]
}

func NewRedisBus(client *redistore.Client, log *slog.Logger) (*RedisBus, error) {
	if client == nil {
		return nil, errors.New("redis coordination client is required")
	}
	return &RedisBus{client: client, log: log}, nil
}

func (b *RedisBus) PublishDaemonReplicaWakeup(ctx context.Context, replicaID uuid.UUID, msg WakeupMessage) error {
	if b == nil || b.client == nil {
		return errors.New("redis bus is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if replicaID == uuid.Nil {
		return errors.New("replica id is required")
	}
	if err := msg.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	channel := daemonReplicaWakeupChannel(replicaID)
	return b.client.Publish(ctx, channel, payload)
}

func (b *RedisBus) SubscribeDaemonReplicaWakeups(
	ctx context.Context,
	replicaID uuid.UUID,
	handler func(context.Context, WakeupMessage),
) (Subscription, error) {
	if b == nil || b.client == nil {
		return nil, errors.New("redis bus is closed")
	}
	if replicaID == uuid.Nil {
		return nil, errors.New("replica id is required")
	}
	if handler == nil {
		return nil, errors.New("daemon wakeup handler is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	channel := daemonReplicaWakeupChannel(replicaID)
	return b.client.Subscribe(ctx, channel, b.log, func(ctx context.Context, _ string, payload []byte) {
		var msg WakeupMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			if b.log != nil {
				b.log.Warn("decode daemon wakeup failed", "replica_id", replicaID, "error", err)
			}
			return
		}
		if err := msg.Validate(); err != nil {
			if b.log != nil {
				b.log.Warn("drop invalid daemon wakeup", "replica_id", replicaID, "error", err)
			}
			return
		}
		handler(ctx, msg)
	})
}

func (b *RedisBus) PublishDaemonReplicaInbox(ctx context.Context, replicaID uuid.UUID, msg DaemonInboxMessage) error {
	if b == nil || b.client == nil {
		return errors.New("redis bus is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if replicaID == uuid.Nil {
		return errors.New("replica id is required")
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return b.client.Publish(ctx, daemonReplicaInboxChannel(replicaID), payload)
}

func (b *RedisBus) SubscribeDaemonReplicaInbox(
	ctx context.Context,
	replicaID uuid.UUID,
	handler func(context.Context, []byte),
) (Subscription, error) {
	if b == nil || b.client == nil {
		return nil, errors.New("redis bus is closed")
	}
	if replicaID == uuid.Nil {
		return nil, errors.New("replica id is required")
	}
	if handler == nil {
		return nil, errors.New("daemon inbox handler is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	channel := daemonReplicaInboxChannel(replicaID)
	return b.client.Subscribe(ctx, channel, b.log, func(ctx context.Context, _ string, payload []byte) {
		handler(ctx, payload)
	})
}

// SubscribeChannel subscribes to an arbitrary Redis pub/sub channel.
func (b *RedisBus) SubscribeChannel(
	ctx context.Context,
	channel string,
	handler func(context.Context, []byte),
) (Subscription, error) {
	if b == nil || b.client == nil {
		return nil, errors.New("redis bus is closed")
	}
	if channel == "" {
		return nil, errors.New("subscribe channel is required")
	}
	return b.client.Subscribe(ctx, channel, b.log, func(ctx context.Context, _ string, payload []byte) {
		handler(ctx, payload)
	})
}

// PublishChannel publishes a raw payload to an arbitrary Redis pub/sub channel.
func (b *RedisBus) PublishChannel(ctx context.Context, channel string, payload []byte) error {
	if b == nil || b.client == nil {
		return errors.New("redis bus is closed")
	}
	if channel == "" {
		return errors.New("publish channel is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.client.Publish(ctx, channel, payload)
}

func (b *RedisBus) SubscribeIntegrationDeliveryUpdates(
	ctx context.Context,
	notifyRef uuid.UUID,
	handler func(context.Context),
) (Subscription, error) {
	if notifyRef == uuid.Nil {
		return nil, errors.New("integration delivery notification ref is required")
	}
	if handler == nil {
		return nil, errors.New("integration delivery handler is required")
	}
	return b.SubscribeChannel(ctx, integrationDeliveryChannel(notifyRef), func(ctx context.Context, _ []byte) {
		handler(ctx)
	})
}

func (b *RedisBus) PublishIntegrationDeliveryUpdate(ctx context.Context, notifyRef uuid.UUID) error {
	if notifyRef == uuid.Nil {
		return errors.New("integration delivery notification ref is required")
	}
	return b.PublishChannel(ctx, integrationDeliveryChannel(notifyRef), []byte{1})
}

func (b *RedisBus) PublishAgentEventWakeup(ctx context.Context, agentID uuid.UUID) error {
	if b == nil || b.client == nil {
		return errors.New("redis bus is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if agentID == uuid.Nil {
		return errors.New("agent id is required")
	}
	channel := agentEventWakeupChannel(agentID)
	return b.client.Publish(ctx, channel, []byte{1})
}

func (b *RedisBus) PublishAgentToolCallUpdate(
	ctx context.Context,
	update ToolCallUpdatedCommitted,
) error {
	if b == nil || b.client == nil {
		return errors.New("redis bus is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if update.AgentID == uuid.Nil || update.ToolCallID == uuid.Nil || update.State == "" {
		return errors.New("agent, tool call, and state are required")
	}
	payload, err := json.Marshal(update)
	if err != nil {
		return err
	}
	return b.client.Publish(ctx, agentToolCallUpdateChannel(update.AgentID), payload)
}

func (b *RedisBus) PublishAgentStreamDelta(
	ctx context.Context,
	agentID uuid.UUID,
	streamPayload json.RawMessage,
) error {
	if b == nil || b.client == nil {
		return errors.New("redis bus is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if agentID == uuid.Nil {
		return errors.New("agent id is required")
	}
	if len(streamPayload) == 0 {
		return errors.New("stream delta payload is required")
	}
	channel := agentStreamDeltaChannel(agentID)
	return b.client.Publish(ctx, channel, streamPayload)
}

func (b *RedisBus) PublishWorkerControl(ctx context.Context, workerProcessID uuid.UUID, msg WorkerControl) error {
	if b == nil || b.client == nil {
		return errors.New("redis bus is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if workerProcessID == uuid.Nil {
		return errors.New("worker process id is required")
	}
	if err := msg.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	channel := workerControlChannel(workerProcessID)
	return b.client.Publish(ctx, channel, payload)
}

func (b *RedisBus) SubscribeWorkerControl(
	ctx context.Context,
	workerProcessID uuid.UUID,
	handler func(context.Context, WorkerControl),
) (Subscription, error) {
	if b == nil || b.client == nil {
		return nil, errors.New("redis bus is closed")
	}
	if workerProcessID == uuid.Nil {
		return nil, errors.New("worker process id is required")
	}
	if handler == nil {
		return nil, errors.New("worker control handler is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	channel := workerControlChannel(workerProcessID)
	return b.client.Subscribe(ctx, channel, b.log, func(ctx context.Context, _ string, payload []byte) {
		var msg WorkerControl
		if err := json.Unmarshal(payload, &msg); err != nil {
			if b.log != nil {
				b.log.Warn("decode worker control failed", "worker_process_id", workerProcessID, "error", err)
			}
			return
		}
		if err := msg.Validate(); err != nil {
			if b.log != nil {
				b.log.Warn("drop invalid worker control", "worker_process_id", workerProcessID, "error", err)
			}
			return
		}
		handler(ctx, msg)
	})
}

func (b *RedisBus) SubscribeAgentEventWakeups(
	ctx context.Context,
	agentID uuid.UUID,
	handler func(context.Context),
) (Subscription, error) {
	if b == nil || b.client == nil {
		return nil, errors.New("redis bus is closed")
	}
	if agentID == uuid.Nil {
		return nil, errors.New("agent id is required")
	}
	if handler == nil {
		return nil, errors.New("agent event handler is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return subscribeAgentFanout(
		ctx,
		b,
		agentEventWakeupChannel(agentID),
		func(ctx context.Context, _ struct{}) {
			handler(ctx)
		},
		func() (*agentFanout[struct{}], bool) {
			if b.agentEventFanouts == nil {
				b.agentEventFanouts = map[uuid.UUID]*agentFanout[struct{}]{}
			}
			fanout, existed := b.agentEventFanouts[agentID]
			if existed {
				return fanout, false
			}
			fanout = newAgentFanout(
				b,
				func(context.Context, []byte) (struct{}, bool) {
					return struct{}{}, true
				},
				func(f *agentFanout[struct{}]) {
					if b.agentEventFanouts[agentID] == f {
						delete(b.agentEventFanouts, agentID)
					}
				},
			)
			b.agentEventFanouts[agentID] = fanout
			return fanout, true
		},
	)
}

func (b *RedisBus) SubscribeAgentStreamDeltas(
	ctx context.Context,
	agentID uuid.UUID,
	handler func(context.Context, json.RawMessage),
) (Subscription, error) {
	if b == nil || b.client == nil {
		return nil, errors.New("redis bus is closed")
	}
	if agentID == uuid.Nil {
		return nil, errors.New("agent id is required")
	}
	if handler == nil {
		return nil, errors.New("agent stream handler is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return subscribeAgentFanout(
		ctx,
		b,
		agentStreamDeltaChannel(agentID),
		handler,
		func() (*agentFanout[json.RawMessage], bool) {
			if b.streamDeltaFanouts == nil {
				b.streamDeltaFanouts = map[uuid.UUID]*agentFanout[json.RawMessage]{}
			}
			fanout, existed := b.streamDeltaFanouts[agentID]
			if existed {
				return fanout, false
			}
			fanout = newAgentFanout(
				b,
				func(_ context.Context, payload []byte) (json.RawMessage, bool) {
					return json.RawMessage(payload), true
				},
				func(f *agentFanout[json.RawMessage]) {
					if b.streamDeltaFanouts[agentID] == f {
						delete(b.streamDeltaFanouts, agentID)
					}
				},
			)
			b.streamDeltaFanouts[agentID] = fanout
			return fanout, true
		},
	)
}

func (b *RedisBus) SubscribeAgentToolCallUpdates(
	ctx context.Context,
	agentID uuid.UUID,
	handler func(context.Context, ToolCallUpdatedCommitted),
) (Subscription, error) {
	if b == nil || b.client == nil {
		return nil, errors.New("redis bus is closed")
	}
	if agentID == uuid.Nil {
		return nil, errors.New("agent id is required")
	}
	if handler == nil {
		return nil, errors.New("tool call update handler is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return subscribeAgentFanout(
		ctx,
		b,
		agentToolCallUpdateChannel(agentID),
		handler,
		func() (*agentFanout[ToolCallUpdatedCommitted], bool) {
			if b.toolCallFanouts == nil {
				b.toolCallFanouts = map[uuid.UUID]*agentFanout[ToolCallUpdatedCommitted]{}
			}
			fanout, existed := b.toolCallFanouts[agentID]
			if existed {
				return fanout, false
			}
			fanout = newAgentFanout(
				b,
				func(_ context.Context, payload []byte) (ToolCallUpdatedCommitted, bool) {
					var update ToolCallUpdatedCommitted
					if err := json.Unmarshal(payload, &update); err != nil ||
						update.ToolCallID == uuid.Nil || update.State == "" {
						return ToolCallUpdatedCommitted{}, false
					}
					update.AgentID = agentID
					return update, true
				},
				func(f *agentFanout[ToolCallUpdatedCommitted]) {
					if b.toolCallFanouts[agentID] == f {
						delete(b.toolCallFanouts, agentID)
					}
				},
			)
			b.toolCallFanouts[agentID] = fanout
			return fanout, true
		},
	)
}

func subscribeAgentFanout[T any](
	ctx context.Context,
	b *RedisBus,
	channel string,
	handler func(context.Context, T),
	getOrCreate func() (*agentFanout[T], bool),
) (Subscription, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		b.mu.Lock()
		fanout, weOwnInit := getOrCreate()
		b.mu.Unlock()

		if weOwnInit {
			subCtx, cancelSub := context.WithCancel(context.Background())
			//nolint:contextcheck // detached ctx is intentional: the shared subscription outlives any single subscriber's ctx
			redisSub, err := b.client.Subscribe(subCtx, channel, b.log, func(ctx context.Context, _ string, payload []byte) {
				fanout.dispatch(ctx, payload)
			})
			if err != nil {
				cancelSub()
				fanout.initErr = err
				close(fanout.ready)
				b.mu.Lock()
				if fanout.remove != nil {
					fanout.remove(fanout)
				}
				b.mu.Unlock()
				fanout.markRemoved()
				return nil, err
			}
			fanout.redisSub = redisSub
			fanout.cancelSub = cancelSub
			close(fanout.ready)
		} else {
			select {
			case <-fanout.ready:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if fanout.initErr != nil {
				return nil, fanout.initErr
			}
		}

		if sub, ok := fanout.subscribe(ctx, handler); ok {
			if err := ctx.Err(); err != nil {
				_ = sub.Unsubscribe()
				return nil, err
			}
			return sub, nil
		}
		if err := fanout.waitRemoved(ctx); err != nil {
			return nil, err
		}
	}
}

// Daemon-work, runtime-ended, agent-event, and worker-cancel messages are
// recoverable from durable state. Process-termination and stream-delta
// messages are ephemeral and not replayed.
func daemonReplicaWakeupChannel(replicaID uuid.UUID) string {
	return "omnara:wakeup:api:" + replicaID.String()
}

func agentEventWakeupChannel(agentID uuid.UUID) string {
	return "omnara:agent_event_wakeups:" + agentID.String()
}

func agentStreamDeltaChannel(agentID uuid.UUID) string {
	return "omnara:agent_stream_deltas:" + agentID.String()
}

func agentToolCallUpdateChannel(agentID uuid.UUID) string {
	return "omnara:agent_tool_call_updates:" + agentID.String()
}

func workerControlChannel(workerProcessID uuid.UUID) string {
	return "omnara:worker_control:" + workerProcessID.String()
}

func integrationDeliveryChannel(notifyRef uuid.UUID) string {
	return "omnara:integration_delivery_updates:" + notifyRef.String()
}
