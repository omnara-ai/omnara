package executionstore

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func normalizeCronChannelBindings(
	kind CronTriggerTargetKind, input []LaunchChannelBinding,
) ([]LaunchChannelBinding, error) {
	if len(input) > MaxLaunchChannelBindings || (len(input) > 0 && kind != CronTriggerTargetAgentProfile) {
		return nil, storeerr.InvalidRequest(fmt.Errorf(
			"only profile cron triggers accept channel bindings, up to %d", MaxLaunchChannelBindings))
	}
	bindings := slices.Clone(input)
	slices.SortFunc(bindings, func(a, b LaunchChannelBinding) int {
		return slices.Compare(a.ChannelID[:], b.ChannelID[:])
	})
	for i, binding := range bindings {
		if binding.ChannelID == uuid.Nil || (i > 0 && binding.ChannelID == bindings[i-1].ChannelID) {
			return nil, storeerr.InvalidRequest(errors.New("channel bindings must name distinct channels"))
		}
		if !binding.Grants.ReceiveAllowed && !binding.Grants.ReadAllowed && !binding.Grants.SendAllowed {
			return nil, storeerr.InvalidRequest(errors.New("at least one channel grant is required"))
		}
		if reply := binding.ReplyChannelGrants; reply != nil {
			if !binding.Grants.SendAllowed || (!reply.ReceiveAllowed && !reply.ReadAllowed && !reply.SendAllowed) {
				return nil, storeerr.InvalidRequest(errors.New("reply grants require send and a nonempty child grant"))
			}
			replyGrants := *reply
			bindings[i].ReplyChannelGrants = &replyGrants
		}
	}
	return bindings, nil
}

func sameCronChannelBindings(a, b []LaunchChannelBinding) bool {
	return slices.EqualFunc(a, b, func(a, b LaunchChannelBinding) bool {
		if a.ChannelID != b.ChannelID || a.Grants != b.Grants {
			return false
		}
		if a.ReplyChannelGrants == nil || b.ReplyChannelGrants == nil {
			return a.ReplyChannelGrants == nil && b.ReplyChannelGrants == nil
		}
		return *a.ReplyChannelGrants == *b.ReplyChannelGrants
	})
}

// Installation gates/rows precede profile locks. Target rows then precede the
// cron row, matching profile launch's eventual binding creation and completion.
func lockCronChannelTargetsTx(
	ctx context.Context, q *dbsqlc.Queries, bindings []integrationstore.CreateIntegrationTargetBindingInput,
) error {
	for _, binding := range bindings {
		_, err := q.LockIntegrationTargetForBinding(ctx, dbsqlc.LockIntegrationTargetForBindingParams{
			ProjectID: binding.ProjectID, IntegrationInstallID: binding.IntegrationInstallID,
			IntegrationTargetID: binding.IntegrationTargetID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock cron channel: %w", err)
		}
	}
	return nil
}

func loadCronChannelBindings(
	ctx context.Context, q *dbsqlc.Queries, triggerIDs []uuid.UUID,
) (map[uuid.UUID][]LaunchChannelBinding, error) {
	bindings := make(map[uuid.UUID][]LaunchChannelBinding, len(triggerIDs))
	for _, id := range triggerIDs {
		bindings[id] = []LaunchChannelBinding{}
	}
	if len(triggerIDs) == 0 {
		return bindings, nil
	}
	rows, err := q.ListCronTriggerChannelBindings(ctx, dbsqlc.ListCronTriggerChannelBindingsParams{TriggerIds: triggerIDs})
	if err != nil {
		return nil, fmt.Errorf("load cron channel bindings: %w", err)
	}
	for _, row := range rows {
		binding := LaunchChannelBinding{
			ChannelID: row.ChannelID,
			Grants: integrationstore.ChannelGrants{
				ReceiveAllowed: row.ReceiveAllowed, ReadAllowed: row.ReadAllowed, SendAllowed: row.SendAllowed,
			},
		}
		if row.ReplyReceiveAllowed != nil {
			binding.ReplyChannelGrants = &integrationstore.ChannelGrants{
				ReceiveAllowed: *row.ReplyReceiveAllowed, ReadAllowed: *row.ReplyReadAllowed, SendAllowed: *row.ReplySendAllowed,
			}
		}
		bindings[row.CronTriggerID] = append(bindings[row.CronTriggerID], binding)
	}
	return bindings, nil
}

func replaceCronChannelBindingsTx(
	ctx context.Context, q *dbsqlc.Queries, projectID, triggerID uuid.UUID, bindings []LaunchChannelBinding,
) error {
	if err := q.DeleteCronTriggerChannelBindings(ctx, dbsqlc.DeleteCronTriggerChannelBindingsParams{
		ProjectID: projectID, CronTriggerID: triggerID,
	}); err != nil {
		return fmt.Errorf("replace cron channel bindings: %w", err)
	}
	for _, binding := range bindings {
		params := dbsqlc.InsertCronTriggerChannelBindingParams{
			ProjectID: projectID, CronTriggerID: triggerID, ChannelID: binding.ChannelID,
			ReceiveAllowed: binding.Grants.ReceiveAllowed, ReadAllowed: binding.Grants.ReadAllowed,
			SendAllowed: binding.Grants.SendAllowed,
		}
		if reply := binding.ReplyChannelGrants; reply != nil {
			params.ReplyReceiveAllowed, params.ReplyReadAllowed, params.ReplySendAllowed =
				&reply.ReceiveAllowed, &reply.ReadAllowed, &reply.SendAllowed
		}
		if err := q.InsertCronTriggerChannelBinding(ctx, params); err != nil {
			return fmt.Errorf("insert cron channel binding: %w", err)
		}
	}
	return nil
}

// Only changed grants or enabling a schedule acquire channel authority. An
// administrator can still disable a broken schedule after its channel retires.
func (s *Store) prepareCronChannelUpdateTx(
	ctx context.Context, tx pgx.Tx, input UpdateCronTriggerInput,
) ([]LaunchChannelBinding, error) {
	q := s.q.WithTx(tx)
	row, err := q.GetCronTrigger(ctx, dbsqlc.GetCronTriggerParams{ProjectID: input.ProjectID, ID: input.TriggerID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, storeerr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	target := cronTriggerTargetFromColumns(row.AgentProfileID, row.AgentID, row.DeliveryMode)
	var bindings []LaunchChannelBinding
	if input.ChannelBindings != nil {
		bindings = *input.ChannelBindings
	} else {
		stored, err := loadCronChannelBindings(ctx, q, []uuid.UUID{input.TriggerID})
		if err != nil {
			return nil, err
		}
		bindings = stored[input.TriggerID]
	}
	bindings, err = normalizeCronChannelBindings(target.Kind, bindings)
	if err != nil {
		return nil, err
	}
	prepared, err := s.prepareLaunchChannelBindingsTx(ctx, tx, LaunchAgentInput{
		ProjectID: input.ProjectID, ChannelBindings: bindings,
	})
	if err != nil {
		return nil, err
	}
	if target.Kind == CronTriggerTargetAgentProfile {
		if _, err := lockAgentProfileTx(ctx, q, input.ProjectID, target.ID); err != nil {
			return nil, err
		}
	}
	if err := lockCronChannelTargetsTx(ctx, q, prepared); err != nil {
		return nil, err
	}
	return bindings, nil
}
