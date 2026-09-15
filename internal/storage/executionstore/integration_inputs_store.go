package executionstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
)

// IntegrationRuntimeLeaseProof fences mutations from a leased connector.
// Webhook ingress leaves it nil.
type IntegrationRuntimeLeaseProof = integrationstore.IntegrationRuntimeLeaseProof

func lockIntegrationInputAgentTx(
	ctx context.Context,
	tx pgx.Tx,
	install integrationstore.IntegrationInstallRecord,
	agentID ID,
) error {
	if err := lifecyclelock.EnterActiveProject(ctx, tx, install.OrgID, install.ProjectID); err != nil {
		return err
	}
	if err := dbsqlc.New(tx).LockIntegrationInstallLifecycleShared(
		ctx,
		dbsqlc.LockIntegrationInstallLifecycleSharedParams{InstallID: install.ID},
	); err != nil {
		return fmt.Errorf("lock integration install lifecycle for input: %w", err)
	}
	return lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{{
		ProjectID: install.ProjectID,
		AgentID:   agentID,
	}})
}
