package executionstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type DatabaseToolPlan func(context.Context, *ToolCallReader) (ToolCallCompletionInput, bool, error)

type DatabaseToolPlanner func(ClaimedAgentWork, ToolCallRecord) (DatabaseToolPlan, bool, error)

func (s *Store) completeClaimedDatabaseTools(
	ctx context.Context, unit *agentexecution.Unit, claim ClaimedAgentWork, planner DatabaseToolPlanner,
) ([]*toolCallTransaction, error) {
	if planner == nil || claim.Tool.Prepared == nil || claim.Tool.Prepared.FirstCall == nil {
		return nil, nil
	}
	prepared := claim.Tool.Prepared
	if prepared.databaseBlocked {
		return nil, nil
	}
	calls := prepared.databaseCalls
	plans := make([]DatabaseToolPlan, len(calls))
	transactions := make([]*toolCallTransaction, len(calls))
	q := dbsqlc.New(unit.DB())
	var err error
	for i, call := range calls {
		if call.Type != "built_in" || call.State != ToolCallStateAwaitingAuthorization && call.State != ToolCallStateReady {
			return nil, nil
		}
		if call.TurnID != claim.Tool.TurnID || call.ModelCallContextID != claim.Tool.ModelCallContextID ||
			call.SourceEventID != claim.Tool.SourceEventID {
			return nil, storeerr.ErrStateTransitionConflict
		}
		var eligible bool
		plans[i], eligible, err = planner(claim, call)
		if err != nil || !eligible {
			return nil, databaseToolFallbackError(err)
		}
		if plans[i] == nil {
			return nil, errors.New("database tool planner returned no plan")
		}
		transactions[i] = &toolCallTransaction{
			unit: unit, store: s, tx: unit.DB(), q: q, notifications: unit.Notifications(),
			input: ExecuteToolCallInput{
				RootAgentID: claim.RootAgentID, ProjectID: claim.ProjectID, AgentID: claim.AgentID,
				RuntimeLockID: claim.RuntimeLock.ID, ToolCallID: call.ID, Authorize: true,
				BatchOutputID: claim.Tool.ModelOutputID, RequireCompletion: true,
			},
			observedState: call.State,
		}
	}
	completions := make([]ToolCallCompletionInput, len(plans))
	for i, plan := range plans {
		var complete bool
		completions[i], complete, err = plan(ctx, &ToolCallReader{transaction: transactions[i]})
		if err != nil || !complete {
			return nil, databaseToolFallbackError(err)
		}
	}
	for i, tx := range transactions {
		if _, err := tx.completeToolCall(ctx, completions[i]); err != nil {
			return nil, err
		}
		if !tx.applied {
			return nil, storeerr.ErrStateTransitionConflict
		}
	}
	if len(transactions) == 0 {
		return nil, nil
	}
	h, err := unit.Handle(claim.ProjectID, claim.AgentID)
	if err != nil {
		return nil, err
	}
	finished, transferred, err := h.FinishToolBatch(ctx, claim.RuntimeLock.ID, claim.Tool.ModelOutputID)
	if err != nil {
		return nil, err
	}
	if !finished || transferred {
		return nil, errors.New("database tool batch did not finish its claim")
	}
	return transactions, nil
}

func databaseToolFallbackError(err error) error {
	var postgres *pgconn.PgError
	if shouldDeferClaim(err) && !errors.As(err, &postgres) &&
		!errors.Is(err, agentexecution.ErrUnitAborted) && !errors.Is(err, agentexecution.ErrUnitClosed) {
		return nil
	}
	return err
}
