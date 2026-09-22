package logent

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/log"
)

func WorkerLoop(ctx context.Context, workerProcessID uuid.UUID) (context.Context, *log.Event) {
	event := log.NewEvent(ctx, "worker.loop")
	ctx = log.WithEvent(ctx, event)
	log.Attach(ctx, log.Fields{"worker.process_id": workerProcessID})
	return ctx, event
}

func WorkerLoopResult(ctx context.Context, worked bool, err error) {
	log.Attach(ctx, log.Fields{
		"worker.loop.worked": worked,
	})
	if !worked && err == nil {
		log.Level(ctx, log.DebugLevel)
	}
}

func AgentWorkScope(ctx context.Context, orgID, projectID, agentID uuid.UUID) {
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

func WorkerFileToolsUnavailable(ctx context.Context, err error) {
	event := log.NewEvent(ctx, "worker.file_tools_unavailable")
	event.Level(log.WarnLevel)
	event.Error(err)
	event.Done(ctx)
}
