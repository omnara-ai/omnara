package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

type Repository struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    User   `json:"owner"`
	Private  bool   `json:"private"`
}

type Branch struct {
	Ref  string      `json:"ref"`
	SHA  string      `json:"sha"`
	Repo *Repository `json:"repo"`
}

type PullRequest struct {
	ID           int64      `json:"id"`
	Number       int        `json:"number"`
	HTMLURL      string     `json:"html_url"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	State        string     `json:"state"`
	Draft        bool       `json:"draft"`
	Merged       bool       `json:"merged"`
	User         User       `json:"user"`
	Head         Branch     `json:"head"`
	Base         Branch     `json:"base"`
	ChangedFiles int        `json:"changed_files"`
	Additions    int        `json:"additions"`
	Deletions    int        `json:"deletions"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ClosedAt     *time.Time `json:"closed_at"`
}

type DiscussionComment struct {
	ID        int64     `json:"id"`
	HTMLURL   string    `json:"html_url"`
	Body      string    `json:"body"`
	User      User      `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ReviewComment struct {
	DiscussionComment
	PullRequestURL      string `json:"pull_request_url"`
	PullRequestReviewID int64  `json:"pull_request_review_id"`
	InReplyToID         int64  `json:"in_reply_to_id"`
	CommitID            string `json:"commit_id"`
	OriginalCommitID    string `json:"original_commit_id"`
	Path                string `json:"path"`
	DiffHunk            string `json:"diff_hunk"`
	Line                *int   `json:"line"`
	StartLine           *int   `json:"start_line"`
	OriginalLine        *int   `json:"original_line"`
	OriginalStartLine   *int   `json:"original_start_line"`
	Side                string `json:"side"`
	StartSide           string `json:"start_side"`
}

type File struct {
	SHA              string `json:"sha"`
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Changes          int    `json:"changes"`
	// Patch can be absent for binaries or omitted/truncated by GitHub. Never
	// interpret an absent patch or an exhausted page as proof of a complete diff.
	Patch *string `json:"patch"`
}

type Diff struct {
	Text string `json:"text"`
}

type DiscussionCommentsPage struct {
	Comments []DiscussionComment `json:"comments"`
	NextPage int                 `json:"next_page,omitempty"`
}

type ReviewCommentsPage struct {
	Comments []ReviewComment `json:"comments"`
	NextPage int             `json:"next_page,omitempty"`
}

// FilesPage is capped by GitHub at 3,000 files across all pages. Compare with PullRequest.ChangedFiles
// when deciding completeness. GetDiff fails explicitly if its byte bound is hit.
type FilesPage struct {
	Files    []File `json:"files"`
	NextPage int    `json:"next_page,omitempty"`
}

func (c *Client) GetPullRequest(ctx context.Context, scope Scope) (PullRequest, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	pull, err := c.preparePull(ctx, scope, false)
	if err != nil {
		return PullRequest{}, err
	}
	return pull.metadata, nil
}

// GetDiff uses the authenticated API media type, never the payload's diff_url.
// A response exceeding ResponseMaxBytes fails instead of silently truncating.
func (c *Client) GetDiff(ctx context.Context, scope Scope) (Diff, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	pull, err := c.preparePull(ctx, scope, false)
	if err != nil {
		return Diff{}, err
	}
	body, _, err := c.do(ctx, http.MethodGet, pullPath(pull.repository, pull.number),
		pull.token, "application/vnd.github.diff", nil, false)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
		c.invalidateToken(ctx, pull.token)
	}
	return Diff{Text: string(body)}, err
}

func (c *Client) ListFiles(
	ctx context.Context, scope Scope, options PageOptions,
) (FilesPage, error) {
	files, next, err := readPage[File](ctx, c, scope, "/pulls/"+strconv.Itoa(scope.PullRequest)+"/files", options)
	return FilesPage{Files: files, NextPage: next}, err
}

func (c *Client) ListDiscussionComments(
	ctx context.Context, scope Scope, options PageOptions,
) (DiscussionCommentsPage, error) {
	path := "/issues/" + strconv.Itoa(scope.PullRequest) + "/comments"
	comments, next, err := readPage[DiscussionComment](ctx, c, scope, path, options)
	return DiscussionCommentsPage{Comments: comments, NextPage: next}, err
}

func (c *Client) ListReviewComments(
	ctx context.Context, scope Scope, options PageOptions,
) (ReviewCommentsPage, error) {
	path := "/pulls/" + strconv.Itoa(scope.PullRequest) + "/comments"
	comments, next, err := readPage[ReviewComment](ctx, c, scope, path, options)
	return ReviewCommentsPage{Comments: comments, NextPage: next}, err
}

const CommentMaxBytes = 65536

func validateBody(body string) error {
	if strings.TrimSpace(body) == "" || len(body) > CommentMaxBytes || !utf8.ValidString(body) {
		return errors.New("github comment requires nonempty UTF-8 body of at most 65536 bytes")
	}
	return nil
}

func (c *Client) CreateDiscussionComment(
	ctx context.Context, scope Scope, body string,
) (DiscussionComment, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if err := validateBody(body); err != nil {
		return DiscussionComment{}, err
	}
	pull, err := c.preparePull(ctx, scope, true)
	if err != nil {
		return DiscussionComment{}, err
	}
	path := repoPath(pull.repository) + "/issues/" + strconv.Itoa(pull.number) + "/comments"
	var result DiscussionComment
	_, err = c.request(ctx, pull.token, http.MethodPost, path, struct {
		Body string `json:"body"`
	}{body}, &result)
	if err == nil && result.ID <= 0 {
		err = &APIError{Code: DeliveryUnknown}
	}
	return result, err
}

type InlineCommentArgs struct {
	Body      string `json:"body"`
	CommitID  string `json:"commit_id"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine *int   `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
}

func (args InlineCommentArgs) validate() error {
	if err := validateBody(args.Body); err != nil {
		return err
	}
	if strings.TrimSpace(args.CommitID) == "" || len(args.CommitID) > 128 ||
		strings.TrimSpace(args.Path) == "" || len(args.Path) > 4096 || !utf8.ValidString(args.Path) ||
		args.Line <= 0 || !diffSide(args.Side) {
		return errors.New("github inline comment requires commit_id, path, positive line and LEFT or RIGHT side")
	}
	if (args.StartLine == nil) != (args.StartSide == "") {
		return errors.New("github multiline comment requires both start_line and start_side")
	}
	if args.StartLine != nil && (*args.StartLine <= 0 || !diffSide(args.StartSide) ||
		(args.StartSide == args.Side && *args.StartLine >= args.Line)) {
		return errors.New("invalid github multiline comment range")
	}
	return nil
}

func diffSide(side string) bool { return side == "LEFT" || side == "RIGHT" }

func (c *Client) CreateInlineComment(
	ctx context.Context, scope Scope, args InlineCommentArgs,
) (ReviewComment, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if err := args.validate(); err != nil {
		return ReviewComment{}, err
	}
	pull, err := c.preparePull(ctx, scope, true)
	if err != nil {
		return ReviewComment{}, err
	}
	var result ReviewComment
	_, err = c.request(ctx, pull.token, http.MethodPost, pullPath(pull.repository, pull.number)+"/comments", args, &result)
	if err == nil && result.ID <= 0 {
		err = &APIError{Code: DeliveryUnknown}
	}
	return result, err
}

// Reply verifies PR membership before sending, including for IDs obtained from
// a different resource. Replies to replies are normalized to the verified root
// comment because GitHub only accepts top-level review comment IDs.
func (c *Client) Reply(
	ctx context.Context, scope Scope, commentID int64, body string,
) (ReviewComment, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if err := validateBody(body); err != nil {
		return ReviewComment{}, err
	}
	if commentID <= 0 {
		return ReviewComment{}, errors.New("github reply requires a positive review comment ID")
	}
	pull, err := c.preparePull(ctx, scope, true)
	if err != nil {
		return ReviewComment{}, err
	}
	comment, err := c.getReviewComment(ctx, pull, commentID)
	if err != nil {
		return ReviewComment{}, err
	}
	if comment.InReplyToID > 0 {
		comment, err = c.getReviewComment(ctx, pull, comment.InReplyToID)
		if err != nil {
			return ReviewComment{}, err
		}
		if comment.InReplyToID != 0 {
			return ReviewComment{}, &APIError{Code: InvalidResponse}
		}
	}
	path := pullPath(pull.repository, pull.number) + "/comments/" + strconv.FormatInt(comment.ID, 10) + "/replies"
	var result ReviewComment
	_, err = c.request(ctx, pull.token, http.MethodPost, path, struct {
		Body string `json:"body"`
	}{body}, &result)
	if err == nil && result.ID <= 0 {
		err = &APIError{Code: DeliveryUnknown}
	}
	return result, err
}

func (c *Client) getReviewComment(
	ctx context.Context, pull preparedPull, id int64,
) (ReviewComment, error) {
	var comment ReviewComment
	path := repoPath(pull.repository) + "/pulls/comments/" + strconv.FormatInt(id, 10)
	_, err := c.request(ctx, pull.token, http.MethodGet, path, nil, &comment)
	if err != nil {
		return ReviewComment{}, err
	}
	u, err := url.Parse(comment.PullRequestURL)
	if err != nil || !c.sameEndpoint(u, pullPath(pull.repository, pull.number)) || u.RawQuery != "" || comment.ID != id {
		return ReviewComment{}, &APIError{Code: ScopeMismatch}
	}
	return comment, nil
}
