package github

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-jose/go-jose/v4"
)

type Account struct {
	ID     string
	Login  string
	NodeID string
	Type   string
}

// Permissions intentionally projects only the communication permissions this
// integration uses. Additional App permissions never grant setup extra behavior.
type Permissions struct {
	PullRequests string `json:"pull_requests"`
	Issues       string `json:"issues"`
	Metadata     string `json:"metadata"`
}

type App struct {
	ID              string
	ClientID        string
	Slug            string
	InstallationURL string
	Owner           Account
	Permissions     Permissions
}

type nativeAccount struct {
	ID     nativeID `json:"id"`
	Login  string   `json:"login"`
	NodeID string   `json:"node_id"`
	Type   string   `json:"type"`
}

func (account nativeAccount) project() (Account, error) {
	if !validID(string(account.ID)) || !boundedText(account.Login, 256) ||
		!boundedText(account.NodeID, 256) || !boundedText(account.Type, 32) {
		return Account{}, ErrInvalidResponse
	}
	return Account{ID: string(account.ID), Login: account.Login, NodeID: account.NodeID, Type: account.Type}, nil
}

// VerifyApp validates the key, numeric App ID and OAuth client ID together. The
// returned slug, not a caller-selected name or client ID, identifies its install UI.
func (c *Client) VerifyApp(ctx context.Context) (App, error) {
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	token, err := c.appJWT()
	if err != nil {
		return App{}, err
	}
	return c.verifyApp(ctx, token)
}

func (c *Client) verifyApp(ctx context.Context, token string) (App, error) {
	var response struct {
		ID          nativeID      `json:"id"`
		ClientID    string        `json:"client_id"`
		Slug        string        `json:"slug"`
		Owner       nativeAccount `json:"owner"`
		Permissions Permissions   `json:"permissions"`
	}
	if err := c.get(ctx, "/app", token, &response); err != nil {
		return App{}, err
	}
	if string(response.ID) != c.appID || response.ClientID != c.clientID {
		return App{}, ErrAppMismatch
	}
	owner, err := response.Owner.project()
	if err != nil || !validSlug(response.Slug) {
		return App{}, ErrInvalidResponse
	}
	return App{
		ID: c.appID, ClientID: c.clientID, Slug: response.Slug, Owner: owner, Permissions: response.Permissions,
		InstallationURL: "https://github.com/apps/" + response.Slug + "/installations/new",
	}, nil
}

func (c *Client) appJWT() (string, error) {
	now := time.Now()
	claims, err := json.Marshal(struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}{now.Add(-time.Minute).Unix(), now.Add(9 * time.Minute).Unix(), c.clientID})
	if err != nil {
		return "", ErrInvalidConfig
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: c.privateKey}, (&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return "", ErrInvalidConfig
	}
	signed, err := signer.Sign(claims)
	if err != nil {
		return "", ErrInvalidConfig
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		return "", ErrInvalidConfig
	}
	return token, nil
}

func validSlug(value string) bool {
	if len(value) == 0 || len(value) > 100 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
			return false
		}
	}
	return true
}

func (permissions Permissions) sufficient() bool {
	return permissions.PullRequests == "write" &&
		(permissions.Issues == "read" || permissions.Issues == "write") && permissions.Metadata == "read"
}
