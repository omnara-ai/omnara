package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Credentials are supplied by the connection owner, never resolved from a DB.
// PrivateKeyPEM accepts an unencrypted PKCS#1 or PKCS#8 RSA private key.
type Credentials struct {
	AppID         int64
	PrivateKeyPEM string
	WebhookSecret string
}

type cachedToken struct {
	repositoryID int64
	token        string
	expiresAt    time.Time
}

func parsePrivateKey(raw string) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode([]byte(raw))
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 || len(block.Headers) != 0 {
		return nil, errors.New("github requires one unencrypted RSA private key PEM")
	}
	var key *rsa.PrivateKey
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = parsed.(*rsa.PrivateKey)
	default:
		return nil, errors.New("github requires an RSA private key")
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 || key.Validate() != nil {
		return nil, errors.New("github requires a valid RSA private key of at least 2048 bits")
	}
	return key, nil
}

func (c *Client) appJWT() (string, error) {
	now := c.now()
	claims, err := json.Marshal(struct {
		IssuedAt int64  `json:"iat"`
		Expires  int64  `json:"exp"`
		Issuer   string `json:"iss"`
	}{now.Add(-time.Minute).Unix(), now.Add(9 * time.Minute).Unix(), strconv.FormatInt(c.appID, 10)})
	if err != nil {
		return "", errors.New("github JWT encoding failed")
	}
	encode := base64.RawURLEncoding.EncodeToString
	unsigned := encode([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + encode(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.New("github JWT signing failed")
	}
	return unsigned + "." + encode(signature), nil
}

// The two cache slots bound memory regardless of how many repos a connection
// accesses. Serializing minting avoids duplicate tokens; waiting is cancellable.
func (c *Client) installationToken(ctx context.Context, repositoryID int64, write bool) (string, error) {
	select {
	case c.tokenGate <- struct{}{}:
		defer func() { <-c.tokenGate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	index, permission := 0, "read"
	if write {
		index, permission = 1, "write"
	}
	cached := c.tokens[index]
	if cached.repositoryID == repositoryID && cached.expiresAt.After(c.now().Add(time.Minute)) {
		return cached.token, nil
	}
	jwt, err := c.appJWT()
	if err != nil {
		return "", err
	}
	input := struct {
		RepositoryIDs []int64           `json:"repository_ids"`
		Permissions   map[string]string `json:"permissions"`
	}{[]int64{repositoryID}, map[string]string{"pull_requests": permission}}
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := "/app/installations/" + strconv.FormatInt(c.installationID, 10) + "/access_tokens"
	_, err = c.doJSON(ctx, http.MethodPost, path, jwt, input, &result, false)
	if err != nil {
		return "", err
	}
	if result.Token == "" || strings.ContainsAny(result.Token, "\r\n") ||
		!result.ExpiresAt.After(c.now().Add(time.Minute)) {
		return "", &APIError{Code: InvalidResponse}
	}
	c.tokens[index] = cachedToken{repositoryID: repositoryID, token: result.Token, expiresAt: result.ExpiresAt}
	return result.Token, nil
}

func (c *Client) invalidateToken(ctx context.Context, token string) {
	select {
	case c.tokenGate <- struct{}{}:
		defer func() { <-c.tokenGate }()
		for i := range c.tokens {
			if c.tokens[i].token == token {
				c.tokens[i] = cachedToken{}
			}
		}
	case <-ctx.Done():
	}
}
