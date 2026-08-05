package storagetest

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage"
)

func SeedAgentIntegrationTarget(
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID, agentID, integrationTargetID storage.ID,
) error {
	var target any
	if integrationTargetID != storage.NilID {
		target = integrationTargetID
	}
	tag, err := pool.Exec(
		ctx,
		`
UPDATE agents
SET integration_target_id = $3::uuid,
    updated_at = statement_timestamp()
WHERE project_id = $1
  AND id = $2
  AND (
    $3::uuid IS NULL
    OR EXISTS (
      SELECT 1
      FROM integration_targets target
      JOIN integration_installs install
        ON install.project_id = target.project_id
       AND install.id = target.integration_install_id
       AND install.state = 'active'
      WHERE target.project_id = agents.project_id
        AND target.agent_id = agents.id
        AND target.id = $3::uuid
    )
  )`,
		projectID,
		agentID,
		target,
	)
	if err != nil {
		return fmt.Errorf("seed agent integration target: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("agent integration target fixture was not applicable")
	}
	return nil
}
