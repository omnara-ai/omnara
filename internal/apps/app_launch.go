package apps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

// AppLauncher must use the app definition's trigger matching: the inbox prefilter
// may discard other events before invoking the launcher.
type AppLauncher func(context.Context, AppLaunchContext) ([]AppLaunchIntent, error)

type AppLaunchContext struct {
	Receipt    appstore.AppInboxRecord
	App        appstore.ProjectAppRecord
	Event      AppEvent
	Address    appstore.ConversationAddress
	Candidates appstore.AppRoutingCandidates
}

type AppLaunchWorkflow struct {
	Log *slog.Logger

	router    *AppRouter
	launchers map[appdefinition.Type]AppLauncher
}

func NewAppLaunchWorkflow(router *AppRouter, launchers map[appdefinition.Type]AppLauncher) *AppLaunchWorkflow {
	registered := make(map[appdefinition.Type]AppLauncher, len(launchers))
	for id, launcher := range launchers {
		registered[id] = launcher
	}
	return &AppLaunchWorkflow{router: router, launchers: registered}
}

func (w *AppLaunchWorkflow) Decide(
	ctx context.Context,
	lease appstore.AppInboxLease,
	receipt appstore.AppInboxRecord,
	appSetup appstore.ProjectAppRecord,
	events []AppEvent,
) ([]AppEvent, error) {
	log := w.Log
	if log == nil {
		log = slog.Default()
	}
	requests, err := prepareAppEvents(events, appSetup)
	if err != nil {
		return nil, err
	}
	err = w.router.apps.WithAppInboxLease(ctx, lease,
		func(work *appstore.AppInboxLeaseTx) error {
			for i := range requests {
				request := &requests[i]
				request.candidates, err = w.router.apps.AppRoutingCandidatesForInbox(
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
		event.Launches, event.Directed = nil, false
		if app := request.candidates.Launcher; app != nil {
			launcher := w.launchers[app.AppType]
			if launcher == nil {
				return nil, fmt.Errorf("no launcher registered for app %s", app.AppType)
			}
			intents, err := launcher(ctx, AppLaunchContext{
				Receipt: receipt, App: *app,
				Event: request.event, Address: request.address, Candidates: request.candidates,
			})
			if err != nil {
				if errors.Is(err, ErrAppLaunchUnavailable) {
					log.WarnContext(ctx, "app launcher unavailable", "app_id", app.ID, "receipt_id", receipt.ID, "error", err)
				} else {
					return nil, err
				}
			}
			for _, intent := range intents {
				if intent.AppID != app.ID {
					return nil, fmt.Errorf("launcher for app %s returned an intent for another app", app.ID)
				}
				if intent.AgentID != uuid.Nil {
					agent, err := w.router.execution.GetAgentInProject(ctx, receipt.ProjectID, intent.AgentID)
					if err != nil {
						return nil, err
					}
					if agent.State == executionstore.AgentStateArchived {
						log.WarnContext(ctx, "skip archived app launcher recipient",
							"app_id", app.ID, "receipt_id", receipt.ID, "agent_id", intent.AgentID, "slot", intent.Slot)
						continue
					}
				}
				event.Launches = append(event.Launches, intent)
			}
		}
	}
	return result, nil
}

func EverySlotAppLauncher(_ context.Context, input AppLaunchContext) ([]AppLaunchIntent, error) {
	launcher := input.App.Settings.Launcher
	definition, _ := appdefinition.Lookup(input.App.AppType)
	if input.App.State != appstore.ProjectAppStateActive || launcher == nil ||
		!definition.MatchesLauncher(input.Event.Event, launcher.Trigger) {
		return nil, nil
	}
	profilesSelected := false
	for _, subscription := range input.Candidates.Subscriptions {
		profilesSelected = profilesSelected || subscription.Address == input.Address
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
