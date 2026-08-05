package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/omnara-ai/omnara/internal/redistore"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const oauthStateTTL = 10 * time.Minute

type OAuthStateStore interface {
	Create(context.Context, OAuthStateCreateInput) (OAuthStateRecord, error)
	Consume(context.Context, storage.ID, string, string) (OAuthStateRecord, error)
}

type OAuthStateCreateInput struct {
	AuthConnectorID     storage.ID
	State               string
	BrowserBindingToken string
	CodeVerifier        string
	Nonce               string
	ReturnTo            string
}

type OAuthStateRecord struct {
	AuthConnectorID storage.ID
	CodeVerifier    string
	Nonce           string
	ReturnTo        string
}

type RedisOAuthStateStore struct {
	client *redistore.Client
}

func NewRedisOAuthStateStore(client *redistore.Client) *RedisOAuthStateStore {
	return &RedisOAuthStateStore{client: client}
}

type redisOAuthStatePayload struct {
	AuthConnectorID    string `json:"auth_connector_id"`
	BrowserBindingHash string `json:"browser_binding_hash"`
	CodeVerifier       string `json:"code_verifier"`
	Nonce              string `json:"nonce"`
	ReturnTo           string `json:"return_to"`
}

func (s *RedisOAuthStateStore) Create(ctx context.Context, input OAuthStateCreateInput) (OAuthStateRecord, error) {
	if s == nil || s.client == nil {
		return OAuthStateRecord{}, errors.New("auth oauth state store unavailable")
	}
	if input.AuthConnectorID == storage.NilID || input.State == "" || input.BrowserBindingToken == "" ||
		input.CodeVerifier == "" ||
		input.Nonce == "" {
		return OAuthStateRecord{}, errors.New("auth connector, state, browser binding, code verifier, and nonce are required")
	}
	if input.ReturnTo == "" {
		input.ReturnTo = "/"
	}
	payload := redisOAuthStatePayload{
		AuthConnectorID:    input.AuthConnectorID.String(),
		BrowserBindingHash: identitystore.HashBearerToken(input.BrowserBindingToken),
		CodeVerifier:       input.CodeVerifier,
		Nonce:              input.Nonce,
		ReturnTo:           input.ReturnTo,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return OAuthStateRecord{}, fmt.Errorf("marshal auth oauth state: %w", err)
	}
	created, err := s.client.SetNX(ctx, oauthStateRedisKey(input.State), raw, oauthStateTTL)
	if err != nil {
		return OAuthStateRecord{}, fmt.Errorf("create auth oauth state: %w", err)
	}
	if !created {
		return OAuthStateRecord{}, storeerr.ErrIdempotencyConflict
	}
	return OAuthStateRecord{
		AuthConnectorID: input.AuthConnectorID,
		CodeVerifier:    input.CodeVerifier,
		Nonce:           input.Nonce,
		ReturnTo:        input.ReturnTo,
	}, nil
}

const consumeOAuthStateScript = `
local raw = redis.call("GET", KEYS[1])
if not raw then
  return nil
end
local payload = cjson.decode(raw)
if payload["auth_connector_id"] ~= ARGV[1] then
  return nil
end
if payload["browser_binding_hash"] ~= ARGV[2] then
  return nil
end
redis.call("DEL", KEYS[1])
return raw
`

func (s *RedisOAuthStateStore) Consume(
	ctx context.Context,
	authConnectorID storage.ID,
	state, browserBindingToken string,
) (OAuthStateRecord, error) {
	if s == nil || s.client == nil {
		return OAuthStateRecord{}, errors.New("auth oauth state store unavailable")
	}
	if authConnectorID == storage.NilID || state == "" || browserBindingToken == "" {
		return OAuthStateRecord{}, storeerr.ErrUnauthorized
	}
	raw, ok, err := s.client.EvalBytes(
		ctx,
		consumeOAuthStateScript,
		[]string{oauthStateRedisKey(state)},
		authConnectorID.String(),
		identitystore.HashBearerToken(browserBindingToken),
	)
	if err != nil {
		return OAuthStateRecord{}, fmt.Errorf("consume auth oauth state: %w", err)
	}
	if !ok {
		return OAuthStateRecord{}, storeerr.ErrUnauthorized
	}
	var payload redisOAuthStatePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return OAuthStateRecord{}, fmt.Errorf("parse auth oauth state: %w", err)
	}
	return OAuthStateRecord{
		AuthConnectorID: authConnectorID,
		CodeVerifier:    payload.CodeVerifier,
		Nonce:           payload.Nonce,
		ReturnTo:        payload.ReturnTo,
	}, nil
}

func oauthStateRedisKey(state string) string {
	return "auth:oauth_state:" + identitystore.HashBearerToken(state)
}
