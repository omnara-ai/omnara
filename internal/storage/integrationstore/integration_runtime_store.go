package integrationstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/registryname"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) UpsertIntegrationRuntimeUnit(
	ctx context.Context,
	input UpsertIntegrationRuntimeUnitInput,
) (IntegrationRuntimeUnitRecord, error) {
	var err error
	input, err = normalizeUpsertIntegrationRuntimeUnitInput(input)
	if err != nil {
		return IntegrationRuntimeUnitRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationRuntimeUnitRecord{}, fmt.Errorf("begin runtime configuration: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.lockRuntimeConfigurationTx(ctx, tx, input); err != nil {
		return IntegrationRuntimeUnitRecord{}, err
	}
	unit, err := upsertIntegrationRuntimeUnitTx(ctx, s.q.WithTx(tx), input)
	if err != nil {
		return IntegrationRuntimeUnitRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationRuntimeUnitRecord{}, fmt.Errorf("commit runtime configuration: %w", err)
	}
	return unit, nil
}

func upsertIntegrationRuntimeUnitTx(
	ctx context.Context, q *dbsqlc.Queries, input UpsertIntegrationRuntimeUnitInput,
) (IntegrationRuntimeUnitRecord, error) {
	var row dbsqlc.IntegrationRuntimeUnit
	var err error
	if input.IntegrationInstallID == uuid.Nil {
		row, err = q.UpsertIntegrationAppRuntimeUnit(
			ctx,
			dbsqlc.UpsertIntegrationAppRuntimeUnitParams{
				OrgID: input.OrgID, IntegrationAppID: input.IntegrationAppID,
				UnitKey: input.UnitKey, RuntimeKind: input.RuntimeKind,
				DesiredState: string(input.DesiredState), SpecRevision: int32(input.SpecRevision),
				Configuration: input.Configuration,
			},
		)
	} else {
		row, err = q.UpsertIntegrationInstallRuntimeUnit(
			ctx,
			dbsqlc.UpsertIntegrationInstallRuntimeUnitParams{
				OrgID: input.OrgID, IntegrationAppID: storeutil.IDFromNil(input.IntegrationAppID),
				ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID,
				UnitKey: input.UnitKey, RuntimeKind: input.RuntimeKind,
				DesiredState: string(input.DesiredState), SpecRevision: int32(input.SpecRevision),
				Configuration: input.Configuration,
			},
		)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationRuntimeUnitRecord{}, storeerr.ErrConflict
	}
	if err != nil {
		return IntegrationRuntimeUnitRecord{}, integrationChannelWriteError(
			"upsert integration runtime unit",
			err,
		)
	}
	return integrationRuntimeUnitRecordFromSQLC(row), nil
}

func (s *Store) ClaimIntegrationRuntimeUnits(
	ctx context.Context,
	input ClaimIntegrationRuntimeUnitsInput,
) ([]IntegrationRuntimeUnitRecord, error) {
	var err error
	input.LeaseOwner = strings.TrimSpace(input.LeaseOwner)
	if input.LeaseOwner == "" {
		return nil, errors.New("runtime lease owner is required")
	}
	if len(input.LeaseOwner) > 256 {
		return nil, errors.New("runtime lease owner exceeds its size limit")
	}
	if err := validateLeaseAndLimit(input.LeaseDuration, input.Limit); err != nil {
		return nil, err
	}
	capability, err := normalizedClaimCapability(input.Capability)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ClaimIntegrationRuntimeUnits(ctx, dbsqlc.ClaimIntegrationRuntimeUnitsParams{
		LeaseOwner: input.LeaseOwner, LeaseMicroseconds: input.LeaseDuration.Microseconds(),
		ConnectorKey: capability.ConnectorKey, Provider: capability.Provider,
		RowLimit: int32(input.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("claim integration runtime units: %w", err)
	}
	out := make([]IntegrationRuntimeUnitRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, integrationRuntimeUnitRecordFromSQLC(row))
	}
	return out, nil
}

func (s *Store) HeartbeatIntegrationRuntimeUnit(
	ctx context.Context,
	input HeartbeatIntegrationRuntimeUnitInput,
) (IntegrationRuntimeUnitRecord, error) {
	var err error
	input, err = normalizeHeartbeatIntegrationRuntimeUnitInput(input)
	if err != nil {
		return IntegrationRuntimeUnitRecord{}, err
	}
	connectorKeys, providers, err := normalizedCapabilityColumns(input.Capabilities)
	if err != nil {
		return IntegrationRuntimeUnitRecord{}, err
	}
	row, err := s.q.HeartbeatIntegrationRuntimeUnit(ctx, dbsqlc.HeartbeatIntegrationRuntimeUnitParams{
		ID: input.ID, LeaseToken: input.LeaseToken, LeaseGeneration: input.LeaseGeneration,
		LeaseMicroseconds: input.LeaseDuration.Microseconds(), WriteCheckpoint: input.WriteCheckpoint,
		CheckpointVersion: int32(input.CheckpointVersion), Checkpoint: input.Checkpoint,
		ConnectorKeys: connectorKeys, Providers: providers,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationRuntimeUnitRecord{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return IntegrationRuntimeUnitRecord{}, integrationChannelWriteError(
			"heartbeat integration runtime unit",
			err,
		)
	}
	return integrationRuntimeUnitRecordFromSQLC(row), nil
}

func (s *Store) ReleaseIntegrationRuntimeUnit(
	ctx context.Context,
	input ReleaseIntegrationRuntimeUnitInput,
) (IntegrationRuntimeUnitRecord, error) {
	if input.ID == uuid.Nil || input.LeaseToken == uuid.Nil || input.LeaseGeneration <= 0 {
		return IntegrationRuntimeUnitRecord{}, storeerr.InvalidRequest(errors.New(
			"runtime unit, lease token, and positive lease generation are required",
		))
	}
	lastError, err := normalizedJSONObject(input.LastError, "last_error")
	if err != nil {
		return IntegrationRuntimeUnitRecord{}, err
	}
	if input.WriteCheckpoint {
		if input.CheckpointVersion <= 0 {
			return IntegrationRuntimeUnitRecord{}, storeerr.InvalidRequest(
				errors.New("positive checkpoint version is required"),
			)
		}
		input.Checkpoint, err = normalizedJSONObject(input.Checkpoint, "checkpoint")
		if err != nil {
			return IntegrationRuntimeUnitRecord{}, err
		}
	} else if input.CheckpointVersion != 0 || len(input.Checkpoint) != 0 {
		return IntegrationRuntimeUnitRecord{}, storeerr.InvalidRequest(errors.New(
			"checkpoint fields require write checkpoint",
		))
	}
	connectorKeys, providers, err := normalizedCapabilityColumns(input.Capabilities)
	if err != nil {
		return IntegrationRuntimeUnitRecord{}, err
	}
	row, err := s.q.ReleaseIntegrationRuntimeUnit(ctx, dbsqlc.ReleaseIntegrationRuntimeUnitParams{
		ID: input.ID, LeaseToken: input.LeaseToken, LeaseGeneration: input.LeaseGeneration,
		WriteCheckpoint: input.WriteCheckpoint, CheckpointVersion: int32(input.CheckpointVersion),
		Checkpoint: input.Checkpoint, Error: lastError,
		ConnectorKeys: connectorKeys, Providers: providers,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = s.q.RelinquishStaleIntegrationRuntimeUnit(
			ctx,
			dbsqlc.RelinquishStaleIntegrationRuntimeUnitParams{
				ID: input.ID, LeaseToken: input.LeaseToken,
				LeaseGeneration: input.LeaseGeneration,
				ConnectorKeys:   connectorKeys, Providers: providers,
			},
		)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationRuntimeUnitRecord{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return IntegrationRuntimeUnitRecord{}, integrationChannelWriteError(
			"release integration runtime unit",
			err,
		)
	}
	return integrationRuntimeUnitRecordFromSQLC(row), nil
}

func (s *Store) IntegrationRuntimeLeaseIsCurrent(
	ctx context.Context,
	integrationAppID, id, integrationInstallID, leaseToken uuid.UUID,
	leaseGeneration int64,
) (bool, error) {
	if integrationAppID == uuid.Nil || id == uuid.Nil || integrationInstallID == uuid.Nil ||
		leaseToken == uuid.Nil || leaseGeneration <= 0 {
		return false, errors.New(
			"integration app, runtime unit, installation, lease token, and positive lease generation are required",
		)
	}
	current, err := s.q.IntegrationRuntimeLeaseIsCurrent(
		ctx,
		dbsqlc.IntegrationRuntimeLeaseIsCurrentParams{
			IntegrationAppID: integrationAppID, ID: id,
			IntegrationInstallID: integrationInstallID,
			LeaseToken:           leaseToken, LeaseGeneration: leaseGeneration,
		},
	)
	if err != nil {
		return false, fmt.Errorf("validate integration runtime lease: %w", err)
	}
	return current, nil
}

func ValidateIntegrationRuntimeLeaseProof(proof *IntegrationRuntimeLeaseProof) error {
	if proof == nil {
		return nil
	}
	if proof.IntegrationAppID == uuid.Nil || proof.UnitID == uuid.Nil ||
		proof.LeaseToken == uuid.Nil || proof.LeaseGeneration <= 0 {
		return storeerr.InvalidRequest(errors.New(
			"runtime integration app, unit, lease token, and positive generation are required",
		))
	}
	return nil
}

// LockIntegrationRuntimeLeaseForMutation verifies a runtime's ownership and
// takes share locks on the installation, app, and unit for the caller's
// transaction. Ownership-changing writes therefore cannot cross the guarded
// mutation's commit boundary.
func LockIntegrationRuntimeLeaseForMutation(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	proof *IntegrationRuntimeLeaseProof,
	projectID, integrationInstallID uuid.UUID,
) error {
	if proof == nil {
		return nil
	}
	if err := ValidateIntegrationRuntimeLeaseProof(proof); err != nil {
		return err
	}
	if projectID == uuid.Nil || integrationInstallID == uuid.Nil {
		return errors.New("runtime mutation project and integration installation are required")
	}
	current, err := qtx.LockIntegrationRuntimeLeaseForMutation(
		ctx,
		dbsqlc.LockIntegrationRuntimeLeaseForMutationParams{
			ProjectID:            projectID,
			IntegrationInstallID: integrationInstallID,
			ID:                   proof.UnitID,
			IntegrationAppID:     proof.IntegrationAppID,
			LeaseToken:           proof.LeaseToken,
			LeaseGeneration:      proof.LeaseGeneration,
		},
	)
	if err != nil {
		return fmt.Errorf("lock integration runtime lease: %w", err)
	}
	if !current {
		return storeerr.ErrStateTransitionConflict
	}
	return nil
}

func normalizeUpsertIntegrationRuntimeUnitInput(
	input UpsertIntegrationRuntimeUnitInput,
) (UpsertIntegrationRuntimeUnitInput, error) {
	if input.OrgID == uuid.Nil || input.IntegrationAppID == uuid.Nil {
		return UpsertIntegrationRuntimeUnitInput{}, errors.New(
			"org and integration app are required",
		)
	}
	if input.ProjectID == uuid.Nil != (input.IntegrationInstallID == uuid.Nil) {
		return UpsertIntegrationRuntimeUnitInput{}, errors.New(
			"runtime project and installation must either both be set or both be omitted",
		)
	}
	input.UnitKey = strings.TrimSpace(input.UnitKey)
	input.RuntimeKind = strings.TrimSpace(input.RuntimeKind)
	if input.UnitKey == "" || input.RuntimeKind == "" || input.SpecRevision <= 0 {
		return UpsertIntegrationRuntimeUnitInput{}, errors.New(
			"unit key, runtime kind, and positive configuration version are required",
		)
	}
	if len(input.UnitKey) > 512 || !registryname.Valid(input.RuntimeKind) {
		return UpsertIntegrationRuntimeUnitInput{}, errors.New(
			"runtime unit key or kind exceeds its contract",
		)
	}
	if input.DesiredState != IntegrationRuntimeDesiredStateRunning &&
		input.DesiredState != IntegrationRuntimeDesiredStateStopped {
		return UpsertIntegrationRuntimeUnitInput{}, fmt.Errorf(
			"unsupported runtime desired state %q", input.DesiredState,
		)
	}
	configuration, err := normalizedJSONObject(input.Configuration, "configuration")
	if err != nil {
		return UpsertIntegrationRuntimeUnitInput{}, err
	}
	input.Configuration = configuration
	return input, nil
}

func normalizeHeartbeatIntegrationRuntimeUnitInput(
	input HeartbeatIntegrationRuntimeUnitInput,
) (HeartbeatIntegrationRuntimeUnitInput, error) {
	if input.ID == uuid.Nil || input.LeaseToken == uuid.Nil || input.LeaseGeneration <= 0 {
		return HeartbeatIntegrationRuntimeUnitInput{}, storeerr.InvalidRequest(errors.New(
			"runtime unit, lease token, and positive lease generation are required",
		))
	}
	if err := validateLeaseAndLimit(input.LeaseDuration, 1); err != nil {
		return HeartbeatIntegrationRuntimeUnitInput{}, err
	}
	if !input.WriteCheckpoint {
		input.CheckpointVersion = 1
		input.Checkpoint = []byte(`{}`)
	} else {
		if input.CheckpointVersion <= 0 {
			return HeartbeatIntegrationRuntimeUnitInput{}, storeerr.InvalidRequest(
				errors.New("positive checkpoint version is required"),
			)
		}
		checkpoint, err := normalizedJSONObject(input.Checkpoint, "checkpoint")
		if err != nil {
			return HeartbeatIntegrationRuntimeUnitInput{}, err
		}
		input.Checkpoint = checkpoint
	}
	var err error
	input.Capabilities, err = channelconnector.NormalizeCapabilities(input.Capabilities)
	if err != nil {
		return HeartbeatIntegrationRuntimeUnitInput{}, err
	}
	return input, nil
}

func integrationRuntimeUnitRecordFromSQLC(
	row dbsqlc.IntegrationRuntimeUnit,
) IntegrationRuntimeUnitRecord {
	return IntegrationRuntimeUnitRecord{
		ID:                            row.ID,
		OrgID:                         row.OrgID,
		IntegrationAppID:              row.IntegrationAppID,
		ProjectID:                     storeutil.IDFromPtr(row.ProjectID),
		IntegrationInstallID:          storeutil.IDFromPtr(row.IntegrationInstallID),
		Provider:                      row.Provider,
		ConnectorKey:                  row.ConnectorKey,
		UnitKey:                       row.UnitKey,
		RuntimeKind:                   row.RuntimeKind,
		DesiredState:                  IntegrationRuntimeDesiredState(row.DesiredState),
		SpecRevision:                  int(row.SpecRevision),
		Configuration:                 row.Configuration,
		Status:                        IntegrationRuntimeStatus(row.Status),
		LeaseOwner:                    stringFromPtr(row.LeaseOwner),
		LeaseToken:                    storeutil.IDFromPtr(row.LeaseToken),
		LeaseGeneration:               row.LeaseGeneration,
		LeasedAt:                      row.LeasedAt,
		RenewedAt:                     row.RenewedAt,
		LeaseExpiresAt:                row.LeaseExpiresAt,
		LeaseSpecRevision:             intFromPtr(row.LeaseSpecRevision),
		LeaseAppConfigurationRevision: int64FromPtr(row.LeaseAppConfigurationRevision),
		LeaseInstallConfigRevision:    int64FromPtr(row.LeaseInstallConfigurationRevision),
		CheckpointVersion:             int(row.CheckpointVersion),
		CheckpointRevision:            row.CheckpointRevision,
		Checkpoint:                    row.Checkpoint,
		LastError:                     row.LastError,
		CreatedAt:                     row.CreatedAt,
		UpdatedAt:                     row.UpdatedAt,
	}
}

func intFromPtr(value *int32) int {
	if value == nil {
		return 0
	}
	return int(*value)
}

func int64FromPtr(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func validateLeaseAndLimit(duration time.Duration, limit int) error {
	if duration < time.Millisecond {
		return errors.New("lease duration must be at least one millisecond")
	}
	return validateRowLimit(limit)
}
