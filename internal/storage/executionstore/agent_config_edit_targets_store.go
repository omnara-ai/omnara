package executionstore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type AgentConfigEditProfileAgentsWindow struct {
	CreatedBefore time.Time
}

func (s *Store) ListAgentConfigEditProfileAgentIDs(
	ctx context.Context,
	projectID, profileID uuid.UUID,
	window AgentConfigEditProfileAgentsWindow,
	limit int,
) ([]uuid.UUID, error) {
	ids, err := s.q.ListAgentConfigEditProfileAgentIDs(ctx, dbsqlc.ListAgentConfigEditProfileAgentIDsParams{
		ProjectID:      projectID,
		AgentProfileID: &profileID,
		CreatedBefore:  window.CreatedBefore,
		RowLimit:       int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list agent config edit profile agent ids: %w", err)
	}
	return ids, nil
}

type AgentConfigEditTargetCheck struct {
	Profiles []uuid.UUID
	Cutoff   time.Time
}

func (s *Store) CheckAgentConfigEditTargets(
	ctx context.Context,
	projectID uuid.UUID,
	profileIDs []uuid.UUID,
	profileKeys []string,
) (AgentConfigEditTargetCheck, error) {
	keys := make([]string, len(profileKeys))
	for i, key := range profileKeys {
		keys[i] = agentProfileRetargetIdempotencyKey(key)
	}
	row, err := s.q.CheckAgentConfigEditTargets(ctx, dbsqlc.CheckAgentConfigEditTargetsParams{
		ProjectID: projectID, ProfileIds: profileIDs, ProfileKeys: keys,
	})
	if err != nil {
		return AgentConfigEditTargetCheck{}, fmt.Errorf("check agent config edit targets: %w", err)
	}
	return AgentConfigEditTargetCheck(row), nil
}

func (s *Store) AgentConfigChangeByIdempotencyKey(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	key string,
) (uuid.UUID, []MachineRecord, bool, error) {
	input, found, err := loadAgentInputByIdempotencyMaybeTx(ctx, s.pool, projectID, agentID, "agent_config_change", key)
	if err != nil || !found {
		return uuid.Nil, nil, found, err
	}
	machines, err := sourceRemovedPoolMachinesTx(ctx, s.q, projectID, agentID)
	return input.AgentConfigID, machines, true, err
}

func (s *Store) AgentProfileVersionByIdempotencyKey(
	ctx context.Context,
	projectID, profileID uuid.UUID,
	key string,
) (uuid.UUID, int, bool, error) {
	version, found, err := loadAgentProfileVersionByIdempotencyKeyMaybeTx(
		ctx, s.q, projectID, profileID, agentProfileRetargetIdempotencyKey(key),
	)
	return version.AgentConfigID, int(version.Generation), found, err
}
