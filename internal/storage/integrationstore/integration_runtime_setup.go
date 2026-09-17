package integrationstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const discordRuntimeKind = "discord_gateway"

type discordShardConfiguration struct {
	ShardID    int `json:"shard_id"`
	ShardCount int `json:"shard_count"`
}

// initializeDiscordRuntimeTx is reached with the runtime configuration advisory
// lock and validated installation/app authority. No app lock upgrade is needed.
// Existing units are retained verbatim: reconnect is not a reshard or restart.
func initializeDiscordRuntimeTx(
	ctx context.Context, q *dbsqlc.Queries, input UpsertIntegrationInstallInput,
) (int, error) {
	units, err := q.ListDiscordRuntimeSetupUnits(ctx, dbsqlc.ListDiscordRuntimeSetupUnitsParams{
		OrgID: input.OrgID, IntegrationAppID: input.IntegrationAppID,
	})
	if err != nil {
		return 0, fmt.Errorf("load Discord runtime configuration: %w", err)
	}
	if len(units) > 0 {
		for _, unit := range units {
			var fields map[string]*int
			if json.Unmarshal(unit.Configuration, &fields) != nil || len(fields) != 2 ||
				fields["shard_id"] == nil || fields["shard_count"] == nil {
				return 0, storeerr.ErrConflict
			}
			shardID, shardCount := *fields["shard_id"], *fields["shard_count"]
			if shardCount != len(units) || shardID < 0 || shardID >= len(units) ||
				unit.RuntimeKind != discordRuntimeKind ||
				unit.UnitKey != discordRuntimeKind+":"+strconv.Itoa(shardID) ||
				unit.SpecRevision != units[0].SpecRevision {
				return 0, storeerr.ErrConflict
			}
		}
		return len(units), nil
	}
	for shardID := range input.DiscordRuntimeShardCount {
		configuration, err := json.Marshal(discordShardConfiguration{
			ShardID: shardID, ShardCount: input.DiscordRuntimeShardCount,
		})
		if err != nil {
			return 0, fmt.Errorf("encode Discord shard configuration: %w", err)
		}
		_, err = upsertIntegrationRuntimeUnitTx(ctx, q, UpsertIntegrationRuntimeUnitInput{
			OrgID: input.OrgID, IntegrationAppID: input.IntegrationAppID,
			UnitKey: discordRuntimeKind + ":" + strconv.Itoa(shardID), RuntimeKind: discordRuntimeKind,
			DesiredState: IntegrationRuntimeDesiredStateRunning, SpecRevision: 1, Configuration: configuration,
		})
		if err != nil {
			return 0, err
		}
	}
	return input.DiscordRuntimeShardCount, nil
}

// All runtime configuration writers use the same order: active scopes, app-keyed
// advisory serialization, app SHARE, unit. Installation setup takes its own
// lifecycle/row locks between advisory serialization and the app lock.
// Discovery is unlocked and immutable ownership is checked again under locks.
func (s *Store) lockRuntimeConfigurationTx(
	ctx context.Context, tx pgx.Tx, input UpsertIntegrationRuntimeUnitInput,
) error {
	q := s.q.WithTx(tx)
	appRow, err := q.GetIntegrationApp(ctx, dbsqlc.GetIntegrationAppParams{
		OrgID: input.OrgID, ID: input.IntegrationAppID,
	})
	if err != nil {
		return integrationChannelReadError("get runtime app", err)
	}
	app := integrationAppRecordFromSQLC(appRow)
	projectID := app.OwnerProjectID
	if projectID == uuid.Nil {
		err = lifecyclelock.EnterActiveOrganization(ctx, tx, input.OrgID)
	} else {
		err = lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, projectID)
	}
	if err != nil {
		return err
	}
	if err := q.LockIntegrationRuntimeConfiguration(ctx, dbsqlc.LockIntegrationRuntimeConfigurationParams{
		IntegrationAppID: input.IntegrationAppID,
	}); err != nil {
		return fmt.Errorf("lock runtime configuration: %w", err)
	}
	locked, err := q.LockIntegrationAppForInstallation(ctx, dbsqlc.LockIntegrationAppForInstallationParams{
		OrgID: input.OrgID, ID: input.IntegrationAppID,
	})
	if err != nil {
		return integrationChannelReadError("lock runtime app", err)
	}
	if locked.DeletedAt != nil {
		return storeerr.ErrNotFound
	}
	// The upsert's predicates recheck app state after these locks,
	// preserving its ability to stop an unavailable (but nondeleted) runtime.
	return nil
}
