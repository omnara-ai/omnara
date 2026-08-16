package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateAgentProfile(
	ctx context.Context,
	input CreateAgentProfileInput,
) (AgentProfileRecord, error) {
	if isNilID(input.ProjectID) {
		return AgentProfileRecord{}, errors.New("project id is required")
	}
	if input.Name == "" {
		return AgentProfileRecord{}, errors.New("agent profile name is required")
	}
	if err := resourcename.Validate("agent profile name", input.Name); err != nil {
		return AgentProfileRecord{}, storeerr.InvalidRequest(err)
	}
	if isNilID(input.CurrentConfigID) {
		return AgentProfileRecord{}, errors.New("current config is required")
	}
	input.IdempotencyKey = agentProfileCreateIdempotencyKey(input.IdempotencyKey)

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentProfileRecord{}, fmt.Errorf("begin create agent profile: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)
	project, err := loadProjectTx(ctx, qtx, input.ProjectID)
	if err != nil {
		return AgentProfileRecord{}, err
	}
	input.OrgID = project.OrgID
	config, err := loadAgentConfigTx(ctx, qtx, input.ProjectID, input.CurrentConfigID)
	if err != nil {
		return AgentProfileRecord{}, err
	}
	record, inserted, err := insertAgentProfileTx(ctx, qtx, input)
	if err != nil {
		return AgentProfileRecord{}, err
	}
	if !inserted {
		if err := tx.Commit(ctx); err != nil {
			return AgentProfileRecord{}, fmt.Errorf(
				"commit idempotent create agent profile: %w",
				err,
			)
		}
		return record, nil
	}
	if err := lockResourceCreation(ctx, qtx, resourceAgentProfiles, input.ProjectID.String()); err != nil {
		return AgentProfileRecord{}, err
	}
	limits, err := resolveResourceLimits(ctx, qtx, input.OrgID)
	if err != nil {
		return AgentProfileRecord{}, err
	}
	profileCount, err := qtx.CountActiveAgentProfilesForProject(
		ctx,
		dbsqlc.CountActiveAgentProfilesForProjectParams{ProjectID: input.ProjectID},
	)
	if err != nil {
		return AgentProfileRecord{}, fmt.Errorf("count active agent profiles: %w", err)
	}
	if profileCount > limits.MaxActiveAgentProfilesPerProject {
		return AgentProfileRecord{}, resourceLimitExceeded(
			"active agent profiles",
			limits.MaxActiveAgentProfilesPerProject,
		)
	}

	record.CurrentConfig = config
	if err := tx.Commit(ctx); err != nil {
		return AgentProfileRecord{}, fmt.Errorf("commit create agent profile: %w", err)
	}
	return record, nil
}

type RetargetAgentProfileInput struct {
	ProjectID               ID
	ProfileID               ID
	ExpectedCurrentConfigID ID
	ConfigID                ID
	Reason                  string
	IdempotencyKey          string
}

func (s *Store) RetargetAgentProfile(
	ctx context.Context,
	input RetargetAgentProfileInput,
) (AgentProfileRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.ProfileID) {
		return AgentProfileRecord{}, errors.New("project and profile are required")
	}
	if isNilID(input.ExpectedCurrentConfigID) {
		return AgentProfileRecord{}, errors.New("expected current config is required")
	}
	if isNilID(input.ConfigID) {
		return AgentProfileRecord{}, errors.New("agent config is required")
	}
	if input.Reason == "" {
		input.Reason = "retarget"
	}
	input.IdempotencyKey = agentProfileRetargetIdempotencyKey(input.IdempotencyKey)

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentProfileRecord{}, fmt.Errorf("begin retarget agent profile: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)
	profile, err := lockAgentProfileTx(ctx, qtx, input.ProjectID, input.ProfileID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return AgentProfileRecord{}, fmt.Errorf("agent profile not found: %w", storeerr.ErrNotFound)
	}
	if err != nil {
		return AgentProfileRecord{}, err
	}
	config, err := loadAgentConfigTx(ctx, qtx, input.ProjectID, input.ConfigID)
	if err != nil {
		return AgentProfileRecord{}, err
	}
	if input.IdempotencyKey != "" {
		version, found, err := loadAgentProfileVersionByIdempotencyKeyMaybeTx(
			ctx,
			qtx,
			input.ProjectID,
			input.ProfileID,
			input.IdempotencyKey,
		)
		if err != nil {
			return AgentProfileRecord{}, err
		}
		if found {
			if version.Reason != input.Reason {
				return AgentProfileRecord{}, storeerr.ErrIdempotencyConflict
			}
			if version.AgentConfigID != input.ConfigID {
				return AgentProfileRecord{}, storeerr.ErrIdempotencyConflict
			}
			if profile.CurrentConfigID != version.AgentConfigID {
				return AgentProfileRecord{}, storeerr.ErrIdempotencyConflict
			}
			profile.CurrentConfig = config
			if err := tx.Commit(ctx); err != nil {
				return AgentProfileRecord{}, fmt.Errorf(
					"commit idempotent retarget agent profile: %w",
					err,
				)
			}
			return profile, nil
		}
	}
	if profile.CurrentConfigID != input.ExpectedCurrentConfigID {
		return AgentProfileRecord{}, fmt.Errorf(
			"agent profile current config changed: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if config.ID == profile.CurrentConfigID {
		// Profile version rows are the idempotency ledger, so a no-op retarget
		// deliberately does not consume the supplied key.
		profile.CurrentConfig = config
		if err := tx.Commit(ctx); err != nil {
			return AgentProfileRecord{}, fmt.Errorf("commit no-op retarget agent profile: %w", err)
		}
		return profile, nil
	}
	row, err := qtx.RetargetAgentProfile(
		ctx,
		dbsqlc.RetargetAgentProfileParams{
			CurrentConfigID:         config.ID,
			ExpectedCurrentConfigID: input.ExpectedCurrentConfigID,
			ProjectID:               input.ProjectID,
			ProfileID:               input.ProfileID,
			Reason:                  input.Reason,
			IdempotencyKey:          sqlcTextFromEmpty(input.IdempotencyKey),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentProfileRecord{}, fmt.Errorf(
			"agent profile current config changed: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if err != nil {
		return AgentProfileRecord{}, fmt.Errorf("retarget agent profile: %w", err)
	}
	record := agentProfileRecordFromRetargetSQLC(row, profile.OrgID)
	record.CurrentConfig = config
	if err := tx.Commit(ctx); err != nil {
		return AgentProfileRecord{}, fmt.Errorf("commit retarget agent profile: %w", err)
	}
	return record, nil
}

func (s *Store) GetAgentProfile(ctx context.Context, projectID, id ID) (AgentProfileRecord, error) {
	if isNilID(projectID) {
		return AgentProfileRecord{}, errors.New("project id is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentProfileRecord{}, fmt.Errorf("begin get agent: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)
	record, err := loadAgentProfileTx(ctx, qtx, projectID, id)
	if err != nil {
		return AgentProfileRecord{}, err
	}
	config, err := loadAgentConfigTx(ctx, qtx, projectID, record.CurrentConfigID)
	if err != nil {
		return AgentProfileRecord{}, err
	}
	record.CurrentConfig = config
	if err := tx.Commit(ctx); err != nil {
		return AgentProfileRecord{}, fmt.Errorf("commit get agent: %w", err)
	}
	return record, nil
}

type ListAgentProfilesForProjectInput struct {
	ProjectID ID
	Filters   AgentProfileListFilters
	List      listing.Options
	Limit     int
}

type AgentProfileListFilters struct {
	ModelProviderConfigID ID
	ConfiguredModelID     ID
	APIFormats            []string
	APIVariants           []string
}

type ListAgentProfilesForProjectResult struct {
	Profiles []AgentProfileRecord
	HasMore  bool
	Next     listing.Cursor
}

// ListAgentProfilesForProject returns one newest-first page of agent profiles.
func (s *Store) ListAgentProfilesForProject(
	ctx context.Context,
	input ListAgentProfilesForProjectInput,
) (ListAgentProfilesForProjectResult, error) {
	if isNilID(input.ProjectID) {
		return ListAgentProfilesForProjectResult{}, errors.New("project id is required")
	}
	if input.Limit <= 0 {
		return ListAgentProfilesForProjectResult{}, errors.New("limit must be positive")
	}
	input.List = listing.Normalize(input.List)
	if !listing.SortAllowed(
		input.List.SortField,
		"name", "created_at", "updated_at", "model_provider", "model", "api_format", "api_variant",
	) {
		return ListAgentProfilesForProjectResult{}, errors.New("unsupported agent profile list sort")
	}
	params := dbsqlc.ListAgentProfilesForProjectParams{
		ProjectID: input.ProjectID, RowLimit: int64(input.Limit) + 1,
		NamePattern: input.List.NamePattern, SortField: input.List.SortField,
		SortDesc: input.List.SortDesc, CursorSet: input.List.After.Set,
		CursorKey: input.List.After.Key, CursorID: input.List.After.ID,
		ModelProviderConfigID: sqlcIDFromNil(input.Filters.ModelProviderConfigID),
		ConfiguredModelID:     sqlcIDFromNil(input.Filters.ConfiguredModelID),
		ApiFormats:            input.Filters.APIFormats, ApiVariants: input.Filters.APIVariants,
	}
	rows, err := s.q.ListAgentProfilesForProject(ctx, params)
	if err != nil {
		return ListAgentProfilesForProjectResult{}, fmt.Errorf("list agent profiles: %w", err)
	}
	result := ListAgentProfilesForProjectResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	if result.HasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		result.Next = listing.Cursor{Set: true, Key: last.SortKey, ID: last.ID}
	}
	result.Profiles = make([]AgentProfileRecord, 0, len(rows))
	for _, row := range rows {
		record := agentProfileRecordFromListForProjectSQLC(row)
		result.Profiles = append(result.Profiles, record)
	}
	return result, nil
}

type ListRecentAgentProfilesForProjectsInput struct {
	ProjectIDs []ID
	Limit      int
}

// ListRecentAgentProfilesForProjects returns the most recently updated agent
// profiles across the given projects, newest first.
func (s *Store) ListRecentAgentProfilesForProjects(
	ctx context.Context,
	input ListRecentAgentProfilesForProjectsInput,
) ([]AgentProfileRecord, error) {
	if input.Limit <= 0 {
		return nil, errors.New("limit must be positive")
	}
	if len(input.ProjectIDs) == 0 {
		return []AgentProfileRecord{}, nil
	}
	rows, err := s.q.ListRecentAgentProfilesForProjects(ctx, dbsqlc.ListRecentAgentProfilesForProjectsParams{
		ProjectIds: input.ProjectIDs,
		RowLimit:   int64(input.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list recent agent profiles: %w", err)
	}
	records := make([]AgentProfileRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, agentProfileRecordFromListRecentForProjectsSQLC(row))
	}
	return records, nil
}

func (s *Store) DeleteAgentProfile(ctx context.Context, projectID, id ID) error {
	if isNilID(projectID) || isNilID(id) {
		return errors.New("project and agent profile are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin delete agent profile: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := lockAgentProfileTx(ctx, qtx, projectID, id); err != nil {
		return err
	}
	hasInstall, err := qtx.AgentProfileHasIntegrationInstall(ctx, dbsqlc.AgentProfileHasIntegrationInstallParams{
		ProjectID: projectID, ProfileID: &id,
	})
	if err != nil {
		return fmt.Errorf("check agent profile integration installs: %w", err)
	}
	if hasInstall {
		return fmt.Errorf("agent profile is referenced by an integration install: %w", storeerr.ErrConflict)
	}
	rows, err := qtx.DeleteAgentProfile(
		ctx,
		dbsqlc.DeleteAgentProfileParams{ProjectID: projectID, ProfileID: id},
	)
	if err != nil {
		return fmt.Errorf("delete agent profile: %w", err)
	}
	if rows == 0 {
		return storeerr.ErrNotFound
	}
	if err := qtx.DeleteAgentProfileVersions(ctx, dbsqlc.DeleteAgentProfileVersionsParams{
		ProjectID: projectID, ProfileID: id,
	}); err != nil {
		return fmt.Errorf("delete agent profile versions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete agent profile: %w", err)
	}
	return nil
}

type CreateAgentProfileInput struct {
	OrgID           ID
	ProjectID       ID
	Name            string
	CurrentConfigID ID
	IdempotencyKey  string
}

type AgentProfileRecord struct {
	ID                ID                `json:"id"`
	OrgID             ID                `json:"org_id"`
	ProjectID         ID                `json:"project_id"`
	Name              string            `json:"name"`
	CurrentConfigID   ID                `json:"current_config_id"`
	CurrentGeneration int               `json:"current_generation"`
	IdempotencyKey    string            `json:"-"`
	CurrentConfig     AgentConfigRecord `json:"current_config"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
	Created           bool              `json:"-"`
}

func insertAgentProfileTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input CreateAgentProfileInput,
) (AgentProfileRecord, bool, error) {
	row, err := qtx.InsertAgentProfile(
		ctx,
		dbsqlc.InsertAgentProfileParams{
			ProjectID:       input.ProjectID,
			Name:            input.Name,
			CurrentConfigID: input.CurrentConfigID,
			IdempotencyKey:  sqlcTextFromEmpty(input.IdempotencyKey),
		},
	)
	if err == nil {
		record := agentProfileRecordFromInsertSQLC(row, input.OrgID)
		record.Created = true
		return record, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		if isUniqueViolationOnConstraint(err, "agent_profiles_active_name_idx") {
			return AgentProfileRecord{}, false, fmt.Errorf(
				"agent profile name already exists: %w",
				storeerr.ErrConflict,
			)
		}
		return AgentProfileRecord{}, false, fmt.Errorf("create agent profile: %w", err)
	}
	if input.IdempotencyKey == "" {
		return AgentProfileRecord{}, false, storeerr.ErrIdempotencyConflict
	}
	record, err := loadAgentProfileByIdempotencyKeyTx(
		ctx,
		qtx,
		input.ProjectID,
		input.IdempotencyKey,
	)
	if err != nil {
		return AgentProfileRecord{}, false, err
	}
	if record.ProjectID != input.ProjectID || record.Name != input.Name {
		return AgentProfileRecord{}, false, storeerr.ErrIdempotencyConflict
	}
	initialVersion, err := qtx.GetAgentProfileVersionByGeneration(
		ctx,
		dbsqlc.GetAgentProfileVersionByGenerationParams{
			ProjectID:  record.ProjectID,
			ProfileID:  record.ID,
			Generation: 1,
		},
	)
	if err != nil {
		return AgentProfileRecord{}, false, fmt.Errorf(
			"load initial agent profile version: %w",
			err,
		)
	}
	if initialVersion.AgentConfigID != input.CurrentConfigID {
		return AgentProfileRecord{}, false, storeerr.ErrIdempotencyConflict
	}
	currentConfig, err := loadAgentConfigTx(ctx, qtx, record.ProjectID, record.CurrentConfigID)
	if err != nil {
		return AgentProfileRecord{}, false, err
	}
	record.CurrentConfig = currentConfig
	return record, false, nil
}

func loadAgentProfileTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, id ID,
) (AgentProfileRecord, error) {
	row, err := qtx.GetAgentProfile(ctx, dbsqlc.GetAgentProfileParams{ProjectID: projectID, ID: id})
	if err != nil {
		return AgentProfileRecord{}, fmt.Errorf("load agent profile: %w", err)
	}
	return agentProfileRecordFromGetSQLC(row), nil
}

func lockAgentProfileTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, id ID,
) (AgentProfileRecord, error) {
	_, err := qtx.LockAgentProfile(
		ctx,
		dbsqlc.LockAgentProfileParams{ProjectID: projectID, ProfileID: id},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentProfileRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return AgentProfileRecord{}, fmt.Errorf("lock agent profile: %w", err)
	}
	return loadAgentProfileTx(ctx, qtx, projectID, id)
}

func loadAgentProfileByIdempotencyKeyTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID ID,
	idempotencyKey string,
) (AgentProfileRecord, error) {
	row, err := qtx.GetAgentProfileByIdempotencyKey(
		ctx,
		dbsqlc.GetAgentProfileByIdempotencyKeyParams{
			ProjectID:      projectID,
			IdempotencyKey: idempotencyKey,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// The key is held by a soft-deleted profile; the key stays consumed.
		return AgentProfileRecord{}, storeerr.ErrIdempotencyConflict
	}
	if err != nil {
		return AgentProfileRecord{}, fmt.Errorf("load idempotent agent profile: %w", err)
	}
	return agentProfileRecordFromGetByIdempotencySQLC(row), nil
}

func loadAgentProfileVersionByIdempotencyKeyMaybeTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, profileID ID,
	idempotencyKey string,
) (dbsqlc.GetAgentProfileVersionByIdempotencyKeyRow, bool, error) {
	row, err := qtx.GetAgentProfileVersionByIdempotencyKey(
		ctx,
		dbsqlc.GetAgentProfileVersionByIdempotencyKeyParams{
			ProjectID:      projectID,
			ProfileID:      profileID,
			IdempotencyKey: idempotencyKey,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return dbsqlc.GetAgentProfileVersionByIdempotencyKeyRow{}, false, nil
	}
	if err != nil {
		return dbsqlc.GetAgentProfileVersionByIdempotencyKeyRow{}, false, fmt.Errorf(
			"load idempotent agent profile version: %w",
			err,
		)
	}
	return row, true, nil
}

func loadProjectTx(ctx context.Context, qtx *dbsqlc.Queries, id ID) (identitystore.ProjectRecord, error) {
	row, err := qtx.GetProjectByID(ctx, dbsqlc.GetProjectByIDParams{ID: id})
	if err != nil {
		return identitystore.ProjectRecord{}, fmt.Errorf("load project: %w", err)
	}
	return identitystore.ProjectRecord{
		ID:             row.ID,
		OrgID:          row.OrgID,
		Name:           row.Name,
		IdempotencyKey: row.IdempotencyKey,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}

func agentProfileCreateIdempotencyKey(key string) string {
	if key == "" {
		return ""
	}
	return "agent_profile.create:" + key
}

func agentProfileRetargetIdempotencyKey(key string) string {
	if key == "" {
		return ""
	}
	return "agent_profile.retarget:" + key
}
