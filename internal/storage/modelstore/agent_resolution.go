package modelstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func ResolveForAgentTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	orgID, projectID, configuredModelID uuid.UUID,
	options agentconfig.ModelOverrides,
) (ConfiguredModelRevisionRecord, error) {
	configuredModelRow, err := q.LockConfiguredModelForUse(ctx, dbsqlc.LockConfiguredModelForUseParams{
		OrgID: orgID,
		ID:    configuredModelID,
	})
	if err != nil {
		return ConfiguredModelRevisionRecord{}, fmt.Errorf("load configured model: %w", err)
	}
	configuredModel := configuredModelRecordFromLockForUseSQLC(configuredModelRow)
	providerConfig, err := q.GetModelProviderConfig(ctx, dbsqlc.GetModelProviderConfigParams{
		OrgID: orgID,
		ID:    configuredModel.ModelProviderConfigID,
	})
	if err != nil {
		return ConfiguredModelRevisionRecord{}, fmt.Errorf("load model provider config: %w", err)
	}
	grantRow, err := q.GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		dbsqlc.GetActiveProjectModelGrantForConfiguredModelParams{
			OrgID:             orgID,
			ProjectID:         projectID,
			ConfiguredModelID: configuredModelID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfiguredModelRevisionRecord{}, fmt.Errorf(
			"configured model has no active project grant: %w",
			storeerr.ErrNotFound,
		)
	}
	if err != nil {
		return ConfiguredModelRevisionRecord{}, fmt.Errorf("load project model grant: %w", err)
	}
	effective, err := EffectiveConfiguredModelForAgentOptions(
		modelprotocol.APIFormat(providerConfig.ApiFormat),
		configuredModel,
		projectModelGrantRecordFromActiveSQLC(grantRow),
		options,
	)
	if err != nil {
		return ConfiguredModelRevisionRecord{}, err
	}
	return effective, nil
}
