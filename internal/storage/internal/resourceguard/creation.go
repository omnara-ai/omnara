package resourceguard

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

const (
	AgentConfigs        = "agent_configs"
	Agents              = "agents"
	Machines            = "machines"
	MachineDaemonTokens = "machine_daemon_tokens"
)

type Creation struct {
	Kind      string
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	MachineID uuid.UUID
}

func (c Creation) scope() string {
	if c.Kind == MachineDaemonTokens {
		return c.OrgID.String() + ":" + c.MachineID.String()
	}
	if c.Kind == Machines {
		return c.OrgID.String()
	}
	return c.ProjectID.String()
}

func CheckCreations(ctx context.Context, tx dbsqlc.DBTX, creations []Creation) error {
	if len(creations) == 0 {
		return nil
	}
	ordered := slices.Clone(creations)
	slices.SortFunc(ordered, func(a, b Creation) int {
		if order := cmp.Compare(a.Kind, b.Kind); order != 0 {
			return order
		}
		return cmp.Compare(a.scope(), b.scope())
	})
	ordered = slices.Compact(ordered)
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return fmt.Errorf("validate resource creation constraints: %w", err)
	}
	q := dbsqlc.New(tx)
	for _, creation := range ordered {
		if err := Lock(ctx, q, creation.Kind, creation.scope()); err != nil {
			return err
		}
	}
	limitsByOrg := make(map[uuid.UUID]dbsqlc.EffectiveResourceLimit)
	for _, creation := range ordered {
		limits, ok := limitsByOrg[creation.OrgID]
		if !ok {
			var err error
			limits, err = ResolveLimits(ctx, q, creation.OrgID)
			if err != nil {
				return err
			}
			limitsByOrg[creation.OrgID] = limits
		}
		if err := creation.check(ctx, q, limits); err != nil {
			return err
		}
	}
	return nil
}

func (c Creation) check(ctx context.Context, q *dbsqlc.Queries, limits dbsqlc.EffectiveResourceLimit) error {
	var err error
	var count, limit int64
	var name string
	switch c.Kind {
	case Agents:
		count, err = q.CountActiveAgentsForProject(ctx, dbsqlc.CountActiveAgentsForProjectParams{ProjectID: c.ProjectID})
		limit, name = limits.MaxActiveAgentsPerProject, "active agents"
	case AgentConfigs:
		count, err = q.CountAgentConfigsForProject(ctx, dbsqlc.CountAgentConfigsForProjectParams{ProjectID: c.ProjectID})
		limit, name = limits.MaxAgentConfigsPerProject, "agent configs"
	case MachineDaemonTokens:
		count, err = q.CountActiveMachineDaemonTokensForMachine(ctx, dbsqlc.CountActiveMachineDaemonTokensForMachineParams{
			OrgID: c.OrgID, MachineID: c.MachineID,
		})
		limit, name = limits.MaxActiveByoDaemonTokensPerMachine, "active machine daemon tokens"
	case Machines:
		count, err = q.CountLiveMachinesForOrg(ctx, dbsqlc.CountLiveMachinesForOrgParams{OrgID: c.OrgID})
		limit, name = limits.MaxLiveMachinesPerOrg, "live machines"
	default:
		return fmt.Errorf("unknown creation resource %q", c.Kind)
	}
	if err != nil {
		return fmt.Errorf("count %s: %w", name, err)
	}
	if count > limit {
		return LimitExceeded(name, limit)
	}
	return nil
}
