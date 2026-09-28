package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// IntegrationLauncher must use the integration definition's event matching: the inbox prefilter
// may discard other events before invoking the launcher.
type IntegrationLauncher func(context.Context, IntegrationLaunchContext) ([]IntegrationLaunchIntent, error)

type IntegrationLaunchContext struct {
	Receipt     integrationstore.IntegrationInboxRecord
	Integration integrationstore.IntegrationRecord
	Event       IntegrationEvent
	Address     integrationstore.ConversationAddress
	Candidates  integrationstore.IntegrationRoutingCandidates
}

type IntegrationLaunchWorkflow struct {
	Log       *slog.Logger
	router    *IntegrationRouter
	launchers map[integrationdefinition.Kind]IntegrationLauncher
	providers map[string]IntegrationInboxProvider
}

func NewIntegrationLaunchWorkflow(
	router *IntegrationRouter,
	launchers map[integrationdefinition.Kind]IntegrationLauncher,
	providers map[string]IntegrationInboxProvider,
) *IntegrationLaunchWorkflow {
	registered := make(map[integrationdefinition.Kind]IntegrationLauncher, len(launchers))
	for id, launcher := range launchers {
		registered[id] = launcher
	}
	return &IntegrationLaunchWorkflow{router: router, launchers: registered, providers: providers}
}

func (w *IntegrationLaunchWorkflow) Decide(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	receipt integrationstore.IntegrationInboxRecord,
	integrationSetup integrationstore.IntegrationRecord,
	event IntegrationEvent,
) (*IntegrationEvent, error) {
	request, err := prepareIntegrationEvent(event, integrationSetup)
	if err != nil {
		return nil, err
	}
	err = w.router.integrations.WithIntegrationInboxLease(ctx, lease,
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			request.candidates, err = w.router.candidatesForEvent(ctx, work, request)
			if err != nil {
				return err
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	result := event
	result.Launches, result.Directed = nil, false
	if integration := request.candidates.Launcher; integration != nil {
		launcher := w.launchers[integration.IntegrationKind]
		if launcher == nil {
			return nil, fmt.Errorf("no launcher registered for integration %s", integration.IntegrationKind)
		}
		input := IntegrationLaunchContext{Receipt: receipt, Integration: *integration,
			Event: request.event, Address: request.address, Candidates: request.candidates}
		intents, err := launcher(ctx, input)
		if errors.Is(err, ErrIntegrationLaunchUnavailable) {
			w.launchUnavailable(ctx, input, err)
			return &result, nil
		}
		if err != nil {
			return nil, err
		}
		for _, intent := range intents {
			if intent.IntegrationID != integration.ID {
				return nil, fmt.Errorf("launcher for integration %s returned an intent for another integration", integration.ID)
			}
			if intent.ProfileID != uuid.Nil && !integrationHasLaunchOwner(input) {
				if _, err := w.router.execution.GetAgentProfile(ctx, receipt.ProjectID, intent.ProfileID); err != nil {
					if storeerr.IsNotFound(err) {
						w.launchUnavailable(ctx, input, fmt.Errorf("configured launcher profile %s is unavailable: %w",
							intent.ProfileID, ErrIntegrationLaunchUnavailable))
						continue
					}
					return nil, err
				}
			}
			result.Launches = append(result.Launches, intent)
		}
	}
	return &result, nil
}

func integrationLaunchEligible(input IntegrationLaunchContext) bool {
	definition, _ := integrationdefinition.Lookup(input.Integration.IntegrationKind)
	if input.Integration.State != integrationstore.IntegrationStateActive ||
		!definition.MatchesLaunch(input.Integration.Settings, input.Event.Event) {
		return false
	}
	return !integrationHasLaunchOwner(input)
}

func integrationHasLaunchOwner(input IntegrationLaunchContext) bool {
	for _, selected := range input.Candidates.Selections {
		if selected.IntegrationID == input.Integration.ID && selected.LaunchKey != "" {
			return true
		}
	}
	return false
}

func GitHubIntegrationLauncher(_ context.Context, input IntegrationLaunchContext) ([]IntegrationLaunchIntent, error) {
	if !integrationLaunchEligible(input) {
		return nil, nil
	}
	settings, err := integrationdefinition.ReadGitHubSettings(input.Integration.Settings)
	if err != nil {
		return nil, err
	}
	if settings.Launcher == nil {
		return nil, nil
	}
	profileID, err := publicid.Decode(publicid.KindAgentProfile, settings.Launcher.Profile)
	if err != nil {
		return nil, fmt.Errorf("invalid configured GitHub profile: %w", err)
	}
	return []IntegrationLaunchIntent{{
		IntegrationID: input.Integration.ID, LaunchKey: integrationdefinition.ProfileLaunchKey, ProfileID: profileID,
	}}, nil
}
