//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestPersonalAccessTokenListAndRevoke(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)

	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "pat@example.com", DisplayName: "Pat"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	bootstrap, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: user.ID, Name: "bootstrap"},
	)
	if err != nil {
		t.Fatalf("create bootstrap pat: %v", err)
	}
	token := bootstrap.Token
	if _, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    user.ID,
			Token:     "pat-browser-session",
			CSRFToken: "pat-browser-csrf",
			TTL:       time.Hour,
		},
	); err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	browserHeaders := browserAuthHeaders("pat-browser-session", "pat-browser-csrf")

	list := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/personal-access-tokens",
		"",
		"",
		http.StatusOK,
		authHeaders(token),
	)
	if data := list["data"].([]any); len(data) != 1 || data[0].(map[string]any)["name"] != "bootstrap" {
		t.Fatalf("unexpected initial token list: %+v", list)
	}
	if _, ok := list["next_cursor"]; !ok {
		t.Fatalf("response missing next_cursor: %+v", list)
	}
	if list["next_cursor"] != nil {
		t.Fatalf("single full page should have null next_cursor, got %v", list["next_cursor"])
	}

	first := list["data"].([]any)[0].(map[string]any)
	if _, leaked := first["token"]; leaked {
		t.Fatalf("list leaked token plaintext: %+v", first)
	}
	if _, leaked := first["token_hash"]; leaked {
		t.Fatalf("list leaked token hash: %+v", first)
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/personal-access-tokens",
		`{"name":"pat cannot mint"}`,
		"",
		http.StatusForbidden,
		authHeaders(token),
	)

	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/personal-access-tokens",
		`{"name":"ci"}`,
		"",
		http.StatusCreated,
		browserHeaders,
	)
	ciTokenID := created["token_record"].(map[string]any)["id"].(string)
	ciToken := created["token"].(string)

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/personal-access-tokens",
		"",
		"",
		http.StatusOK,
		authHeaders(ciToken),
	)

	list = requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/personal-access-tokens",
		"",
		"",
		http.StatusOK,
		authHeaders(token),
	)
	data := list["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("expected 2 tokens, got %+v", data)
	}
	if data[0].(map[string]any)["id"] != ciTokenID {
		t.Fatalf("expected newest token first, got %+v", data)
	}
	for _, raw := range data {
		if raw.(map[string]any)["revoked_at"] != nil {
			t.Fatalf("expected active tokens, got %+v", raw)
		}
	}

	const extraTokens = 4
	for i := 0; i < extraTokens; i++ {
		if _, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(ctx, identitystore.CreatePersonalAccessTokenInput{
			UserID: user.ID,
			Name:   "page-" + string(rune('a'+i)),
		}); err != nil {
			t.Fatalf("seed paging token %d: %v", i, err)
		}
	}
	wantTotal := 2 + extraTokens
	pagedIDs := make([]string, 0, wantTotal)
	pagedCreatedAt := make([]time.Time, 0, wantTotal)
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		path := "/api/v1/personal-access-tokens?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page := requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", http.StatusOK, authHeaders(token))
		rows := page["data"].([]any)
		if len(rows) > 2 {
			t.Fatalf("page returned %d tokens, want <= limit 2", len(rows))
		}
		for _, raw := range rows {
			row := raw.(map[string]any)
			id := row["id"].(string)
			if seen[id] {
				t.Fatalf("cursor paging returned duplicate token %s", id)
			}
			seen[id] = true
			pagedIDs = append(pagedIDs, id)
			ts, err := time.Parse(time.RFC3339Nano, row["created_at"].(string))
			if err != nil {
				t.Fatalf("parse token created_at: %v", err)
			}
			pagedCreatedAt = append(pagedCreatedAt, ts)
		}
		if pages > wantTotal+2 {
			t.Fatalf("pagination did not terminate; got=%v", pagedIDs)
		}
		next, ok := page["next_cursor"]
		if !ok {
			t.Fatalf("response missing next_cursor: %+v", page)
		}
		if next == nil {
			break
		}
		cursor = next.(string)
	}
	if len(pagedIDs) != wantTotal {
		t.Fatalf("paged %d tokens, want %d: %v", len(pagedIDs), wantTotal, pagedIDs)
	}
	for i := 1; i < len(pagedCreatedAt); i++ {
		if pagedCreatedAt[i].After(pagedCreatedAt[i-1]) {
			t.Fatalf(
				"token %d created_at %s newer than predecessor %s; want newest-first",
				i,
				pagedCreatedAt[i],
				pagedCreatedAt[i-1],
			)
		}
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/personal-access-tokens?cursor=not-a-valid-cursor",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/personal-access-tokens?limit=101",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(token),
	)

	other, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "other-pat@example.com", DisplayName: "Other"})
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	otherPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: other.ID, Name: "other"},
	)
	if err != nil {
		t.Fatalf("create other pat: %v", err)
	}
	otherList := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/personal-access-tokens",
		"",
		"",
		http.StatusOK,
		authHeaders(otherPAT.Token),
	)
	if d := otherList["data"].([]any); len(d) != 1 || d[0].(map[string]any)["name"] != "other" {
		t.Fatalf("token list leaked across users: %+v", otherList)
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/personal-access-tokens/"+ciTokenID+"/revoke",
		"",
		"",
		http.StatusNotFound,
		authHeaders(otherPAT.Token),
	)

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/personal-access-tokens/"+ciTokenID+"/revoke",
		`{"unexpected":true}`,
		"",
		http.StatusBadRequest,
		authHeaders(token),
	)

	revoked := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/personal-access-tokens/"+ciTokenID+"/revoke",
		"",
		"",
		http.StatusOK,
		authHeaders(token),
	)
	if revoked["id"] != ciTokenID || revoked["revoked_at"] == nil {
		t.Fatalf("expected revoked token with revoked_at, got %+v", revoked)
	}
	firstRevokedAt := revoked["revoked_at"]

	revokedAgain := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/personal-access-tokens/"+ciTokenID+"/revoke",
		"",
		"",
		http.StatusOK,
		authHeaders(token),
	)
	if revokedAgain["revoked_at"] != firstRevokedAt {
		t.Fatalf("revoke not idempotent: %v vs %v", revokedAgain["revoked_at"], firstRevokedAt)
	}

	missing, err := publicid.Encode(publicid.KindPersonalAccessToken, httpTestID("missing-pat"))
	if err != nil {
		t.Fatalf("encode missing id: %v", err)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/personal-access-tokens/"+missing+"/revoke",
		"",
		"",
		http.StatusNotFound,
		authHeaders(token),
	)

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/personal-access-tokens",
		"",
		"",
		http.StatusUnauthorized,
		authHeaders(ciToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/personal-access-tokens",
		"",
		"",
		http.StatusOK,
		authHeaders(token),
	)
}
