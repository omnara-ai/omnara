package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type IntegrationProfileChoiceExecution interface {
	GetAgentProfileDisplayNames(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]string, error)
	GetIntegrationInboxOutcomes(
		context.Context, integrationstore.IntegrationInboxRecord,
	) (map[string]executionstore.InboxSlotOutcome, error)
}

type IntegrationProfileChoiceProvider interface {
	PresentProfileChoice(context.Context, integrationstore.IntegrationRecord,
		integrationstore.IntegrationProfileChoiceRecord, func(context.Context) error) (string, string, error)
	DismissProfileChoice(context.Context, integrationstore.IntegrationRecord,
		integrationstore.IntegrationProfileChoiceRecord, string) error
}

type ChatIntegrationLauncher struct {
	store     *integrationstore.Store
	execution IntegrationProfileChoiceExecution
	providers map[string]IntegrationInboxProvider
}

func NewChatIntegrationLauncher(
	store *integrationstore.Store,
	execution IntegrationProfileChoiceExecution,
	providers map[string]IntegrationInboxProvider,
) *ChatIntegrationLauncher {
	return &ChatIntegrationLauncher{store: store, execution: execution, providers: providers}
}

func integrationChoiceSourceKey(event IntegrationEvent) string {
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

func (
	l *ChatIntegrationLauncher,
) Decide(ctx context.Context, input IntegrationLaunchContext) ([]IntegrationLaunchIntent, error) {
	if !integrationLaunchEligible(input) {
		return l.decideProfiles(ctx, input, nil)
	}
	ids, err := integrationdefinition.ChatLaunchProfiles(input.Integration.Settings)
	if err != nil {
		return nil, err
	}
	profiles := make([]IntegrationLaunchIntent, 0, len(ids))
	for _, id := range ids {
		profiles = append(profiles, IntegrationLaunchIntent{
			IntegrationID: input.Integration.ID, Slot: integrationdefinition.ProfileLaunchKey, ProfileID: id,
		})
	}
	return l.decideProfiles(ctx, input, profiles)
}

func (l *ChatIntegrationLauncher) decideProfiles(
	ctx context.Context, input IntegrationLaunchContext, profiles []IntegrationLaunchIntent,
) ([]IntegrationLaunchIntent, error) {
	sourceKey := integrationChoiceSourceKey(input.Event)
	choice, exists, err := l.store.GetIntegrationProfileChoiceBySource(ctx, input.Integration.ProjectID,
		input.Integration.ID, sourceKey)
	if err != nil {
		return nil, err
	}
	if exists && choice.SelectedKey != "" {
		return l.selectedChoiceIntent(ctx, choice)
	}
	if !exists && len(profiles) == 0 {
		return nil, nil
	}
	var options []integrationstore.IntegrationProfileChoiceOption
	if exists {
		options = choice.Options
	} else {
		ids := make([]uuid.UUID, 0, len(profiles))
		for _, intent := range profiles {
			ids = append(ids, intent.ProfileID)
		}
		names, err := l.execution.GetAgentProfileDisplayNames(ctx, input.Integration.ProjectID, ids)
		if err != nil {
			return nil, err
		}
		for _, intent := range profiles {
			name, ok := names[intent.ProfileID]
			if !ok {
				return nil, fmt.Errorf("configured launcher profile %s is unavailable: %w",
					intent.ProfileID, ErrIntegrationLaunchUnavailable)
			}
			key, err := publicid.Encode(publicid.KindAgentProfile, intent.ProfileID)
			if err != nil {
				return nil, err
			}
			options = append(options, integrationstore.IntegrationProfileChoiceOption{
				Key: key, ProfileID: intent.ProfileID, Name: name,
			})
		}
	}
	if !exists && len(profiles) == 1 {
		return profiles, nil
	}
	source := input.Event
	source.Launches, source.Directed = nil, false
	raw, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	choice, _, err = l.store.EnsureIntegrationProfileChoice(ctx, input.Receipt.Lease(),
		integrationstore.EnsureIntegrationProfileChoiceInput{
			IntegrationID: input.Integration.ID, Address: input.Address, SourceKey: sourceKey,
			Event: raw, Payload: input.Receipt.Payload, Options: options,
			HasAttachments: len(source.Files) != 0 || (source.Sibling != nil && source.Sibling.AttachmentNotice != ""),
		})
	if errors.Is(err, integrationstore.ErrIntegrationSelectionSettled) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if choice.SelectedKey != "" {
		if choice.SourceKey == sourceKey {
			return l.selectedChoiceIntent(ctx, choice)
		}
		return nil, nil
	}
	if choice.OwnerReceiptID != input.Receipt.ID || !time.Now().Before(choice.ExpiresAt) || choice.MessageID != "" {
		return nil, nil
	}
	provider, ok := l.providers[input.Integration.Provider].(IntegrationProfileChoiceProvider)
	if !ok {
		return nil, fmt.Errorf("profile menus unavailable for provider %s", input.Integration.Provider)
	}
	check := func(ctx context.Context) error {
		if err := l.store.WithIntegrationInboxLease(ctx, input.Receipt.Lease(),
			func(*integrationstore.IntegrationInboxLeaseTx) error { return nil }); err != nil {
			return err
		}
		integration, err := l.store.GetIntegration(ctx, choice.ProjectID, choice.IntegrationID)
		if err != nil {
			return err
		}
		if integration.State != integrationstore.IntegrationStateActive {
			return ErrIntegrationLaunchUnavailable
		}
		return nil
	}
	channel, message, err := provider.PresentProfileChoice(ctx, input.Integration, choice, check)
	if err != nil {
		if errors.Is(err, ErrIntegrationLaunchUnavailable) {
			if expireErr := l.store.ExpireIntegrationProfileChoice(
				ctx, choice.ProjectID, choice.IntegrationID, choice.ID); expireErr != nil {
				return nil, fmt.Errorf("retire unavailable profile menu: %w", expireErr)
			}
		}
		return nil, err
	}
	if err := l.store.RecordIntegrationProfileChoiceMessage(ctx,
		choice.ProjectID, choice.IntegrationID, choice.ID, channel, message); err != nil {
		return nil, err
	}
	return nil, nil
}

// A sibling callback may add attachments, but cannot launch an uncommitted choice:
// the original handoff could fail between this lookup and Freeze.
func (l *ChatIntegrationLauncher) selectedChoiceIntent(
	ctx context.Context, choice integrationstore.IntegrationProfileChoiceRecord,
) ([]IntegrationLaunchIntent, error) {
	receipt, found, err := l.store.GetIntegrationProfileChoiceInbox(ctx, choice.ProjectID, choice.IntegrationID, choice.ID)
	if err != nil || !found {
		return nil, err
	}
	if len(receipt.Plan) != 0 {
		plan, err := decodeIntegrationInboxPlan(receipt.Plan)
		if err != nil {
			return nil, err
		}
		outcomes, err := l.execution.GetIntegrationInboxOutcomes(ctx, receipt)
		if err != nil {
			return nil, err
		}
		for key := range plan.Recipients {
			if outcomes[key] == executionstore.InboxSlotDelivered {
				return choiceIntent(choice), nil
			}
		}
	}
	if receipt.State == integrationstore.IntegrationInboxQueued ||
		receipt.State == integrationstore.IntegrationInboxProcessing {
		return nil, &integrationstore.IntegrationSelectionReservationError{ReceiptID: receipt.ID, State: receipt.State}
	}
	return nil, nil
}

func choiceIntent(choice integrationstore.IntegrationProfileChoiceRecord) []IntegrationLaunchIntent {
	for _, option := range choice.Options {
		if option.Key == choice.SelectedKey {
			return []IntegrationLaunchIntent{
				{IntegrationID: choice.IntegrationID, Slot: integrationdefinition.ProfileLaunchKey, ProfileID: option.ProfileID},
			}
		}
	}
	return nil
}

func SelectChatIntegrationProfile(
	ctx context.Context, store *integrationstore.Store, integrationSetup integrationstore.IntegrationRecord,
	id uuid.UUID, key, actorID, channelID, messageID string,
) (integrationstore.IntegrationProfileChoiceRecord, error) {
	for range 3 {
		choice, err := store.GetIntegrationProfileChoice(ctx, integrationSetup.ProjectID, integrationSetup.ID, id)
		if err != nil {
			return choice, err
		}
		result, err := store.ChooseIntegrationProfile(ctx, integrationstore.ChooseIntegrationProfileInput{
			ProjectID: integrationSetup.ProjectID, IntegrationID: integrationSetup.ID, ID: id,
			Key: key, ActorID: actorID, MessageChannelID: channelID, MessageID: messageID,
			SourceChoiceRevision: choice.Revision, SourceSetupRevision: integrationSetup.SetupRevision,
		})
		if !errors.Is(err, storeerr.ErrConflict) {
			return result, err
		}
	}
	return integrationstore.IntegrationProfileChoiceRecord{}, storeerr.ErrConflict
}

func selectedIntegrationEvent(choice integrationstore.IntegrationProfileChoiceRecord) (*IntegrationEvent, error) {
	var event IntegrationEvent
	if json.Unmarshal(choice.Event, &event) != nil {
		return nil, fmt.Errorf("invalid saved profile-choice event")
	}
	event.Launches, event.Directed = choiceIntent(choice), true
	if len(event.Launches) != 1 {
		return nil, storeerr.ErrUnauthorized
	}
	return &event, nil
}
