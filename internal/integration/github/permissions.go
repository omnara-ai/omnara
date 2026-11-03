package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// CanDirectPullRequest uses GitHub's base permission; maintain maps to write.
// https://docs.github.com/en/rest/collaborators/collaborators#get-repository-permissions-for-a-user
func (c *Client) CanDirectPullRequest(ctx context.Context, scope Scope, user User) (bool, error) {
	if user.ID <= 0 || !validRepositorySegment(user.Login) {
		return false, errors.New("github sender requires a user ID and login")
	}
	pull, err := c.preparePull(ctx, scope, false)
	if err != nil {
		return false, err
	}
	var result struct {
		Permission string `json:"permission"`
		User       User   `json:"user"`
	}
	_, err = c.request(ctx, pull.token, http.MethodGet,
		repoPath(pull.repository)+"/collaborators/"+url.PathEscape(user.Login)+"/permission", nil, &result)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if result.User.ID != user.ID {
		return false, nil
	}
	return result.Permission == "write" || result.Permission == "admin", nil
}
