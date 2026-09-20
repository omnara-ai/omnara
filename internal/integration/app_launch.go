package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

// AppLauncher decides what a saved setup does with an event. It runs before
// config derivation or agent admission, and may return zero, one or many intents.
// Pending human choices belong to the app implementation, not the agent loop.
type AppLauncher func(context.Context, AppLaunchContext) ([]AppLaunchIntent, error)

type AppLaunchContext struct {
	Receipt    integrationstore.IntegrationInboxRecord
	App        integrationstore.ProjectAppRecord
	Event      AppEvent
	Address    integrationstore.ConversationAddress
	Candidates integrationstore.AppRoutingCandidates
}

type AppLaunchWorkflow struct {
	router    *AppRouter
	launchers map[string]AppLauncher
	// OnUnavailable lets an app update its presentation after a setup edit
	// invalidates a decided launch. Failure to notify never changes admission.
	OnUnavailable func(context.Context, integrationstore.ProjectAppRecord, []AppEvent) error
}

func NewAppLaunchWorkflow(router *AppRouter, launchers map[string]AppLauncher) *AppLaunchWorkflow {
	registered := make(map[string]AppLauncher, len(launchers))
	for id, launcher := range launchers {
		registered[id] = launcher
	}
	return &AppLaunchWorkflow{router: router, launchers: registered}
}

// Decide reads routing under the receipt lease, releases all locks, then invokes
// app code. Freeze independently validates its resulting intents under current
// authority. Provider I/O and a human response never hold database locks.
func (w *AppLaunchWorkflow) Decide(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	receipt integrationstore.IntegrationInboxRecord,
	appSetup integrationstore.ProjectAppRecord,
	events []AppEvent,
) ([]AppEvent, error) {
	requests, err := prepareAppEvents(events, appSetup)
	if err != nil {
		return nil, err
	}
	err = w.router.integrations.WithIntegrationInboxLease(ctx, lease,
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			for i := range requests {
				request := &requests[i]
				request.candidates, err = w.router.integrations.AppRoutingCandidatesForInbox(
					ctx, work, request.address, request.scopes, request.event.Event.Kind,
				)
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
		// Normalization describes provider facts. Only app code supplies launch
		// decisions; callback handoffs skip this stage entirely.
		event.Launches, event.Directed = nil, false
		if app := request.candidates.Launcher; app != nil {
			launcher := w.launchers[app.DefinitionID]
			if launcher == nil {
				return nil, fmt.Errorf("no launcher registered for app %s", app.DefinitionID)
			}
			intents, err := launcher(ctx, AppLaunchContext{
				Receipt: receipt, App: *app,
				Event: request.event, Address: request.address, Candidates: request.candidates,
			})
			if err != nil {
				if errors.Is(err, ErrAppLaunchUnavailable) {
					slog.WarnContext(ctx, "app launcher unavailable", "app_id", app.ID, "receipt_id", receipt.ID, "error", err)
				} else {
					return nil, err
				}
			}
			for _, intent := range intents {
				if intent.AppID != app.ID {
					return nil, fmt.Errorf("launcher for app %s returned an intent for another app", app.ID)
				}
			}
			event.Launches = append(event.Launches, intents...)
		}
	}
	return result, nil
}

// EverySlotAppLauncher is GitHub's explicit launch policy. Other app behaviors
// may reuse it; the generic planner does not choose fan-out as a default.
func EverySlotAppLauncher(_ context.Context, input AppLaunchContext) ([]AppLaunchIntent, error) {
	launcher := input.App.Settings.Launcher
	if input.App.State != integrationstore.ProjectAppStateActive || launcher == nil ||
		!input.Event.Event.MatchesLauncher(launcher.Trigger) {
		return nil, nil
	}
	profilesSelected := false
	for _, listener := range input.Candidates.Listeners {
		profilesSelected = profilesSelected || listener.Address == input.Address
	}
	for _, selected := range input.Candidates.Selections {
		profilesSelected = profilesSelected || selected.AppID == input.App.ID
	}
	var intents []AppLaunchIntent
	for _, slot := range launcher.Slots {
		intent := AppLaunchIntent{AppID: input.App.ID, Slot: slot.Key}
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
