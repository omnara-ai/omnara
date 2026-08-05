package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CreateDaemonMachineInput struct {
	OrgID          ID
	DisplayName    string
	Description    string
	Cwd            string
	Env            json.RawMessage
	SecretEnv      json.RawMessage
	IdempotencyKey string
	Metadata       json.RawMessage
}

type UpdateMachineInput struct {
	OrgID     ID
	MachineID ID
	Cwd       *string
	Env       *json.RawMessage
	SecretEnv *json.RawMessage
}

type DeleteMachineInput struct {
	OrgID     ID
	MachineID ID
}

type ProjectMachineGrantSourceKind string

const (
	ProjectMachineGrantSourceKindExplicit ProjectMachineGrantSourceKind = "explicit"
	ProjectMachineGrantSourceKindPool     ProjectMachineGrantSourceKind = "pool"
)

type ProjectMachineGrantRecord struct {
	ID                        ID                            `json:"id"`
	OrgID                     ID                            `json:"org_id"`
	ProjectID                 ID                            `json:"project_id"`
	MachineID                 ID                            `json:"machine_id"`
	SourceKind                ProjectMachineGrantSourceKind `json:"source_kind"`
	ProjectMachinePoolGrantID ID                            `json:"project_machine_pool_grant_id,omitempty"`
	Description               string                        `json:"description"`
	IdempotencyKey            string                        `json:"idempotency_key,omitempty"`
	Metadata                  json.RawMessage               `json:"metadata"`
	CreatedAt                 time.Time                     `json:"created_at"`
	UpdatedAt                 time.Time                     `json:"updated_at"`
	Created                   bool                          `json:"-"`
}

type ListProjectMachineGrantsInput struct {
	OrgID     ID
	ProjectID ID
	Limit     int
	MachineID ID
	Filters   MachineGrantListFilters
	List      listing.Options
}

type MachineGrantListFilters struct {
	SourceKinds      []ProjectMachineGrantSourceKind
	Providers        []string
	LifecycleStates  []MachineLifecycleState
	ConnectionStates []MachineConnectionState
}

type ProjectMachineGrantListRecord struct {
	Grant   ProjectMachineGrantRecord
	Machine MachineSummaryRecord
}

type ListProjectMachineGrantsResult struct {
	Grants  []ProjectMachineGrantListRecord
	HasMore bool
	Next    listing.Cursor
}

type MachineAccessSourceKind string

const (
	MachineAccessSourceKindOrganizationRole    MachineAccessSourceKind = "org_role"
	MachineAccessSourceKindProjectMachineGrant MachineAccessSourceKind = "project_machine_grant"
)

type MachineAccessSourceRecord struct {
	Kind            MachineAccessSourceKind
	ProjectID       ID
	GrantID         ID
	GrantSourceKind ProjectMachineGrantSourceKind
}

type VisibleMachineRecord struct {
	Machine   MachineSummaryRecord
	Sources   []MachineAccessSourceRecord
	CanManage bool
}

type ListVisibleMachinesForPrincipalInput struct {
	OrgID     ID
	Principal identitystore.PrincipalRecord
	Filters   MachineListFilters
	Limit     int
	List      listing.Options
}

type MachineListFilters struct {
	Providers        []string
	SourceKinds      []MachineSourceKind
	LifecycleStates  []MachineLifecycleState
	ConnectionStates []MachineConnectionState
	MachinePoolID    ID
}

type ListVisibleMachinesForPrincipalResult struct {
	Machines []VisibleMachineRecord
	HasMore  bool
	Next     listing.Cursor
}

type ProjectVisibleMachineRecord struct {
	Machine         MachineSummaryRecord
	GrantID         ID
	GrantSourceKind ProjectMachineGrantSourceKind
	CanManage       bool
}

type ListProjectVisibleMachinesForPrincipalInput struct {
	OrgID     ID
	ProjectID ID
	Principal identitystore.PrincipalRecord
	Filters   MachineListFilters
	Limit     int
	List      listing.Options
}

type ListProjectVisibleMachinesForPrincipalResult struct {
	Machines []ProjectVisibleMachineRecord
	HasMore  bool
	Next     listing.Cursor
}

type CreateProjectMachineGrantInput struct {
	OrgID          ID
	ProjectID      ID
	MachineID      ID
	Description    string
	IdempotencyKey string
	Metadata       json.RawMessage
}

type MachineDaemonTokenRecord struct {
	ID           ID              `json:"id"`
	OrgID        ID              `json:"org_id"`
	MachineID    ID              `json:"machine_id"`
	Name         string          `json:"name"`
	TokenHash    string          `json:"-"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    time.Time       `json:"created_at"`
	LastUsedAt   *time.Time      `json:"last_used_at,omitempty"`
	RevokedAt    *time.Time      `json:"revoked_at,omitempty"`
	RevokeReason string          `json:"revoke_reason,omitempty"`
}

type CreatedMachineDaemonToken struct {
	Record MachineDaemonTokenRecord
	Token  string
}

type CreateBYOMachineDaemonTokenInput struct {
	OrgID     ID
	MachineID ID
	Name      string
	Token     string
	Metadata  json.RawMessage
}

type MachineDaemonBootstrapInput struct {
	OrgID         ID
	MachineID     ID
	DaemonTokenID ID
}

type MachineBootstrapRecord struct {
	InstallationID ID
	OrgID          ID
	MachineID      ID
}

func (s *Store) CreateDaemonMachine(ctx context.Context, input CreateDaemonMachineInput) (MachineRecord, error) {
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if isNilID(input.OrgID) || input.DisplayName == "" {
		return MachineRecord{}, errors.New("org and display name are required")
	}
	var err error
	input.Metadata, err = normalizedJSONObject(input.Metadata, "machine metadata")
	if err != nil {
		return MachineRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MachineRecord{}, fmt.Errorf("begin create machine: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if strings.ContainsRune(input.Cwd, 0) {
		return MachineRecord{}, errors.New("cwd cannot contain NUL")
	}
	environment, err := MachineEnvironmentFromColumns(input.Env, input.SecretEnv)
	if err != nil {
		return MachineRecord{}, err
	}
	env, secretEnv, err := machineEnvironmentToColumns(environment)
	if err != nil {
		return MachineRecord{}, err
	}
	row, err := insertMachineWithResourceLimitTx(
		ctx,
		qtx,
		dbsqlc.InsertMachineParams{
			OrgID:                  input.OrgID,
			MachinePoolID:          nil,
			SourceKind:             string(MachineSourceKindBYO),
			DisplayName:            input.DisplayName,
			Description:            input.Description,
			Provider:               "byo",
			LifecycleState:         string(MachineLifecycleStateActive),
			Cwd:                    input.Cwd,
			Env:                    env,
			SecretEnv:              secretEnv,
			IdempotencyKey:         sqlcTextFromEmpty(input.IdempotencyKey),
			LifecycleReasonMessage: "",
			Metadata:               input.Metadata,
		},
	)
	if err == nil {
		if err := validateMachineEnvironmentSecretsTx(ctx, qtx, input.OrgID, NilID, environment); err != nil {
			return MachineRecord{}, err
		}
		record := machineRecordFromInsertSQLC(row)
		record.Created = true
		if err := tx.Commit(ctx); err != nil {
			return MachineRecord{}, fmt.Errorf("commit create machine: %w", err)
		}
		return record, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) || input.IdempotencyKey == "" {
		return MachineRecord{}, fmt.Errorf("insert machine: %w", err)
	}
	replay, err := qtx.GetMachineByIdempotency(
		ctx,
		dbsqlc.GetMachineByIdempotencyParams{
			OrgID:          input.OrgID,
			IdempotencyKey: input.IdempotencyKey,
		},
	)
	if err != nil {
		return MachineRecord{}, fmt.Errorf("get machine by idempotency: %w", err)
	}
	record := machineRecordFromIdempotencySQLC(replay)
	if err := tx.Commit(ctx); err != nil {
		return MachineRecord{}, fmt.Errorf("commit replay create machine: %w", err)
	}
	return record, nil
}

func (s *Store) UpdateMachine(ctx context.Context, input UpdateMachineInput) (MachineRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.MachineID) {
		return MachineRecord{}, errors.New("org and machine are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MachineRecord{}, fmt.Errorf("begin update machine: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	projectID, err := qtx.LockPoolMachineGrant(
		ctx,
		dbsqlc.LockPoolMachineGrantParams{OrgID: input.OrgID, MachineID: input.MachineID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		projectID = NilID
	} else if err != nil {
		return MachineRecord{}, fmt.Errorf("lock pool machine grant: %w", err)
	}
	locked, err := qtx.LockMachineExecutionDefaults(
		ctx,
		dbsqlc.LockMachineExecutionDefaultsParams{OrgID: input.OrgID, ID: input.MachineID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MachineRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return MachineRecord{}, fmt.Errorf("lock machine for update: %w", err)
	}
	if err := qtx.LockMachineEnvironmentKey(
		ctx,
		dbsqlc.LockMachineEnvironmentKeyParams{MachineID: input.MachineID},
	); err != nil {
		return MachineRecord{}, fmt.Errorf("lock machine environment: %w", err)
	}
	cwd := locked.Cwd
	if input.Cwd != nil {
		cwd = *input.Cwd
	}
	if strings.ContainsRune(cwd, 0) {
		return MachineRecord{}, errors.New("cwd cannot contain NUL")
	}
	env, secretEnv := locked.Env, locked.SecretEnv
	if input.Env != nil || input.SecretEnv != nil {
		if input.Env != nil {
			env = *input.Env
		}
		if input.SecretEnv != nil {
			secretEnv = *input.SecretEnv
		}
		environment, err := MachineEnvironmentFromColumns(env, secretEnv)
		if err != nil {
			return MachineRecord{}, err
		}
		if input.SecretEnv != nil {
			if err := validateMachineEnvironmentSecretsTx(ctx, qtx, input.OrgID, projectID, environment); err != nil {
				return MachineRecord{}, err
			}
		}
		bindings, err := qtx.ListAttachedMachineBindingOverlays(
			ctx,
			dbsqlc.ListAttachedMachineBindingOverlaysParams{OrgID: input.OrgID, MachineID: input.MachineID},
		)
		if err != nil {
			return MachineRecord{}, fmt.Errorf("list attached machine bindings: %w", err)
		}
		for _, binding := range bindings {
			overlay, err := machineEnvironmentOverlayFromColumns(binding.EnvOverlay, binding.SecretEnvOverlay)
			if err != nil {
				return MachineRecord{}, fmt.Errorf("load attached binding %s environment: %w", binding.ID, err)
			}
			if _, err := resolveMachineEnvironment(environment, overlay); err != nil {
				return MachineRecord{}, fmt.Errorf("machine environment conflicts with attached binding %s: %w", binding.ID, err)
			}
		}
		env, secretEnv, err = machineEnvironmentToColumns(environment)
		if err != nil {
			return MachineRecord{}, err
		}
	}
	updated, err := qtx.UpdateMachineExecutionDefaults(
		ctx,
		dbsqlc.UpdateMachineExecutionDefaultsParams{
			Cwd:       cwd,
			Env:       env,
			SecretEnv: secretEnv,
			OrgID:     input.OrgID,
			ID:        input.MachineID,
		},
	)
	if err != nil {
		return MachineRecord{}, fmt.Errorf("update machine execution defaults: %w", err)
	}
	if updated != 1 {
		return MachineRecord{}, storeerr.ErrStateTransitionConflict
	}
	row, err := qtx.GetMachine(ctx, dbsqlc.GetMachineParams{OrgID: input.OrgID, ID: input.MachineID})
	if err != nil {
		return MachineRecord{}, fmt.Errorf("load updated machine: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return MachineRecord{}, fmt.Errorf("commit update machine: %w", err)
	}
	return machineRecordFromGetSQLC(row), nil
}

func (s *Store) ListVisibleMachinesForPrincipal(
	ctx context.Context,
	input ListVisibleMachinesForPrincipalInput,
) (ListVisibleMachinesForPrincipalResult, error) {
	userID, orgAPIKeyID := identitystore.AccountPrincipalIDs(input.Principal)
	if isNilID(input.OrgID) || (userID == nil && orgAPIKeyID == nil) {
		return ListVisibleMachinesForPrincipalResult{}, errors.New("org id and principal are required")
	}
	if input.Limit <= 0 {
		return ListVisibleMachinesForPrincipalResult{}, errors.New("limit must be positive")
	}
	if err := validateMachineSourceKindFilters(input.Filters.SourceKinds); err != nil {
		return ListVisibleMachinesForPrincipalResult{}, err
	}
	input.List = listing.Normalize(input.List)
	params := dbsqlc.ListVisibleMachineSourcesForPrincipalParams{
		OrgID: input.OrgID, UserID: userID, OrgApiKeyID: orgAPIKeyID, RowLimit: int64(input.Limit) + 1,
		SortField: input.List.SortField, SortDesc: input.List.SortDesc, NamePattern: input.List.NamePattern,
		Providers: input.Filters.Providers, SourceKinds: sqlcStrings(input.Filters.SourceKinds),
		LifecycleStates:  sqlcStrings(input.Filters.LifecycleStates),
		ConnectionStates: sqlcStrings(input.Filters.ConnectionStates),
		MachinePoolID:    sqlcIDFromNil(input.Filters.MachinePoolID),
	}
	if !listing.SortAllowed(
		input.List.SortField,
		"name", "created_at", "updated_at", "provider", "source_kind", "lifecycle_state", "connection_state",
	) {
		return ListVisibleMachinesForPrincipalResult{}, errors.New("unsupported sort")
	}
	if input.List.After.Set {
		params.CursorSet, params.CursorKey, params.CursorID = true, input.List.After.Key, input.List.After.ID
	}
	rows, err := s.q.ListVisibleMachineSourcesForPrincipal(ctx, params)
	if err != nil {
		return ListVisibleMachinesForPrincipalResult{}, fmt.Errorf("list visible machines for principal: %w", err)
	}
	out := make([]VisibleMachineRecord, 0, len(rows))
	cursors := make([]listing.Cursor, 0, len(rows))
	index := make(map[ID]int)
	for _, row := range rows {
		i, ok := index[row.ID]
		if !ok {
			index[row.ID] = len(out)
			out = append(
				out,
				VisibleMachineRecord{Machine: machineSummaryFromVisibleSourceSQLC(row)},
			)
			cursors = append(cursors, listing.Cursor{Set: true, IsNull: row.SortIsNull, Key: row.SortKey, ID: row.ID})
			i = len(out) - 1
		}
		out[i].CanManage = out[i].CanManage || row.CanManage
		out[i].Sources = append(
			out[i].Sources,
			MachineAccessSourceRecord{
				Kind:            MachineAccessSourceKind(row.AccessSourceKind),
				ProjectID:       idFromSQLCPtr(row.AccessProjectID),
				GrantID:         idFromSQLCPtr(row.AccessGrantID),
				GrantSourceKind: ProjectMachineGrantSourceKind(row.AccessGrantSourceKind),
			},
		)
	}
	result := ListVisibleMachinesForPrincipalResult{}
	if len(out) > input.Limit {
		result.HasMore = true
		out = out[:input.Limit]
	}
	result.Machines = out
	if len(out) > 0 {
		result.Next = cursors[len(out)-1]
	}
	return result, nil
}

func (s *Store) DeleteMachine(
	ctx context.Context,
	input DeleteMachineInput,
) (MachineRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.MachineID) {
		return MachineRecord{}, errors.New("org and machine are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MachineRecord{}, fmt.Errorf("begin delete machine: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txNotifications := s.newTxNotifications()
	machine, err := s.DeleteMachineTx(ctx, tx, txNotifications, input)
	if err != nil {
		return MachineRecord{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "delete machine"); err != nil {
		return MachineRecord{}, err
	}
	return machine, nil
}

func (s *Store) DeleteMachineTx(
	ctx context.Context,
	tx pgx.Tx,
	txNotifications *notifications.TxNotifications,
	input DeleteMachineInput,
) (MachineRecord, error) {
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{OrgID: input.OrgID, ID: input.MachineID},
	); err != nil {
		return MachineRecord{}, fmt.Errorf("lock machine for delete: %w", err)
	}
	active, err := qtx.ListActiveDaemonRuntimesForUpdate(
		ctx,
		dbsqlc.ListActiveDaemonRuntimesForUpdateParams{
			OrgID:     input.OrgID,
			MachineID: input.MachineID,
		},
	)
	if err != nil {
		return MachineRecord{}, fmt.Errorf("list active daemon runtimes for delete: %w", err)
	}
	for _, runtime := range active {
		if _, err := qtx.ForceEndDaemonRuntime(
			ctx,
			dbsqlc.ForceEndDaemonRuntimeParams{
				OrgID:     runtime.OrgID,
				MachineID: runtime.MachineID,
				ID:        runtime.ID,
				Reason:    sqlcTextFromEmpty("machine_deleted"),
				Message:   "",
			},
		); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			return MachineRecord{}, fmt.Errorf("end daemon runtime for delete: %w", err)
		}
		txNotifications.AddDaemonRuntimeEnded(
			runtime.ID,
			runtime.MachineID,
			notifications.DaemonRuntimeEndMachineDecommissioned,
		)
	}
	if err := completeMachineLifecycleTerminalWorkTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		input.OrgID,
		input.MachineID,
		"machine_deleted",
	); err != nil {
		return MachineRecord{}, err
	}
	if err := qtx.RevokeMachineDaemonTokensForMachine(
		ctx,
		dbsqlc.RevokeMachineDaemonTokensForMachineParams{
			OrgID:     input.OrgID,
			MachineID: input.MachineID,
			Reason:    "machine_deleted",
		},
	); err != nil {
		return MachineRecord{}, fmt.Errorf("revoke machine daemon tokens for delete: %w", err)
	}
	if err := qtx.DeleteProjectMachineGrantsForMachine(ctx, dbsqlc.DeleteProjectMachineGrantsForMachineParams{
		OrgID: input.OrgID, MachineID: input.MachineID,
	}); err != nil {
		return MachineRecord{}, fmt.Errorf("delete project machine grants for machine delete: %w", err)
	}
	row, err := qtx.DeleteMachine(
		ctx,
		dbsqlc.DeleteMachineParams{
			OrgID:   input.OrgID,
			ID:      input.MachineID,
			Reason:  sqlcTextFromEmpty("user_deleted"),
			Message: "",
		},
	)
	if err != nil {
		return MachineRecord{}, fmt.Errorf("delete machine: %w", err)
	}
	if err := qtx.ReleaseAgentMachineBindingsForMachine(
		ctx,
		dbsqlc.ReleaseAgentMachineBindingsForMachineParams{
			OrgID:     input.OrgID,
			MachineID: input.MachineID,
		},
	); err != nil {
		return MachineRecord{}, fmt.Errorf("release agent machine bindings for archive: %w", err)
	}
	return machineRecordFromDeleteSQLC(row), nil
}

func (s *Store) CreateProjectMachineGrant(
	ctx context.Context,
	input CreateProjectMachineGrantInput,
) (ProjectMachineGrantRecord, MachineRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.ProjectID) || isNilID(input.MachineID) {
		return ProjectMachineGrantRecord{}, MachineRecord{}, errors.New(
			"org, project, and machine are required",
		)
	}
	var err error
	input.Metadata, err = normalizedJSONObject(input.Metadata, "project machine grant metadata")
	if err != nil {
		return ProjectMachineGrantRecord{}, MachineRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectMachineGrantRecord{}, MachineRecord{}, fmt.Errorf(
			"begin create project machine grant: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	machineRow, err := qtx.GetMachine(
		ctx,
		dbsqlc.GetMachineParams{OrgID: input.OrgID, ID: input.MachineID},
	)
	if err != nil {
		return ProjectMachineGrantRecord{}, MachineRecord{}, fmt.Errorf(
			"get machine for project grant: %w",
			err,
		)
	}
	machine := machineRecordFromGetSQLC(machineRow)
	grantRow, err := qtx.UpsertProjectMachineGrant(
		ctx,
		dbsqlc.UpsertProjectMachineGrantParams{
			OrgID:                     input.OrgID,
			ProjectID:                 input.ProjectID,
			MachineID:                 input.MachineID,
			SourceKind:                string(ProjectMachineGrantSourceKindExplicit),
			ProjectMachinePoolGrantID: nil,
			Description:               input.Description,
			IdempotencyKey:            sqlcTextFromEmpty(input.IdempotencyKey),
			Metadata:                  input.Metadata,
		},
	)
	if err == nil {
		grant := projectMachineGrantFromUpsert(grantRow)
		grant.Created = true
		if err := tx.Commit(ctx); err != nil {
			return ProjectMachineGrantRecord{}, MachineRecord{}, fmt.Errorf(
				"commit create project machine grant: %w",
				err,
			)
		}
		return grant, machine, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ProjectMachineGrantRecord{}, MachineRecord{}, fmt.Errorf(
			"upsert project machine grant: %w",
			err,
		)
	}
	if input.IdempotencyKey != "" {
		replay, replayErr := qtx.GetProjectMachineGrantByIdempotency(
			ctx,
			dbsqlc.GetProjectMachineGrantByIdempotencyParams{
				ProjectID:      input.ProjectID,
				IdempotencyKey: input.IdempotencyKey,
			},
		)
		if replayErr == nil {
			grant := projectMachineGrantFromIdempotency(replay)
			if grant.MachineID != input.MachineID ||
				grant.SourceKind != ProjectMachineGrantSourceKindExplicit ||
				grant.ProjectMachinePoolGrantID != NilID ||
				grant.Description != input.Description ||
				!sameJSON(grant.Metadata, input.Metadata) {
				return ProjectMachineGrantRecord{}, MachineRecord{}, storeerr.ErrIdempotencyConflict
			}
			if err := tx.Commit(ctx); err != nil {
				return ProjectMachineGrantRecord{}, MachineRecord{}, fmt.Errorf(
					"commit replay create project machine grant: %w",
					err,
				)
			}
			return grant, machine, nil
		}
	}
	return ProjectMachineGrantRecord{}, MachineRecord{}, storeerr.ErrIdempotencyConflict
}

func (s *Store) ListProjectMachineGrants(
	ctx context.Context,
	input ListProjectMachineGrantsInput,
) (ListProjectMachineGrantsResult, error) {
	if isNilID(input.OrgID) || isNilID(input.ProjectID) {
		return ListProjectMachineGrantsResult{}, errors.New("org and project are required")
	}
	if input.Limit <= 0 {
		return ListProjectMachineGrantsResult{}, errors.New("limit must be positive")
	}
	input.List = listing.Normalize(input.List)
	params := dbsqlc.ListProjectMachineGrantsParams{
		OrgID:     input.OrgID,
		ProjectID: input.ProjectID,
		RowLimit:  int64(input.Limit) + 1,
		SortField: input.List.SortField, SortDesc: input.List.SortDesc, NamePattern: input.List.NamePattern,
		MachineID: sqlcIDFromNil(input.MachineID), SourceKinds: sqlcStrings(input.Filters.SourceKinds),
		Providers: input.Filters.Providers, LifecycleStates: sqlcStrings(input.Filters.LifecycleStates),
		ConnectionStates: sqlcStrings(input.Filters.ConnectionStates),
	}
	if !listing.SortAllowed(
		input.List.SortField,
		"name", "created_at", "updated_at", "source_kind", "provider",
		"lifecycle_state", "connection_state",
	) {
		return ListProjectMachineGrantsResult{}, errors.New("unsupported sort")
	}
	if input.List.After.Set {
		params.CursorSet, params.CursorKey, params.CursorID = true, input.List.After.Key, input.List.After.ID
	}
	rows, err := s.q.ListProjectMachineGrants(ctx, params)
	if err != nil {
		return ListProjectMachineGrantsResult{}, fmt.Errorf("list project machine grants: %w", err)
	}
	result := ListProjectMachineGrantsResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Grants = make([]ProjectMachineGrantListRecord, 0, len(rows))
	for _, row := range rows {
		result.Grants = append(result.Grants, ProjectMachineGrantListRecord{
			Grant: ProjectMachineGrantRecord{
				ID: row.ID, OrgID: row.OrgID, ProjectID: row.ProjectID, MachineID: row.MachineID,
				SourceKind:                ProjectMachineGrantSourceKind(row.SourceKind),
				ProjectMachinePoolGrantID: idFromSQLCPtr(row.ProjectMachinePoolGrantID),
				Description:               row.Description,
				IdempotencyKey:            row.IdempotencyKey, Metadata: row.Metadata,
				CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			},
			Machine: MachineSummaryRecord{
				ID: row.MachineID, OrgID: row.OrgID, SourceKind: MachineSourceKind(row.MachineSourceKind),
				DisplayName: row.DisplayName, Description: row.MachineDescription, Provider: row.Provider,
				LifecycleState:  MachineLifecycleState(row.LifecycleState),
				ConnectionState: MachineConnectionState(row.ConnectionState),
				LastObservedAt:  row.LastObservedAt, DeletedAt: row.DeletedAt,
				CreatedAt: row.MachineCreatedAt, UpdatedAt: row.MachineUpdatedAt,
			},
		})
		result.Next = listing.Cursor{Set: true, IsNull: row.SortIsNull, Key: row.SortKey, ID: row.ID}
	}
	return result, nil
}

func (s *Store) ListProjectVisibleMachinesForPrincipal(
	ctx context.Context,
	input ListProjectVisibleMachinesForPrincipalInput,
) (ListProjectVisibleMachinesForPrincipalResult, error) {
	userID, orgAPIKeyID := identitystore.AccountPrincipalIDs(input.Principal)
	if isNilID(input.OrgID) || isNilID(input.ProjectID) || (userID == nil && orgAPIKeyID == nil) {
		return ListProjectVisibleMachinesForPrincipalResult{}, errors.New(
			"org id, project id, and principal are required",
		)
	}
	if input.Limit <= 0 {
		return ListProjectVisibleMachinesForPrincipalResult{}, errors.New("limit must be positive")
	}
	if err := validateMachineSourceKindFilters(input.Filters.SourceKinds); err != nil {
		return ListProjectVisibleMachinesForPrincipalResult{}, err
	}
	input.List = listing.Normalize(input.List)
	params := dbsqlc.ListProjectVisibleMachinesParams{
		OrgID:       input.OrgID,
		ProjectID:   input.ProjectID,
		UserID:      userID,
		OrgApiKeyID: orgAPIKeyID,
		RowLimit:    int64(input.Limit) + 1,
		SortField:   input.List.SortField, SortDesc: input.List.SortDesc, NamePattern: input.List.NamePattern,
		Providers: input.Filters.Providers, SourceKinds: sqlcStrings(input.Filters.SourceKinds),
		LifecycleStates:  sqlcStrings(input.Filters.LifecycleStates),
		ConnectionStates: sqlcStrings(input.Filters.ConnectionStates),
		MachinePoolID:    sqlcIDFromNil(input.Filters.MachinePoolID),
	}
	if !listing.SortAllowed(
		input.List.SortField,
		"name", "created_at", "updated_at", "provider", "source_kind", "lifecycle_state", "connection_state",
	) {
		return ListProjectVisibleMachinesForPrincipalResult{}, errors.New("unsupported sort")
	}
	if input.List.After.Set {
		params.CursorSet, params.CursorKey, params.CursorID = true, input.List.After.Key, input.List.After.ID
	}
	rows, err := s.q.ListProjectVisibleMachines(ctx, params)
	if err != nil {
		return ListProjectVisibleMachinesForPrincipalResult{}, fmt.Errorf("list project visible machines: %w", err)
	}
	result := ListProjectVisibleMachinesForPrincipalResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Machines = make([]ProjectVisibleMachineRecord, 0, len(rows))
	for _, row := range rows {
		result.Machines = append(
			result.Machines,
			ProjectVisibleMachineRecord{
				Machine:         machineSummaryFromProjectVisibleSQLC(row),
				GrantID:         row.GrantID,
				GrantSourceKind: ProjectMachineGrantSourceKind(row.GrantSourceKind),
				CanManage:       row.CanManage,
			},
		)
		result.Next = listing.Cursor{Set: true, IsNull: row.SortIsNull, Key: row.SortKey, ID: row.ID}
	}
	return result, nil
}

func (s *Store) ListActiveProjectMachineGrantsForMachine(
	ctx context.Context,
	orgID, machineID ID,
) ([]ProjectMachineGrantRecord, error) {
	rows, err := s.q.ListActiveProjectMachineGrantsForMachine(
		ctx,
		dbsqlc.ListActiveProjectMachineGrantsForMachineParams{OrgID: orgID, MachineID: machineID},
	)
	if err != nil {
		return nil, fmt.Errorf("list active project machine grants for machine: %w", err)
	}
	out := make([]ProjectMachineGrantRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, projectMachineGrantFromActiveMachine(row))
	}
	return out, nil
}

func validateMachineSourceKindFilters(sourceKinds []MachineSourceKind) error {
	for _, sourceKind := range sourceKinds {
		switch sourceKind {
		case MachineSourceKindBYO, MachineSourceKindPool:
		default:
			return errors.New("source kind must be byo or pool")
		}
	}
	return nil
}

func (s *Store) GetProjectMachineGrant(
	ctx context.Context,
	orgID, projectID, id ID,
) (ProjectMachineGrantRecord, error) {
	row, err := s.q.GetProjectMachineGrant(
		ctx,
		dbsqlc.GetProjectMachineGrantParams{OrgID: orgID, ProjectID: projectID, ID: id},
	)
	if err != nil {
		return ProjectMachineGrantRecord{}, fmt.Errorf("get project machine grant: %w", err)
	}
	return projectMachineGrantFromGet(row), nil
}

func (s *Store) DeleteProjectMachineGrant(
	ctx context.Context,
	orgID, projectID, id ID,
) (ProjectMachineGrantRecord, error) {
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectMachineGrantRecord{}, fmt.Errorf(
			"begin delete project machine grant: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	existing, err := qtx.GetProjectMachineGrant(
		ctx,
		dbsqlc.GetProjectMachineGrantParams{OrgID: orgID, ProjectID: projectID, ID: id},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProjectMachineGrantRecord{}, storeerr.ErrNotFound
		}
		return ProjectMachineGrantRecord{}, fmt.Errorf("get project machine grant for delete: %w", err)
	}
	if ProjectMachineGrantSourceKind(existing.SourceKind) == ProjectMachineGrantSourceKindPool {
		return ProjectMachineGrantRecord{}, storeerr.ErrStateTransitionConflict
	}
	// Process completion joins the grant rows, so it runs before the delete.
	if err := completeExecutionRevokedProcessesTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		executionRevokedProcessScope{
			projectID:             projectID,
			projectMachineGrantID: id,
		},
		"project_machine_grant_revoked",
	); err != nil {
		return ProjectMachineGrantRecord{}, err
	}
	row, err := qtx.DeleteProjectMachineGrant(
		ctx,
		dbsqlc.DeleteProjectMachineGrantParams{
			OrgID:     orgID,
			ProjectID: projectID,
			ID:        id,
		},
	)
	if err != nil {
		return ProjectMachineGrantRecord{}, fmt.Errorf("delete project machine grant: %w", err)
	}
	record := projectMachineGrantFromDelete(row)
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "delete project machine grant"); err != nil {
		return ProjectMachineGrantRecord{}, err
	}
	return record, nil
}
