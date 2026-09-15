package httpapi

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/notifications"
)

type subagentStreamSubscriptions struct {
	server        *Server
	projectID     uuid.UUID
	rootAgentID   uuid.UUID
	updates       chan<- notifications.ToolCallUpdatedCommitted
	subscriptions map[uuid.UUID]notifications.Subscription
}

func newSubagentStreamSubscriptions(
	server *Server,
	projectID, rootAgentID uuid.UUID,
	updates chan<- notifications.ToolCallUpdatedCommitted,
) *subagentStreamSubscriptions {
	return &subagentStreamSubscriptions{
		server:        server,
		projectID:     projectID,
		rootAgentID:   rootAgentID,
		updates:       updates,
		subscriptions: map[uuid.UUID]notifications.Subscription{},
	}
}

func (s *subagentStreamSubscriptions) refresh(ctx context.Context) error {
	descendants, err := s.server.store.Execution().ListAgentDescendantIDs(ctx, s.projectID, s.rootAgentID)
	if err != nil {
		return err
	}
	current := make(map[uuid.UUID]struct{}, len(descendants))
	for _, agentID := range descendants {
		current[agentID] = struct{}{}
		if _, ok := s.subscriptions[agentID]; ok {
			continue
		}
		subscription, err := s.server.agentToolCallUpdateSubscriber.SubscribeAgentToolCallUpdates(
			ctx,
			agentID,
			func(_ context.Context, update notifications.ToolCallUpdatedCommitted) {
				select {
				case s.updates <- update:
				default:
					if s.server.log != nil {
						s.server.log.Debug(
							"drop subagent tool call update because subscriber buffer is full",
							"agent_id",
							agentID,
						)
					}
				}
			},
		)
		if err != nil {
			return err
		}
		s.subscriptions[agentID] = subscription
	}
	for agentID, subscription := range s.subscriptions {
		if _, ok := current[agentID]; ok {
			continue
		}
		_ = subscription.Unsubscribe()
		delete(s.subscriptions, agentID)
	}
	return nil
}

func (s *subagentStreamSubscriptions) close() {
	for agentID, subscription := range s.subscriptions {
		_ = subscription.Unsubscribe()
		delete(s.subscriptions, agentID)
	}
}
