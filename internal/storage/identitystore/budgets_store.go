package identitystore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type BudgetRecord struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	ProjectID      *uuid.UUID
	AgentProfileID *uuid.UUID
	AgentID        *uuid.UUID
	Interval       string
	AmountUSD      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Usage          *BudgetUsage
}

type BudgetUsage struct {
	AmountUSD string
}

type CreateBudgetInput struct {
	OrgID          uuid.UUID
	ProjectID      *uuid.UUID
	AgentProfileID *uuid.UUID
	AgentID        *uuid.UUID
	Interval       string
	AmountUSD      string
}

type ListBudgetsInput struct {
	OrgID          uuid.UUID
	FilterScope    bool
	ProjectID      *uuid.UUID
	AgentProfileID *uuid.UUID
	AgentID        *uuid.UUID
	Limit          int
	After          listing.KeysetCursor
}

type ListBudgetsResult struct {
	Budgets []BudgetRecord
	HasMore bool
}

func (s *Store) CreateBudget(ctx context.Context, input CreateBudgetInput) (BudgetRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return BudgetRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := enterBudgetScope(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return BudgetRecord{}, err
	}
	qtx := s.q.WithTx(tx)
	if err := validateBudgetScope(ctx, qtx, input); err != nil {
		return BudgetRecord{}, err
	}
	row, err := qtx.InsertBudget(ctx, dbsqlc.InsertBudgetParams{
		OrgID: input.OrgID, ProjectID: input.ProjectID, AgentProfileID: input.AgentProfileID, AgentID: input.AgentID,
		Interval: input.Interval, AmountUsd: input.AmountUSD,
	})
	if storeutil.IsUniqueViolationOnConstraint(err, "budgets_active_scope_interval_idx") {
		return BudgetRecord{}, storeerr.Tag(storeerr.ErrConflict,
			errors.New("a budget for this interval already exists"))
	}
	if err != nil {
		return BudgetRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BudgetRecord{}, err
	}
	return budgetRecord(dbsqlc.UpdateBudgetAmountRow(row), nil)
}

func (s *Store) GetBudget(ctx context.Context, orgID, id uuid.UUID) (BudgetRecord, error) {
	row, err := s.q.GetBudget(ctx, dbsqlc.GetBudgetParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return BudgetRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return BudgetRecord{}, err
	}
	return budgetRecordWithUsage(dbsqlc.ListBudgetsRow(row))
}

func (s *Store) ListBudgets(ctx context.Context, input ListBudgetsInput) (ListBudgetsResult, error) {
	if input.Limit < 1 || input.Limit > 100 {
		return ListBudgetsResult{}, storeerr.InvalidRequest(errors.New("limit between 1 and 100 is required"))
	}
	if input.ProjectID != nil {
		_, err := s.q.GetProject(ctx, dbsqlc.GetProjectParams{OrgID: input.OrgID, ID: *input.ProjectID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ListBudgetsResult{}, storeerr.ErrNotFound
		}
		if err != nil {
			return ListBudgetsResult{}, err
		}
	}
	params := dbsqlc.ListBudgetsParams{
		OrgID: input.OrgID, FilterScope: input.FilterScope, ProjectID: input.ProjectID,
		AgentProfileID: input.AgentProfileID, AgentID: input.AgentID, RowLimit: int32(input.Limit) + 1,
	}
	if input.After.Set {
		params.CursorCreatedAt = &input.After.CreatedAt
		params.CursorID = &input.After.ID
	}
	rows, err := s.q.ListBudgets(ctx, params)
	if err != nil {
		return ListBudgetsResult{}, err
	}
	result := ListBudgetsResult{HasMore: len(rows) > input.Limit}
	if result.HasMore {
		rows = rows[:input.Limit]
	}
	result.Budgets = make([]BudgetRecord, 0, len(rows))
	for _, row := range rows {
		record, err := budgetRecordWithUsage(row)
		if err != nil {
			return ListBudgetsResult{}, err
		}
		result.Budgets = append(result.Budgets, record)
	}
	return result, nil
}

func (s *Store) UpdateBudgetAmount(ctx context.Context, orgID, id uuid.UUID, amountUSD string) (BudgetRecord, error) {
	current, err := s.GetBudget(ctx, orgID, id)
	if err != nil {
		return BudgetRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return BudgetRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := enterBudgetScope(ctx, tx, orgID, current.ProjectID); err != nil {
		return BudgetRecord{}, err
	}
	row, err := s.q.WithTx(tx).UpdateBudgetAmount(ctx, dbsqlc.UpdateBudgetAmountParams{
		OrgID: orgID, ID: id, AmountUsd: amountUSD,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return BudgetRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return BudgetRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BudgetRecord{}, err
	}
	return budgetRecord(row, nil)
}

func (s *Store) DeleteBudget(ctx context.Context, orgID, id uuid.UUID) error {
	count, err := s.q.DeleteBudget(ctx, dbsqlc.DeleteBudgetParams{OrgID: orgID, ID: id})
	if err != nil {
		return err
	}
	if count == 0 {
		return storeerr.ErrNotFound
	}
	return nil
}

func (s *Store) MergeDailyModelCosts(ctx context.Context, limit int32) (int64, error) {
	return s.q.MergeDailyModelCosts(ctx, dbsqlc.MergeDailyModelCostsParams{LimitCount: limit})
}

func (s *Store) CountPendingModelCosts(ctx context.Context, limit int32) (int64, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, fmt.Errorf("count pending model costs: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '3s'"); err != nil {
		return 0, fmt.Errorf("count pending model costs: %w", err)
	}
	count, err := dbsqlc.New(tx).CountPendingModelCosts(ctx, dbsqlc.CountPendingModelCostsParams{RowLimit: limit})
	if err != nil {
		return 0, fmt.Errorf("count pending model costs: %w", err)
	}
	return count, nil
}

type BudgetAttachment struct {
	Interval  string
	AmountUSD string
}

func CreateBudgetsTx(
	ctx context.Context,
	tx dbsqlc.DBTX,
	scope CreateBudgetInput,
	attachments []BudgetAttachment,
) error {
	q := dbsqlc.New(tx)
	for _, attachment := range attachments {
		if _, err := q.InsertBudget(ctx, dbsqlc.InsertBudgetParams{
			OrgID: scope.OrgID, ProjectID: scope.ProjectID, AgentProfileID: scope.AgentProfileID,
			AgentID: scope.AgentID, Interval: attachment.Interval, AmountUsd: attachment.AmountUSD,
		}); err != nil {
			return fmt.Errorf("create budget: %w", err)
		}
	}
	return nil
}

func validateBudgetScope(ctx context.Context, q *dbsqlc.Queries, input CreateBudgetInput) error {
	if input.AgentProfileID == nil && input.AgentID == nil {
		return nil
	}
	if input.ProjectID == nil {
		return storeerr.InvalidRequest(errors.New("profile and agent budgets need a project"))
	}
	switch {
	case input.AgentProfileID != nil:
		_, err := q.LockAgentProfile(ctx, dbsqlc.LockAgentProfileParams{
			ProjectID: *input.ProjectID, ProfileID: *input.AgentProfileID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return err
	case input.AgentID != nil:
		agent, err := q.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{
			ProjectID: *input.ProjectID, ID: *input.AgentID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		if err != nil {
			return err
		}
		if agent.ParentAgentID != nil {
			return storeerr.InvalidRequest(errors.New("budgets apply to top-level agents, not subagents"))
		}
	}
	return nil
}

func enterBudgetScope(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, projectID *uuid.UUID) error {
	if projectID == nil {
		return lifecyclelock.EnterActiveOrganization(ctx, tx, orgID)
	}
	return lifecyclelock.EnterActiveProject(ctx, tx, orgID, *projectID)
}

func budgetRecordWithUsage(row dbsqlc.ListBudgetsRow) (BudgetRecord, error) {
	spent, ok := modelenvelope.ParseCostTotalUSD(row.SpentUsd)
	if !ok {
		return BudgetRecord{}, errors.New("invalid budget spend")
	}
	return budgetRecord(dbsqlc.UpdateBudgetAmountRow{
		ID: row.ID, OrgID: row.OrgID, ProjectID: row.ProjectID, AgentProfileID: row.AgentProfileID,
		AgentID: row.AgentID, Interval: row.Interval, AmountUsd: row.AmountUsd,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, &BudgetUsage{AmountUSD: string(spent)})
}

func budgetRecord(row dbsqlc.UpdateBudgetAmountRow, usage *BudgetUsage) (BudgetRecord, error) {
	amount, ok := modelenvelope.ParseProviderReportedCostUSD(row.AmountUsd)
	if !ok {
		return BudgetRecord{}, errors.New("invalid budget amount")
	}
	return BudgetRecord{
		ID: row.ID, OrgID: row.OrgID, ProjectID: row.ProjectID, AgentProfileID: row.AgentProfileID,
		AgentID: row.AgentID, Interval: row.Interval, AmountUSD: string(amount),
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Usage: usage,
	}, nil
}
