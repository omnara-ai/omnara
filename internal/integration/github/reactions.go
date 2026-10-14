package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

func (c *Client) AcknowledgeComment(ctx context.Context, scope Scope, commentID int64, reviewComment bool) error {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if commentID <= 0 {
		return errors.New("github reaction requires a positive comment ID")
	}
	pull, err := c.preparePull(ctx, scope, true)
	if err != nil {
		return err
	}
	path := repoPath(pull.repository) + "/issues/comments/" + strconv.FormatInt(commentID, 10)
	if reviewComment {
		path = repoPath(pull.repository) + "/pulls/comments/" + strconv.FormatInt(commentID, 10)
	}
	var reaction struct{}
	_, err = c.request(ctx, pull.token, http.MethodPost, path+"/reactions", struct {
		Content string `json:"content"`
	}{"eyes"}, &reaction)
	return err
}
