package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"unicode/utf8"
)

type ReviewThreadsOptions struct {
	Cursor string
	Limit  int
}

type ReviewThread struct {
	ID         string `json:"id"`
	IsResolved bool   `json:"is_resolved"`
	IsOutdated bool   `json:"is_outdated"`
	Path       string `json:"path"`
	Line       *int   `json:"line"`
	// CommentID identifies the first available comment, for review_comments/reply.
	// Bodies and replies are read through the existing REST comment pages.
	CommentID int64 `json:"comment_id,omitempty"`
}

type ReviewThreadsPage struct {
	Threads    []ReviewThread `json:"threads"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// The REST node_id links to the same immutable PR in GraphQL. In particular,
// neither caller-supplied names nor a cursor may select a different repository.
// https://docs.github.com/en/graphql/guides/using-global-node-ids
const reviewThreadsQuery = `query ReviewThreads($id: ID!, $first: Int!, $after: String) {
  node(id: $id) {
    __typename
    ... on PullRequest {
      fullDatabaseId
      number
      reviewThreads(first: $first, after: $after) {
        nodes {
          id
          isResolved
          isOutdated
          path
          line
          comments(first: 1) { nodes { fullDatabaseId } }
        }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}`

func (c *Client) ListReviewThreads(
	ctx context.Context, scope Scope, options ReviewThreadsOptions,
) (ReviewThreadsPage, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if options.Limit == 0 {
		options.Limit = 30
	}
	if options.Limit < 1 || options.Limit > 100 || len(options.Cursor) > 4096 || !utf8.ValidString(options.Cursor) {
		return ReviewThreadsPage{}, errors.New(
			"github review threads require limit between 1 and 100 and a valid cursor of at most 4096 bytes",
		)
	}
	pull, err := c.preparePull(ctx, scope, false)
	if err != nil {
		return ReviewThreadsPage{}, err
	}
	if pull.metadata.NodeID == "" {
		return ReviewThreadsPage{}, &APIError{Code: InvalidResponse}
	}
	var after *string
	if options.Cursor != "" {
		after = &options.Cursor
	}
	input := struct {
		Query     string `json:"query"`
		Variables struct {
			ID    string  `json:"id"`
			First int     `json:"first"`
			After *string `json:"after"`
		} `json:"variables"`
	}{Query: reviewThreadsQuery}
	input.Variables.ID, input.Variables.First, input.Variables.After = pull.metadata.NodeID, options.Limit, after
	var response struct {
		Errors []json.RawMessage `json:"errors"`
		Data   struct {
			Node *struct {
				TypeName       string      `json:"__typename"`
				FullDatabaseID json.Number `json:"fullDatabaseId"`
				Number         int         `json:"number"`
				ReviewThreads  struct {
					Nodes []struct {
						ID         string `json:"id"`
						IsResolved *bool  `json:"isResolved"`
						IsOutdated *bool  `json:"isOutdated"`
						Path       string `json:"path"`
						Line       *int   `json:"line"`
						Comments   struct {
							Nodes []struct {
								FullDatabaseID json.Number `json:"fullDatabaseId"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
					PageInfo struct {
						HasNextPage *bool  `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"reviewThreads"`
			} `json:"node"`
		} `json:"data"`
	}
	// POST is transport here, not a mutation. Reuse bounded HTTP, credential
	// fences and safe read retries, without ambiguous-publication errors.
	header, err := c.doJSON(ctx, http.MethodPost, "/graphql", pull.token, input, &response, false)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
		c.invalidateToken(ctx, pull.token)
	}
	if err != nil {
		return ReviewThreadsPage{}, err
	}
	if len(response.Errors) != 0 {
		// HTTP 200 can contain partial data or a rate-limit error. Never return
		// partial thread state or expose the provider's error messages.
		if header.Get("X-Ratelimit-Remaining") == "0" || header.Get("Retry-After") != "" {
			err := responseError(http.StatusForbidden, header, nil, false, c.now())
			err.StatusCode = http.StatusOK
			return ReviewThreadsPage{}, err
		}
		return ReviewThreadsPage{}, &APIError{Code: InvalidResponse, StatusCode: http.StatusOK}
	}
	node := response.Data.Node
	if node == nil {
		return ReviewThreadsPage{}, &APIError{Code: InvalidResponse}
	}
	id, err := node.FullDatabaseID.Int64()
	if err != nil || id <= 0 {
		return ReviewThreadsPage{}, &APIError{Code: InvalidResponse}
	}
	// GitHub may return a newer global ID encoding for the same node. The
	// verified REST database ID, node type and PR number pin the resource.
	if node.TypeName != "PullRequest" || id != pull.metadata.ID || node.Number != pull.number {
		return ReviewThreadsPage{}, &APIError{Code: ScopeMismatch}
	}
	connection := node.ReviewThreads
	if connection.Nodes == nil || len(connection.Nodes) > options.Limit || connection.PageInfo.HasNextPage == nil {
		return ReviewThreadsPage{}, &APIError{Code: InvalidResponse}
	}
	result := ReviewThreadsPage{Threads: make([]ReviewThread, 0, len(connection.Nodes))}
	if *connection.PageInfo.HasNextPage {
		cursor := connection.PageInfo.EndCursor
		if len(connection.Nodes) == 0 || cursor == "" || cursor == options.Cursor || len(cursor) > 4096 {
			return ReviewThreadsPage{}, &APIError{Code: InvalidResponse}
		}
		result.NextCursor = cursor
	}
	for _, thread := range connection.Nodes {
		if thread.ID == "" || thread.Path == "" || thread.IsResolved == nil || thread.IsOutdated == nil ||
			thread.Comments.Nodes == nil || len(thread.Comments.Nodes) > 1 {
			return ReviewThreadsPage{}, &APIError{Code: InvalidResponse}
		}
		value := ReviewThread{ID: thread.ID, IsResolved: *thread.IsResolved, IsOutdated: *thread.IsOutdated,
			Path: thread.Path, Line: thread.Line}
		if len(thread.Comments.Nodes) == 1 {
			value.CommentID, err = thread.Comments.Nodes[0].FullDatabaseID.Int64()
			if err != nil || value.CommentID <= 0 {
				return ReviewThreadsPage{}, &APIError{Code: InvalidResponse}
			}
		}
		result.Threads = append(result.Threads, value)
	}
	return result, nil
}
