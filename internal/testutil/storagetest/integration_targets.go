package storagetest

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func SeedAgentIntegrationTarget(
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID, agentID, integrationTargetID uuid.UUID,
) error {
	var target any
	if integrationTargetID != uuid.Nil {
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
      JOIN integration_target_bindings binding
        ON binding.project_id = target.project_id
       AND binding.integration_target_id = target.id
       AND binding.agent_id = agents.id
       AND binding.revoked_at IS NULL
      JOIN integration_installs install
        ON install.project_id = target.project_id
       AND install.id = target.integration_install_id
       AND install.state = 'active'
       AND install.deleted_at IS NULL
      WHERE target.project_id = agents.project_id
        AND target.id = $3::uuid
        AND target.deleted_at IS NULL
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
