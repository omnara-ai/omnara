//go:build integration

package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func bindIntegrationToolTarget(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	agentID uuid.UUID, target integrationstore.IntegrationTargetRecord,
) {
	t.Helper()
	_, err := store.Integrations().CreateIntegrationTargetBinding(ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: target.ProjectID, AgentID: agentID, IntegrationInstallID: target.IntegrationInstallID,
			IntegrationTargetID: target.ID, ReceiveAllowed: true, ReadAllowed: true, SendAllowed: true,
			Source: "tools-fixture",
		})
	require.NoError(t, err)
}

// Fixtures select an already-bound current channel before constructing a model
// turn. Product setter execution is covered separately through its real tool.
func seedIntegrationToolCurrentChannel(
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID, agentID, channelID uuid.UUID) error {
	var channel any
	if channelID != uuid.Nil {
		channel = channelID
	}
	tag, err := pool.Exec(ctx, `UPDATE agents SET integration_target_id = $3::uuid
		WHERE project_id = $1 AND id = $2 AND ($3::uuid IS NULL OR EXISTS (
			SELECT 1 FROM integration_target_bindings b
			JOIN integration_installs i ON i.id = b.integration_install_id AND i.project_id = b.project_id
			WHERE b.project_id = $1 AND b.agent_id = $2 AND b.integration_target_id = $3
			AND b.revoked_at IS NULL AND i.state = 'active'
		))`, projectID, agentID, channel)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("current-channel fixture requires an active binding")
	}
	return nil
}
