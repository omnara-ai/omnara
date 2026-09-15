package executionstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

// AgentNotificationReader resolves immutable ancestry outside business
// transactions. It can be wired before stores that depend on the publisher.
type AgentNotificationReader struct {
	q *dbsqlc.Queries
}

var _ notifications.AgentAncestryReader = (*AgentNotificationReader)(nil)

func NewAgentNotificationReader(pool *pgxpool.Pool) *AgentNotificationReader {
	return &AgentNotificationReader{q: dbsqlc.New(pool)}
}

func (r *AgentNotificationReader) ListAgentAncestors(
	ctx context.Context,
	projectID, agentID uuid.UUID,
) ([]uuid.UUID, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil {
		return nil, errors.New("project and agent ids are required")
	}
	return r.q.ListAgentNotificationAncestors(ctx, dbsqlc.ListAgentNotificationAncestorsParams{
		ProjectID: projectID,
		AgentID:   agentID,
		MaxDepth:  int32(agentconfig.MaxSubagentDepth),
	})
}
