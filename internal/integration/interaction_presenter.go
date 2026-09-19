package integration

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// InteractionPresenter mirrors core interactions. Handler authority is independent
// of model send tools and listeners. Errors never resolve or fail the interaction.
type InteractionPresenter struct {
	Store      *storage.Store
	HTTPClient *http.Client
}

type InteractionReceipt struct {
	Provider  string `json:"provider"`
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
}

// DismissCanceled is the post-commit hook for input admission/worker callers.
// Its failure must not retry or roll back the accepted input. A late receipt is
// independently handled by Present; this path handles already confirmed sends.
func (p InteractionPresenter) DismissCanceled(
	ctx context.Context, projectID, agentID uuid.UUID, ids []uuid.UUID,
) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var failures []error
	for _, id := range ids {
		record, found, err := p.Store.Execution().GetAgentInteraction(ctx, projectID, agentID, id)
		if err == nil && found {
			err = p.Dismiss(ctx, record)
		}
		if err != nil {
			failures = append(failures, err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(failures...)
}

type interactionAccess struct {
	connection  integrationstore.IntegrationConnectionRecord
	credential  secretstore.SecretPayloadRecord
	destination executionstore.InteractionDestination
}

func (p InteractionPresenter) access(
	ctx context.Context, projectID uuid.UUID, destination executionstore.InteractionDestination,
) (interactionAccess, error) {
	connection, err := p.Store.Integrations().GetIntegrationConnection(ctx, projectID, destination.ConnectionID)
	if err != nil {
		return interactionAccess{}, err
	}
	if connection.State != integrationstore.IntegrationConnectionStateActive {
		return interactionAccess{}, storeerr.ErrUnauthorized
	}
	switch destination.HandlerDefinition {
	case appdefinition.SlackInteractions:
		if connection.Provider != appdefinition.ProviderSlack {
			return interactionAccess{}, storeerr.ErrUnauthorized
		}
	case appdefinition.DiscordInteractions:
		if connection.Provider != appdefinition.ProviderDiscord ||
			DiscordInteractionPublicKey(connection.ProviderConfig) == "" {
			return interactionAccess{}, storeerr.ErrUnauthorized
		}
	default:
		return interactionAccess{}, storeerr.ErrUnauthorized
	}
	kind, err := integrationstore.IntegrationConnectionCredentialKind(connection.Provider)
	if err != nil {
		return interactionAccess{}, err
	}
	input := secretstore.ReadProjectAvailableSecretPayloadInput{
		OrgID: connection.OrgID, ProjectID: projectID, SecretID: connection.CredentialSecretID, Kind: kind,
	}
	credential, err := p.Store.Secrets().ReadProjectAvailableSecretPayload(ctx, input)
	return interactionAccess{connection: connection, credential: credential, destination: destination}, err
}

func DiscordInteractionPublicKey(config json.RawMessage) string {
	var value struct {
		PublicKey string `json:"public_key"`
	}
	if json.Unmarshal(config, &value) != nil {
		return ""
	}
	return value.PublicKey
}

func (p InteractionPresenter) recheck(
	ctx context.Context, access interactionAccess, authority func(context.Context) error,
) error {
	if err := authority(ctx); err != nil {
		return err
	}
	current, err := p.Store.Integrations().
		GetIntegrationConnection(ctx, access.connection.ProjectID, access.connection.ID)
	if err != nil {
		return err
	}
	if current.State != integrationstore.IntegrationConnectionStateActive ||
		!current.UpdatedAt.Equal(access.connection.UpdatedAt) {
		return storeerr.ErrUnauthorized
	}
	secret, err := p.Store.Secrets().GetProjectAvailableSecret(
		ctx, current.OrgID, current.ProjectID, current.CredentialSecretID,
	)
	if err != nil {
		return err
	}
	if secret.Secret.CurrentVersionID != access.credential.CurrentVersionID {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func (a interactionAccess) slackTarget() (slack.MessageTarget, error) {
	credentials, err := slack.AppCredentialsFromPayload(a.credential.Payload)
	if err != nil {
		return slack.MessageTarget{}, err
	}
	channel, thread, err := slack.Destination(a.destination.Address.Kind, a.destination.Address.Ref)
	return slack.MessageTarget{Channel: channel, ThreadTS: thread, BotToken: credentials.BotToken}, err
}

func (a interactionAccess) discordScope() (discord.Scope, error) {
	address := a.destination.Address
	if address.Kind == "channel" {
		return discord.Scope{ChannelID: address.Ref}, nil
	}
	if address.Kind == "thread" {
		channel, thread, ok := strings.Cut(address.Ref, ":")
		if ok {
			return discord.Scope{ChannelID: channel, ThreadID: thread}, nil
		}
	}
	return discord.Scope{}, errors.New("unsupported Discord interaction scope")
}

func (p InteractionPresenter) discordClient(
	ctx context.Context, access interactionAccess, check func(context.Context) error,
) (*discord.Client, error) {
	client, err := discord.NewClient(discord.Config{HTTPClient: p.HTTPClient, BeforeRequest: check,
		Credentials: discord.Credentials{ApplicationID: access.connection.ProviderTenantID,
			BotUserID: access.connection.ProviderAccountRef, BotToken: access.credential.Payload[secrets.KeyValue]}})
	if err != nil {
		return nil, err
	}
	// Secret values can rotate independently of connection setup. Revalidate the
	// live token's identity before any prompt, dismissal or runtime message.
	if err := client.CheckIdentity(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

// SlackInteractionPromptPayload renders a captured form and destination. It is
// pure; the presenter separately checks live authority before every request.
func SlackInteractionPromptPayload(
	destination executionstore.InteractionDestination, agentID uuid.UUID, record executionstore.AgentInteractionRecord,
) (json.RawMessage, error) {
	form, err := record.Form()
	if err != nil {
		return nil, err
	}
	interactionID, err := publicid.Encode(publicid.KindAgentInteraction, record.ID)
	if err != nil {
		return nil, err
	}
	agent, err := publicid.Encode(publicid.KindAgent, agentID)
	if err != nil {
		return nil, err
	}
	targetID, err := publicid.Encode(publicid.KindIntegrationTarget, destination.IntegrationTargetID)
	if err != nil {
		return nil, err
	}
	channel, thread, err := slack.Destination(destination.Address.Kind, destination.Address.Ref)
	if err != nil {
		return nil, err
	}
	text, blocks := slack.InteractionFormPromptBlocks(form, slack.PromptActionValue{Type: slack.PromptType,
		InteractionID: interactionID, AgentID: agent, IntegrationTargetID: targetID})
	return slack.PromptPayload(slack.MessageTarget{Channel: channel, ThreadTS: thread}, text, blocks)
}

func (p InteractionPresenter) Present(ctx context.Context, projectID, agentID, id uuid.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, interactionPresentationTimeout)
	defer cancel()
	record, found, err := p.Store.Execution().GetAgentInteraction(ctx, projectID, agentID, id)
	if err != nil || !found {
		return err
	}
	destination, err := record.CapturedDestination()
	if err != nil || destination == nil {
		return err
	}
	if record.State != executionstore.AgentInteractionStateOpen {
		return p.Dismiss(ctx, record)
	}
	if len(record.PresentationReceipt) != 0 {
		return nil
	}
	if destination.HandlerDefinition != appdefinition.SlackInteractions &&
		destination.HandlerDefinition != appdefinition.DiscordInteractions {
		return fmt.Errorf("unsupported interaction handler %q", destination.HandlerDefinition)
	}
	// A claim is permanent, including an uncertain commit or provider response.
	// Recovery can discover work that never started, but cannot safely infer that
	// a previous provider request failed. The dashboard remains authoritative.
	claimed, err := p.Store.Execution().ClaimInteractionPresentation(ctx, projectID, agentID, id)
	if err != nil || !claimed {
		return err
	}
	checkAuthority := func(ctx context.Context) error {
		current, err := p.Store.Execution().GetAgentInteractionForPresentation(ctx, projectID, agentID, id)
		if err != nil {
			return err
		}
		if current.State != executionstore.AgentInteractionStateOpen {
			return storeerr.ErrStateTransitionConflict
		}
		return nil
	}
	if err := checkAuthority(ctx); err != nil {
		return err
	}
	access, err := p.access(ctx, projectID, *destination)
	if err != nil {
		return err
	}
	check := func(ctx context.Context) error { return p.recheck(ctx, access, checkAuthority) }
	form, err := record.Form()
	if err != nil {
		return err
	}
	interactionID, err := publicid.Encode(publicid.KindAgentInteraction, id)
	if err != nil {
		return err
	}
	var receipt InteractionReceipt
	switch destination.HandlerDefinition {
	case appdefinition.SlackInteractions:
		target, err := access.slackTarget()
		if err != nil {
			return err
		}
		payload, err := SlackInteractionPromptPayload(*destination, agentID, record)
		if err != nil {
			return err
		}
		client := slack.WithRequestCheck(p.HTTPClient, check)
		messageID, err := postSlackInteraction(ctx, client, target, payload, interactionID)
		if err != nil {
			return err
		}
		receipt = InteractionReceipt{
			Provider:  appdefinition.ProviderSlack,
			ChannelID: target.Channel,
			MessageID: messageID,
		}
	case appdefinition.DiscordInteractions:
		client, err := p.discordClient(ctx, access, check)
		if err != nil {
			return err
		}
		scope, err := access.discordScope()
		if err != nil {
			return err
		}
		text, components := discord.InteractionFormPrompt(form, interactionID)
		message, err := client.CreateMessage(ctx, scope, discord.MessageArgs{Content: text, Components: components,
			Nonce: base64.RawURLEncoding.EncodeToString(id[:])})
		if err != nil {
			return err
		}
		receipt = InteractionReceipt{
			Provider: appdefinition.ProviderDiscord, ChannelID: message.ChannelID, MessageID: message.ID,
		}
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	// A provider success can arrive after cancellation. Persist its identity even
	// when the request context ended, then best-effort remove the stale controls.
	receiptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	input := executionstore.RecordInteractionPresentationReceiptInput{
		ProjectID: projectID, AgentID: agentID, ID: id, Destination: *destination, Receipt: raw,
	}
	current, err := p.Store.Execution().RecordInteractionPresentationReceipt(receiptCtx, input)
	// Retry only persistence of the confirmed receipt, never publication. This
	// operation is idempotent even when the previous commit response was lost.
	for attempt := 0; err != nil && attempt < 2 && receiptCtx.Err() == nil; attempt++ {
		if errors.Is(err, storeerr.ErrNotFound) || errors.Is(err, storeerr.ErrUnauthorized) ||
			errors.Is(err, storeerr.ErrIdempotencyConflict) {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-receiptCtx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
		current, err = p.Store.Execution().RecordInteractionPresentationReceipt(receiptCtx, input)
	}
	if err != nil {
		return err
	}
	if current.State != executionstore.AgentInteractionStateOpen {
		return p.Dismiss(receiptCtx, current)
	}
	return nil
}

func slackPromptError(result slack.APIResult, err error) error {
	if err != nil {
		return err
	}
	if result.RateLimited || result.TransientFailure || result.PermanentFailure || result.DeliveryUnknown {
		return errors.New("slack interaction presentation failed")
	}
	return nil
}

func postSlackInteraction(
	ctx context.Context, client *http.Client, target slack.MessageTarget, payload json.RawMessage, interactionID string,
) (string, error) {
	var slept time.Duration
	for attempt := range 3 {
		id, result, err := slack.PostPromptReceipt(ctx, client, target, payload)
		if err != nil {
			return "", err
		}
		if id != "" {
			return id, nil
		}
		if result.RateLimited && attempt < 2 && result.RetryAfter >= 0 && slept+result.RetryAfter <= 3*time.Second {
			timer := time.NewTimer(result.RetryAfter)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
			slept += result.RetryAfter
			continue
		}
		if result.DeliveryUnknown || result.TransientFailure {
			id, result, err = slack.ReconcilePromptReceipt(ctx, client, target, interactionID)
			if err == nil && id != "" {
				return id, nil
			}
			if err == nil {
				err = errors.New("slack interaction delivery outcome is unknown")
			}
		}
		return "", slackPromptError(result, err)
	}
	return "", errors.New("slack interaction retry limit reached")
}

func (p InteractionPresenter) Dismiss(ctx context.Context, record executionstore.AgentInteractionRecord) error {
	if record.State == executionstore.AgentInteractionStateOpen || len(record.PresentationReceipt) == 0 {
		return nil
	}
	destination, err := record.CapturedDestination()
	if err != nil || destination == nil {
		return err
	}
	var receipt InteractionReceipt
	if json.Unmarshal(record.PresentationReceipt, &receipt) != nil || receipt.MessageID == "" {
		return errors.New("invalid interaction receipt")
	}
	checkAuthority := func(ctx context.Context) error {
		_, err := p.Store.Execution().
			GetAgentInteractionForPresentation(ctx, record.ProjectID, record.AgentID, record.ID)
		return err
	}
	if err := checkAuthority(ctx); err != nil {
		return err
	}
	access, err := p.access(ctx, record.ProjectID, *destination)
	if err != nil {
		return err
	}
	if receipt.Provider != access.connection.Provider {
		return storeerr.ErrUnauthorized
	}
	check := func(ctx context.Context) error { return p.recheck(ctx, access, checkAuthority) }
	text := "This interaction is closed."
	if record.State == executionstore.AgentInteractionStateResolved {
		text = "Response recorded."
	}
	switch destination.HandlerDefinition {
	case appdefinition.SlackInteractions:
		target, err := access.slackTarget()
		if err != nil {
			return err
		}
		if target.Channel != receipt.ChannelID {
			return storeerr.ErrUnauthorized
		}
		result, err := slack.DismissPrompt(
			ctx,
			slack.WithRequestCheck(p.HTTPClient, check),
			target,
			receipt.MessageID,
			text,
		)
		return slackPromptError(result, err)
	case appdefinition.DiscordInteractions:
		client, err := p.discordClient(ctx, access, check)
		if err != nil {
			return err
		}
		scope, err := access.discordScope()
		if err != nil {
			return err
		}
		channel := scope.ChannelID
		if scope.ThreadID != "" {
			channel = scope.ThreadID
		}
		if channel != receipt.ChannelID {
			return storeerr.ErrUnauthorized
		}
		_, err = client.EditMessage(ctx, scope, receipt.MessageID, text, nil)
		return err
	}
	return nil
}

// PostRuntimeMessage uses only the currently selected eligible handler. It grants
// no model send capability and never falls back to another destination.
func (p InteractionPresenter) PostRuntimeMessage(
	ctx context.Context, projectID, agentID, operationID uuid.UUID, text string,
) error {
	choices, err := p.Store.Execution().ListInteractionDestinations(ctx, projectID, agentID)
	if err != nil {
		return err
	}
	var destination *executionstore.InteractionDestination
	for _, choice := range choices.Destinations {
		if choice.Destination.IntegrationTargetID == choices.Current.IntegrationTargetID &&
			choice.Destination.ResourceKey == choices.Current.ResourceKey {
			d := choice.Destination
			destination = &d
		}
	}
	if destination == nil {
		return nil
	}
	access, err := p.access(ctx, projectID, *destination)
	if err != nil {
		return err
	}
	authority := func(ctx context.Context) error {
		if err := p.Store.Execution().EnsureRuntimeLockActive(ctx, projectID, agentID, operationID); err != nil {
			return err
		}
		current, err := p.Store.Execution().ListInteractionDestinations(ctx, projectID, agentID)
		if err != nil {
			return err
		}
		if current.Current != choices.Current {
			return storeerr.ErrUnauthorized
		}
		for _, choice := range current.Destinations {
			if choice.Destination == *destination {
				return nil
			}
		}
		return storeerr.ErrUnauthorized
	}
	check := func(ctx context.Context) error { return p.recheck(ctx, access, authority) }
	switch destination.HandlerDefinition {
	case appdefinition.SlackInteractions:
		target, err := access.slackTarget()
		if err != nil {
			return err
		}
		payload, err := slack.PromptPayload(target, text, nil)
		if err != nil {
			return err
		}
		_, result, err := slack.PostPromptReceipt(ctx, slack.WithRequestCheck(p.HTTPClient, check), target, payload)
		return slackPromptError(result, err)
	case appdefinition.DiscordInteractions:
		client, err := p.discordClient(ctx, access, check)
		if err != nil {
			return err
		}
		scope, err := access.discordScope()
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(fmt.Sprint(operationID, text)))
		runes := []rune(text)
		if len(runes) > 2000 {
			text = string(runes[:1999]) + "…"
		}
		_, err = client.CreateMessage(ctx, scope, discord.MessageArgs{
			Content: text, Nonce: base64.RawURLEncoding.EncodeToString(digest[:16]),
		})
		return err
	}
	return nil
}
