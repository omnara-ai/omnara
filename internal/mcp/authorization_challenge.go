package mcp

import (
	"errors"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

const bearerErrorInsufficientScope = "insufficient_scope"

// AuthorizationChallenge is a request rejected because the caller lacks OAuth
// authorization: a 401 Bearer challenge (no or invalid token) or a 403
// insufficient_scope challenge asking the client to step up its scopes.
type AuthorizationChallenge struct {
	Status              int
	Error               string
	ErrorDescription    string
	Scopes              []string
	ResourceMetadataURL string
}

func (c AuthorizationChallenge) InsufficientScope() bool {
	return c.Error == bearerErrorInsufficientScope
}

// AuthorizationChallengeFromError reports the Bearer challenge carried by an
// MCP HTTP failure. A 403 counts only with error="insufficient_scope", matching
// the MCP authorization spec's scope challenge; other 403s are plain denials.
func AuthorizationChallengeFromError(err error) (AuthorizationChallenge, bool) {
	status, headers := challengeStatusAndHeaders(err)
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return AuthorizationChallenge{}, false
	}
	challenges, parseErr := oauthex.ParseWWWAuthenticate(headers)
	if parseErr != nil {
		return AuthorizationChallenge{}, false
	}
	bearer, ok := bearerChallenge(challenges)
	if !ok {
		return AuthorizationChallenge{}, false
	}
	challenge := AuthorizationChallenge{
		Status:              status,
		Error:               bearer.Params["error"],
		ErrorDescription:    bearer.Params["error_description"],
		Scopes:              strings.Fields(bearer.Params["scope"]),
		ResourceMetadataURL: bearer.Params["resource_metadata"],
	}
	if status == http.StatusForbidden && !challenge.InsufficientScope() {
		return AuthorizationChallenge{}, false
	}
	return challenge, true
}

func challengeStatusAndHeaders(err error) (int, []string) {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status, httpErr.WWWAuthenticate
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.HTTPStatus, rpcErr.WWWAuthenticate
	}
	return 0, nil
}

func bearerChallenge(challenges []oauthex.Challenge) (oauthex.Challenge, bool) {
	for _, challenge := range challenges {
		if strings.EqualFold(challenge.Scheme, "bearer") {
			return challenge, true
		}
	}
	return oauthex.Challenge{}, false
}
