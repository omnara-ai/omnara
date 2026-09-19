package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AppProfileNames interface {
	GetAgentProfileDisplayNames(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]string, error)
}

// AppProfileChoiceProvider presents app-owned launch choices. These are not
// agent interactions: there is no agent, config derivation or permission tool.
type AppProfileChoiceProvider interface {
	PresentProfileChoice(context.Context, integrationstore.IntegrationConnectionRecord,
		integrationstore.AppProfileChoiceRecord, func(context.Context) error) (string, string, error)
	DismissProfileChoice(context.Context, integrationstore.IntegrationConnectionRecord,
		integrationstore.AppProfileChoiceRecord, string) error
}

type ChatAppLauncher struct {
	store     *integrationstore.Store
	profiles  AppProfileNames
	providers map[string]AppInboxProvider
}

func NewChatAppLauncher(
	store *integrationstore.Store, profiles AppProfileNames, providers map[string]AppInboxProvider,
) *ChatAppLauncher {
	return &ChatAppLauncher{store: store, profiles: profiles, providers: providers}
}

func appChoiceSourceKey(event AppEvent) string {
	key := event.SemanticKey
	if event.Sibling != nil && event.Sibling.Key != "" && event.Sibling.Key < key {
		key = event.Sibling.Key
	}
	return key
}

func profileChoiceExpiryText(expiresAt time.Time) string {
	return "This menu expires " + expiresAt.UTC().Format("Jan 2 at 15:04 MST") +
		". Follow-up messages sent before selection may be missed."
}

// Decide implements our chat apps' one-profile-or-menu behavior. Other apps may
// choose every slot or implement another policy using the same admission path.
func (l *ChatAppLauncher) Decide(ctx context.Context, input AppLaunchContext) ([]AppLaunchIntent, error) {
	intents, err := EverySlotAppLauncher(ctx, input)
	if err != nil {
		return nil, err
	}
	var fixed, profiles []AppLaunchIntent
	for _, intent := range intents {
		if intent.AgentID != uuid.Nil {
			fixed = append(fixed, intent)
		} else {
			profiles = append(profiles, intent)
		}
	}
	chosen, err := l.decideProfiles(ctx, input, profiles)
	return append(fixed, chosen...), err
}

func (l *ChatAppLauncher) decideProfiles(
	ctx context.Context, input AppLaunchContext, profiles []AppLaunchIntent,
) ([]AppLaunchIntent, error) {
	sourceKey := appChoiceSourceKey(input.Event)
	choice, exists, err := l.store.GetAppProfileChoiceBySource(ctx, input.Connection.ProjectID,
		input.Connection.ID, input.App.ID, sourceKey)
	if err != nil {
		return nil, err
	}
	// A delayed callback for the initiating message must recover its own chosen
	// recipient, even if another setup has since launched into the conversation.
	if exists && choice.SelectedKey != "" {
		return choiceIntent(choice), nil
	}
	if !exists && len(profiles) <= 1 {
		return profiles, nil
	}
	var options []integrationstore.AppProfileChoiceOption
	if exists {
		options = choice.Options
	} else {
		ids := make([]uuid.UUID, 0, len(profiles))
		for _, intent := range profiles {
			ids = append(ids, intent.ProfileID)
		}
		names, err := l.profiles.GetAgentProfileDisplayNames(ctx, input.Connection.ProjectID, ids)
		if err != nil {
			return nil, err
		}
		for _, intent := range profiles {
			name, ok := names[intent.ProfileID]
			if !ok {
				return nil, fmt.Errorf("profile is no longer available: %w", ErrAppLaunchUnavailable)
			}
			options = append(options, integrationstore.AppProfileChoiceOption{
				Key: intent.Slot, ProfileID: intent.ProfileID, Name: name,
			})
		}
	}
	// Store only provider facts; neither a prior stage nor a provider payload can
	// choose a recipient through fields in the normalized source.
	source := input.Event
	source.Launches, source.Directed = nil, false
	raw, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	choice, _, err = l.store.EnsureAppProfileChoice(ctx, input.Receipt.Lease(),
		integrationstore.EnsureAppProfileChoiceInput{
			AppID: input.App.ID, Address: input.Address, SourceKey: sourceKey,
			Event: raw, Payload: input.Receipt.Payload, Options: options,
			HasAttachments: len(source.Files) != 0 || (source.Sibling != nil && source.Sibling.AttachmentNotice != ""),
		})
	if errors.Is(err, integrationstore.ErrAppSelectionSettled) {
		// Admission may have settled this setup after the launcher's snapshot.
		// Freeze will re-read current listeners; do not publish a stale menu.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if choice.SelectedKey != "" {
		if choice.SourceKey == sourceKey {
			return choiceIntent(choice), nil
		}
		return nil, nil
	}
	if choice.OwnerReceiptID != input.Receipt.ID || !time.Now().Before(choice.ExpiresAt) || choice.MessageID != "" {
		return nil, nil
	}
	provider, ok := l.providers[input.Connection.Provider].(AppProfileChoiceProvider)
	if !ok {
		return nil, fmt.Errorf("profile menus unavailable for provider %s", input.Connection.Provider)
	}
	check := func(ctx context.Context) error {
		if err := l.store.WithIntegrationInboxLease(ctx, input.Receipt.Lease(),
			func(*integrationstore.IntegrationInboxLeaseTx) error { return nil }); err != nil {
			return err
		}
		app, err := l.store.GetProjectApp(ctx, choice.ProjectID, choice.AppID)
		if err != nil {
			return err
		}
		if !app.Enabled {
			return ErrAppLaunchUnavailable
		}
		return nil
	}
	channel, message, err := provider.PresentProfileChoice(ctx, input.Connection, choice, check)
	if err != nil {
		if errors.Is(err, ErrAppLaunchUnavailable) {
			if expireErr := l.store.ExpireAppProfileChoice(
				ctx, choice.ProjectID, choice.ConnectionID, choice.ID); expireErr != nil {
				return nil, fmt.Errorf("retire unavailable profile menu: %w", expireErr)
			}
		}
		return nil, err
	}
	if err := l.store.RecordAppProfileChoiceMessage(ctx,
		choice.ProjectID, choice.ConnectionID, choice.ID, channel, message); err != nil {
		return nil, err
	}
	return nil, nil
}

func choiceIntent(choice integrationstore.AppProfileChoiceRecord) []AppLaunchIntent {
	for _, option := range choice.Options {
		if option.Key == choice.SelectedKey {
			return []AppLaunchIntent{{AppID: choice.AppID, Slot: option.Key, ProfileID: option.ProfileID}}
		}
	}
	return nil
}

func (l *ChatAppLauncher) NotifyUnavailable(
	ctx context.Context, connection integrationstore.IntegrationConnectionRecord, events []AppEvent,
) error {
	provider, ok := l.providers[connection.Provider].(AppProfileChoiceProvider)
	if !ok {
		return nil
	}
	var failures []error
	for _, event := range events {
		for _, intent := range event.Launches {
			choice, found, err := l.store.GetAppProfileChoiceBySource(ctx, connection.ProjectID,
				connection.ID, intent.AppID, appChoiceSourceKey(event))
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if found && choice.MessageID != "" {
				failures = append(failures, provider.DismissProfileChoice(ctx, connection, choice,
					"The app setup changed before this request could start. Mention the bot again to choose a profile."))
			}
		}
	}
	return errors.Join(failures...)
}

// SelectChatAppProfile performs no provider I/O or agent launch. A successful
// choice durably hands the original request to the existing worker before ACK.
func SelectChatAppProfile(
	ctx context.Context, store *integrationstore.Store, connection integrationstore.IntegrationConnectionRecord,
	id uuid.UUID, key, actorID, channelID, messageID string,
) (integrationstore.AppProfileChoiceRecord, error) {
	for range 3 {
		choice, err := store.GetAppProfileChoice(ctx, connection.ProjectID, connection.ID, id)
		if err != nil {
			return choice, err
		}
		var source AppEvent
		if err := json.Unmarshal(choice.Event, &source); err != nil {
			return choice, fmt.Errorf("decode original app request: %w", err)
		}
		selected := choice
		selected.SelectedKey = key
		source.Launches, source.Directed = choiceIntent(selected), true
		if len(source.Launches) != 1 {
			return choice, storeerr.ErrUnauthorized
		}
		events, err := json.Marshal([]AppEvent{source})
		if err != nil {
			return choice, err
		}
		result, err := store.ChooseAppProfile(ctx, integrationstore.ChooseAppProfileInput{
			ProjectID: connection.ProjectID, ConnectionID: connection.ID, ID: id,
			Key: key, ActorID: actorID, MessageChannelID: channelID, MessageID: messageID,
			SourceChoiceUpdatedAt: choice.UpdatedAt, SourceConnectionUpdatedAt: connection.UpdatedAt, Events: events,
		})
		if !errors.Is(err, storeerr.ErrConflict) {
			return result, err
		}
	}
	return integrationstore.AppProfileChoiceRecord{}, storeerr.ErrConflict
}
