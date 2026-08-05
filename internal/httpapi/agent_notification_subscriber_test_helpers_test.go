package httpapi

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/notifications"
)

type noopAgentNotificationSubscriber struct{}

func (noopAgentNotificationSubscriber) SubscribeAgentEventWakeups(
	context.Context,
	uuid.UUID,
	func(context.Context),
) (notifications.Subscription, error) {
	return noopSubscription{}, nil
}

func (noopAgentNotificationSubscriber) SubscribeAgentStreamDeltas(
	context.Context,
	uuid.UUID,
	func(context.Context, json.RawMessage),
) (notifications.Subscription, error) {
	return noopSubscription{}, nil
}

type noopSubscription struct{}

func (noopSubscription) Unsubscribe() error { return nil }

type noopDaemonWakeupSubscriber struct{}

func (noopDaemonWakeupSubscriber) SubscribeDaemonReplicaWakeups(
	context.Context,
	uuid.UUID,
	func(context.Context, notifications.WakeupMessage),
) (notifications.Subscription, error) {
	return noopSubscription{}, nil
}

func (noopDaemonWakeupSubscriber) SubscribeDaemonReplicaInbox(
	context.Context,
	uuid.UUID,
	func(context.Context, []byte),
) (notifications.Subscription, error) {
	return noopSubscription{}, nil
}

type noopDaemonPresenceStore struct {
	notifications.DaemonPresenceStore
}
