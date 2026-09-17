package storagetest

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func DeleteAgentWakeup(
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID, agentID uuid.UUID,
) error {
	_, err := pool.Exec(ctx, `
DELETE FROM agent_wakeups wake
USING agents agent
WHERE agent.project_id = $1
  AND agent.id = $2
  AND wake.agent_id = agent.id
`, projectID, agentID)
	return err
}
