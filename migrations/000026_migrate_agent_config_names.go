package migrations

import (
	"context"
	"database/sql"

	"github.com/omnara-ai/omnara/internal/agentconfignamemigration"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upMigrateAgentConfigNames, nil)
}

func upMigrateAgentConfigNames(ctx context.Context, tx *sql.Tx) error {
	return agentconfignamemigration.Up(ctx, tx)
}
