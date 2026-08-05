package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage"
)

func WorkerLoop(ctx context.Context, workerProcessID storage.ID) (context.Context, *log.Event) {
	event := log.NewEvent(ctx, "worker.loop")
	ctx = log.WithEvent(ctx, event)
	log.Attach(ctx, log.Fields{"worker.process_id": workerProcessID})
	return ctx, event
}

func WorkerLoopResult(ctx context.Context, worked bool) {
	log.Attach(ctx, log.Fields{
		"worker.loop.worked": worked,
	})
}

func AgentWorkScope(ctx context.Context, orgID, projectID, agentID storage.ID) {
	log.Attach(ctx, log.Fields{
		"org.id":     orgID,
		"project.id": projectID,
		"agent.id":   agentID,
	})
}

func WorkerLoopRecoverableTurnRace(ctx context.Context, err error) {
	fields := log.Fields{"worker.loop.recoverable_race": true}
	if err != nil {
		fields["worker.loop.recoverable_error"] = err.Error()
	}
	log.Attach(ctx, fields)
}

func RuntimeRenewalFailed(ctx context.Context, err error) {
	fields := log.Fields{"runtime_lock.renewal.result": "failed"}
	if err != nil {
		fields["runtime_lock.renewal.error"] = err.Error()
	}
	log.Attach(ctx, fields)
}
