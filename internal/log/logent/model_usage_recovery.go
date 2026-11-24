package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	log "github.com/omnara-ai/omnara/observability/wideevent"
)

func ModelUsageRecoveryMissed(
	ctx context.Context, call executionstore.ModelUsageRecovery, cause string, err error,
) {
	outcome := "failed"
	if call.Final {
		outcome = "gave_up"
	}
	event := log.NewEvent(ctx, "model_usage_recovery.attempt", log.Fields{
		"org.id":                             call.OrgID,
		"project.id":                         call.ProjectID,
		"agent.id":                           call.AgentID,
		"model_call_context_id":              call.ModelCallContextID,
		"model_provider_config_id":           call.ModelProviderConfigID,
		"model_usage_recovery.generation_id": call.GenerationID,
		"model_usage_recovery.outcome":       outcome,
		"model_usage_recovery.cause":         cause,
		"model_usage_recovery.gave_up":       call.Final,
	})
	event.Error(err)
	if call.Final && cause != "not_recorded" {
		event.Level(log.ErrorLevel)
	} else {
		event.Level(log.WarnLevel)
	}
	event.Done(ctx)
}
