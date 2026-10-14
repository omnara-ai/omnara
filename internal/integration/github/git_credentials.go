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
func (c *Client) InstallationGitCredentials(ctx context.Context) (InstallationCredentials, error) {
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
	if c.gitCredentials.ExpiresAt.After(c.now().Add(gitCredentialExpiryMargin)) {
		return c.gitCredentials, nil
	}
	jwt, err := c.appJWT()
	if err != nil {
		return InstallationCredentials{}, err
	}
	var result struct {
		InstallationCredentials
		Permissions map[string]string `json:"permissions"`
	}
	path := "/app/installations/" + strconv.FormatInt(c.installationID, 10) + "/access_tokens"
	if _, err := c.doJSON(ctx, http.MethodPost, path, jwt, nil, &result, false); err != nil {
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
	c.gitCredentials = result.InstallationCredentials
	return c.gitCredentials, nil
}
