package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type ReviewReceipt struct {
	ID       int64  `json:"id"`
	State    string `json:"state"`
	CommitID string `json:"commit_id"`
	HTMLURL  string `json:"html_url"`
}

type PendingReviewComment struct {
	ID       int64  `json:"id"`
	ReviewID int64  `json:"review_id"`
	State    string `json:"state"`
	HTMLURL  string `json:"html_url"`
}

type reviewRecord struct {
	Review
	NodeID         string `json:"node_id"`
	PullRequestURL string `json:"pull_request_url"`
}

func (r reviewRecord) receipt() ReviewReceipt {
	return ReviewReceipt{ID: r.ID, State: r.State, CommitID: r.CommitID, HTMLURL: r.HTMLURL}
}

func (c *Client) getReview(ctx context.Context, pull preparedPull, id int64) (reviewRecord, error) {
	var review reviewRecord
	_, err := c.request(ctx, pull.token, http.MethodGet,
		pullPath(pull.repository, pull.number)+"/reviews/"+strconv.FormatInt(id, 10), nil, &review)
	if err != nil {
		return reviewRecord{}, err
	}
	if review.ID != id || !c.matchesPullURL(pull, review.PullRequestURL) {
		return reviewRecord{}, &APIError{Code: ScopeMismatch}
	}
	if review.State == "" {
		return reviewRecord{}, &APIError{Code: InvalidResponse}
	}
	return review, nil
}

func (c *Client) GetReview(ctx context.Context, scope Scope, id int64) (Review, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if id <= 0 {
		return Review{}, errors.New("github review requires a positive review_id")
	}
	pull, err := c.preparePull(ctx, scope, false)
	if err != nil {
		return Review{}, err
	}
	review, err := c.getReview(ctx, pull, id)
	return review.Review, err
}

func (c *Client) ListReviewCommentsForReview(
	ctx context.Context, scope Scope, id int64, options PageOptions,
) (ReviewCommentsPage, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if id <= 0 {
		return ReviewCommentsPage{}, errors.New("github review comments require a positive review_id")
	}
	options, err := canonicalPageOptions(options)
	if err != nil {
		return ReviewCommentsPage{}, err
	}
	pull, err := c.preparePull(ctx, scope, false)
	if err != nil {
		return ReviewCommentsPage{}, err
	}
	suffix := "/pulls/" + strconv.Itoa(scope.PullRequest) + "/reviews/" + strconv.FormatInt(id, 10) + "/comments"
	comments, next, err := readPreparedPage[ReviewComment](ctx, c, pull, suffix, options)
	if err != nil {
		return ReviewCommentsPage{}, err
	}
	for _, comment := range comments {
		if comment.PullRequestReviewID != id || !c.matchesPullURL(pull, comment.PullRequestURL) {
			return ReviewCommentsPage{}, &APIError{Code: ScopeMismatch}
		}
	}
	return ReviewCommentsPage{Comments: comments, NextPage: next}, nil
}

func (c *Client) GetPendingReview(ctx context.Context, scope Scope, botUserID int64) (Review, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if botUserID <= 0 {
		return Review{}, false, errors.New("github pending review requires the verified bot user ID")
	}
	pull, err := c.preparePull(ctx, scope, false)
	if err != nil {
		return Review{}, false, err
	}
	// https://github.com/github/github-mcp-server/pull/3353: REST can find
	// installation-token drafts that viewer-based GraphQL lookups miss.
	for page := 1; page != 0; {
		reviews, next, err := readPreparedPage[reviewRecord](ctx, c, pull,
			"/pulls/"+strconv.Itoa(pull.number)+"/reviews", PageOptions{Page: page, PerPage: 30})
		if err != nil {
			return Review{}, false, err
		}
		for _, review := range reviews {
			if review.State != "PENDING" {
				continue
			}
			if review.ID <= 0 {
				return Review{}, false, &APIError{Code: InvalidResponse}
			}
			if review.User.ID != botUserID || !c.matchesPullURL(pull, review.PullRequestURL) {
				return Review{}, false, &APIError{Code: ScopeMismatch}
			}
			return review.Review, true, nil
		}
		page = next
	}
	return Review{}, false, nil
}

func (c *Client) StartReview(ctx context.Context, scope Scope, commitID string) (ReviewReceipt, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if strings.TrimSpace(commitID) == "" || len(commitID) > 128 {
		return ReviewReceipt{}, errors.New("github review requires commit_id")
	}
	pull, err := c.preparePull(ctx, scope, true)
	if err != nil {
		return ReviewReceipt{}, err
	}
	var review reviewRecord
	_, err = c.request(ctx, pull.token, http.MethodPost, pullPath(pull.repository, pull.number)+"/reviews",
		struct {
			CommitID string `json:"commit_id"`
		}{commitID}, &review)
	if err != nil {
		return ReviewReceipt{}, reviewValidationError(err,
			"GitHub rejected the draft; check commit_id and read pending_review. Only "+
				"one draft per bot per PR is allowed; you can continue, submit, or discard an existing draft")
	}
	if review.ID <= 0 || review.State != "PENDING" || !strings.EqualFold(review.CommitID, commitID) ||
		!c.matchesPullURL(pull, review.PullRequestURL) {
		return ReviewReceipt{}, &APIError{Code: DeliveryUnknown}
	}
	return review.receipt(), nil
}

// https://docs.github.com/en/graphql/reference/pulls#addpullrequestreviewthread
const addPendingReviewCommentMutation = `mutation AddPendingReviewComment($input: AddPullRequestReviewThreadInput!) {
  addPullRequestReviewThread(input: $input) {
    thread { comments(first: 1) { nodes { fullDatabaseId url state pullRequestReview { fullDatabaseId } } } }
  }
}`

func (c *Client) AddPendingReviewComment(
	ctx context.Context, scope Scope, reviewID int64, args ReviewCommentArgs,
) (PendingReviewComment, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if reviewID <= 0 || args.CommitID != "" {
		return PendingReviewComment{}, errors.New("a pending comment requires review_id instead of commit_id")
	}
	if err := args.validateLocation(); err != nil {
		return PendingReviewComment{}, err
	}
	pull, err := c.preparePull(ctx, scope, true)
	if err != nil {
		return PendingReviewComment{}, err
	}
	review, err := c.getReview(ctx, pull, reviewID)
	if err != nil {
		return PendingReviewComment{}, err
	}
	if review.State != "PENDING" {
		return PendingReviewComment{}, errors.New("review is no longer pending; read pending_review to find the current " +
			"draft, or start_review to create one")
	}
	if review.NodeID == "" || review.CommitID == "" {
		return PendingReviewComment{}, &APIError{Code: InvalidResponse}
	}
	input := map[string]any{
		"pullRequestReviewId": review.NodeID, "body": args.Body, "path": args.Path, "line": args.Line, "side": args.Side,
	}
	if args.StartLine != nil {
		input["startLine"], input["startSide"] = *args.StartLine, args.StartSide
	}
	var response struct {
		Add *struct {
			Thread *struct {
				Comments struct {
					Nodes []struct {
						ID     json.Number `json:"fullDatabaseId"`
						URL    string      `json:"url"`
						State  string      `json:"state"`
						Review struct {
							ID json.Number `json:"fullDatabaseId"`
						} `json:"pullRequestReview"`
					} `json:"nodes"`
				} `json:"comments"`
			} `json:"thread"`
		} `json:"addPullRequestReviewThread"`
	}
	if err := c.graphQL(ctx, pull.token, addPendingReviewCommentMutation,
		map[string]any{"input": input}, &response, true); err != nil {
		return PendingReviewComment{}, reviewValidationError(err,
			"GitHub rejected the draft comment; read the review to check it is still "+
				"pending and check path, line, side and any start_line/start_side against its commit diff")
	}
	if response.Add != nil && response.Add.Thread == nil {
		return PendingReviewComment{}, &APIError{Code: DeliveryUnknown,
			cause: errors.New("GitHub returned no review thread; if read-back finds no comment, " +
				"check that path, line and side belong to the review commit diff")}
	}
	if response.Add == nil || response.Add.Thread == nil || len(response.Add.Thread.Comments.Nodes) != 1 {
		return PendingReviewComment{}, &APIError{Code: DeliveryUnknown}
	}
	comment := response.Add.Thread.Comments.Nodes[0]
	id, err := comment.ID.Int64()
	returnedReviewID, reviewErr := comment.Review.ID.Int64()
	if err != nil || id <= 0 || reviewErr != nil || returnedReviewID != reviewID || comment.State != "PENDING" {
		return PendingReviewComment{}, &APIError{Code: DeliveryUnknown}
	}
	return PendingReviewComment{ID: id, ReviewID: reviewID, State: comment.State, HTMLURL: comment.URL}, nil
}

func (c *Client) SubmitReview(ctx context.Context, scope Scope, id int64, body string) (ReviewReceipt, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if id <= 0 {
		return ReviewReceipt{}, errors.New("github review requires a positive review_id")
	}
	if err := validateBody(body); err != nil {
		return ReviewReceipt{}, err
	}
	pull, err := c.preparePull(ctx, scope, true)
	if err != nil {
		return ReviewReceipt{}, err
	}
	review, err := c.getReview(ctx, pull, id)
	if err != nil {
		return ReviewReceipt{}, err
	}
	if review.State != "PENDING" {
		return ReviewReceipt{}, errors.New("review is not pending; read the review before deciding what to do next")
	}
	var submitted reviewRecord
	_, err = c.request(ctx, pull.token, http.MethodPost,
		pullPath(pull.repository, pull.number)+"/reviews/"+strconv.FormatInt(id, 10)+"/events",
		struct {
			Event string `json:"event"`
			Body  string `json:"body"`
		}{"COMMENT", body}, &submitted)
	if err != nil {
		return ReviewReceipt{}, reviewValidationError(err,
			"GitHub rejected submission; read review with review_id to check its current state. "+
				"Only pending reviews can be submitted")
	}
	if submitted.ID != id || submitted.State != "COMMENTED" ||
		submitted.SubmittedAt == nil ||
		!c.matchesPullURL(pull, submitted.PullRequestURL) {
		return ReviewReceipt{}, &APIError{Code: DeliveryUnknown}
	}
	return submitted.receipt(), nil
}

type DiscardedReview struct {
	ID      int64 `json:"id"`
	Deleted bool  `json:"deleted"`
}

func (c *Client) DiscardReview(ctx context.Context, scope Scope, id int64) (DiscardedReview, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if id <= 0 {
		return DiscardedReview{}, errors.New("github review requires a positive review_id")
	}
	pull, err := c.preparePull(ctx, scope, true)
	if err != nil {
		return DiscardedReview{}, err
	}
	review, err := c.getReview(ctx, pull, id)
	if err != nil {
		return DiscardedReview{}, err
	}
	if review.State != "PENDING" {
		return DiscardedReview{}, errors.New("only a pending review can be discarded; published reviews cannot be " +
			"deleted by this tool")
	}
	var deleted reviewRecord
	_, err = c.request(ctx, pull.token, http.MethodDelete,
		pullPath(pull.repository, pull.number)+"/reviews/"+strconv.FormatInt(id, 10), nil, &deleted)
	if err != nil {
		return DiscardedReview{}, reviewValidationError(err,
			"GitHub rejected deletion; read review with review_id to check its current state. "+
				"Only pending reviews can be discarded")
	}
	if deleted.ID != id || !c.matchesPullURL(pull, deleted.PullRequestURL) {
		return DiscardedReview{}, &APIError{Code: DeliveryUnknown}
	}
	return DiscardedReview{ID: id, Deleted: true}, nil
}

func reviewValidationError(err error, hint string) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Code == PermanentFailure &&
		apiErr.StatusCode == http.StatusUnprocessableEntity && apiErr.cause == nil {
		return fmt.Errorf("%s: %w", hint, err)
	}
	return err
}
