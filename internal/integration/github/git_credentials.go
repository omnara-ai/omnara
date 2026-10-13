package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"
)

const gitCredentialExpiryMargin = 15 * time.Minute

var ErrContentsPermissionRequired = errors.New(
	"github installation requires Contents read or write permission for Git credentials; " +
		"update the GitHub App permissions and approve them for this installation",
)

type InstallationCredentials struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// InstallationGitCredentials requires caller-owned live authorization checks
// before and after every call, including cache hits.
func (c *Client) InstallationGitCredentials(ctx context.Context, write bool) (InstallationCredentials, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	select {
	case c.gitTokenGate <- struct{}{}:
		defer func() { <-c.gitTokenGate }()
	case <-ctx.Done():
		return InstallationCredentials{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return InstallationCredentials{}, err
	}
	index := 0
	if write {
		index = 1
	}
	if c.gitCredentials[index].ExpiresAt.After(c.now().Add(gitCredentialExpiryMargin)) {
		return c.gitCredentials[index], nil
	}
	jwt, err := c.appJWT()
	if err != nil {
		return InstallationCredentials{}, err
	}
	var result struct {
		InstallationCredentials
		Permissions map[string]string `json:"permissions"`
	}
	var input any
	if !write {
		input = map[string]map[string]string{"permissions": {"contents": "read", "metadata": "read"}}
	}
	path := "/app/installations/" + strconv.FormatInt(c.installationID, 10) + "/access_tokens"
	if _, err := c.doJSON(ctx, http.MethodPost, path, jwt, input, &result, false); err != nil {
		return InstallationCredentials{}, err
	}
	if err := ctx.Err(); err != nil {
		return InstallationCredentials{}, err
	}
	if result.Token == "" || !result.ExpiresAt.After(c.now().Add(gitCredentialExpiryMargin)) {
		return InstallationCredentials{}, &APIError{Code: InvalidResponse}
	}
	for _, ch := range result.Token {
		if ch <= ' ' || ch > '~' {
			return InstallationCredentials{}, &APIError{Code: InvalidResponse}
		}
	}
	if result.Permissions["contents"] != "read" && result.Permissions["contents"] != "write" {
		_, _, _ = c.do(ctx, http.MethodDelete, "/installation/token", result.Token, jsonMediaType, nil, true)
		return InstallationCredentials{}, ErrContentsPermissionRequired
	}
	c.gitCredentials[index] = result.InstallationCredentials
	return c.gitCredentials[index], nil
}
