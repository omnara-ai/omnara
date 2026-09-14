package httpapi

import (
	"context"
	"net/http"

	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type subagentStreamIdentity struct {
	name        string
	subagentKey string
}

type subagentStreamSubscriptions struct {
	server        *Server
	projectID     storage.ID
	rootAgentID   storage.ID
	updates       chan notifications.ToolCallUpdatedCommitted
	subscriptions map[storage.ID]notifications.Subscription
	identities    map[storage.ID]subagentStreamIdentity
}

func newSubagentStreamSubscriptions(
	server *Server,
	projectID, rootAgentID storage.ID,
) *subagentStreamSubscriptions {
	return &subagentStreamSubscriptions{
		server:        server,
		projectID:     projectID,
		rootAgentID:   rootAgentID,
		updates:       make(chan notifications.ToolCallUpdatedCommitted, streamFrameChannelSize),
		subscriptions: map[storage.ID]notifications.Subscription{},
		identities:    map[storage.ID]subagentStreamIdentity{},
	}
}

func (s *subagentStreamSubscriptions) refresh(ctx context.Context) error {
	descendants, err := s.server.store.Execution().ListAgentDescendantIDs(ctx, s.projectID, s.rootAgentID)
	if err != nil {
		return err
	}
	current := make(map[storage.ID]struct{}, len(descendants))
	for _, agentID := range descendants {
		current[agentID] = struct{}{}
		if _, ok := s.subscriptions[agentID]; ok {
			continue
		}
		agent, err := s.server.store.Execution().GetAgentInProject(ctx, s.projectID, agentID)
		if err != nil {
			return err
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
		s.identities[agentID] = subagentStreamIdentity{name: agent.Name, subagentKey: agent.SubagentKey}
	}
	for agentID, subscription := range s.subscriptions {
		if _, ok := current[agentID]; ok {
			continue
		}
		_ = subscription.Unsubscribe()
		delete(s.subscriptions, agentID)
		delete(s.identities, agentID)
	}
	return nil
}

func (s *subagentStreamSubscriptions) close() {
	for agentID, subscription := range s.subscriptions {
		_ = subscription.Unsubscribe()
		delete(s.subscriptions, agentID)
	}
}

// writeFrames emits best-effort frames for one subagent tool call update: the
// tool call itself when it is a custom tool, and any question or permission
// interaction attached to it. Missing rows are skipped because the update may
// outrun the read projection or describe a call this stream cannot see.
func (s *subagentStreamSubscriptions) writeFrames(
	ctx context.Context,
	w http.ResponseWriter,
	project identitystore.ProjectRecord,
	update notifications.ToolCallUpdatedCommitted,
) bool {
	identity, ok := s.identities[update.AgentID]
	if !ok {
		return true
	}
	store := s.server.store.Execution()
	toolCall, err := store.GetToolCall(ctx, s.projectID, update.AgentID, update.ToolCallID)
	if err != nil {
		return ctx.Err() == nil
	}
	if toolCall.Type == toolcatalog.ToolTypeCustom {
		response, err := publicToolCallFromRecord(toolCall)
		if err != nil {
			return false
		}
		if !writeSSEJSONFrame(w, "subagent_tool_call", "", response) {
			return false
		}
	}
	kinds := []executionstore.AgentInteractionKind{executionstore.AgentInteractionKindPermission}
	if toolCall.Name == toolcatalog.ToolNameAskQuestion {
		kinds = append(kinds, executionstore.AgentInteractionKindQuestion)
	}
	for _, kind := range kinds {
		interaction, found, err := store.GetAgentInteractionByToolCallKind(
			ctx, s.projectID, update.AgentID, update.ToolCallID, kind,
		)
		if err != nil {
			return ctx.Err() == nil
		}
		if !found {
			continue
		}
		response, err := agentInteractionResponseFromRecord(project.OrgID, interaction)
		if err != nil {
			return false
		}
		response.AgentName = &identity.name
		response.SubagentKey = ptrFromNonEmpty(identity.subagentKey)
		if !writeSSEJSONFrame(w, "subagent_interaction", "", response) {
			return false
		}
	}
	return true
}
