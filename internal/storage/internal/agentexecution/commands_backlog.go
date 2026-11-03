package agentexecution

import (
	"context"
	"errors"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type BacklogChange struct {
	ID           uuid.UUID
	DeliveryMode string
	Cancel       bool
	BeforeID     uuid.UUID
	Position     string
}

func (h *Handle) ChangeBacklog(ctx context.Context, change BacklogChange) (bool, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (bool, error) {
		if change.ID == uuid.Nil || change.DeliveryMode != "queued" && change.DeliveryMode != "steering" {
			return false, errors.New("invalid backlog change")
		}
		switch change.Position {
		case "", "front", "back":
			if change.BeforeID != uuid.Nil {
				return false, errors.New("backlog change does not accept an anchor")
			}
		case "before", "after":
			if change.BeforeID == uuid.Nil || change.BeforeID == change.ID {
				return false, storeerr.ErrStateTransitionConflict
			}
		default:
			return false, errors.New("invalid backlog position")
		}
		if change.Cancel && change.Position != "" ||
			(change.Cancel || change.Position != "") && change.DeliveryMode != "queued" {
			return false, errors.New("only queued backlog supports cancellation and movement")
		}

		selected, anchor, err := h.backlogInputs(ctx, change)
		if err != nil {
			return false, err
		}
		if selected.InputKind != "content" || selected.State != "received" {
			if change.DeliveryMode == "steering" && selected.InputKind == "content" &&
				selected.State == "resolved" && selected.AdmittedEventID != nil {
				return false, nil
			}
			return false, storeerr.ErrStateTransitionConflict
		}
		if change.Cancel {
			if selected.DeliveryMode != "queued" {
				return false, storeerr.ErrStateTransitionConflict
			}
			return h.changeBacklogRow(ctx, m, selected, true)
		}
		if change.Position != "" && selected.DeliveryMode != "queued" {
			return false, storeerr.ErrStateTransitionConflict
		}
		if change.Position == "" && change.DeliveryMode == selected.DeliveryMode {
			if change.DeliveryMode == "steering" {
				return false, nil
			}
			return false, storeerr.ErrStateTransitionConflict
		}
		var changed bool
		for attempt := range 2 {
			rank, available, err := h.backlogDestination(ctx, change, anchor)
			if err != nil {
				return false, err
			}
			if available {
				selected.DeliveryMode = change.DeliveryMode
				selected.InputRank = rank
				updated, err := h.changeBacklogRow(ctx, m, selected, false)
				return changed || updated, err
			}
			if attempt == 1 {
				break
			}
			n, err := executiondb.New().RebalanceExecutionBacklog(ctx, h.unit.DB(), executiondb.RebalanceExecutionBacklogParams{
				AgentID: h.route.AgentID, Mode: change.DeliveryMode,
			})
			if err != nil {
				return false, err
			}
			if n > 0 {
				changed = true
				m.changed()
			}
			selected, anchor, err = h.backlogInputs(ctx, change)
			if err != nil {
				return false, err
			}
		}
		return false, storeerr.ErrStateTransitionConflict
	})
}

func (h *Handle) backlogInputs(
	ctx context.Context,
	change BacklogChange,
) (executiondb.GetExecutionBacklogInputsRow, executiondb.GetExecutionBacklogInputsRow, error) {
	var selected, anchor executiondb.GetExecutionBacklogInputsRow
	ids := []uuid.UUID{change.ID}
	if change.BeforeID != uuid.Nil {
		ids = append(ids, change.BeforeID)
	}
	rows, err := executiondb.New().GetExecutionBacklogInputs(ctx, h.unit.DB(),
		executiondb.GetExecutionBacklogInputsParams{AgentID: h.route.AgentID, Ids: ids})
	if err != nil {
		return selected, anchor, err
	}
	for _, row := range rows {
		if row.ID == change.ID {
			selected = row
		}
		if row.ID == change.BeforeID {
			anchor = row
		}
	}
	if selected.ID == uuid.Nil || change.BeforeID != uuid.Nil &&
		(anchor.ID == uuid.Nil || anchor.State != "received" ||
			anchor.InputKind != "content" || anchor.DeliveryMode != "queued") {
		return selected, anchor, storeerr.ErrStateTransitionConflict
	}
	return selected, anchor, nil
}

func (h *Handle) backlogDestination(
	ctx context.Context,
	change BacklogChange,
	anchor executiondb.GetExecutionBacklogInputsRow,
) (int64, bool, error) {
	q := executiondb.New()
	var lower, upper int64
	var err error
	switch change.Position {
	case "front":
		upper, err = q.FirstExecutionBacklogRank(ctx, h.unit.DB(), executiondb.FirstExecutionBacklogRankParams{
			AgentID: h.route.AgentID, Mode: change.DeliveryMode, SkipID: change.ID,
		})
	case "before":
		upper = anchor.InputRank
		lower, err = q.PreviousExecutionBacklogRank(ctx, h.unit.DB(), executiondb.PreviousExecutionBacklogRankParams{
			AgentID: h.route.AgentID, Mode: change.DeliveryMode, SkipID: change.ID,
			Rank: anchor.InputRank, QueuedAt: anchor.QueuedAt, AnchorID: anchor.ID,
		})
	case "after":
		lower = anchor.InputRank
		upper, err = q.NextExecutionBacklogRank(ctx, h.unit.DB(), executiondb.NextExecutionBacklogRankParams{
			AgentID: h.route.AgentID, Mode: change.DeliveryMode, SkipID: change.ID,
			Rank: anchor.InputRank, QueuedAt: anchor.QueuedAt, AnchorID: anchor.ID,
		})
	default:
		lower, err = q.LastExecutionBacklogRank(ctx, h.unit.DB(), executiondb.LastExecutionBacklogRankParams{
			AgentID: h.route.AgentID, Mode: change.DeliveryMode, SkipID: change.ID,
		})
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	if upper > 0 {
		return lower + (upper-lower)/2, upper-lower > 1, nil
	}
	if lower > math.MaxInt64-1024 {
		return 0, false, errors.New("backlog rank overflow")
	}
	return lower + 1024, true, nil
}

func (h *Handle) changeBacklogRow(ctx context.Context, m *executionMutation,
	row executiondb.GetExecutionBacklogInputsRow, cancel bool) (bool, error) {
	n, err := executiondb.New().ChangeExecutionInput(ctx, h.unit.DB(), executiondb.ChangeExecutionInputParams{
		AgentID: h.route.AgentID, ID: row.ID, Mode: row.DeliveryMode, Rank: row.InputRank, Cancel: cancel})
	if n > 0 {
		m.changed()
	}
	return n > 0, err
}
