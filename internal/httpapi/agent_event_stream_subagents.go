package httpapi

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/publicevents"
	"github.com/omnara-ai/omnara/internal/notifications"
)

type toolCallStreamUpdate struct {
	notifications.ToolCallUpdatedCommitted
	agent publicevents.AgentContext
}

type subagentStreamSubscriptions struct {
	orgID         uuid.UUID
	server        *Server
	projectID     uuid.UUID
	streamAgentID uuid.UUID
	updates       chan<- toolCallStreamUpdate
	subscriptions map[uuid.UUID]notifications.Subscription
}

func newSubagentStreamSubscriptions(
	server *Server,
	orgID, projectID, streamAgentID uuid.UUID,
	updates chan<- toolCallStreamUpdate,
) *subagentStreamSubscriptions {
	return &subagentStreamSubscriptions{
		server:        server,
		orgID:         orgID,
		projectID:     projectID,
		streamAgentID: streamAgentID,
		updates:       updates,
		subscriptions: map[uuid.UUID]notifications.Subscription{},
	}
}

func (s *subagentStreamSubscriptions) refresh(ctx context.Context) error {
	descendants, err := s.server.store.Execution().ListAgentDescendants(ctx, s.projectID, s.streamAgentID)
	if err != nil {
		return err
	}
	current := make(map[uuid.UUID]struct{}, len(descendants))
	for _, agent := range descendants {
		agentID := agent.ID
		current[agentID] = struct{}{}
		if _, ok := s.subscriptions[agentID]; ok {
			continue
		}
		subscription, err := s.server.agentToolCallUpdateSubscriber.SubscribeAgentToolCallUpdates(
			ctx,
			agentID,
			func(_ context.Context, update notifications.ToolCallUpdatedCommitted) {
				select {
				case s.updates <- toolCallStreamUpdate{
					ToolCallUpdatedCommitted: update,
					agent: publicevents.AgentContext{
						OrgID: s.orgID, ProjectID: s.projectID,
						AgentID: agentID, AgentProfileID: agent.AgentProfileID,
						ParentAgentID: agent.ParentAgentID, RootAgentID: agent.RootAgentID,
					},
				}:
				default:
					s.server.agentStreamRecorder.RecordDrop("tool_call_update")
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
