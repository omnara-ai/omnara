package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

// IntegrationLauncher must use the integration definition's trigger matching: the inbox prefilter
// may discard other events before invoking the launcher.
type IntegrationLauncher func(context.Context, IntegrationLaunchContext) ([]IntegrationLaunchIntent, error)

type IntegrationLaunchContext struct {
	Receipt     integrationstore.IntegrationInboxRecord
	Integration integrationstore.ProjectIntegrationRecord
	Event       IntegrationEvent
	Address     integrationstore.ConversationAddress
	Candidates  integrationstore.IntegrationRoutingCandidates
}

type IntegrationLaunchWorkflow struct {
	Log       *slog.Logger
	router    *IntegrationRouter
	launchers map[integrationdefinition.Type]IntegrationLauncher
}

func NewIntegrationLaunchWorkflow(
	router *IntegrationRouter,
	launchers map[integrationdefinition.Type]IntegrationLauncher,
) *IntegrationLaunchWorkflow {
	registered := make(map[integrationdefinition.Type]IntegrationLauncher, len(launchers))
	for id, launcher := range launchers {
		registered[id] = launcher
	}
	return &IntegrationLaunchWorkflow{router: router, launchers: registered}
}

func (w *IntegrationLaunchWorkflow) Decide(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	receipt integrationstore.IntegrationInboxRecord,
	integrationSetup integrationstore.ProjectIntegrationRecord,
	events []IntegrationEvent,
) ([]IntegrationEvent, error) {
	log := w.Log
	if log == nil {
		log = slog.Default()
	}
	requests, err := prepareIntegrationEvents(events, integrationSetup)
	if err != nil {
		return nil, err
	}
	err = w.router.integrations.WithIntegrationInboxLease(ctx, lease,
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			for i := range requests {
				request := &requests[i]
				request.candidates, err = w.router.candidatesForEvent(ctx, work, *request)
				if err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	result := slices.Clone(events)
	for _, request := range requests {
		event := &result[request.order]
		event.Launches, event.Directed = nil, false
		if integration := request.candidates.Launcher; integration != nil {
			launcher := w.launchers[integration.IntegrationType]
			if launcher == nil {
				return nil, fmt.Errorf("no launcher registered for integration %s", integration.IntegrationType)
			}
			intents, err := launcher(ctx, IntegrationLaunchContext{
				Receipt: receipt, Integration: *integration,
				Event: request.event, Address: request.address, Candidates: request.candidates,
			})
			if err != nil {
				if errors.Is(err, ErrIntegrationLaunchUnavailable) {
					log.WarnContext(
						ctx,
						"integration launcher unavailable",
						"integration_id",
						integration.ID,
						"receipt_id",
						receipt.ID,
						"error",
						err,
					)
				} else {
					return nil, err
				}
			}
			for _, intent := range intents {
				if intent.IntegrationID != integration.ID {
					return nil, fmt.Errorf("launcher for integration %s returned an intent for another integration", integration.ID)
				}
				if intent.AgentID != uuid.Nil {
					agent, err := w.router.execution.GetAgentInProject(ctx, receipt.ProjectID, intent.AgentID)
					if err != nil {
						return nil, err
					}
					if agent.State == executionstore.AgentStateArchived {
						log.WarnContext(ctx, "skip archived integration launcher recipient",
							"integration_id", integration.ID, "receipt_id", receipt.ID, "agent_id", intent.AgentID, "slot", intent.Slot)
						continue
					}
				}
				event.Launches = append(event.Launches, intent)
			}
		}
	}
	return result, nil
}

func EverySlotIntegrationLauncher(
	_ context.Context,
	input IntegrationLaunchContext,
) ([]IntegrationLaunchIntent, error) {
	launcher := input.Integration.Settings.Launcher
	definition, _ := integrationdefinition.Lookup(input.Integration.IntegrationType)
	if input.Integration.State != integrationstore.ProjectIntegrationStateActive || launcher == nil ||
		!definition.MatchesLauncher(input.Event.Event, launcher.Trigger) {
		return nil, nil
	}
	profilesSelected := false
	for _, subscription := range input.Candidates.Subscriptions {
		profilesSelected = profilesSelected || subscription.Address == input.Address
	}
	for _, selected := range input.Candidates.Selections {
		profilesSelected = profilesSelected || selected.IntegrationID == input.Integration.ID
	}
	var intents []IntegrationLaunchIntent
	for _, slot := range launcher.Slots {
		intent := IntegrationLaunchIntent{IntegrationID: input.Integration.ID, Slot: slot.Key}
		switch {
		case slot.AgentID != nil:
			intent.AgentID = *slot.AgentID
		case slot.AgentProfileID != nil && !profilesSelected:
			intent.ProfileID = *slot.AgentProfileID
		default:
			continue
		}
		intents = append(intents, intent)
	}
	return intents, nil
}
