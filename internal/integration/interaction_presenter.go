package integration

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type InteractionPresenter struct {
	Store      *storage.Store
	HTTPClient *http.Client
	Log        *slog.Logger
}

const AgentRequestFailureMessage = "I couldn't complete this request. " +
	"Please try again later or contact this bot's owner."

type InteractionReceipt struct {
	Provider  string `json:"provider"`
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
}

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
	integrationSetup integrationstore.ProjectIntegrationRecord
	credential       secretstore.SecretPayloadRecord
	destination      executionstore.InteractionDestination
}

func (p InteractionPresenter) access(
	ctx context.Context, projectID uuid.UUID, destination executionstore.InteractionDestination,
) (interactionAccess, error) {
	integrationSetup, err := p.Store.Integrations().GetProjectIntegration(ctx, projectID, destination.IntegrationID)
	if err != nil {
		return interactionAccess{}, err
	}
	if integrationSetup.State != integrationstore.ProjectIntegrationStateActive ||
		integrationSetup.IntegrationType != destination.IntegrationType {
		return interactionAccess{}, storeerr.ErrUnauthorized
	}
	switch integrationSetup.Provider {
	case integrationdefinition.ProviderSlack:
	case integrationdefinition.ProviderDiscord:
	default:
		return interactionAccess{}, storeerr.ErrUnauthorized
	}
	kind, err := integrationstore.ProjectIntegrationCredentialKind(integrationSetup.Provider)
	if err != nil {
		return interactionAccess{}, err
	}
	input := secretstore.ReadProjectAvailableSecretPayloadInput{
		OrgID: integrationSetup.OrgID, ProjectID: projectID, SecretID: integrationSetup.CredentialSecretID, Kind: kind,
	}
	credential, err := p.Store.Secrets().ReadProjectAvailableSecretPayload(ctx, input)
	return interactionAccess{integrationSetup: integrationSetup, credential: credential, destination: destination}, err
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
		GetProjectIntegration(ctx, access.integrationSetup.ProjectID, access.integrationSetup.ID)
	if err != nil {
		return err
	}
	if current.State != integrationstore.ProjectIntegrationStateActive ||
		current.SetupRevision != access.integrationSetup.SetupRevision {
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

func (a interactionAccess) slackTarget(ctx context.Context, client *http.Client) (slack.MessageTarget, error) {
	credentials, err := slack.AppCredentialsFromPayload(a.credential.Payload)
	if err != nil {
		return slack.MessageTarget{}, err
	}
	identity, err := slack.ParseInstallIdentity(a.integrationSetup.ProviderIdentity)
	if err != nil {
		return slack.MessageTarget{}, err
	}
	if err := slack.CheckIdentity(ctx, slack.OAuthConfig{HTTPClient: client}, credentials.BotToken, slack.Identity{
		AppID:       a.integrationSetup.ProviderAccountRef,
		WorkspaceID: a.integrationSetup.ProviderTenantID,
		BotUserID:   identity.BotUserID,
	}); err != nil {
		return slack.MessageTarget{}, err
	}
	channel, thread, err := slack.Destination(a.destination.Address.Kind, a.destination.Address.Ref)
	return slack.MessageTarget{Channel: channel, ThreadTS: thread, BotToken: credentials.BotToken}, err
}

func DiscordInteractionScope(destination executionstore.InteractionDestination) (discord.Scope, error) {
	if integrationdefinition.ProviderForType(destination.IntegrationType) != integrationdefinition.ProviderDiscord {
		return discord.Scope{}, storeerr.ErrUnauthorized
	}
	scope, err := integrationdefinition.ResolveDestination(integrationdefinition.ProviderDiscord, destination.Args)
	if err != nil {
		return discord.Scope{}, err
	}
	kind, ref, err := scope.Conversation()
	if err != nil {
		return discord.Scope{}, err
	}
	if destination.Address != (integrationstore.ConversationAddress{Kind: kind, Ref: ref}) {
		return discord.Scope{}, storeerr.ErrUnauthorized
	}
	return discord.Scope{GuildID: scope.Discord.GuildID, ChannelID: scope.Discord.ChannelID,
		ThreadID: scope.Discord.ThreadID}, nil
}

func (p InteractionPresenter) discordClient(
	ctx context.Context, access interactionAccess, check func(context.Context) error,
) (*discord.Client, error) {
	client, err := discord.NewClient(discord.Config{HTTPClient: p.HTTPClient, BeforeRequest: check,
		Credentials: discord.Credentials{ApplicationID: access.integrationSetup.ProviderTenantID,
			BotUserID: access.integrationSetup.ProviderAccountRef, BotToken: access.credential.Payload[secrets.KeyValue]}})
	if err != nil {
		return nil, err
	}
	if err := client.CheckIdentity(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

func interactionPromptForm(record executionstore.AgentInteractionRecord) (interactionform.Form, error) {
	form, err := record.Form()
	if err != nil {
		return interactionform.Form{}, err
	}
	if record.InteractionKind == executionstore.AgentInteractionKindPermission {
		for i := range form.Questions {
			for j := range form.Questions[i].Options {
				form.Questions[i].Options[j].AllowsText = false
			}
		}
	}
	return form, nil
}

func SlackInteractionPromptPayload(
	destination executionstore.InteractionDestination, agentID uuid.UUID, record executionstore.AgentInteractionRecord,
) (json.RawMessage, error) {
	form, err := interactionPromptForm(record)
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
	// Claims are permanent because an unconfirmed provider send may have succeeded;
	// retrying publication could duplicate the prompt.
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
	var access interactionAccess
	if err := retryInteractionPreflight(ctx, func() error {
		if err := checkAuthority(ctx); err != nil {
			return err
		}
		var err error
		access, err = p.access(ctx, projectID, *destination)
		return err
	}); err != nil {
		return err
	}
	checkOnce := func(ctx context.Context) error { return p.recheck(ctx, access, checkAuthority) }
	check := func(ctx context.Context) error {
		return retryInteractionPreflight(ctx, func() error { return checkOnce(ctx) })
	}
	form, err := interactionPromptForm(record)
	if err != nil {
		return err
	}
	interactionID, err := publicid.Encode(publicid.KindAgentInteraction, id)
	if err != nil {
		return err
	}
	var receipt InteractionReceipt
	switch access.integrationSetup.Provider {
	case integrationdefinition.ProviderSlack:
		// The outer loop owns retries for both authority reads and auth.test.
		preflightClient := slack.WithRequestCheck(p.HTTPClient, checkOnce)
		var target slack.MessageTarget
		if err := retryInteractionPreflight(ctx, func() error {
			var err error
			target, err = access.slackTarget(ctx, preflightClient)
			return err
		}); err != nil {
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
			Provider:  integrationdefinition.ProviderSlack,
			ChannelID: target.Channel,
			MessageID: messageID,
		}
	case integrationdefinition.ProviderDiscord:
		if DiscordInteractionPublicKey(access.integrationSetup.ProviderConfig) == "" {
			return storeerr.ErrUnauthorized
		}
		client, err := p.discordClient(ctx, access, check)
		if err != nil {
			return err
		}
		scope, err := DiscordInteractionScope(access.destination)
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
			Provider: integrationdefinition.ProviderDiscord, ChannelID: message.ChannelID, MessageID: message.ID,
		}
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	receiptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	input := executionstore.RecordInteractionPresentationReceiptInput{
		ProjectID: projectID, AgentID: agentID, ID: id, Destination: *destination, Receipt: raw,
	}
	current, err := p.Store.Execution().RecordInteractionPresentationReceipt(receiptCtx, input)
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
		return fmt.Errorf("slack interaction presentation: %w", &slack.APIError{Result: result})
	}
	return nil
}

// Only use this for reads before publication. The permanent presentation claim
// remains owned by this attempt; neither a send nor an ambiguous send result may
// enter this loop. Discord retries its identity/channel reads in its HTTP client.
func retryInteractionPreflight(ctx context.Context, read func() error) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := read()
		if err == nil || attempt == 2 || !transientInteractionPreflight(err) {
			return err
		}
		delay := max(time.Duration(attempt+1)*100*time.Millisecond, providerRetryDelay(err))
		if delay > time.Second {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func transientInteractionPreflight(err error) bool {
	if errors.Is(err, storeerr.ErrUnauthorized) || errors.Is(err, storeerr.ErrNotFound) ||
		errors.Is(err, storeerr.ErrStateTransitionConflict) || errors.Is(err, context.Canceled) {
		return false
	}
	var slackErr *slack.APIError
	if errors.As(err, &slackErr) {
		// auth.test is a read even though Slack uses POST and classifies network
		// failures as DeliveryUnknown. No prompt has been sent at this stage.
		return !slackErr.Result.PermanentFailure && (slackErr.Result.RateLimited ||
			slackErr.Result.TransientFailure || slackErr.Result.DeliveryUnknown)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "08000", "08003", "08006", "53300", "57P01", "57P02", "57P03":
			return true
		}
		return false
	}
	var networkErr net.Error
	return pgconn.SafeToRetry(err) || errors.As(err, &networkErr) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
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
		if result.RateLimited && result.StatusCode < 500 && attempt < 2 &&
			result.RetryAfter >= 0 && result.RetryAfter <= 3*time.Second-slept {
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
		if result.StatusCode >= 500 || result.DeliveryUnknown || result.TransientFailure {
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

func InteractionResolvedText(record executionstore.AgentInteractionRecord) string {
	switch record.InteractionKind {
	case executionstore.AgentInteractionKindPermission:
		request, err := toolpermission.ParseRequest(record.Request)
		if err != nil {
			return "Permission response recorded."
		}
		resolution, err := interactionform.ParseResolution(request.Form, record.Resolution)
		if err != nil {
			return "Permission response recorded."
		}
		decision, err := toolpermission.Resolve(request, resolution)
		if err != nil {
			return "Permission response recorded."
		}
		if decision.Decision == toolpermission.DecisionAllow {
			return "Approved: " + request.Authorization.ToolName
		}
		return "Denied: " + request.Authorization.ToolName
	case executionstore.AgentInteractionKindQuestion:
		return "Answers recorded."
	default:
		return "Recorded."
	}
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
	if receipt.Provider != access.integrationSetup.Provider {
		return storeerr.ErrUnauthorized
	}
	check := func(ctx context.Context) error { return p.recheck(ctx, access, checkAuthority) }
	text := "This interaction is closed."
	if record.State == executionstore.AgentInteractionStateResolved {
		text = InteractionResolvedText(record)
	}
	switch access.integrationSetup.Provider {
	case integrationdefinition.ProviderSlack:
		client := slack.WithRequestCheck(p.HTTPClient, check)
		target, err := access.slackTarget(ctx, client)
		if err != nil {
			return err
		}
		if target.Channel != receipt.ChannelID {
			return storeerr.ErrUnauthorized
		}
		result, err := slack.DismissPrompt(
			ctx,
			client,
			target,
			receipt.MessageID,
			text,
		)
		return slackPromptError(result, err)
	case integrationdefinition.ProviderDiscord:
		client, err := p.discordClient(ctx, access, check)
		if err != nil {
			return err
		}
		scope, err := DiscordInteractionScope(access.destination)
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

func (p InteractionPresenter) PostRuntimeMessage(
	ctx context.Context, projectID, agentID, operationID uuid.UUID, text string,
) error {
	destination, err := p.Store.Execution().GetSelectedInteractionDestination(ctx, projectID, agentID)
	if err != nil {
		return err
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
		current, err := p.Store.Execution().GetSelectedInteractionDestination(ctx, projectID, agentID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, destination) {
			return storeerr.ErrUnauthorized
		}
		return nil
	}
	check := func(ctx context.Context) error { return p.recheck(ctx, access, authority) }
	switch access.integrationSetup.Provider {
	case integrationdefinition.ProviderSlack:
		client := slack.WithRequestCheck(p.HTTPClient, check)
		target, err := access.slackTarget(ctx, client)
		if err != nil {
			return err
		}
		payload, err := slack.PromptPayload(target, text, nil)
		if err != nil {
			return err
		}
		_, result, err := slack.PostPromptReceipt(ctx, client, target, payload)
		return slackPromptError(result, err)
	case integrationdefinition.ProviderDiscord:
		client, err := p.discordClient(ctx, access, check)
		if err != nil {
			return err
		}
		scope, err := DiscordInteractionScope(access.destination)
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
