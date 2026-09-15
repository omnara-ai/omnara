package notifications

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const agentAncestryLookupTimeout = 500 * time.Millisecond

type agentAncestryResult struct {
	ids []uuid.UUID
	err error
}

// Failed lookups are cached only within a batch. Successful immutable ancestry,
// including roots, also lives in the publisher's bounded cache. Only the publisher
// goroutine accesses that cache; deleted projects cannot reuse agent identities.
type agentAncestryCache map[agentNotificationKey]agentAncestryResult

func (p *RoutedPublisher) lookupAgentAncestors(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	cache agentAncestryCache,
	intent PostCommitIntent,
) []uuid.UUID {
	key := agentNotificationKey{projectID: projectID, agentID: agentID}
	result, found := cache[key]
	if !found && p.ancestryCache != nil {
		result.ids, found = p.ancestryCache.Get(key)
	}
	if !found {
		// Reserve time for the owner publish even if the lookup exhausts its
		// budget. The parent context still bounds the whole notification.
		timeout := agentAncestryLookupTimeout
		if deadline, ok := ctx.Deadline(); ok {
			timeout = min(timeout, time.Until(deadline)/2)
		}
		lookupCtx, cancel := context.WithTimeout(ctx, timeout)
		result.ids, result.err = p.agentAncestry.ListAgentAncestors(lookupCtx, projectID, agentID)
		cancel()
		if result.err == nil && p.ancestryCache != nil {
			p.ancestryCache.Add(key, result.ids)
		}
		if cache != nil {
			cache[key] = result
		}
	}
	if result.err != nil {
		p.record(intent, "error", "routing_failed")
		if p.log != nil {
			p.log.Warn("resolve agent notification ancestry failed",
				"project_id", projectID, "agent_id", agentID, "error", result.err)
		}
		return nil
	}
	return result.ids
}

func (p *RoutedPublisher) publishToolCallUpdate(
	ctx context.Context,
	intent ToolCallUpdatedCommitted,
	cache agentAncestryCache,
) {
	update := AgentUpdate{ToolCallUpdate: &intent}
	if intent.ProjectID == uuid.Nil || intent.ToolType == "" || update.Validate() != nil {
		p.record(intent, "skipped", "invalid_intent")
		return
	}
	p.publishAgentUpdate(ctx, intent.AgentID, update, intent)
	if intent.ToolType != toolcatalog.ToolTypeCustom {
		return
	}
	ancestors := p.lookupAgentAncestors(ctx, intent.ProjectID, intent.AgentID, cache, intent)
	for _, destinationID := range ancestors {
		p.publishAgentUpdate(ctx, destinationID, update, intent)
	}
}

func (p *RoutedPublisher) publishAgentChange(
	ctx context.Context,
	intent AgentChangeCommitted,
	cache agentAncestryCache,
) {
	update := AgentUpdate{Change: &intent}
	if intent.ProjectID == uuid.Nil || update.Validate() != nil {
		p.record(intent, "skipped", "invalid_intent")
		return
	}
	ancestors := p.lookupAgentAncestors(ctx, intent.ProjectID, intent.AgentID, cache, intent)
	intent.ParentAgentID = nil
	if len(ancestors) > 0 {
		parentID := ancestors[0]
		intent.ParentAgentID = &parentID
	}
	p.publishAgentUpdate(ctx, intent.AgentID, update, intent)
	if !slices.Contains(intent.Changes, AgentChangeInteractions) && len(ancestors) > 1 {
		ancestors = ancestors[:1]
	}
	for index, destinationID := range ancestors {
		routed := intent
		if index > 0 {
			routed.Changes = []AgentChangeKind{AgentChangeInteractions}
		}
		p.publishAgentUpdate(ctx, destinationID, AgentUpdate{Change: &routed}, intent)
	}
}

func (p *RoutedPublisher) publishAgentUpdate(
	ctx context.Context,
	destinationID uuid.UUID,
	update AgentUpdate,
	intent PostCommitIntent,
) {
	if err := p.agentUpdatePublisher.PublishAgentUpdate(ctx, destinationID, update); err != nil {
		p.record(intent, "error", "publish_failed")
		if p.log != nil {
			p.log.Warn("publish agent update notification failed", "destination_agent_id", destinationID, "error", err)
		}
		return
	}
	p.record(intent, "published", "none")
}
