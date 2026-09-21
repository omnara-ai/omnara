package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// GitHubAppIdentity belongs in app.ProviderIdentity. Setup must resolve
// the App's bot account: neither its numeric App ID nor installation ID is the
// bot's user ID or @mention login. This normalizer performs no provider I/O.
type GitHubAppIdentity struct {
	BotUserID int64  `json:"bot_user_id"`
	BotLogin  string `json:"bot_login"`
}

// GitHubAppInboxProvider expands already-verified raw receipts. Header metadata
// is deliberately not needed: GitHub signs the body, not X-GitHub-Event or the
// delivery ID. Signed object shapes/actions establish kind and semantic identity.
// Do not feed unverified HTTP bodies to this adapter.
type GitHubAppInboxProvider struct{}

func (GitHubAppInboxProvider) Expand(
	ctx context.Context, appSetup integrationstore.ProjectAppRecord, payload []byte,
) (AppInboxExpansion, error) {
	if err := ctx.Err(); err != nil {
		return AppInboxExpansion{}, err
	}
	event, ok, err := NormalizeGitHubAppEvent(appSetup, payload)
	if err != nil || !ok {
		return AppInboxExpansion{}, err
	}
	return AppInboxExpansion{Events: []AppEvent{event}}, nil
}

func (GitHubAppInboxProvider) DownloadFile(
	_ context.Context, _ integrationstore.ProjectAppRecord, _ []byte, _ string,
) (AppInboxFile, error) {
	return AppInboxFile{}, fmt.Errorf("GitHub webhook events do not plan file downloads")
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

// GitHubEventMetadata keeps display names separate from immutable scope. URLs
// and diff paths are presentation only, never authorities or download targets.
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

// NormalizeGitHubAppEvent handles new human discussion/diff comments, submitted
// reviews, PR opens and synchronize events. Edits, lifecycle events, pushes
// without proven PR identity, bots' comments and self-events produce no input.
// Human comments/reviews steer and cancel open interactions; opens/commits queue.
// No provider mutation (including review publication) happens at this boundary.
func NormalizeGitHubAppEvent(
	appSetup integrationstore.ProjectAppRecord, raw []byte,
) (AppEvent, bool, error) {
	if appSetup.Provider != integrationstore.IntegrationProviderGitHub {
		return AppEvent{}, false, storeerr.ErrUnauthorized
	}
	var payload githubEventPayload
	if len(raw) > integrationstore.IntegrationInboxMaxPayloadBytes || !utf8.Valid(raw) ||
		!strings.HasPrefix(strings.TrimSpace(string(raw)), "{") || json.Unmarshal(raw, &payload) != nil {
		return AppEvent{}, false, fmt.Errorf("invalid GitHub receipt JSON")
	}
	// Unsupported payloads (including signed ping) need no bot metadata. All
	// routable shapes below must prove the installation and repository identity.
	eventType := githubPayloadEventType(payload)
	if eventType == "" {
		return AppEvent{}, false, nil
	}
	if !GitHubWebhookInstallationMatches(appSetup, payload.Webhook) {
		return AppEvent{}, false, storeerr.ErrUnauthorized
	}
	var identity GitHubAppIdentity
	if json.Unmarshal(appSetup.ProviderIdentity, &identity) != nil ||
		identity.BotUserID <= 0 || !validGitHubBotLogin(identity.BotLogin) {
		return AppEvent{}, false, fmt.Errorf("GitHub app requires verified bot_user_id and bot_login")
	}
	if githubSelfEvent(payload.Sender, identity) {
		return AppEvent{}, false, nil
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
			return AppEvent{}, false, fmt.Errorf("invalid submitted GitHub review state")
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
				return AppEvent{}, false, fmt.Errorf("invalid GitHub synchronize commit identity")
			}
			kind = "commit"
			metadata.Before, metadata.After = payload.Before, payload.After
			text = "Pull request head changed from " + payload.Before + " to " + payload.After
			key = fmt.Sprintf("pull_request:%d:synchronize:%s:%s", payload.PullRequest.ID, payload.Before, payload.After)
			// A later force-push can repeat the same transition. GitHub's signed
			// PR update time distinguishes it while preserving redelivery dedupe.
			if !payload.PullRequest.UpdatedAt.IsZero() {
				key += ":" + payload.PullRequest.UpdatedAt.UTC().Format(time.RFC3339Nano)
			}
		}
		metadata.URL = payload.PullRequest.HTMLURL
	}
	if eventType != "issue_comment" {
		pull := payload.PullRequest
		if pull.ID <= 0 || pull.Base.Repo == nil || pull.Base.Repo.ID != payload.Repository.ID {
			return AppEvent{}, false, fmt.Errorf("GitHub pull request repository identity mismatch")
		}
		metadata.PullRequest = pull.Number
	}
	if payload.Repository.ID <= 0 || metadata.PullRequest <= 0 || actor.ID <= 0 || actor.Login == "" ||
		(payload.Comment != nil && metadata.CommentID <= 0) || (payload.Review != nil && metadata.ReviewID <= 0) {
		return AppEvent{}, false, fmt.Errorf("GitHub event lacks durable object or actor identity")
	}
	humanInput := kind == "discussion_comment" || kind == "review_comment"
	if githubSelfEvent(actor, identity) || (humanInput && (actor.Type != "User" || payload.Sender.Type != "User")) {
		return AppEvent{}, false, nil
	}
	// New-comment/review actors must agree with the signed sender. Edited events
	// (where sender and author may differ legitimately) are intentionally ignored.
	if humanInput && actor.ID != payload.Sender.ID {
		return AppEvent{}, false, fmt.Errorf("GitHub comment author differs from event sender")
	}
	appActor, err := executionstore.AppActorParams(appSetup.ID, strconv.FormatInt(actor.ID, 10), &actor.Login)
	if err != nil {
		return AppEvent{}, false, err
	}
	result := AppEvent{
		Event: appdefinition.Event{
			Scope: appdefinition.Scope{GitHub: &appdefinition.GitHubScope{
				RepositoryID: payload.Repository.ID, PullRequest: metadata.PullRequest,
			}},
			Kind: kind, Mentioned: humanInput && githubMentionsBot(mentionText, identity.BotLogin),
		},
		SemanticKey:  fmt.Sprintf("github:%d:%s", payload.Repository.ID, key),
		DisplayName:  fmt.Sprintf("%s#%d", payload.Repository.FullName, metadata.PullRequest),
		Actor:        appActor,
		DeliveryMode: executionstore.DeliveryModeQueued,
	}
	if humanInput {
		result.DeliveryMode = executionstore.DeliveryModeSteering
		result.CancelOpenInteractions = true
	}
	if err := result.Event.Validate(); err != nil {
		return AppEvent{}, false, err
	}
	result.ContentBlocks, err = json.Marshal([]map[string]string{{"type": "text", "text": text}})
	if err != nil {
		return AppEvent{}, false, err
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

// GitHubWebhookInstallationMatches checks signed payload facts against the
// app. GitHub omits app_id on many installation summaries; HMAC validation
// with that app's App credentials establishes App identity at intake.
func GitHubWebhookInstallationMatches(c integrationstore.ProjectAppRecord, event github.Webhook) bool {
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

// Mentions are explicit text tokens, case insensitive. GitHub Apps are commonly
// addressed by @slug as well as the bot account's full @slug[bot] login. This is
// a textual trigger, not GitHub notification delivery or a Markdown renderer.
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
