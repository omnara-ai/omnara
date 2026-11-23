package tooloutput

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

// SlackMessage is the result of posting a text-only Slack message.
type SlackMessage struct {
	Integration string `json:"integration"`
	ChannelID   string `json:"channel_id"`
	MessageTS   string `json:"message_ts"`
	// ThreadTS is empty when the conversation is a channel or DM rather than a thread.
	ThreadTS string `json:"thread_ts"`
}

// SlackFiles is the result of posting a Slack message with files; the text is the files' initial comment.
type SlackFiles struct {
	Integration string   `json:"integration"`
	ChannelID   string   `json:"channel_id"`
	ThreadTS    string   `json:"thread_ts"`
	FileIDs     []string `json:"file_ids"`
}

// DiscordMessage is the result of posting a Discord message.
type DiscordMessage struct {
	Integration string `json:"integration"`
	// ChannelID is the thread's ID when the message was posted to a thread.
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
	ThreadID  string `json:"thread_id"`
}

// GitHubPullRequestAttachment is the result of attaching or detaching a pull request. URL is only set on attach.
type GitHubPullRequestAttachment struct {
	RepositoryID int64  `json:"repository_id"`
	PullRequest  int    `json:"pull_request"`
	URL          string `json:"url,omitempty"`
}

type GitHubAttachedPullRequests struct {
	PullRequests []integrationdefinition.GitHubScope `json:"pull_requests"`
	listing.OffsetPage
}

var (
	SlackPostMessageSchema = outputSchema(func() (json.RawMessage, error) {
		return anyOfSchema("Without paths the message's timestamp; with paths the uploaded file IDs.",
			titled("text only", reflectSchema[SlackMessage]),
			titled("with files", reflectSchema[SlackFiles]),
		)
	})
	DiscordPostMessageSchema         = outputSchema(reflectOutputSchema[DiscordMessage])
	GitHubDiscussionCommentSchema    = outputSchema(reflectOutputSchema[github.DiscussionComment])
	GitHubReplySchema                = outputSchema(reflectOutputSchema[github.ReviewComment])
	GitHubResolveThreadSchema        = outputSchema(constOutputSchema[github.ResolvedReviewThread]("is_resolved", true))
	GitHubStartReviewSchema          = outputSchema(constOutputSchema[github.ReviewReceipt]("state", "PENDING"))
	GitHubSubmitReviewSchema         = outputSchema(constOutputSchema[github.ReviewReceipt]("state", "COMMENTED"))
	GitHubDiscardReviewSchema        = outputSchema(constOutputSchema[github.DiscardedReview]("deleted", true))
	GitHubPullRequestAttachSchema    = outputSchema(reflectOutputSchema[GitHubPullRequestAttachment])
	GitHubAttachedPullRequestsSchema = outputSchema(reflectOutputSchema[GitHubAttachedPullRequests])
	GitHubReviewCommentSchema        = outputSchema(func() (json.RawMessage, error) {
		return anyOfSchema("With commit_id the published comment; with review_id the comment saved to that pending review.",
			titled("commit_id", reflectSchema[github.ReviewComment]),
			titled("review_id", constSchema[github.PendingReviewComment]("state", "PENDING")),
		)
	})
)

func constSchema[T any](property string, value any) func() (*jsonschema.Schema, error) {
	return func() (*jsonschema.Schema, error) {
		schema, err := reflectSchema[T]()
		if err != nil {
			return nil, err
		}
		schema.Properties[property] = &jsonschema.Schema{Const: &value}
		return schema, nil
	}
}

func constOutputSchema[T any](property string, value any) func() (json.RawMessage, error) {
	return func() (json.RawMessage, error) {
		schema, err := constSchema[T](property, value)()
		if err != nil {
			return nil, err
		}
		return json.Marshal(schema)
	}
}
