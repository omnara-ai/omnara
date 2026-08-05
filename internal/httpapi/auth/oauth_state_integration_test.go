//go:build integration

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
)

func TestRedisOAuthStateStoreConsumesMatchingStateOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := integrationredis.OpenClient(t)
	store := NewRedisOAuthStateStore(client)
	connectorID := uuid.New()
	state := "state-" + uuid.NewString()

	created, err := store.Create(ctx, OAuthStateCreateInput{
		AuthConnectorID:     connectorID,
		State:               state,
		BrowserBindingToken: "browser-binding",
		CodeVerifier:        "code-verifier",
		Nonce:               "nonce",
		ReturnTo:            "/after",
	})
	if err != nil {
		t.Fatalf("create oauth state: %v", err)
	}
	if created.CodeVerifier != "code-verifier" || created.Nonce != "nonce" || created.ReturnTo != "/after" {
		t.Fatalf("created oauth state = %+v", created)
	}
	ttlMilliseconds, err := client.EvalInt(
		ctx,
		`return redis.call("PTTL", KEYS[1])`,
		[]string{oauthStateRedisKey(state)},
	)
	if err != nil {
		t.Fatalf("load oauth state ttl: %v", err)
	}
	if ttlMilliseconds <= 0 || ttlMilliseconds > int(oauthStateTTL.Milliseconds()) {
		t.Fatalf("oauth state ttl = %dms, want Redis-owned ttl up to %s", ttlMilliseconds, oauthStateTTL)
	}

	consumed, err := store.Consume(ctx, connectorID, "wrong-state", "browser-binding")
	if !errors.Is(err, storeerr.ErrUnauthorized) || consumed.CodeVerifier != "" {
		t.Fatalf("consume wrong state = %+v, %v; want unauthorized", consumed, err)
	}

	consumed, err = store.Consume(ctx, connectorID, state, "browser-binding")
	if err != nil {
		t.Fatalf("consume oauth state: %v", err)
	}
	if consumed.CodeVerifier != "code-verifier" || consumed.Nonce != "nonce" || consumed.ReturnTo != "/after" {
		t.Fatalf("consumed oauth state = %+v", consumed)
	}
	if _, err := store.Consume(ctx, connectorID, state, "browser-binding"); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("replay consume error = %v, want unauthorized", err)
	}
}

func TestRedisOAuthStateStoreWrongBindingOrConnectorDoesNotConsume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewRedisOAuthStateStore(integrationredis.OpenClient(t))
	connectorID := uuid.New()
	state := "state-" + uuid.NewString()

	if _, err := store.Create(
		ctx,
		OAuthStateCreateInput{
			AuthConnectorID:     connectorID,
			State:               state,
			BrowserBindingToken: "browser-binding",
			CodeVerifier:        "code-verifier",
			Nonce:               "nonce",
			ReturnTo:            "/",
		},
	); err != nil {
		t.Fatalf("create oauth state: %v", err)
	}
	if _, err := store.Consume(ctx, connectorID, state, "wrong-binding"); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("consume with wrong binding error = %v, want unauthorized", err)
	}
	if _, err := store.Consume(ctx, uuid.New(), state, "browser-binding"); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("consume with wrong connector error = %v, want unauthorized", err)
	}
	consumed, err := store.Consume(ctx, connectorID, state, "browser-binding")
	if err != nil {
		t.Fatalf("consume after mismatches: %v", err)
	}
	if consumed.CodeVerifier != "code-verifier" {
		t.Fatalf("consumed oauth state = %+v", consumed)
	}
}

func TestRedisOAuthStateStoreRejectsDuplicateState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewRedisOAuthStateStore(integrationredis.OpenClient(t))
	connectorID := uuid.New()
	state := "state-" + uuid.NewString()

	if _, err := store.Create(
		ctx,
		OAuthStateCreateInput{
			AuthConnectorID:     connectorID,
			State:               state,
			BrowserBindingToken: "browser-binding",
			CodeVerifier:        "code-verifier",
			Nonce:               "nonce",
		},
	); err != nil {
		t.Fatalf("create oauth state: %v", err)
	}
	if _, err := store.Create(
		ctx,
		OAuthStateCreateInput{
			AuthConnectorID:     connectorID,
			State:               state,
			BrowserBindingToken: "browser-binding",
			CodeVerifier:        "code-verifier",
			Nonce:               "nonce",
		},
	); !errors.Is(
		err,
		storeerr.ErrIdempotencyConflict,
	) {
		t.Fatalf("duplicate create error = %v, want idempotency conflict", err)
	}
}
