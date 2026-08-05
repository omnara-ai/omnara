//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestGetCurrentUserReturnsIdentityAndOrgs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := storage.NewStore(pool)

	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "me@example.com", DisplayName: "Me Myself"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	pat, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: user.ID, Name: "me", TokenID: "me"},
	)
	if err != nil {
		t.Fatalf("create pat: %v", err)
	}
	token := pat.Token
	wantUserID, err := publicid.Encode(publicid.KindUser, user.ID)
	if err != nil {
		t.Fatalf("encode user id: %v", err)
	}

	requestJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/me", "", "", http.StatusUnauthorized, nil)

	me := requestJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/me", "", "", http.StatusOK, authHeaders(token))
	identity := me["user"].(map[string]any)
	if identity["id"] != wantUserID {
		t.Fatalf("user id = %v, want %v", identity["id"], wantUserID)
	}
	if identity["email"] != "me@example.com" {
		t.Fatalf("email = %v, want me@example.com", identity["email"])
	}
	if identity["display_name"] != "Me Myself" {
		t.Fatalf("display_name = %v, want Me Myself", identity["display_name"])
	}
	if orgs := me["orgs"].([]any); len(orgs) != 0 {
		t.Fatalf("expected no orgs before joining, got %+v", orgs)
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Beta Org"}`,
		"idem-beta",
		http.StatusCreated,
		authHeaders(token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Alpha Org"}`,
		"idem-alpha",
		http.StatusCreated,
		authHeaders(token),
	)

	other, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "other@example.com", DisplayName: "Other"})
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	otherPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: other.ID, Name: "other", TokenID: "other"},
	)
	if err != nil {
		t.Fatalf("create other pat: %v", err)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Other Org"}`,
		"idem-other",
		http.StatusCreated,
		authHeaders(otherPAT.Token),
	)

	me = requestJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/me", "", "", http.StatusOK, authHeaders(token))
	orgs := me["orgs"].([]any)
	if len(orgs) != 2 {
		t.Fatalf("expected exactly the caller's 2 orgs, got %+v", orgs)
	}
	first := orgs[0].(map[string]any)
	second := orgs[1].(map[string]any)
	if first["name"] != "Alpha Org" || second["name"] != "Beta Org" {
		t.Fatalf("orgs not sorted by name: %+v", orgs)
	}
	for _, raw := range orgs {
		org := raw.(map[string]any)
		if org["role"] != "owner" {
			t.Fatalf("expected owner role on created org, got %+v", org)
		}
		if org["id"] == "" || org["created_at"] == "" {
			t.Fatalf("org missing id/created_at: %+v", org)
		}
	}
}
