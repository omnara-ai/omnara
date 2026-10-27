//go:build integration

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestListOrgMembers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := storage.NewStore(pool)

	project := bootstrapPublicHTTPProject(t, handler, "members")

	member, err := storagetest.CreateVerifiedUser(
		ctx, pool, storagetest.CreateVerifiedUserInput{Email: "members-second@example.com", DisplayName: "Second"},
	)
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
	data := testutil.RequireType[[]any](t, listed["data"])
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
		row := testutil.RequireType[map[string]any](t, raw)
		byName[testutil.RequireType[string](t, row["display_name"])] = row
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
	nameSortedRows := orderedMemberNames(t, testutil.RequireType[[]any](t, nameSorted["data"]))
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
	filteredRows := orderedMemberNames(t, testutil.RequireType[[]any](t, filtered["data"]))
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

func TestTransferOrgOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := storage.NewStore(pool)

	project := bootstrapPublicHTTPProject(t, handler, "transfer-owner")

	admin, err := storagetest.CreateVerifiedUser(
		ctx, pool, storagetest.CreateVerifiedUserInput{Email: "transfer-owner-admin@example.com", DisplayName: "Admin"},
	)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: project.OrgUUID, UserID: admin.ID, Role: "admin"},
	); err != nil {
		t.Fatalf("add admin: %v", err)
	}
	adminPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: admin.ID, Name: "admin"},
	)
	if err != nil {
		t.Fatalf("create admin pat: %v", err)
	}
	outsider, err := storagetest.CreateVerifiedUser(
		ctx, pool, storagetest.CreateVerifiedUserInput{Email: "transfer-owner-outsider@example.com", DisplayName: "Outsider"},
	)
	if err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	adminUserID, err := publicid.Encode(publicid.KindUser, admin.ID)
	if err != nil {
		t.Fatalf("encode admin user id: %v", err)
	}
	outsiderUserID, err := publicid.Encode(publicid.KindUser, outsider.ID)
	if err != nil {
		t.Fatalf("encode outsider user id: %v", err)
	}

	membersPath := "/api/v1/orgs/" + project.OrgID + "/members"
	transfer := func(userID, token string, wantStatus int) {
		t.Helper()
		requestJSONWithHeaders(
			t, handler, http.MethodPost, membersPath+"/"+userID+"/transfer-ownership", "", "",
			wantStatus, authHeaders(token),
		)
	}
	transfer(adminUserID, adminPAT.Token, http.StatusForbidden)
	transfer(project.AdminUserID, project.AdminToken, http.StatusBadRequest)
	transfer(outsiderUserID, project.AdminToken, http.StatusNotFound)
	transfer(adminUserID, project.AdminToken, http.StatusNoContent)

	listed := requestJSONWithHeaders(
		t, handler, http.MethodGet, membersPath, "", "", http.StatusOK, authHeaders(project.AdminToken),
	)
	roles := map[any]any{}
	for _, raw := range testutil.RequireType[[]any](t, listed["data"]) {
		row := testutil.RequireType[map[string]any](t, raw)
		roles[row["user_id"]] = row["role"]
	}
	if roles[adminUserID] != "owner" || roles[project.AdminUserID] != "admin" {
		t.Fatalf("roles after transfer = %v, want %s owner and %s admin", roles, adminUserID, project.AdminUserID)
	}

	transfer(adminUserID, project.AdminToken, http.StatusForbidden)
	if err := store.Identity().TransferOrgOwnership(ctx, identitystore.TransferOrgOwnershipInput{
		OrgID: project.OrgUUID, OwnerUserID: project.AdminUserUUID, NewOwnerUserID: admin.ID,
	}); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("transfer by former owner err = %v, want unauthorized", err)
	}
	if _, err := store.Organizations().DeleteOrganization(
		ctx, project.OrgUUID, identitystore.NewUserPrincipal(project.AdminUserUUID),
	); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("delete by former owner err = %v, want unauthorized", err)
	}

	requestJSONWithHeaders(
		t, handler, http.MethodDelete, membersPath+"/"+project.AdminUserID, "", "",
		http.StatusNoContent, authHeaders(project.AdminToken),
	)
}

func orderedMemberNames(t *testing.T, data []any) []string {
	t.Helper()
	names := make([]string, 0, len(data))
	for _, raw := range data {
		names = append(names, testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, raw)["display_name"]))
	}
	return names
}

func assertMembersNewestFirst(t *testing.T, data []any) {
	t.Helper()
	var prev time.Time
	for i, raw := range data {
		ts, err := time.Parse(
			time.RFC3339Nano,
			testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, raw)["created_at"]),
		)
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
		rows := testutil.RequireType[[]any](t, page["data"])
		if len(rows) > 1 {
			t.Fatalf("page returned %d members, want <= limit 1", len(rows))
		}
		for _, raw := range rows {
			row := testutil.RequireType[map[string]any](t, raw)
			id := testutil.RequireType[string](t, row["user_id"])
			if seen[id] {
				t.Fatalf("cursor paging returned duplicate member %s", id)
			}
			seen[id] = true
			got = append(got, testutil.RequireType[string](t, row["display_name"]))
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
		cursor = testutil.RequireType[string](t, next)
	}
	return got
}
