package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func Agent(ctx context.Context, r executionstore.AgentRecord) {
	log.Attach(ctx, log.Fields{
		"org.id":          r.OrgID,
		"project.id":      r.ProjectID,
		"agent.id":        r.ID,
		"agent.state":     string(r.State),
		"agent.config_id": r.CurrentConfigID,
	})
}
func RuntimeLock(ctx context.Context, r executionstore.AgentRuntimeLockRecord) {
	log.Attach(ctx, log.Fields{
		"agent.id":                         r.AgentID,
		"runtime_lock.id":                  r.ID,
		"worker.process_id":                r.WorkerProcessID,
		"runtime_lock.started_at":          r.StartedAt,
		"runtime_lock.renewed_at":          r.RenewedAt,
		"runtime_lock.lease_expires_at":    r.LeaseExpiresAt,
		"runtime_lock.cancel_requested_at": r.CancelRequestedAt,
	})
}
func Turn(ctx context.Context, r executionstore.AgentTurnRecord) {
	log.Attach(ctx, log.Fields{
		"project.id":                    r.ProjectID,
		"agent.id":                      r.AgentID,
		"turn.id":                       r.ID,
		"turn.sequence":                 r.TurnSequence,
		"turn.latest_event_id":          r.LatestEventID,
		"turn.latest_semantic_event_id": r.LatestSemanticEventID,
	})
}
func AgentInput(ctx context.Context, r executionstore.AgentInputRecord) {
	log.Attach(ctx, log.Fields{
		"project.id":                  r.ProjectID,
		"agent.id":                    r.AgentID,
		"input.id":                    r.ID,
		"input.state":                 r.State,
		"input.rank":                  r.InputRank,
		"input.kind":                  r.InputKind,
		"input.actor_id":              r.ActorID,
		"input.delivery_mode":         string(r.DeliveryMode),
		"input.queued_at":             r.QueuedAt,
		"input.admitted_at":           r.AdmittedAt,
		"input.admitted_event_id":     r.AdmittedEventID,
		"input.target_interaction_id": r.TargetInteractionID,
	})
}

func AdmittedAgentInputTurn(ctx context.Context, r executionstore.AdmittedAgentInputTurn) {
	for _, input := range r.Inputs {
		AgentInput(ctx, input)
	}
	Turn(ctx, r.Turn)
}
