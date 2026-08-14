package crontrigger

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

const (
	FireBatchSize                = 100
	fireMachineProvisionTimeout  = 2 * time.Minute
	fireIdempotencyTimestampSpec = time.RFC3339
)

type Service struct {
	execution    *executionstore.Store
	machinePools *machinepool.Manager
	logger       *slog.Logger
}

func NewService(
	execution *executionstore.Store,
	machinePools *machinepool.Manager,
	logger *slog.Logger,
) *Service {
	return &Service{execution: execution, machinePools: machinePools, logger: logger}
}

type FireStats struct {
	Claimed  int
	Launched int
	Inputs   int
	Disabled int
	Failures int
}

func (s *Service) FireDueTriggers(ctx context.Context) (FireStats, error) {
	claim, err := s.execution.ClaimDueCronTriggers(ctx, FireBatchSize)
	if err != nil {
		return FireStats{}, fmt.Errorf("claim due cron triggers: %w", err)
	}
	stats := FireStats{Claimed: len(claim.Claimed), Disabled: len(claim.Disabled)}
	for _, disabled := range claim.Disabled {
		s.logger.Error(
			"disabled cron trigger with unparseable schedule",
			"cron_trigger_id", disabled,
		)
	}
	for _, trigger := range claim.Claimed {
		if err := s.fireTrigger(ctx, trigger); err != nil {
			stats.Failures++
			s.logger.Error(
				"fire cron trigger",
				"cron_trigger_id", trigger.TriggerID,
				"project_id", trigger.ProjectID,
				"target_kind", trigger.Target.Kind,
				"error", err,
			)
			continue
		}
		switch trigger.Target.Kind {
		case executionstore.CronTriggerTargetAgentProfile:
			stats.Launched++
		case executionstore.CronTriggerTargetAgent:
			stats.Inputs++
		}
	}
	return stats, nil
}

func (s *Service) fireTrigger(ctx context.Context, trigger executionstore.ClaimedCronTrigger) error {
	message, err := cronschedule.RenderMessage(
		trigger.MessageTemplate,
		cronschedule.MessageData(trigger.Name, trigger.FiredAt, trigger.LastFiredAt),
	)
	if err != nil {
		message = trigger.MessageTemplate
		s.logger.Error(
			"render cron trigger message template; sending raw template",
			"cron_trigger_id", trigger.TriggerID,
			"error", err,
		)
	}
	idempotencyKey := fireIdempotencyKey(trigger)
	switch trigger.Target.Kind {
	case executionstore.CronTriggerTargetAgentProfile:
		return s.launchFromProfile(ctx, trigger, message, idempotencyKey)
	case executionstore.CronTriggerTargetAgent:
		return s.sendAgentInput(ctx, trigger, message, idempotencyKey)
	default:
		return fmt.Errorf("unsupported cron trigger target kind %q", trigger.Target.Kind)
	}
}

func (s *Service) launchFromProfile(
	ctx context.Context,
	trigger executionstore.ClaimedCronTrigger,
	message string,
	idempotencyKey string,
) error {
	profile, err := s.execution.GetAgentProfile(ctx, trigger.ProjectID, trigger.Target.ID)
	if err != nil {
		return fmt.Errorf("load target agent profile: %w", err)
	}
	actor, err := cronTriggerActorParams(trigger)
	if err != nil {
		return err
	}
	launch, err := s.execution.LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:     trigger.ProjectID,
		ProfileID:     profile.ID,
		AgentConfigID: profile.CurrentConfigID,
		LaunchedBy: identitystore.PrincipalRecord{
			Type: identitystore.PrincipalTypeSystem,
			ID:   trigger.TriggerID,
		},
		Message:        message,
		MessageActor:   actor,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return fmt.Errorf("launch agent from profile: %w", err)
	}
	s.provisionLaunchMachines(ctx, launch)
	return nil
}

func (s *Service) sendAgentInput(
	ctx context.Context,
	trigger executionstore.ClaimedCronTrigger,
	message string,
	idempotencyKey string,
) error {
	actor, err := cronTriggerActorParams(trigger)
	if err != nil {
		return err
	}
	contentBlocks, err := json.Marshal([]map[string]any{{"type": "text", "text": message}})
	if err != nil {
		return fmt.Errorf("marshal cron trigger message: %w", err)
	}
	if _, _, _, err := s.execution.CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      trigger.ProjectID,
		AgentID:        trigger.Target.ID,
		Actor:          actor,
		ContentBlocks:  contentBlocks,
		DeliveryMode:   executionstore.DeliveryModeQueued,
		IdempotencyKey: idempotencyKey,
	}); err != nil {
		return fmt.Errorf("send cron trigger input: %w", err)
	}
	return nil
}

func (s *Service) provisionLaunchMachines(
	ctx context.Context,
	launch executionstore.LaunchAgentResult,
) {
	if s.machinePools == nil || len(launch.ProvisionMachineIDs) == 0 {
		return
	}
	provisionCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		fireMachineProvisionTimeout,
	)
	defer cancel()
	for _, machineID := range launch.ProvisionMachineIDs {
		if err := s.machinePools.ProvisionMachine(provisionCtx, launch.Agent.OrgID, machineID); err != nil {
			s.logger.Warn(
				"cron trigger launch machine provisioning failed",
				"org_id", launch.Agent.OrgID,
				"machine_id", machineID,
				"error", err,
			)
		}
	}
}

func cronTriggerActorParams(
	trigger executionstore.ClaimedCronTrigger,
) (*executionstore.ActorParams, error) {
	tenantID, err := publicid.Encode(publicid.KindOrganization, trigger.OrgID)
	if err != nil {
		return nil, fmt.Errorf("encode cron trigger actor tenant: %w", err)
	}
	providerUserID, err := publicid.Encode(publicid.KindCronTrigger, trigger.TriggerID)
	if err != nil {
		return nil, fmt.Errorf("encode cron trigger actor: %w", err)
	}
	displayName := trigger.Name
	return &executionstore.ActorParams{
		Provider:         executionstore.ActorProviderOmnara,
		ProviderTenantID: tenantID,
		ProviderUserID:   providerUserID,
		DisplayName:      &displayName,
	}, nil
}

func fireIdempotencyKey(trigger executionstore.ClaimedCronTrigger) string {
	return "cron_trigger:" + trigger.TriggerID.String() + ":" +
		trigger.FiredAt.UTC().Format(fireIdempotencyTimestampSpec)
}
