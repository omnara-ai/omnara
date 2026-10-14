package integration

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const AgentRequestFailureMessage = "I couldn't complete this request. " +
	"Please try again later or contact this bot's owner."

type RuntimeFailureNotifier struct {
	Store      *storage.Store
	HTTPClient *http.Client
}

func (n RuntimeFailureNotifier) Notify(ctx context.Context, projectID, agentID, runtimeID uuid.UUID) error {
	target, found, err := n.Store.Integrations().GetAgentIntegrationLaunchOwner(ctx, projectID, agentID)
	if err != nil || !found {
		return err
	}
	address, found, err := n.Store.Integrations().GetAgentIntegrationConversation(
		ctx, projectID, agentID, target.IntegrationID,
	)
	if err != nil {
		return err
	}
	if !found || address.Kind != target.ScopeKind || address.Ref != target.ScopeRef {
		return storeerr.ErrUnauthorized
	}
	check := func(ctx context.Context) error {
		if err := n.Store.Execution().EnsureRuntimeLockActive(ctx, projectID, agentID, runtimeID); err != nil {
			return err
		}
		current, found, err := n.Store.Integrations().GetAgentIntegrationLaunchOwner(ctx, projectID, agentID)
		if err != nil {
			return err
		}
		if !found || current.ID != target.ID || current.IntegrationID != target.IntegrationID ||
			current.ScopeKind != address.Kind || current.ScopeRef != address.Ref || current.LaunchKey != target.LaunchKey {
			return storeerr.ErrUnauthorized
		}
		return nil
	}
	if err := check(ctx); err != nil {
		return err
	}
	setup, err := n.Store.Integrations().GetIntegration(ctx, projectID, target.IntegrationID)
	if err != nil {
		return err
	}
	return n.send(ctx, setup, address, runtimeID, check)
}

func (n RuntimeFailureNotifier) send(
	ctx context.Context, setup integrationstore.IntegrationRecord, address integrationstore.ConversationAddress,
	runtimeID uuid.UUID, check func(context.Context) error,
) error {
	scope, err := integrationdefinition.ParseConversation(setup.Provider, address.Kind, address.Ref)
	if err != nil {
		return err
	}
	switch setup.Provider {
	case integrationdefinition.ProviderSlack:
		provider := NewSlackIntegrationInboxProvider(
			slack.OAuthConfig{HTTPClient: slack.WithRequestCheck(n.HTTPClient, check)},
			n.Store.Secrets(), n.Store.Integrations(), nil,
		)
		config, token, _, err := provider.requestAccess(ctx, setup)
		if err != nil {
			return err
		}
		result, err := slack.PostPlainMessage(ctx, config, slack.MessageTarget{
			Channel: scope.Slack.ChannelID, ThreadTS: scope.Slack.ThreadTS, BotToken: token,
		}, AgentRequestFailureMessage)
		return slackFeedbackError(result, err)
	case integrationdefinition.ProviderDiscord:
		provider := NewDiscordIntegrationInboxProvider(
			discord.Config{HTTPClient: n.HTTPClient}, n.Store.Secrets(), n.Store.Integrations(),
		)
		client, _, err := provider.requestAccess(ctx, setup, check)
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(fmt.Sprint(runtimeID, AgentRequestFailureMessage)))
		_, err = client.CreateMessage(ctx, discord.Scope(*scope.Discord), discord.MessageArgs{
			Content: AgentRequestFailureMessage, Nonce: base64.RawURLEncoding.EncodeToString(digest[:16]),
		})
		return err
	case integrationdefinition.ProviderGitHub:
		provider := NewGitHubIntegrationInboxProvider(
			github.Config{HTTPClient: n.HTTPClient, BeforeRequest: check}, n.Store.Secrets(), n.Store.Integrations(),
		)
		client, err := provider.requestAccess(ctx, setup)
		if err != nil {
			return err
		}
		_, err = client.CreateDiscussionComment(ctx, github.Scope{
			RepositoryID: scope.GitHub.RepositoryID, PullRequest: scope.GitHub.PullRequest,
		}, AgentRequestFailureMessage)
		return err
	default:
		return nil
	}
}
