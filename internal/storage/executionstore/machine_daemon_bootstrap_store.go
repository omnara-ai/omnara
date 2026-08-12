package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/daemonversion"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/tokenutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateBYOMachineDaemonToken(
	ctx context.Context,
	input CreateBYOMachineDaemonTokenInput,
) (MachineDaemonTokenRecord, error) {
	input, metadata, err := prepareBYOMachineDaemonTokenCreate(input)
	if err != nil {
		return MachineDaemonTokenRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MachineDaemonTokenRecord{}, fmt.Errorf("begin create machine daemon token: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	record, err := createBYOMachineDaemonTokenTx(ctx, dbsqlc.New(tx), input, metadata)
	if err != nil {
		return MachineDaemonTokenRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MachineDaemonTokenRecord{}, fmt.Errorf("commit create machine daemon token: %w", err)
	}
	return record, nil
}

func prepareBYOMachineDaemonTokenCreate(
	input CreateBYOMachineDaemonTokenInput,
) (CreateBYOMachineDaemonTokenInput, json.RawMessage, error) {
	if isNilID(input.OrgID) || isNilID(input.MachineID) || input.Name == "" || input.Token == "" {
		return CreateBYOMachineDaemonTokenInput{}, nil, errors.New(
			"org, machine, name, and token are required",
		)
	}
	metadata, err := metadataColumn(input.Metadata, "daemon token metadata")
	if err != nil {
		return CreateBYOMachineDaemonTokenInput{}, nil, err
	}
	return input, metadata, nil
}

func createBYOMachineDaemonTokenTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input CreateBYOMachineDaemonTokenInput,
	metadata json.RawMessage,
) (MachineDaemonTokenRecord, error) {
	row, err := qtx.CreateBYOMachineDaemonToken(
		ctx,
		dbsqlc.CreateBYOMachineDaemonTokenParams{
			OrgID:     input.OrgID,
			MachineID: input.MachineID,
			Name:      input.Name,
			TokenHash: tokenutil.Hash(input.Token),
			Metadata:  metadata,
		},
	)
	if err != nil {
		return MachineDaemonTokenRecord{}, fmt.Errorf("create BYO machine daemon token: %w", err)
	}
	if err := lockResourceCreation(
		ctx,
		qtx,
		resourceMachineDaemonTokens,
		input.OrgID.String()+":"+input.MachineID.String(),
	); err != nil {
		return MachineDaemonTokenRecord{}, err
	}
	tokenCount, err := qtx.CountActiveMachineDaemonTokensForMachine(
		ctx,
		dbsqlc.CountActiveMachineDaemonTokensForMachineParams{
			OrgID:     input.OrgID,
			MachineID: input.MachineID,
		},
	)
	if err != nil {
		return MachineDaemonTokenRecord{}, fmt.Errorf("count active BYO daemon tokens: %w", err)
	}
	if tokenCount > MaxActiveBYODaemonTokensPerMachine {
		return MachineDaemonTokenRecord{}, resourceLimitExceeded(
			"active machine daemon tokens",
			MaxActiveBYODaemonTokensPerMachine,
		)
	}
	return machineDaemonTokenFromCreate(row), nil
}

type BeginPoolMachineProviderProvisioningInput struct {
	OrgID            ID
	MachineID        ID
	ProvisionAttempt int32
	TokenName        string
}

type PoolMachineProviderProvisioningStart struct {
	DaemonToken                  CreatedMachineDaemonToken
	ProviderProvisionAttemptedAt time.Time
	UpdatedAt                    time.Time
}

func (s *Store) BeginPoolMachineProviderProvisioning(
	ctx context.Context,
	input BeginPoolMachineProviderProvisioningInput,
) (PoolMachineProviderProvisioningStart, error) {
	if isNilID(input.OrgID) || isNilID(input.MachineID) || input.TokenName == "" {
		return PoolMachineProviderProvisioningStart{}, errors.New(
			"org, machine, and token name are required",
		)
	}
	if input.ProvisionAttempt <= 0 {
		return PoolMachineProviderProvisioningStart{}, errors.New("provision attempt is required")
	}
	secret, err := tokenutil.RandomHex(32)
	if err != nil {
		return PoolMachineProviderProvisioningStart{}, fmt.Errorf(
			"generate machine daemon token: %w",
			err,
		)
	}
	token := MachineDaemonTokenPlaintextPrefix + secret
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PoolMachineProviderProvisioningStart{}, fmt.Errorf(
			"begin pool machine provider provisioning: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockPoolMachineForProvisioningClaim(
		ctx,
		dbsqlc.LockPoolMachineForProvisioningClaimParams{
			OrgID: input.OrgID,
			ID:    input.MachineID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PoolMachineProviderProvisioningStart{}, fmt.Errorf(
				"lock pool machine for provider provisioning: %w",
				storeerr.ErrStateTransitionConflict,
			)
		}
		return PoolMachineProviderProvisioningStart{}, fmt.Errorf(
			"lock pool machine for provider provisioning: %w",
			err,
		)
	}
	row, err := qtx.BeginPoolMachineProviderProvisioning(
		ctx,
		dbsqlc.BeginPoolMachineProviderProvisioningParams{
			OrgID:            input.OrgID,
			MachineID:        input.MachineID,
			ProvisionAttempt: input.ProvisionAttempt,
			Name:             input.TokenName,
			TokenHash:        tokenutil.Hash(token),
			Metadata:         []byte(`{}`),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineProviderProvisioningStart{}, fmt.Errorf(
			"begin pool machine provider provisioning: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if err != nil {
		return PoolMachineProviderProvisioningStart{}, fmt.Errorf(
			"begin pool machine provider provisioning: %w",
			err,
		)
	}
	if row.ProviderProvisionAttemptedAt == nil {
		return PoolMachineProviderProvisioningStart{}, errors.New(
			"begin pool machine provider provisioning returned no attempt timestamp",
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return PoolMachineProviderProvisioningStart{}, fmt.Errorf(
			"commit pool machine provider provisioning: %w",
			err,
		)
	}
	return PoolMachineProviderProvisioningStart{
		DaemonToken: CreatedMachineDaemonToken{
			Record: MachineDaemonTokenRecord{
				ID:           row.ID,
				OrgID:        row.OrgID,
				MachineID:    row.MachineID,
				Name:         row.Name,
				TokenHash:    row.TokenHash,
				Metadata:     row.Metadata,
				CreatedAt:    row.CreatedAt,
				LastUsedAt:   row.LastUsedAt,
				RevokedAt:    row.RevokedAt,
				RevokeReason: row.RevokeReason,
			},
			Token: token,
		},
		ProviderProvisionAttemptedAt: *row.ProviderProvisionAttemptedAt,
		UpdatedAt:                    row.UpdatedAt,
	}, nil
}

type ListBYOMachineDaemonTokensInput struct {
	OrgID     ID
	MachineID ID
	Limit     int
	After     listing.KeysetCursor
}

type ListBYOMachineDaemonTokensResult struct {
	Tokens  []MachineDaemonTokenRecord
	HasMore bool
}

func (s *Store) ListBYOMachineDaemonTokens(
	ctx context.Context,
	input ListBYOMachineDaemonTokensInput,
) (ListBYOMachineDaemonTokensResult, error) {
	if isNilID(input.OrgID) || isNilID(input.MachineID) {
		return ListBYOMachineDaemonTokensResult{}, errors.New("org and machine are required")
	}
	if input.Limit <= 0 {
		return ListBYOMachineDaemonTokensResult{}, errors.New("limit must be positive")
	}
	params := dbsqlc.ListBYOMachineDaemonTokensParams{
		OrgID:     input.OrgID,
		MachineID: input.MachineID,
		RowLimit:  int64(input.Limit) + 1,
	}
	if input.After.Set {
		createdAt := input.After.CreatedAt
		id := input.After.ID
		params.CursorCreatedAt = &createdAt
		params.CursorID = &id
	}
	rows, err := s.q.ListBYOMachineDaemonTokens(ctx, params)
	if err != nil {
		return ListBYOMachineDaemonTokensResult{}, fmt.Errorf("list BYO machine daemon tokens: %w", err)
	}
	result := ListBYOMachineDaemonTokensResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Tokens = make([]MachineDaemonTokenRecord, 0, len(rows))
	for _, row := range rows {
		result.Tokens = append(result.Tokens, machineDaemonTokenFromList(row))
	}
	return result, nil
}

func (s *Store) ListAllMachineDaemonTokens(
	ctx context.Context,
	orgID, machineID ID,
) ([]MachineDaemonTokenRecord, error) {
	rows, err := s.q.ListAllMachineDaemonTokens(
		ctx,
		dbsqlc.ListAllMachineDaemonTokensParams{OrgID: orgID, MachineID: machineID},
	)
	if err != nil {
		return nil, fmt.Errorf("list all machine daemon tokens: %w", err)
	}
	out := make([]MachineDaemonTokenRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, machineDaemonTokenFromList(row))
	}
	return out, nil
}

func (s *Store) RevokeBYOMachineDaemonToken(
	ctx context.Context,
	orgID, machineID, tokenID ID,
	reason string,
) (MachineDaemonTokenRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MachineDaemonTokenRecord{}, fmt.Errorf("begin revoke machine daemon token: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txNotifications := s.newTxNotifications()
	record, err := s.RevokeBYOMachineDaemonTokenTx(ctx, tx, txNotifications, orgID, machineID, tokenID, reason)
	if err != nil {
		return MachineDaemonTokenRecord{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "revoke machine daemon token"); err != nil {
		return MachineDaemonTokenRecord{}, err
	}
	return record, nil
}

func (s *Store) RevokeBYOMachineDaemonTokenTx(
	ctx context.Context,
	tx pgx.Tx,
	txNotifications *notifications.TxNotifications,
	orgID, machineID, tokenID ID,
	reason string,
) (MachineDaemonTokenRecord, error) {
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockMachineForRuntimeRegistration(
		ctx,
		dbsqlc.LockMachineForRuntimeRegistrationParams{OrgID: orgID, ID: machineID},
	); err != nil {
		return MachineDaemonTokenRecord{}, fmt.Errorf("lock machine for token revocation: %w", err)
	}
	row, err := qtx.RevokeBYOMachineDaemonToken(
		ctx,
		dbsqlc.RevokeBYOMachineDaemonTokenParams{
			OrgID:     orgID,
			MachineID: machineID,
			ID:        tokenID,
			Reason:    reason,
		},
	)
	if err != nil {
		return MachineDaemonTokenRecord{}, fmt.Errorf("revoke BYO machine daemon token: %w", err)
	}
	active, err := qtx.ListActiveDaemonRuntimesForUpdate(
		ctx,
		dbsqlc.ListActiveDaemonRuntimesForUpdateParams{OrgID: orgID, MachineID: machineID},
	)
	if err != nil {
		return MachineDaemonTokenRecord{}, fmt.Errorf(
			"list active runtimes for token revocation: %w",
			err,
		)
	}
	for _, runtime := range active {
		if runtime.DaemonTokenID != tokenID {
			continue
		}
		if _, err := qtx.ForceEndDaemonRuntime(
			ctx,
			dbsqlc.ForceEndDaemonRuntimeParams{
				OrgID:     orgID,
				MachineID: machineID,
				ID:        runtime.ID,
				Reason:    sqlcTextFromEmpty(reason),
				Message:   "",
			},
		); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			return MachineDaemonTokenRecord{}, fmt.Errorf(
				"end runtime for token revocation: %w",
				err,
			)
		}
		txNotifications.AddDaemonRuntimeEnded(
			runtime.ID,
			runtime.MachineID,
			notifications.DaemonRuntimeEndAuthorizationRevoked,
		)
		if err := completeMachineLifecycleTerminalWorkTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			orgID,
			machineID,
			reason); err != nil {
			return MachineDaemonTokenRecord{}, err
		}
	}
	return machineDaemonTokenFromRevoke(row), nil
}

func (s *Store) AuthenticateMachineDaemonToken(
	ctx context.Context,
	token string,
) (identitystore.PrincipalRecord, error) {
	if token == "" {
		return identitystore.PrincipalRecord{}, storeerr.ErrUnauthorized
	}
	row, err := s.q.AuthenticateMachineDaemonToken(
		ctx,
		dbsqlc.AuthenticateMachineDaemonTokenParams{
			TokenHash:            tokenutil.Hash(token),
			TouchIntervalSeconds: int64(bearerTokenTouchInterval / time.Second),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return identitystore.PrincipalRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return identitystore.PrincipalRecord{}, fmt.Errorf("authenticate machine daemon token: %w", err)
	}
	return identitystore.PrincipalRecord{
		Type:                 identitystore.PrincipalTypeMachineDaemon,
		ID:                   row.MachineID,
		OrgID:                row.OrgID,
		MachineDaemonTokenID: row.ID,
	}, nil
}

func (s *Store) BootstrapMachineDaemon(
	ctx context.Context,
	input MachineDaemonBootstrapInput,
) (MachineBootstrapRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.MachineID) || isNilID(input.DaemonTokenID) {
		return MachineBootstrapRecord{}, errors.New(
			"org, machine, and daemon token are required",
		)
	}
	row, err := s.q.ValidateMachineDaemonBootstrap(
		ctx,
		dbsqlc.ValidateMachineDaemonBootstrapParams{
			OrgID:         input.OrgID,
			MachineID:     input.MachineID,
			DaemonTokenID: input.DaemonTokenID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MachineBootstrapRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return MachineBootstrapRecord{}, fmt.Errorf("validate machine daemon bootstrap: %w", err)
	}
	return MachineBootstrapRecord{
		InstallationID: row.InstallationID,
		OrgID:          row.OrgID,
		MachineID:      row.MachineID,
	}, nil
}

const MaxMachineFailureReportOutputBytes = 4 * 1024

func (s *Store) RecordMachineFailureReport(
	ctx context.Context,
	input MachineFailureReportInput,
) error {
	if isNilID(input.OrgID) || isNilID(input.MachineID) || isNilID(input.DaemonTokenID) {
		return errors.New("org, machine, and daemon token are required")
	}
	switch input.Stage {
	case MachineFailureStageStartupScript, MachineFailureStageDaemonInstall:
		if input.ExitStatus == nil {
			return storeerr.InvalidRequest(errors.New("exit status is required"))
		}
		if input.DaemonVersion != "" || input.TargetVersion != "" {
			return storeerr.InvalidRequest(errors.New(
				"daemon and target versions are only valid for daemon_update failure reports",
			))
		}
	case MachineFailureStageDaemonUpdate:
		if input.ExitStatus != nil {
			return storeerr.InvalidRequest(errors.New(
				"exit status is not valid for daemon_update failure reports",
			))
		}
		if err := daemonversion.Validate(input.DaemonVersion); err != nil {
			return storeerr.InvalidRequest(fmt.Errorf("invalid daemon version: %w", err))
		}
		if input.TargetVersion != "" {
			if err := daemonversion.Validate(input.TargetVersion); err != nil {
				return storeerr.InvalidRequest(fmt.Errorf("invalid target version: %w", err))
			}
		}
	case MachineFailureStageDaemonUninstall, MachineFailureStageDaemonUninstalled:
		if input.ExitStatus != nil || input.DaemonVersion != "" || input.TargetVersion != "" ||
			(input.Stage == MachineFailureStageDaemonUninstalled &&
				(len(input.OutputTail) != 0 || input.OutputTruncated)) {
			return storeerr.InvalidRequest(errors.New("invalid daemon uninstall report"))
		}
	default:
		return storeerr.InvalidRequest(errors.New("invalid failure report stage"))
	}
	if input.ExitStatus != nil && (*input.ExitStatus < 1 || *input.ExitStatus > 255) {
		return storeerr.InvalidRequest(errors.New("exit status must be between 1 and 255"))
	}
	if len(input.OutputTail) > MaxMachineFailureReportOutputBytes {
		return storeerr.InvalidRequest(fmt.Errorf(
			"output tail must be at most %d bytes",
			MaxMachineFailureReportOutputBytes,
		))
	}
	outputTail := strings.ToValidUTF8(string(input.OutputTail), "?")
	outputTail = strings.ReplaceAll(outputTail, "\x00", "?")
	var exitStatus *int32
	if input.ExitStatus != nil {
		value := int32(*input.ExitStatus)
		exitStatus = &value
	}
	params := dbsqlc.RecordMachineFailureReportParams{
		Stage:           input.Stage,
		ExitStatus:      exitStatus,
		OutputTail:      outputTail,
		OutputTruncated: input.OutputTruncated,
		DaemonVersion:   input.DaemonVersion,
		TargetVersion:   input.TargetVersion,
		OrgID:           input.OrgID,
		MachineID:       input.MachineID,
		DaemonTokenID:   input.DaemonTokenID,
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin machine failure report: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	txNotifications := s.newTxNotifications()
	if input.Stage == MachineFailureStageDaemonUninstalled {
		if _, err := qtx.LockMachineForLifecycle(
			ctx,
			dbsqlc.LockMachineForLifecycleParams{OrgID: input.OrgID, ID: input.MachineID},
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return storeerr.ErrUnauthorized
			}
			return fmt.Errorf("lock machine for daemon uninstall report: %w", err)
		}
	}
	_, err = qtx.RecordMachineFailureReport(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrUnauthorized
	}
	if err != nil {
		return fmt.Errorf("record machine failure report: %w", err)
	}
	if input.Stage == MachineFailureStageDaemonUninstalled {
		if err := completeMachineLifecycleTerminalWorkTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			input.OrgID,
			input.MachineID,
			MachineFailureStageDaemonUninstalled,
		); err != nil {
			return err
		}
	}
	return s.commitTxWithNotifications(ctx, tx, txNotifications, "record machine failure report")
}
