package executionstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CronTriggerTargetKind string

const (
	CronTriggerTargetAgent        CronTriggerTargetKind = "agent"
	CronTriggerTargetAgentProfile CronTriggerTargetKind = "profile"
)

type CronTriggerTarget struct {
	Kind CronTriggerTargetKind
	ID   ID
}

type CreateCronTriggerInput struct {
	OrgID           ID
	ProjectID       ID
	Name            string
	Target          CronTriggerTarget
	CronExpression  string
	Timezone        string
	MessageTemplate string
	Enabled         bool
	IdempotencyKey  string
}

type UpdateCronTriggerInput struct {
	ProjectID       ID
	TriggerID       ID
	Name            *string
	CronExpression  *string
	Timezone        *string
	MessageTemplate *string
	Enabled         *bool
}

type CronTriggerRecord struct {
	ID              ID                        `json:"id"`
	OrgID           ID                        `json:"org_id"`
	ProjectID       ID                        `json:"project_id"`
	Name            string                    `json:"name"`
	Target          CronTriggerTarget         `json:"target"`
	CronExpression  string                    `json:"cron_expression"`
	Timezone        string                    `json:"timezone"`
	MessageTemplate string                    `json:"message_template"`
	Enabled         bool                      `json:"enabled"`
	LastFiredAt     *time.Time                `json:"last_fired_at"`
	NextFireAfter   *time.Time                `json:"next_fire_after"`
	FailureReport   *CronTriggerFailureReport `json:"failure_report"`
	IdempotencyKey  string                    `json:"-"`
	CreatedAt       time.Time                 `json:"created_at"`
	UpdatedAt       time.Time                 `json:"updated_at"`
	Created         bool                      `json:"-"`
}

type CronTriggerFailureReport struct {
	Message   string    `json:"message"`
	WillRetry bool      `json:"will_retry"`
	FailedAt  time.Time `json:"failed_at"`
}

func (s *Store) CreateCronTrigger(
	ctx context.Context,
	input CreateCronTriggerInput,
) (CronTriggerRecord, error) {
	if isNilID(input.ProjectID) {
		return CronTriggerRecord{}, errors.New("project id is required")
	}
	if input.Name == "" {
		return CronTriggerRecord{}, errors.New("cron trigger name is required")
	}
	if err := resourcename.Validate("cron trigger name", input.Name); err != nil {
		return CronTriggerRecord{}, storeerr.InvalidRequest(err)
	}
	if isNilID(input.Target.ID) {
		return CronTriggerRecord{}, errors.New("cron trigger target is required")
	}
	if input.Target.Kind != CronTriggerTargetAgent && input.Target.Kind != CronTriggerTargetAgentProfile {
		return CronTriggerRecord{}, errors.New("unsupported cron trigger target kind")
	}
	if input.MessageTemplate == "" {
		return CronTriggerRecord{}, errors.New("cron trigger message template is required")
	}
	if input.Timezone == "" {
		input.Timezone = "UTC"
	}
	if err := cronschedule.Validate(input.CronExpression, input.Timezone); err != nil {
		return CronTriggerRecord{}, fmt.Errorf("%s: %w", err.Error(), storeerr.ErrInvalidRequest)
	}
	if err := cronschedule.ValidateMessageTemplate(input.MessageTemplate); err != nil {
		return CronTriggerRecord{}, fmt.Errorf("%s: %w", err.Error(), storeerr.ErrInvalidRequest)
	}
	input.IdempotencyKey = cronTriggerCreateIdempotencyKey(input.IdempotencyKey)

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CronTriggerRecord{}, fmt.Errorf("begin create cron trigger: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)
	project, err := loadProjectTx(ctx, qtx, input.ProjectID)
	if err != nil {
		return CronTriggerRecord{}, err
	}
	input.OrgID = project.OrgID
	switch input.Target.Kind {
	case CronTriggerTargetAgentProfile:
		if _, err := lockAgentProfileTx(ctx, qtx, input.ProjectID, input.Target.ID); err != nil {
			return CronTriggerRecord{}, err
		}
	case CronTriggerTargetAgent:
		if _, err := qtx.LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
			ProjectID: input.ProjectID, ID: input.Target.ID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return CronTriggerRecord{}, storeerr.ErrNotFound
			}
			return CronTriggerRecord{}, fmt.Errorf("lock cron trigger target agent: %w", err)
		}
		agent, err := loadAgentInProjectTx(ctx, tx, input.ProjectID, input.Target.ID)
		if err != nil {
			return CronTriggerRecord{}, err
		}
		if agent.ArchivedAt != nil {
			return CronTriggerRecord{}, fmt.Errorf(
				"cron trigger target agent is archived: %w",
				storeerr.ErrInvalidRequest,
			)
		}
	}
	record, inserted, err := insertCronTriggerTx(ctx, qtx, input)
	if err != nil {
		return CronTriggerRecord{}, err
	}
	if !inserted {
		if err := tx.Commit(ctx); err != nil {
			return CronTriggerRecord{}, fmt.Errorf("commit idempotent create cron trigger: %w", err)
		}
		return record, nil
	}
	if err := lockResourceCreation(ctx, qtx, resourceCronTriggers, input.ProjectID.String()); err != nil {
		return CronTriggerRecord{}, err
	}
	limits, err := resolveResourceLimits(ctx, qtx, input.OrgID)
	if err != nil {
		return CronTriggerRecord{}, err
	}
	triggerCount, err := qtx.CountActiveCronTriggersForProject(
		ctx,
		dbsqlc.CountActiveCronTriggersForProjectParams{ProjectID: input.ProjectID},
	)
	if err != nil {
		return CronTriggerRecord{}, fmt.Errorf("count active cron triggers: %w", err)
	}
	if triggerCount > limits.MaxActiveCronTriggersPerProject {
		return CronTriggerRecord{}, resourceLimitExceeded(
			"active cron triggers",
			limits.MaxActiveCronTriggersPerProject,
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return CronTriggerRecord{}, fmt.Errorf("commit create cron trigger: %w", err)
	}
	return record, nil
}

func (s *Store) GetCronTrigger(ctx context.Context, projectID, id ID) (CronTriggerRecord, error) {
	if isNilID(projectID) || isNilID(id) {
		return CronTriggerRecord{}, errors.New("project and cron trigger are required")
	}
	row, err := s.q.GetCronTrigger(ctx, dbsqlc.GetCronTriggerParams{ProjectID: projectID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return CronTriggerRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return CronTriggerRecord{}, fmt.Errorf("load cron trigger: %w", err)
	}
	return cronTriggerRecordFromSQLC(row)
}

type ListCronTriggersForProjectInput struct {
	ProjectID ID
	Filters   CronTriggerListFilters
	List      listing.Options
	Limit     int
}

type CronTriggerListFilters struct {
	AgentProfileID ID
	AgentID        ID
}

type ListCronTriggersForProjectResult struct {
	Triggers []CronTriggerRecord
	HasMore  bool
	Next     listing.Cursor
}

func (s *Store) ListCronTriggersForProject(
	ctx context.Context,
	input ListCronTriggersForProjectInput,
) (ListCronTriggersForProjectResult, error) {
	if isNilID(input.ProjectID) {
		return ListCronTriggersForProjectResult{}, errors.New("project id is required")
	}
	if input.Limit <= 0 {
		return ListCronTriggersForProjectResult{}, errors.New("limit must be positive")
	}
	input.List = listing.Normalize(input.List)
	if !listing.SortAllowed(input.List.SortField, "name", "created_at", "updated_at") {
		return ListCronTriggersForProjectResult{}, errors.New("unsupported cron trigger list sort")
	}
	params := dbsqlc.ListCronTriggersForProjectParams{
		ProjectID: input.ProjectID, RowLimit: int64(input.Limit) + 1,
		NamePattern: input.List.NamePattern, SortField: input.List.SortField,
		SortDesc: input.List.SortDesc, CursorSet: input.List.After.Set,
		CursorKey: input.List.After.Key, CursorID: input.List.After.ID,
		AgentProfileID: sqlcIDFromNil(input.Filters.AgentProfileID),
		AgentID:        sqlcIDFromNil(input.Filters.AgentID),
	}
	rows, err := s.q.ListCronTriggersForProject(ctx, params)
	if err != nil {
		return ListCronTriggersForProjectResult{}, fmt.Errorf("list cron triggers: %w", err)
	}
	result := ListCronTriggersForProjectResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	if result.HasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		result.Next = listing.Cursor{Set: true, Key: last.SortKey, ID: last.ID}
	}
	result.Triggers = make([]CronTriggerRecord, 0, len(rows))
	for _, row := range rows {
		record, err := cronTriggerRecordFromListSQLC(row)
		if err != nil {
			return ListCronTriggersForProjectResult{}, err
		}
		result.Triggers = append(result.Triggers, record)
	}
	return result, nil
}

func (s *Store) UpdateCronTrigger(
	ctx context.Context,
	input UpdateCronTriggerInput,
) (CronTriggerRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.TriggerID) {
		return CronTriggerRecord{}, errors.New("project and cron trigger are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CronTriggerRecord{}, fmt.Errorf("begin update cron trigger: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)
	record, err := lockCronTriggerTx(ctx, qtx, input.ProjectID, input.TriggerID)
	if err != nil {
		return CronTriggerRecord{}, err
	}
	previous := record
	if input.Name != nil {
		record.Name = *input.Name
	}
	if record.Name == "" {
		return CronTriggerRecord{}, storeerr.InvalidRequest(errors.New("cron trigger name is required"))
	}
	if err := resourcename.Validate("cron trigger name", record.Name); err != nil {
		return CronTriggerRecord{}, storeerr.InvalidRequest(err)
	}
	if input.CronExpression != nil {
		record.CronExpression = *input.CronExpression
	}
	if input.Timezone != nil {
		if *input.Timezone == "" {
			return CronTriggerRecord{}, fmt.Errorf(
				"cron trigger timezone cannot be empty: %w",
				storeerr.ErrInvalidRequest,
			)
		}
		record.Timezone = *input.Timezone
	}
	if input.MessageTemplate != nil {
		if *input.MessageTemplate == "" {
			return CronTriggerRecord{}, fmt.Errorf(
				"cron trigger message template cannot be empty: %w",
				storeerr.ErrInvalidRequest,
			)
		}
		record.MessageTemplate = *input.MessageTemplate
	}
	if input.Enabled != nil {
		record.Enabled = *input.Enabled
	}
	if err := cronschedule.Validate(record.CronExpression, record.Timezone); err != nil {
		return CronTriggerRecord{}, fmt.Errorf("%s: %w", err.Error(), storeerr.ErrInvalidRequest)
	}
	if err := cronschedule.ValidateMessageTemplate(record.MessageTemplate); err != nil {
		return CronTriggerRecord{}, fmt.Errorf("%s: %w", err.Error(), storeerr.ErrInvalidRequest)
	}
	scheduleChanged := record.CronExpression != previous.CronExpression ||
		record.Timezone != previous.Timezone
	nextFireAfter := record.NextFireAfter
	switch {
	case !record.Enabled:
		nextFireAfter = nil
	case scheduleChanged || !previous.Enabled || record.NextFireAfter == nil:
		next, err := cronTriggerNextFireTx(ctx, qtx, record.CronExpression, record.Timezone)
		if err != nil {
			return CronTriggerRecord{}, err
		}
		nextFireAfter = &next
	}
	row, err := qtx.UpdateCronTrigger(ctx, dbsqlc.UpdateCronTriggerParams{
		Name:            record.Name,
		CronExpression:  record.CronExpression,
		Timezone:        record.Timezone,
		MessageTemplate: record.MessageTemplate,
		Enabled:         record.Enabled,
		NextFireAfter:   nextFireAfter,
		ProjectID:       input.ProjectID,
		ID:              input.TriggerID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return CronTriggerRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		if isUniqueViolationOnConstraint(err, "cron_triggers_active_name_idx") {
			return CronTriggerRecord{}, fmt.Errorf(
				"cron trigger name already exists: %w",
				storeerr.ErrConflict,
			)
		}
		return CronTriggerRecord{}, fmt.Errorf("update cron trigger: %w", err)
	}
	updated, err := cronTriggerRecordFromWriteSQLC(dbsqlc.InsertCronTriggerRow(row), record.OrgID)
	if err != nil {
		return CronTriggerRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CronTriggerRecord{}, fmt.Errorf("commit update cron trigger: %w", err)
	}
	return updated, nil
}

func (s *Store) DeleteCronTrigger(ctx context.Context, projectID, id ID) error {
	if isNilID(projectID) || isNilID(id) {
		return errors.New("project and cron trigger are required")
	}
	rows, err := s.q.DeleteCronTrigger(
		ctx,
		dbsqlc.DeleteCronTriggerParams{ProjectID: projectID, ID: id},
	)
	if err != nil {
		return fmt.Errorf("delete cron trigger: %w", err)
	}
	if rows == 0 {
		return storeerr.ErrNotFound
	}
	return nil
}

func insertCronTriggerTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input CreateCronTriggerInput,
) (CronTriggerRecord, bool, error) {
	var nextFireAfter *time.Time
	if input.Enabled {
		next, err := cronTriggerNextFireTx(ctx, qtx, input.CronExpression, input.Timezone)
		if err != nil {
			return CronTriggerRecord{}, false, err
		}
		nextFireAfter = &next
	}
	params := dbsqlc.InsertCronTriggerParams{
		ProjectID:       input.ProjectID,
		Name:            input.Name,
		CronExpression:  input.CronExpression,
		Timezone:        input.Timezone,
		MessageTemplate: input.MessageTemplate,
		Enabled:         input.Enabled,
		NextFireAfter:   nextFireAfter,
		IdempotencyKey:  sqlcTextFromEmpty(input.IdempotencyKey),
	}
	switch input.Target.Kind {
	case CronTriggerTargetAgentProfile:
		params.AgentProfileID = &input.Target.ID
	case CronTriggerTargetAgent:
		params.AgentID = &input.Target.ID
	}
	row, err := qtx.InsertCronTrigger(ctx, params)
	if err == nil {
		record, err := cronTriggerRecordFromWriteSQLC(row, input.OrgID)
		if err != nil {
			return CronTriggerRecord{}, false, err
		}
		record.Created = true
		return record, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		if isUniqueViolationOnConstraint(err, "cron_triggers_active_name_idx") {
			return CronTriggerRecord{}, false, fmt.Errorf(
				"cron trigger name already exists: %w",
				storeerr.ErrConflict,
			)
		}
		return CronTriggerRecord{}, false, fmt.Errorf("create cron trigger: %w", err)
	}
	if input.IdempotencyKey == "" {
		return CronTriggerRecord{}, false, storeerr.ErrIdempotencyConflict
	}
	record, err := loadCronTriggerByIdempotencyKeyTx(ctx, qtx, input.ProjectID, input.IdempotencyKey)
	if err != nil {
		return CronTriggerRecord{}, false, err
	}
	if record.Name != input.Name ||
		record.Target != input.Target ||
		record.CronExpression != input.CronExpression ||
		record.Timezone != input.Timezone ||
		record.MessageTemplate != input.MessageTemplate ||
		record.Enabled != input.Enabled {
		return CronTriggerRecord{}, false, storeerr.ErrIdempotencyConflict
	}
	return record, false, nil
}

func lockCronTriggerTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, id ID,
) (CronTriggerRecord, error) {
	row, err := qtx.GetCronTriggerForUpdate(
		ctx,
		dbsqlc.GetCronTriggerForUpdateParams{ProjectID: projectID, ID: id},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CronTriggerRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return CronTriggerRecord{}, fmt.Errorf("lock cron trigger: %w", err)
	}
	return cronTriggerRecordFromSQLC(dbsqlc.GetCronTriggerRow(row))
}

func loadCronTriggerByIdempotencyKeyTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID ID,
	idempotencyKey string,
) (CronTriggerRecord, error) {
	row, err := qtx.GetCronTriggerByIdempotencyKey(
		ctx,
		dbsqlc.GetCronTriggerByIdempotencyKeyParams{
			ProjectID:      projectID,
			IdempotencyKey: idempotencyKey,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CronTriggerRecord{}, storeerr.ErrIdempotencyConflict
	}
	if err != nil {
		return CronTriggerRecord{}, fmt.Errorf("load idempotent cron trigger: %w", err)
	}
	return cronTriggerRecordFromSQLC(dbsqlc.GetCronTriggerRow(row))
}

type ClaimedCronTrigger struct {
	TriggerID       ID
	ClaimToken      ID
	OrgID           ID
	ProjectID       ID
	Name            string
	Target          CronTriggerTarget
	MessageTemplate string
	DueAt           time.Time
	FiredAt         time.Time
	LastFiredAt     *time.Time
}

type ClaimDueCronTriggersResult struct {
	Claimed  []ClaimedCronTrigger
	Disabled []ID
}

const cronTriggerClaimLease = 5 * time.Minute

func (s *Store) ClaimDueCronTriggers(
	ctx context.Context,
	limit int32,
) (ClaimDueCronTriggersResult, error) {
	if limit <= 0 {
		return ClaimDueCronTriggersResult{}, errors.New("limit must be positive")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ClaimDueCronTriggersResult{}, fmt.Errorf("begin claim due cron triggers: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)
	now, err := qtx.DBNow(ctx)
	if err != nil {
		return ClaimDueCronTriggersResult{}, fmt.Errorf("load database time: %w", err)
	}
	rows, err := qtx.SelectDueCronTriggers(ctx, dbsqlc.SelectDueCronTriggersParams{
		RowLimit: int64(limit),
	})
	if err != nil {
		return ClaimDueCronTriggersResult{}, fmt.Errorf("select due cron triggers: %w", err)
	}
	result := ClaimDueCronTriggersResult{}
	claimedUntil := now.Add(cronTriggerClaimLease)
	for _, row := range rows {
		if _, nextErr := cronschedule.Next(row.CronExpression, row.Timezone, now); nextErr != nil {
			if _, err := qtx.DisableCronTrigger(ctx, dbsqlc.DisableCronTriggerParams{
				ProjectID: row.ProjectID, ID: row.ID,
				FailureMessage: cronTriggerFailureMessage(
					"disabled: evaluate cron schedule: " + nextErr.Error(),
				),
			}); err != nil {
				return ClaimDueCronTriggersResult{}, fmt.Errorf("disable cron trigger: %w", err)
			}
			result.Disabled = append(result.Disabled, row.ID)
			continue
		}
		claimToken, err := uuid.NewV7()
		if err != nil {
			return ClaimDueCronTriggersResult{}, fmt.Errorf("generate cron trigger claim token: %w", err)
		}
		if _, err := qtx.ClaimCronTrigger(ctx, dbsqlc.ClaimCronTriggerParams{
			ProjectID: row.ProjectID, ID: row.ID,
			ClaimedUntil: &claimedUntil, ClaimToken: &claimToken,
		}); err != nil {
			return ClaimDueCronTriggersResult{}, fmt.Errorf("claim cron trigger: %w", err)
		}
		result.Claimed = append(result.Claimed, ClaimedCronTrigger{
			TriggerID:       row.ID,
			ClaimToken:      claimToken,
			OrgID:           row.OrgID,
			ProjectID:       row.ProjectID,
			Name:            row.Name,
			Target:          cronTriggerTargetFromColumns(row.AgentProfileID, row.AgentID),
			MessageTemplate: row.MessageTemplate,
			DueAt:           nullableTimeToZero(row.NextFireAfter),
			FiredAt:         now,
			LastFiredAt:     row.LastFiredAt,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return ClaimDueCronTriggersResult{}, fmt.Errorf("commit claim due cron triggers: %w", err)
	}
	return result, nil
}

type CompleteCronTriggerFiringInput struct {
	ProjectID  ID
	TriggerID  ID
	ClaimToken ID
	Fired      bool
}

func (s *Store) CompleteCronTriggerFiring(
	ctx context.Context,
	input CompleteCronTriggerFiringInput,
) error {
	if isNilID(input.ProjectID) || isNilID(input.TriggerID) || isNilID(input.ClaimToken) {
		return errors.New("project, cron trigger, and claim token are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin complete cron trigger firing: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)
	row, err := qtx.GetCronTriggerForUpdate(
		ctx,
		dbsqlc.GetCronTriggerForUpdateParams{ProjectID: input.ProjectID, ID: input.TriggerID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock cron trigger for completion: %w", err)
	}
	record, err := cronTriggerRecordFromSQLC(dbsqlc.GetCronTriggerRow(row))
	if err != nil {
		return err
	}
	var nextFireAfter *time.Time
	if record.Enabled {
		next, err := cronTriggerNextFireTx(ctx, qtx, record.CronExpression, record.Timezone)
		if err != nil {
			return err
		}
		nextFireAfter = &next
	}
	rows, err := qtx.CompleteCronTriggerFiring(ctx, dbsqlc.CompleteCronTriggerFiringParams{
		ProjectID:     input.ProjectID,
		ID:            input.TriggerID,
		ClaimToken:    &input.ClaimToken,
		NextFireAfter: nextFireAfter,
		Fired:         input.Fired,
	})
	if err != nil {
		return fmt.Errorf("complete cron trigger firing: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("cron trigger claim was taken over: %w", storeerr.ErrConflict)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit complete cron trigger firing: %w", err)
	}
	return nil
}

const maxCronTriggerFailureMessageBytes = 4 * 1024

type CronTriggerFailureParams struct {
	ProjectID  ID
	TriggerID  ID
	ClaimToken ID
	Message    string
	WillRetry  bool
}

func (s *Store) RecordCronTriggerFailure(
	ctx context.Context,
	params CronTriggerFailureParams,
) error {
	if isNilID(params.ProjectID) || isNilID(params.TriggerID) || isNilID(params.ClaimToken) {
		return errors.New("project, cron trigger, and claim token are required")
	}
	rows, err := s.q.RecordCronTriggerFailure(ctx, dbsqlc.RecordCronTriggerFailureParams{
		ProjectID:      params.ProjectID,
		ID:             params.TriggerID,
		ClaimToken:     &params.ClaimToken,
		FailureMessage: cronTriggerFailureMessage(params.Message),
		WillRetry:      params.WillRetry,
	})
	if err != nil {
		return fmt.Errorf("record cron trigger failure: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("cron trigger claim was taken over: %w", storeerr.ErrConflict)
	}
	return nil
}

func cronTriggerFailureMessage(message string) string {
	if len(message) > maxCronTriggerFailureMessageBytes {
		message = strings.ToValidUTF8(message[:maxCronTriggerFailureMessageBytes], "")
	}
	return message
}

func cronTriggerNextFireTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	expression, timezone string,
) (time.Time, error) {
	now, err := qtx.DBNow(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("load database time: %w", err)
	}
	next, err := cronschedule.Next(expression, timezone, now)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", err.Error(), storeerr.ErrInvalidRequest)
	}
	return next, nil
}

func cronTriggerTargetFromColumns(agentProfileID, agentID *ID) CronTriggerTarget {
	if agentProfileID != nil {
		return CronTriggerTarget{Kind: CronTriggerTargetAgentProfile, ID: *agentProfileID}
	}
	return CronTriggerTarget{Kind: CronTriggerTargetAgent, ID: idFromSQLCPtr(agentID)}
}

func cronTriggerCreateIdempotencyKey(key string) string {
	if key == "" {
		return ""
	}
	return "cron_trigger.create:" + key
}
