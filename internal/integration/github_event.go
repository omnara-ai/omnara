package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type GitHubAppIdentity struct {
	BotUserID int64  `json:"bot_user_id"`
	BotLogin  string `json:"bot_login"`
}

// GitHubIntegrationInboxProvider derives identity from body fields: GitHub does not sign event/delivery headers.
type GitHubIntegrationInboxProvider struct {
	config       github.Config
	secrets      GitHubInboxSecrets
	integrations GitHubInboxIntegrations
}

func (GitHubIntegrationInboxProvider) Expand(
	ctx context.Context, integrationSetup integrationstore.ProjectIntegrationRecord, payload []byte,
) (IntegrationInboxExpansion, error) {
	if err := ctx.Err(); err != nil {
		return IntegrationInboxExpansion{}, err
	}
	event, ok, err := NormalizeGitHubIntegrationEvent(integrationSetup, payload)
	if err != nil || !ok {
		return IntegrationInboxExpansion{}, err
	}
	return IntegrationInboxExpansion{Events: []IntegrationEvent{event}}, nil
}

func (GitHubIntegrationInboxProvider) DownloadFile(
	_ context.Context, _ integrationstore.ProjectIntegrationRecord, _ []byte, _ string,
) (IntegrationInboxFile, error) {
	return IntegrationInboxFile{}, fmt.Errorf("GitHub webhook events do not plan file downloads")
}

type githubReview struct {
	ID       int64       `json:"id"`
	Body     string      `json:"body"`
	State    string      `json:"state"`
	User     github.User `json:"user"`
	CommitID string      `json:"commit_id"`
	HTMLURL  string      `json:"html_url"`
}

type githubEventPayload struct {
	github.Webhook
	Review *githubReview `json:"review"`
}

type GitHubEventMetadata struct {
	RepositoryID   int64  `json:"repository_id"`
	RepositoryName string `json:"repository_name,omitempty"`
	PullRequest    int    `json:"pull_request"`
	EventType      string `json:"event_type"`
	Action         string `json:"action"`
	URL            string `json:"url,omitempty"`
	CommentID      int64  `json:"comment_id,omitempty"`
	ReviewID       int64  `json:"review_id,omitempty"`
	ReplyToID      int64  `json:"reply_to_id,omitempty"`
	ReviewState    string `json:"review_state,omitempty"`
	CommitID       string `json:"commit_id,omitempty"`
	Path           string `json:"path,omitempty"`
	Line           *int   `json:"line,omitempty"`
	StartLine      *int   `json:"start_line,omitempty"`
	Side           string `json:"side,omitempty"`
	StartSide      string `json:"start_side,omitempty"`
	Before         string `json:"before,omitempty"`
	After          string `json:"after,omitempty"`
}

func NormalizeGitHubIntegrationEvent(
	integrationSetup integrationstore.ProjectIntegrationRecord, raw []byte,
) (IntegrationEvent, bool, error) {
	if integrationSetup.Provider != integrationstore.IntegrationProviderGitHub {
		return IntegrationEvent{}, false, storeerr.ErrUnauthorized
	}
	var payload githubEventPayload
	if len(raw) > integrationstore.IntegrationInboxMaxPayloadBytes || !utf8.Valid(raw) ||
		!strings.HasPrefix(strings.TrimSpace(string(raw)), "{") || json.Unmarshal(raw, &payload) != nil {
		return IntegrationEvent{}, false, fmt.Errorf("invalid GitHub receipt JSON")
	}
	eventType := githubPayloadEventType(payload)
	if eventType == "" {
		return IntegrationEvent{}, false, nil
	}
	if !GitHubWebhookInstallationMatches(integrationSetup, payload.Webhook) {
		return IntegrationEvent{}, false, storeerr.ErrUnauthorized
	}
	var identity GitHubAppIdentity
	if json.Unmarshal(integrationSetup.ProviderIdentity, &identity) != nil ||
		identity.BotUserID <= 0 || !validGitHubBotLogin(identity.BotLogin) {
		return IntegrationEvent{}, false, fmt.Errorf("GitHub integration requires verified bot_user_id and bot_login")
	}
	if githubSelfEvent(payload.Sender, identity) {
		return IntegrationEvent{}, false, nil
	}
	metadata := GitHubEventMetadata{
		RepositoryID: payload.Repository.ID, RepositoryName: payload.Repository.FullName,
		EventType: eventType, Action: payload.Action,
	}
	var kind, key, text, mentionText string
	actor := payload.Sender
	switch eventType {
	case "issue_comment":
		metadata.PullRequest = payload.Issue.Number
		kind = "discussion_comment"
		actor, text = payload.Comment.User, payload.Comment.Body
		metadata.CommentID, metadata.URL = payload.Comment.ID, payload.Comment.HTMLURL
		key = fmt.Sprintf("discussion_comment:%d:created", metadata.CommentID)
		mentionText = text
	case "pull_request_review_comment":
		kind = "review_comment"
		actor, text = payload.Comment.User, payload.Comment.Body
		metadata.CommentID, metadata.URL = payload.Comment.ID, payload.Comment.HTMLURL
		metadata.ReviewID, metadata.ReplyToID = payload.Comment.PullRequestReviewID, payload.Comment.InReplyToID
		metadata.CommitID = payload.Comment.CommitID
		metadata.Path, metadata.Line = payload.Comment.Path, payload.Comment.Line
		metadata.StartLine = payload.Comment.StartLine
		metadata.Side, metadata.StartSide = payload.Comment.Side, payload.Comment.StartSide
		key = fmt.Sprintf("review_comment:%d:created", metadata.CommentID)
		mentionText = text
	case "pull_request_review":
		switch payload.Review.State {
		case "approved", "changes_requested", "commented":
		default:
			return IntegrationEvent{}, false, fmt.Errorf("invalid submitted GitHub review state")
		}
		kind, actor = "review_comment", payload.Review.User
		metadata.ReviewID, metadata.ReviewState = payload.Review.ID, payload.Review.State
		metadata.CommitID = payload.Review.CommitID
		metadata.URL = payload.Review.HTMLURL
		text = "Review " + payload.Review.State + ":\n" + payload.Review.Body
		mentionText = payload.Review.Body
		key = fmt.Sprintf("review:%d:submitted", metadata.ReviewID)
	case "pull_request":
		switch payload.Action {
		case "opened":
			kind = "pull_request_opened"
			text = "Pull request opened: " + payload.PullRequest.Title + "\n" + payload.PullRequest.Body
			key = fmt.Sprintf("pull_request:%d:opened", payload.PullRequest.ID)
		case "synchronize":
			if !validGitHubCommit(payload.Before) || !validGitHubCommit(payload.After) ||
				payload.After != payload.PullRequest.Head.SHA {
				return IntegrationEvent{}, false, fmt.Errorf("invalid GitHub synchronize commit identity")
			}
			kind = "commit"
			metadata.Before, metadata.After = payload.Before, payload.After
			text = "Pull request head changed from " + payload.Before + " to " + payload.After
			key = fmt.Sprintf("pull_request:%d:synchronize:%s:%s", payload.PullRequest.ID, payload.Before, payload.After)
			// A force-push can repeat a transition; the signed update time distinguishes it from redelivery.
			if !payload.PullRequest.UpdatedAt.IsZero() {
				key += ":" + payload.PullRequest.UpdatedAt.UTC().Format(time.RFC3339Nano)
			}
		}
		metadata.URL = payload.PullRequest.HTMLURL
	}
	if eventType != "issue_comment" {
		pull := payload.PullRequest
		if pull.ID <= 0 || pull.Base.Repo == nil || pull.Base.Repo.ID != payload.Repository.ID {
			return IntegrationEvent{}, false, fmt.Errorf("GitHub pull request repository identity mismatch")
		}
		metadata.PullRequest = pull.Number
	}
	if payload.Repository.ID <= 0 || metadata.PullRequest <= 0 || actor.ID <= 0 || actor.Login == "" ||
		(payload.Comment != nil && metadata.CommentID <= 0) || (payload.Review != nil && metadata.ReviewID <= 0) {
		return IntegrationEvent{}, false, fmt.Errorf("GitHub event lacks durable object or actor identity")
	}
	humanInput := kind == "discussion_comment" || kind == "review_comment"
	if githubSelfEvent(actor, identity) || (humanInput && (actor.Type != "User" || payload.Sender.Type != "User")) {
		return IntegrationEvent{}, false, nil
	}
	if humanInput && actor.ID != payload.Sender.ID {
		return IntegrationEvent{}, false, fmt.Errorf("GitHub comment author differs from event sender")
	}
	if payload.Review != nil && payload.Review.State == "commented" && strings.TrimSpace(payload.Review.Body) == "" {
		return IntegrationEvent{}, false, nil
	}
	integrationActor, err := executionstore.IntegrationActorParams(
		integrationSetup.ID,
		strconv.FormatInt(actor.ID, 10),
		&actor.Login,
	)
	if err != nil {
		return IntegrationEvent{}, false, err
	}
	result := IntegrationEvent{
		Event: integrationdefinition.Event{
			Scope: integrationdefinition.Scope{GitHub: &integrationdefinition.GitHubScope{
				RepositoryID: payload.Repository.ID, PullRequest: metadata.PullRequest,
			}},
			Kind: kind, Mentioned: humanInput && githubMentionsBot(mentionText, identity.BotLogin),
		},
		SemanticKey:  fmt.Sprintf("github:%d:%s", payload.Repository.ID, key),
		DisplayName:  fmt.Sprintf("%s#%d", payload.Repository.FullName, metadata.PullRequest),
		Actor:        integrationActor,
		DeliveryMode: executionstore.DeliveryModeQueued,
	}
	if humanInput {
		result.DeliveryMode = executionstore.DeliveryModeSteering
		result.CancelOpenInteractions = true
	}
	if err := result.Event.Validate(); err != nil {
		return IntegrationEvent{}, false, err
	}
	result.ContentBlocks, err = json.Marshal([]map[string]string{{"type": "text", "text": text}})
	if err != nil {
		return IntegrationEvent{}, false, err
	}
	result.Metadata, err = json.Marshal(metadata)
	return result, err == nil, err
}

func githubPayloadEventType(p githubEventPayload) string {
	switch {
	case p.Comment != nil && p.Review == nil && p.Issue != nil && p.PullRequest == nil:
		if p.Action == "created" && p.Issue.PullRequest != nil {
			return "issue_comment"
		}
	case p.Comment != nil && p.Review == nil && p.Issue == nil && p.PullRequest != nil:
		if p.Action == "created" {
			return "pull_request_review_comment"
		}
	case p.Comment == nil && p.Review != nil && p.Issue == nil && p.PullRequest != nil:
		if p.Action == "submitted" {
			return "pull_request_review"
		}
	case p.Comment == nil && p.Review == nil && p.Issue == nil && p.PullRequest != nil:
		if p.Action == "opened" || p.Action == "synchronize" {
			return "pull_request"
		}
	}
	return ""
}

func GitHubWebhookInstallationMatches(c integrationstore.ProjectIntegrationRecord, event github.Webhook) bool {
	// GitHub can omit app_id; intake HMAC verification with this integration's credentials establishes App identity.
	appID, appErr := strconv.ParseInt(c.ProviderTenantID, 10, 64)
	installationID, installationErr := strconv.ParseInt(c.ProviderAccountRef, 10, 64)
	return c.Provider == integrationstore.IntegrationProviderGitHub && appErr == nil && installationErr == nil &&
		appID > 0 && installationID > 0 && strconv.FormatInt(appID, 10) == c.ProviderTenantID &&
		strconv.FormatInt(installationID, 10) == c.ProviderAccountRef && event.Installation.ID == installationID &&
		(event.Installation.AppID == 0 || event.Installation.AppID == appID)
}

func githubSelfEvent(user github.User, identity GitHubAppIdentity) bool {
	return user.ID == identity.BotUserID || strings.EqualFold(user.Login, identity.BotLogin)
}

func validGitHubBotLogin(login string) bool {
	login = strings.TrimSuffix(login, "[bot]")
	if login == "" || len(login) > 100 {
		return false
	}
	for _, ch := range login {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-') {
			return false
		}
	}
	return true
}

func githubMentionsBot(body, login string) bool {
	body = strings.ToLower(body)
	login = strings.ToLower(strings.TrimSuffix(login, "[bot]"))
	for offset := 0; offset < len(body); {
		index := strings.Index(body[offset:], "@"+login)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + 1 + len(login)
		if strings.HasPrefix(body[end:], "[bot]") {
			end += len("[bot]")
		}
		before := start == 0 || !githubMentionWord(body[start-1])
		after := end == len(body) || (!githubMentionWord(body[end]) && body[end] != '[')
		if end < len(body) && body[end] == '.' &&
			(end+1 == len(body) || !githubMentionWord(body[end+1])) {
			after = true
		}
		if before && after {
			return true
		}
		offset = end
	}
	return false
}

func githubMentionWord(ch byte) bool {
	return ch >= 128 || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' ||
		ch == '-' || ch == '_' || ch == '@' || ch == '.'
}

func validGitHubCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}
