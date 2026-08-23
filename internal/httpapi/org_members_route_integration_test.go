//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestListOrgMembers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := storage.NewStore(pool)

	project := bootstrapPublicHTTPProject(t, handler, "members")

	member, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "members-second@example.com", DisplayName: "Second"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: project.OrgUUID, UserID: member.ID, Role: "member"},
	); err != nil {
		t.Fatalf("add member: %v", err)
	}
	memberPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: member.ID, Name: "member"},
	)
	if err != nil {
		t.Fatalf("create member pat: %v", err)
	}

	split, err := store.Identity().CreateUser(ctx, identitystore.CreateUserInput{DisplayName: "Split"})
	if err != nil {
		t.Fatalf("create split member: %v", err)
	}
	if _, err := store.Identity().CreateUserEmail(ctx, identitystore.CreateUserEmailInput{
		UserID:    split.ID,
		Email:     "members-split-primary@example.com",
		IsPrimary: true,
	}); err != nil {
		t.Fatalf("create split unverified primary email: %v", err)
	}
	if _, err := store.Identity().CreateUserEmail(ctx, identitystore.CreateUserEmailInput{
		UserID:    split.ID,
		Email:     "members-split-secondary@example.com",
		Verified:  true,
		IsPrimary: false,
	}); err != nil {
		t.Fatalf("create split verified secondary email: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{
			OrgID:  project.OrgUUID,
			UserID: split.ID,
			Role:   "member",
		},
	); err != nil {
		t.Fatalf("add split member: %v", err)
	}

	membersPath := "/api/v1/orgs/" + project.OrgID + "/members"

	listed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		membersPath,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	data := listed["data"].([]any)
	if len(data) != 3 {
		t.Fatalf("expected 3 members, got %d: %+v", len(data), data)
	}
	if _, ok := listed["next_cursor"]; !ok {
		t.Fatalf("response missing next_cursor: %+v", listed)
	}
	if listed["next_cursor"] != nil {
		t.Fatalf("single full page should have null next_cursor, got %v", listed["next_cursor"])
	}
	byName := map[string]map[string]any{}
	for _, raw := range data {
		row := raw.(map[string]any)
		byName[row["display_name"].(string)] = row
	}
	owner, ok := byName["Owner"]
	if !ok {
		t.Fatalf("missing owner in roster: %+v", data)
	}
	if owner["user_id"] != project.AdminUserID || owner["email"] != "members-owner@example.com" ||
		owner["role"] != "owner" {
		t.Fatalf("unexpected owner row: %+v", owner)
	}
	second, ok := byName["Second"]
	if !ok {
		t.Fatalf("missing member in roster: %+v", data)
	}
	if second["email"] != "members-second@example.com" || second["role"] != "member" {
		t.Fatalf("unexpected member row: %+v", second)
	}
	splitRow, ok := byName["Split"]
	if !ok {
		t.Fatalf("missing split member in roster: %+v", data)
	}
	if splitRow["email"] != "" || splitRow["role"] != "member" {
		t.Fatalf("split member should report empty email, got: %+v", splitRow)
	}

	gotOrder := orderedMemberNames(t, data)
	assertMembersNewestFirst(t, data)
	if posOf(gotOrder, "Split") > posOf(gotOrder, "Second") {
		t.Fatalf("expected Split before Second (Split added later), got %v", gotOrder)
	}

	nameSorted := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		membersPath+"?sort=name",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	nameSortedRows := orderedMemberNames(t, nameSorted["data"].([]any))
	if len(nameSortedRows) != 3 || nameSortedRows[0] != "Owner" ||
		nameSortedRows[1] != "Second" || nameSortedRows[2] != "Split" {
		t.Fatalf("name-sorted members = %v, want Owner, Second, Split", nameSortedRows)
	}

	filtered := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		membersPath+"?name=*eco*&sort=-name",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	filteredRows := orderedMemberNames(t, filtered["data"].([]any))
	if len(filteredRows) != 1 || filteredRows[0] != "Second" {
		t.Fatalf("filtered members = %v, want only Second", filteredRows)
	}

	paged := pageThroughMembers(t, handler, membersPath, project.AdminToken)
	if len(paged) != 3 {
		t.Fatalf("paged %d members, want 3: %v", len(paged), paged)
	}
	for i := range gotOrder {
		if paged[i] != gotOrder[i] {
			t.Fatalf("paged order %v does not match single-page order %v", paged, gotOrder)
		}
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		membersPath+"?cursor=not-a-valid-cursor",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		membersPath+"?limit=0",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)

	requestJSONWithHeaders(t, handler, http.MethodGet, membersPath, "", "", http.StatusOK, authHeaders(memberPAT.Token))

	other := bootstrapPublicHTTPProject(t, handler, "members-other")
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		membersPath,
		"",
		"",
		http.StatusNotFound,
		authHeaders(other.AdminToken),
	)
}

func orderedMemberNames(t *testing.T, data []any) []string {
	t.Helper()
	names := make([]string, 0, len(data))
	for _, raw := range data {
		names = append(names, raw.(map[string]any)["display_name"].(string))
	}
	return names
}

func assertMembersNewestFirst(t *testing.T, data []any) {
	t.Helper()
	var prev time.Time
	for i, raw := range data {
		ts, err := time.Parse(time.RFC3339Nano, raw.(map[string]any)["created_at"].(string))
		if err != nil {
			t.Fatalf("parse created_at for member %d: %v", i, err)
		}
		if i > 0 && ts.After(prev) {
			t.Fatalf("member %d created_at %s is newer than predecessor %s; want newest-first", i, ts, prev)
		}
		prev = ts
	}
}

func posOf(names []string, target string) int {
	for i, name := range names {
		if name == target {
			return i
		}
	}
	return -1
}

func pageThroughMembers(t *testing.T, handler http.Handler, membersPath, token string) []string {
	t.Helper()
	got := make([]string, 0)
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		path := membersPath + "?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page := requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", http.StatusOK, authHeaders(token))
		rows := page["data"].([]any)
		if len(rows) > 1 {
			t.Fatalf("page returned %d members, want <= limit 1", len(rows))
		}
		for _, raw := range rows {
			row := raw.(map[string]any)
			id := row["user_id"].(string)
			if seen[id] {
				t.Fatalf("cursor paging returned duplicate member %s", id)
			}
			seen[id] = true
			got = append(got, row["display_name"].(string))
		}
		if pages > 5 {
			t.Fatalf("pagination did not terminate; got=%v", got)
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
	return got
}
