//go:build integration

package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/omnara-ai/omnara/internal/authn"
	"github.com/omnara-ai/omnara/internal/bearertoken"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

type capturedAuthEmail struct {
	to    string
	token string
	path  string
}

type captureAuthEmailSender struct {
	verification chan capturedAuthEmail
	reset        chan capturedAuthEmail
	account      chan string
	changed      chan string
}

type allowAllAuthLimiter struct{}

func (allowAllAuthLimiter) Allow(context.Context, string, int, time.Duration) error {
	return nil
}

func withRedisOAuthStateAndAllowAllLimiter(t testing.TB) Option {
	t.Helper()
	redisClient := integrationredis.OpenClient(t)
	return func(s *Server) {
		WithOAuthStateStore(httpauth.NewRedisOAuthStateStore(redisClient))(s)
		WithAuthRateLimiter(allowAllAuthLimiter{})(s)
	}
}

func newCaptureAuthEmailSender() *captureAuthEmailSender {
	return &captureAuthEmailSender{
		verification: make(chan capturedAuthEmail, 16),
		reset:        make(chan capturedAuthEmail, 16),
		account:      make(chan string, 16),
		changed:      make(chan string, 16),
	}
}

func (s *captureAuthEmailSender) SendInvite(context.Context, string, string) error {
	return nil
}

func (s *captureAuthEmailSender) SendEmailVerification(_ context.Context, to, verifyURL string) error {
	s.verification <- authEmailFromURL(to, verifyURL)
	return nil
}

func (s *captureAuthEmailSender) SendPasswordReset(_ context.Context, to, resetURL string) error {
	s.reset <- authEmailFromURL(to, resetURL)
	return nil
}

func (s *captureAuthEmailSender) SendAccountExists(_ context.Context, to, _ string) error {
	s.account <- to
	return nil
}

func (s *captureAuthEmailSender) SendPasswordChangedNotice(_ context.Context, to string) error {
	s.changed <- to
	return nil
}

func authEmailFromURL(to, raw string) capturedAuthEmail {
	parsed, err := url.Parse(raw)
	if err != nil {
		return capturedAuthEmail{to: to}
	}
	return capturedAuthEmail{to: to, token: parsed.Query().Get("token"), path: parsed.Path}
}

func waitAuthEmail(t *testing.T, ch <-chan capturedAuthEmail) capturedAuthEmail {
	t.Helper()
	select {
	case email := <-ch:
		return email
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for auth email")
		return capturedAuthEmail{}
	}
}

func waitStringEmail(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case email := <-ch:
		return email
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for auth email")
		return ""
	}
}

func assertNoAuthEmail(t *testing.T, ch <-chan capturedAuthEmail) {
	t.Helper()
	select {
	case email := <-ch:
		t.Fatalf("unexpected auth email: %+v", email)
	case <-time.After(100 * time.Millisecond):
	}
}

func assertNoStringEmail(t *testing.T, ch <-chan string) {
	t.Helper()
	select {
	case email := <-ch:
		t.Fatalf("unexpected auth email to %q", email)
	case <-time.After(100 * time.Millisecond):
	}
}

func newJSONRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func newAuthJSONRequest(method, path, body string) *http.Request {
	req := newJSONRequest(method, "http://omnara.test"+path, body)
	req.Header.Set("Origin", "http://omnara.test")
	return req
}

func cookieValue(cookies []*http.Cookie, name string) string {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func performRequest(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func assertAuthNoSessionCookies(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := cookieValue(rec.Result().Cookies(), httpauth.BrowserSessionCookieName); got != "" {
		t.Fatalf("unexpected session cookie %q", got)
	}
	if got := cookieValue(rec.Result().Cookies(), httpauth.CSRFCookieName); got != "" {
		t.Fatalf("unexpected csrf cookie %q", got)
	}
}

func TestPublicIdentityOrgBootstrapUsesAuthenticatedUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := newIntegrationStore(pool)
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "owner@example.com", DisplayName: "Owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	ownerPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: user.ID,
			Name:   "owner",
		},
	)
	if err != nil {
		t.Fatalf("create pat: %v", err)
	}
	ownerToken := ownerPAT.Token

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Unauthenticated Org"}`,
		"idem-unauthenticated-org",
		http.StatusUnauthorized,
		nil,
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"No Idempotency Org"}`,
		"",
		http.StatusCreated,
		authHeaders(ownerToken),
	)
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Owner Org"}`,
		"idem-owner-org",
		http.StatusCreated,
		authHeaders(ownerToken),
	)
	org := created["org"].(map[string]any)
	project := created["project"].(map[string]any)
	if org["id"] == "" || project["id"] == "" {
		t.Fatalf("expected org and default project: %+v", created)
	}
	orgID, err := publicid.Decode(publicid.KindOrganization, org["id"].(string))
	if err != nil {
		t.Fatalf("decode created organization ID: %v", err)
	}
	storedOrg, err := store.Identity().GetOrg(ctx, orgID)
	if err != nil || storedOrg.ID != orgID {
		t.Fatalf("stored organization = %+v err=%v, want ID %s", storedOrg, err, orgID)
	}
	replayed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Owner Org"}`,
		"idem-owner-org",
		http.StatusOK,
		authHeaders(ownerToken),
	)
	if replayed["org"].(map[string]any)["id"] != org["id"] {
		t.Fatalf("expected idempotent replay, got %+v", replayed)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Changed Org"}`,
		"idem-owner-org",
		http.StatusConflict,
		authHeaders(ownerToken),
	)
}

func TestPublicInvitationFlow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := newIntegrationStore(pool)
	project := bootstrapPublicHTTPProject(t, handler, "invite-flow")
	invitee, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "invitee@example.com", DisplayName: "Invitee"})
	if err != nil {
		t.Fatalf("create invitee: %v", err)
	}
	inviteePAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: invitee.ID,
			Name:   "invitee",
		},
	)
	if err != nil {
		t.Fatalf("create invitee token: %v", err)
	}
	inviteeToken := inviteePAT.Token

	invite := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/invitations",
		`{"email":"Invitee@Example.com","role":"member"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	inviteID := invite["id"].(string)
	if invite["org_name"] != "invite-flow Org" {
		t.Fatalf("invitation organization name = %v, want invite-flow Org", invite["org_name"])
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/invitations",
		`{"email":"invitee@example.com","role":"admin"}`,
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/invitations",
		`{"email":"owner@example.com","role":"owner"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)

	pending := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/invitations",
		"",
		"",
		http.StatusOK,
		authHeaders(inviteeToken),
	)
	data := pending["data"].([]any)
	if len(data) != 1 ||
		data[0].(map[string]any)["id"] != inviteID ||
		data[0].(map[string]any)["org_name"] != "invite-flow Org" {
		t.Fatalf("unexpected pending invitations: %+v", pending)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/invitations/"+inviteID+"/decline",
		"",
		"",
		http.StatusOK,
		authHeaders(inviteeToken),
	)
	reinvited := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/invitations",
		`{"email":"invitee@example.com","role":"admin"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	if reinvited["id"] == inviteID {
		t.Fatalf("invitation after decline reused consumed id: %+v", reinvited)
	}
	inviteID = reinvited["id"].(string)
	accepted := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/invitations/"+inviteID+"/accept",
		"",
		"",
		http.StatusOK,
		authHeaders(inviteeToken),
	)
	if accepted["org_id"] == nil ||
		accepted["id"] == nil ||
		accepted["org_name"] != "invite-flow Org" {
		t.Fatalf("expected consumed invitation receipt, got %+v", accepted)
	}
	launchPublicHTTPAgent(
		t,
		handler,
		project,
		"invitee",
		inviteeToken,
		http.StatusCreated,
	)
}

func TestPublicOrgMemberAndProjectAccessManagement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := newIntegrationStore(pool)
	project := bootstrapPublicHTTPProject(t, handler, "member-mgmt")

	member, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "member-mgmt-member@example.com", DisplayName: "Member"})
	if err != nil {
		t.Fatalf("create member user: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID: project.OrgUUID, UserID: member.ID, Role: "member",
	}); err != nil {
		t.Fatalf("add org membership: %v", err)
	}
	memberPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(ctx, identitystore.CreatePersonalAccessTokenInput{
		UserID: member.ID, Name: "member",
	})
	if err != nil {
		t.Fatalf("create member token: %v", err)
	}
	memberUserID, err := publicid.Encode(publicid.KindUser, member.ID)
	if err != nil {
		t.Fatalf("encode member user id: %v", err)
	}
	memberPath := "/api/v1/orgs/" + project.OrgID + "/members/" + memberUserID

	requestJSONWithHeaders(
		t, handler, http.MethodPatch, memberPath, `{"role":"admin"}`, "",
		http.StatusForbidden, authHeaders(memberPAT.Token),
	)

	updated := requestJSONWithHeaders(
		t, handler, http.MethodPatch, memberPath, `{"role":"admin"}`, "",
		http.StatusOK, authHeaders(project.AdminToken),
	)
	if updated["role"] != "admin" || updated["user_id"] != memberUserID {
		t.Fatalf("unexpected updated membership: %+v", updated)
	}

	projectsPath := memberPath + "/projects"
	empty := requestJSONWithHeaders(
		t, handler, http.MethodGet, projectsPath, "", "",
		http.StatusOK, authHeaders(project.AdminToken),
	)
	if data, _ := empty["data"].([]any); len(data) != 0 {
		t.Fatalf("expected no project grants yet, got %+v", empty)
	}

	grantPath := projectsPath + "/" + project.ProjectID
	grant := requestJSONWithHeaders(
		t, handler, http.MethodPut, grantPath, `{"role":"developer"}`, "",
		http.StatusOK, authHeaders(project.AdminToken),
	)
	if grant["role"] != "developer" || grant["project_id"] != project.ProjectID {
		t.Fatalf("unexpected project grant: %+v", grant)
	}

	listed := requestJSONWithHeaders(
		t, handler, http.MethodGet, projectsPath, "", "",
		http.StatusOK, authHeaders(project.AdminToken),
	)
	data, _ := listed["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["role"] != "developer" {
		t.Fatalf("unexpected project grants: %+v", listed)
	}

	requestJSONWithHeaders(
		t, handler, http.MethodPut, grantPath, `{"role":"not-a-real-role"}`, "",
		http.StatusBadRequest, authHeaders(project.AdminToken),
	)

	requestJSONWithHeaders(
		t, handler, http.MethodDelete, grantPath, "", "",
		http.StatusNoContent, authHeaders(project.AdminToken),
	)
	emptyAgain := requestJSONWithHeaders(
		t, handler, http.MethodGet, projectsPath, "", "",
		http.StatusOK, authHeaders(project.AdminToken),
	)
	if data, _ := emptyAgain["data"].([]any); len(data) != 0 {
		t.Fatalf("expected project grant to be gone, got %+v", emptyAgain)
	}
	requestJSONWithHeaders(
		t, handler, http.MethodDelete, grantPath, "", "",
		http.StatusNotFound, authHeaders(project.AdminToken),
	)

	projectAdmin, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email: "member-mgmt-project-admin@example.com", DisplayName: "Project Admin",
	})
	if err != nil {
		t.Fatalf("create project admin user: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID: project.OrgUUID, UserID: projectAdmin.ID, Role: "member",
	}); err != nil {
		t.Fatalf("add project admin org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, UserID: projectAdmin.ID, Role: "admin",
	}); err != nil {
		t.Fatalf("add project admin project membership: %v", err)
	}
	projectAdminPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(ctx, identitystore.CreatePersonalAccessTokenInput{
		UserID: projectAdmin.ID, Name: "project-admin",
	})
	if err != nil {
		t.Fatalf("create project admin token: %v", err)
	}

	developer, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email: "member-mgmt-developer@example.com", DisplayName: "Developer",
	})
	if err != nil {
		t.Fatalf("create developer user: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID: project.OrgUUID, UserID: developer.ID, Role: "member",
	}); err != nil {
		t.Fatalf("add developer org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, UserID: developer.ID, Role: "developer",
	}); err != nil {
		t.Fatalf("add developer project membership: %v", err)
	}
	developerPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(ctx, identitystore.CreatePersonalAccessTokenInput{
		UserID: developer.ID, Name: "developer",
	})
	if err != nil {
		t.Fatalf("create developer token: %v", err)
	}

	requestJSONWithHeaders(
		t, handler, http.MethodPut, grantPath, `{"role":"viewer"}`, "",
		http.StatusForbidden, authHeaders(developerPAT.Token),
	)
	requestJSONWithHeaders(
		t, handler, http.MethodPut, grantPath, `{"role":"viewer"}`, "",
		http.StatusOK, authHeaders(projectAdminPAT.Token),
	)
	requestJSONWithHeaders(
		t, handler, http.MethodDelete, grantPath, "", "",
		http.StatusForbidden, authHeaders(developerPAT.Token),
	)
	requestJSONWithHeaders(
		t, handler, http.MethodDelete, grantPath, "", "",
		http.StatusNoContent, authHeaders(projectAdminPAT.Token),
	)

	requestJSONWithHeaders(
		t, handler, http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/members/"+project.AdminUserID,
		"", "", http.StatusForbidden, authHeaders(project.AdminToken),
	)

	requestJSONWithHeaders(
		t, handler, http.MethodDelete, memberPath, "", "",
		http.StatusNoContent, authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t, handler, http.MethodPatch, memberPath, `{"role":"member"}`, "",
		http.StatusNotFound, authHeaders(project.AdminToken),
	)
}

func TestOrgAdminCreatesPrivateProjectByDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := newIntegrationStore(pool)
	project := bootstrapPublicHTTPProject(t, handler, "developer-project")

	creator, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "creator@example.com", DisplayName: "Creator"})
	if err != nil {
		t.Fatalf("create creator: %v", err)
	}
	other, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "other-dev@example.com", DisplayName: "Other Dev"})
	if err != nil {
		t.Fatalf("create other developer: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID:  project.OrgUUID,
		UserID: creator.ID,
		Role:   "admin",
	}); err != nil {
		t.Fatalf("add creator admin membership: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID:  project.OrgUUID,
		UserID: other.ID,
		Role:   "member",
	}); err != nil {
		t.Fatalf("add other member membership: %v", err)
	}
	creatorPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: creator.ID,
			Name:   "creator",
		},
	)
	if err != nil {
		t.Fatalf("create creator pat: %v", err)
	}
	otherPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: other.ID,
			Name:   "other",
		},
	)
	if err != nil {
		t.Fatalf("create other pat: %v", err)
	}

	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/projects",
		`{"name":"Restricted Project"}`,
		"idem-restricted-project",
		http.StatusCreated,
		authHeaders(creatorPAT.Token),
	)
	projectID := created["id"].(string)
	projectPath := "/api/v1/orgs/" + project.OrgID + "/projects/" + projectID
	createdProject := publicHTTPProject{
		OrgID:       project.OrgID,
		ProjectID:   projectID,
		OrgUUID:     project.OrgUUID,
		ProjectUUID: mustPublicHTTPID(t, publicid.KindProject, projectID),
		ProjectPath: projectPath,
	}
	grantDefaultPublicHTTPModelToProject(t, handler, project, projectID, creatorPAT.Token)
	profile := createPublicHTTPAgent(t, handler, createdProject, "creator-restricted", creatorPAT.Token)
	profileID := profile["id"].(string)
	configID := profile["current_config"].(map[string]any)["id"].(string)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		projectPath+"/agents",
		`{"profile":"`+profileID+`","config":"`+configID+`"}`,
		"idem-creator-agent",
		http.StatusCreated,
		authHeaders(creatorPAT.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		projectPath+"/agents",
		`{"profile":"`+profileID+`","config":"`+configID+`"}`,
		"idem-other-agent",
		http.StatusNotFound,
		authHeaders(otherPAT.Token),
	)
}

func TestProjectOperatorCanRunAgentsButCannotManageProjectResources(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := newIntegrationStore(pool)
	project := bootstrapPublicHTTPProject(t, handler, "operator-project")

	operator, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "operator@example.com", DisplayName: "Operator"})
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID:  project.OrgUUID,
		UserID: operator.ID,
		Role:   "member",
	}); err != nil {
		t.Fatalf("add operator org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID:     project.OrgUUID,
		ProjectID: project.ProjectUUID,
		UserID:    operator.ID,
		Role:      "operator",
	}); err != nil {
		t.Fatalf("add operator project membership: %v", err)
	}
	operatorPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: operator.ID,
			Name:   "operator",
		},
	)
	if err != nil {
		t.Fatalf("create operator pat: %v", err)
	}
	operatorToken := operatorPAT.Token

	profile := createPublicHTTPAgent(
		t,
		handler,
		project,
		"operator-owned-by-admin",
		project.AdminToken,
	)
	profileID := profile["id"].(string)
	configID := profile["current_config"].(map[string]any)["id"].(string)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles",
		`{"name":"operator forbidden","config":"`+configID+`"}`,
		"idem-operator-create-agent-profile",
		http.StatusForbidden,
		authHeaders(operatorToken),
	)
	launched := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"profile":"`+profileID+`","config":"`+configID+`"}`,
		"idem-operator-agent",
		http.StatusCreated,
		authHeaders(operatorToken),
	)
	agentID := launched["agent"].(map[string]any)["id"].(string)
	changedYAML := "instruction: Operator must not author runtime config.\n" +
		"model:\n" +
		"  provider_config: openai-prod\n" +
		"  name: gpt-test\n"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/config",
		`{"source_format":"yaml","source":`+quotedJSONString(changedYAML)+`}`,
		"idem-operator-change-agent-config",
		http.StatusForbidden,
		authHeaders(operatorToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/archive",
		"",
		"",
		http.StatusForbidden,
		authHeaders(operatorToken),
	)
}

func TestPublicVisibilityAwareLists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := newIntegrationStore(pool)
	project := bootstrapPublicHTTPProject(t, handler, "visibility-lists")
	member, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "visibility-member@example.com", DisplayName: "Visibility Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	outsider, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email:       "visibility-outsider@example.com",
		DisplayName: "Visibility Outsider",
	},
	)
	if err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID:  project.OrgUUID,
		UserID: member.ID,
		Role:   "member",
	}); err != nil {
		t.Fatalf("add member org membership: %v", err)
	}
	memberPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: member.ID,
			Name:   "member",
		},
	)
	if err != nil {
		t.Fatalf("create member pat: %v", err)
	}
	outsiderPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: outsider.ID,
			Name:   "outsider",
		},
	)
	if err != nil {
		t.Fatalf("create outsider pat: %v", err)
	}
	memberToken := memberPAT.Token
	outsiderToken := outsiderPAT.Token

	createdProject := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/projects",
		`{"name":"Second Project"}`,
		"idem-visibility-second-project",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	secondProjectID := createdProject["id"].(string)
	adminProjects := requestJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/orgs/"+project.OrgID+"/projects", "", "", http.StatusOK, authHeaders(project.AdminToken))["data"].([]any)
	if len(adminProjects) != 2 {
		t.Fatalf("admin should see both projects, got %+v", adminProjects)
	}
	for _, projectRow := range adminProjects {
		access := projectRow.(map[string]any)["access"].(map[string]any)
		if access["can_read"] != true || access["can_manage"] != true ||
			access["can_manage_access"] != true ||
			access["can_operate"] != true {
			t.Fatalf("unexpected admin project access: %+v", access)
		}
	}
	memberProjects := requestJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/orgs/"+project.OrgID+"/projects", "", "", http.StatusOK, authHeaders(memberToken))["data"].([]any)
	if len(memberProjects) != 0 {
		t.Fatalf(
			"member without project grants should see no projects, got %+v",
			memberProjects,
		)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/projects",
		"",
		"",
		http.StatusNotFound,
		authHeaders(outsiderToken),
	)

	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID:     project.OrgUUID,
		ProjectID: project.ProjectUUID,
		UserID:    member.ID,
		Role:      "viewer",
	}); err != nil {
		t.Fatalf("add viewer project membership: %v", err)
	}
	memberProjects = requestJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/orgs/"+project.OrgID+"/projects", "", "", http.StatusOK, authHeaders(memberToken))["data"].([]any)
	if len(memberProjects) != 1 || memberProjects[0].(map[string]any)["id"] != project.ProjectID {
		t.Fatalf("viewer should see only granted project, got %+v", memberProjects)
	}
	access := memberProjects[0].(map[string]any)["access"].(map[string]any)
	if access["can_read"] != true || access["can_manage"] != false ||
		access["can_manage_access"] != false ||
		access["can_operate"] != false {
		t.Fatalf("unexpected viewer project access: %+v", access)
	}

	grantedMachine := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		`{"display_name":"granted machine","metadata":{"secret":"do-not-list"}}`,
		"idem-visibility-granted-machine",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		`{"display_name":"hidden machine"}`,
		"idem-visibility-hidden-machine",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	grantedMachineID := grantedMachine["id"].(string)
	grant := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/machine-grants", `{"machine_id":"`+grantedMachineID+`"}`, "idem-visibility-machine-grant", http.StatusCreated, authHeaders(project.AdminToken))["grant"].(map[string]any)
	memberMachines := requestJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/orgs/"+project.OrgID+"/machines", "", "", http.StatusOK, authHeaders(memberToken))["data"].([]any)
	if len(memberMachines) != 1 || memberMachines[0].(map[string]any)["id"] != grantedMachineID {
		t.Fatalf("viewer should see only project-granted machine, got %+v", memberMachines)
	}
	memberMachine := memberMachines[0].(map[string]any)
	for _, field := range []string{
		"installation_id",
		"provider_resource_id",
		"metadata",
		"next_reconcile_after",
		"reconcile_attempts",
	} {
		if _, ok := memberMachine[field]; ok {
			t.Fatalf(
				"visible machine summary leaked %s: %+v",
				field,
				memberMachine,
			)
		}
	}
	machineAccess := memberMachine["access"].(map[string]any)
	if machineAccess["can_manage"] != false {
		t.Fatalf(
			"viewer should not manage granted machine, got %+v",
			machineAccess,
		)
	}
	memberSources := machineAccess["sources"].([]any)
	if len(memberSources) != 1 {
		t.Fatalf("unexpected visible machine source: %+v", memberSources)
	}
	memberSource := memberSources[0].(map[string]any)
	if memberSource["kind"] != "project_machine_grant" ||
		memberSource["project_id"] != project.ProjectID ||
		memberSource["grant_id"] != grant["id"] ||
		memberSource["grant_source_kind"] != "explicit" {
		t.Fatalf("unexpected visible machine source: %+v", memberSource)
	}
	projectMachines := requestJSONFieldWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/machines",
		"",
		"",
		http.StatusOK,
		authHeaders(memberToken),
		"data",
	).([]any)
	if len(projectMachines) != 1 ||
		projectMachines[0].(map[string]any)["id"] != grantedMachineID {
		t.Fatalf(
			"project machines should include granted machine, got %+v",
			projectMachines,
		)
	}
	projectMachineAccess := projectMachines[0].(map[string]any)["access"].(map[string]any)
	projectSources := projectMachineAccess["sources"].([]any)
	if len(projectSources) != 1 {
		t.Fatalf("unexpected project machine source: %+v", projectSources)
	}
	projectSource := projectSources[0].(map[string]any)
	if projectSource["kind"] != "project_machine_grant" ||
		projectSource["project_id"] != project.ProjectID ||
		projectSource["grant_id"] != grant["id"] ||
		projectSource["grant_source_kind"] != "explicit" {
		t.Fatalf("unexpected project machine source: %+v", projectSource)
	}
	memberMachineDetail := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+grantedMachineID,
		"",
		"",
		http.StatusOK,
		authHeaders(memberToken),
	)
	if memberMachineDetail["id"] != grantedMachineID {
		t.Fatalf(
			"project-visible machine detail = %+v, want %s",
			memberMachineDetail,
			grantedMachineID,
		)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+grantedMachineID,
		"",
		"",
		http.StatusForbidden,
		authHeaders(memberToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/projects/"+secondProjectID+"/machines",
		"",
		"",
		http.StatusNotFound,
		authHeaders(memberToken),
	)

	adminMachines := requestJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/orgs/"+project.OrgID+"/machines", "", "", http.StatusOK, authHeaders(project.AdminToken))["data"].([]any)
	if len(adminMachines) != 2 {
		t.Fatalf("admin should see every machine, got %+v", adminMachines)
	}
}

func TestBrowserSessionRequiresCSRFForMutations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := newIntegrationStore(pool)
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "browser@example.com", DisplayName: "Browser"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := store.Identity().CreateBrowserSession(ctx, identitystore.CreateBrowserSessionInput{
		UserID:    user.ID,
		Token:     "browser-session-token",
		CSRFToken: "browser-csrf-token",
		TTL:       time.Hour,
	}); err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	path := "/api/v1/orgs"
	req := newJSONRequest(http.MethodPost, path, `{"name":"Browser Org"}`)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "browser-session-token"})
	rec := performRequest(handler, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf(
			"expected missing csrf to fail, got %d body=%s",
			rec.Code,
			rec.Body.String(),
		)
	}

	req = newJSONRequest(http.MethodPost, path, `{"name":"Browser Org"}`)
	req.Host = "omnara.test"
	req.Header.Set("Origin", "http://omnara.test")
	req.Header.Set("Idempotency-Key", "browser-org")
	req.Header.Set(httpauth.CSRFHeaderName, "browser-csrf-token")
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "browser-session-token"})
	rec = performRequest(handler, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf(
			"expected csrf-protected browser request to pass, got %d body=%s",
			rec.Code,
			rec.Body.String(),
		)
	}

	req = newJSONRequest(http.MethodPost, path, `{"name":"Invalid Bearer Org"}`)
	req.Host = "omnara.test"
	req.Header.Set("Origin", "http://omnara.test")
	unknownPAT, err := bearertoken.Generate(bearertoken.KindPersonalAccess)
	if err != nil {
		t.Fatalf("format unknown personal access token: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+unknownPAT)
	req.Header.Set(httpauth.CSRFHeaderName, "browser-csrf-token")
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "browser-session-token"})
	rec = performRequest(handler, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf(
			"expected invalid bearer to fail before cookie auth, got %d body=%s",
			rec.Code,
			rec.Body.String(),
		)
	}

	checksumCorruptPAT := unknownPAT[:len(unknownPAT)-1] + "0"
	if unknownPAT[len(unknownPAT)-1] == '0' {
		checksumCorruptPAT = unknownPAT[:len(unknownPAT)-1] + "1"
	}
	req = newJSONRequest(http.MethodPost, path, `{"name":"Checksum Bearer Org"}`)
	req.Host = "omnara.test"
	req.Header.Set("Origin", "http://omnara.test")
	req.Header.Set("Authorization", "Bearer "+checksumCorruptPAT)
	req.Header.Set(httpauth.CSRFHeaderName, "browser-csrf-token")
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "browser-session-token"})
	rec = performRequest(handler, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected checksum-corrupt bearer to fail before cookie auth, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = newJSONRequest(http.MethodPost, path, `{"name":"JWT Bearer Org"}`)
	req.Host = "omnara.test"
	req.Header.Set("Origin", "http://omnara.test")
	req.Header.Set("Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJvaWRjLXN1YmplY3QifQ.signature")
	req.Header.Set(httpauth.CSRFHeaderName, "browser-csrf-token")
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "browser-session-token"})
	rec = performRequest(handler, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected JWT-looking bearer to fail before cookie auth, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = newJSONRequest(http.MethodPost, "/api/v1/personal-access-tokens", `{"name":"browser cli"}`)
	req.Host = "omnara.test"
	req.Header.Set("Origin", "http://omnara.test")
	req.Header.Set(httpauth.CSRFHeaderName, "browser-csrf-token")
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "browser-session-token"})
	rec = performRequest(handler, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf(
			"expected browser user to create PAT, got %d body=%s",
			rec.Code,
			rec.Body.String(),
		)
	}
	var patResponse map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &patResponse); err != nil {
		t.Fatalf("decode PAT response: %v", err)
	}
	token, _ := patResponse["token"].(string)
	if err := bearertoken.Validate(token, bearertoken.KindPersonalAccess); err != nil {
		t.Fatalf("expected one-time plaintext PAT, got %+v", patResponse)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/personal-access-tokens",
		`{"name":"pat cannot mint pat"}`,
		"",
		http.StatusForbidden,
		authHeaders(token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"PAT Org"}`,
		"idem-pat-org",
		http.StatusCreated,
		authHeaders(token),
	)

	secureHandler := mustNewServer(t, store, WithPublicURL("https://omnara.test")).Handler()
	req = newJSONRequest(http.MethodPost, path, `{"name":"HTTPS Cookie Toss Org"}`)
	req.Host = "omnara.test"
	req.Header.Set("Origin", "https://omnara.test")
	req.Header.Set(httpauth.CSRFHeaderName, "browser-csrf-token")
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "browser-session-token"})
	rec = performRequest(secureHandler, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTPS non-host session cookie to fail auth, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = newJSONRequest(http.MethodPost, path, `{"name":"Wrong Scheme Org"}`)
	req.Host = "omnara.test"
	req.Header.Set("Origin", "http://omnara.test")
	req.Header.Set(httpauth.CSRFHeaderName, "browser-csrf-token")
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: "browser-session-token"})
	rec = performRequest(secureHandler, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf(
			"expected csrf same-origin scheme mismatch to fail, got %d body=%s",
			rec.Code,
			rec.Body.String(),
		)
	}

	req = newJSONRequest(http.MethodPost, "/api/auth/logout", `{}`)
	req.Host = "omnara.test"
	req.Header.Set("Origin", "http://omnara.test")
	req.Header.Set(httpauth.CSRFHeaderName, "browser-csrf-token")
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "browser-session-token"})
	rec = performRequest(handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"expected logout to pass, got %d body=%s",
			rec.Code,
			rec.Body.String(),
		)
	}

	req = newJSONRequest(http.MethodPost, path, `{"name":"After Logout Org"}`)
	req.Host = "omnara.test"
	req.Header.Set("Origin", "http://omnara.test")
	req.Header.Set(httpauth.CSRFHeaderName, "browser-csrf-token")
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "browser-session-token"})
	rec = performRequest(handler, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf(
			"expected revoked browser session to fail, got %d body=%s",
			rec.Code,
			rec.Body.String(),
		)
	}
}

func TestBearerAuthStorageErrorReturnsServiceUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	pool.Close()

	token, err := bearertoken.Generate(bearertoken.KindPersonalAccess)
	if err != nil {
		t.Fatalf("format valid personal access token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/invitations", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := performRequest(handler, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected closed store bearer auth to return 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "authentication unavailable") {
		t.Fatalf("expected authentication unavailable body, got %s", rec.Body.String())
	}
}

func TestPasswordAuthSignupVerifyLoginAndReset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	const (
		initialPassword = "Correct Horse Battery 1!"
		resetPassword   = "Another Correct Horse 2!"
	)

	emailSender := newCaptureAuthEmailSender()
	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://omnara.test"),
		WithAuthRateLimiter(allowAllAuthLimiter{}),
		WithEmailSender(emailSender),
	)
	store := storage.NewStore(pool)
	runKey := identitystore.HashBearerToken(t.Name() + time.Now().UTC().Format(time.RFC3339Nano))[:12]
	email := "AuthUser+" + runKey + "@Example.com"
	loginEmail := strings.ToLower(email)
	clientAddr := "client-auth-journey-" + runKey
	authReq := func(method, path, body string) *http.Request {
		req := newAuthJSONRequest(method, path, body)
		req.RemoteAddr = clientAddr
		return req
	}

	req := authReq(http.MethodPost, "/api/auth/signup", `{"email":"`+email+`"}`)
	rec := performRequest(handler, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("signup status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertAuthNoSessionCookies(t, rec)
	verification := waitAuthEmail(t, emailSender.verification)
	if verification.to != email || verification.token == "" || verification.path != "/verify-email" {
		t.Fatalf("unexpected verification email: %+v", verification)
	}

	req = authReq(
		http.MethodPost,
		"/api/auth/email/verify",
		`{"token":"`+verification.token+`","password":"all lowercase 1!","display_name":"Auth User"}`,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("weak signup password status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "one uppercase letter") {
		t.Fatalf("weak signup password body=%s, want password policy error", rec.Body.String())
	}

	req = authReq(
		http.MethodPost,
		"/api/auth/email/verify",
		`{"token":"`+verification.token+`","password":"`+initialPassword+`","display_name":"Auth User"}`,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify status=%d body=%s", rec.Code, rec.Body.String())
	}
	sessionToken := cookieValue(rec.Result().Cookies(), httpauth.BrowserSessionCookieName)
	if sessionToken == "" {
		t.Fatalf("verify did not set browser session cookies: %+v", rec.Result().Cookies())
	}
	principal, _, err := store.Identity().AuthenticateBrowserSession(ctx, sessionToken)
	if err != nil {
		t.Fatalf("authenticate verified browser session: %v", err)
	}
	if principal.Type != identitystore.PrincipalTypeUser || principal.ID == storage.NilID ||
		principal.BrowserSessionID == storage.NilID {
		t.Fatalf("unexpected verified session principal: %+v", principal)
	}
	csrfToken := cookieValue(rec.Result().Cookies(), httpauth.CSRFCookieName)
	req = authReq(http.MethodPost, "/api/v1/orgs", `{"name":"Verify Session Org"}`)
	req.Header.Set("Idempotency-Key", "verified-session-org")
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: csrfToken})
	req.Header.Set(httpauth.CSRFHeaderName, csrfToken)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("verified session org bootstrap status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = authReq(http.MethodPost, "/api/auth/logout", `{}`)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: csrfToken})
	req.Header.Set(httpauth.CSRFHeaderName, csrfToken)
	logoutRec := performRequest(handler, req)
	if logoutRec.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", logoutRec.Code, logoutRec.Body.String())
	}

	req = authReq(http.MethodPost, "/api/auth/login", `{"email":"`+loginEmail+`","password":"wrong password value"}`)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid email or password") {
		t.Fatalf("wrong password body=%s, want credential error", rec.Body.String())
	}
	req = authReq(
		http.MethodPost,
		"/api/auth/login",
		`{"email":"`+loginEmail+`","password":"`+initialPassword+`"}`,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", rec.Code, rec.Body.String())
	}
	loginSession := cookieValue(rec.Result().Cookies(), httpauth.BrowserSessionCookieName)
	loginCSRF := cookieValue(rec.Result().Cookies(), httpauth.CSRFCookieName)
	if method := cookieValue(rec.Result().Cookies(), "omnara_last_login_method"); method != "password" {
		t.Fatalf("last login method=%q want password", method)
	}
	req = authReq(http.MethodPost, "/api/v1/personal-access-tokens", `{"name":"login session pat"}`)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: loginSession})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: loginCSRF})
	req.Header.Set(httpauth.CSRFHeaderName, loginCSRF)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("login session PAT status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = authReq(http.MethodPost, "/api/auth/password/reset/request", `{"email":"missing-`+runKey+`@example.com"}`)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("unknown reset status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = authReq(http.MethodPost, "/api/auth/password/reset/request", `{"email":"`+loginEmail+`"}`)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reset request status=%d body=%s", rec.Code, rec.Body.String())
	}
	reset := waitAuthEmail(t, emailSender.reset)
	if reset.to != email || reset.token == "" || reset.path != "/reset-password" {
		t.Fatalf("unexpected reset email: %+v", reset)
	}

	req = authReq(
		http.MethodPost,
		"/api/auth/password/reset",
		`{"token":"`+reset.token+`","password":"ALL UPPERCASE 1!"}`,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("weak reset password status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "one lowercase letter") {
		t.Fatalf("weak reset password body=%s, want password policy error", rec.Body.String())
	}

	req = authReq(
		http.MethodPost,
		"/api/auth/password/reset",
		`{"token":"`+reset.token+`","password":"`+resetPassword+`"}`,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", rec.Code, rec.Body.String())
	}
	if cookieValue(rec.Result().Cookies(), httpauth.BrowserSessionCookieName) == "" {
		t.Fatalf("reset did not set a fresh session: %+v", rec.Result().Cookies())
	}
	resetSession := cookieValue(rec.Result().Cookies(), httpauth.BrowserSessionCookieName)
	resetCSRF := cookieValue(rec.Result().Cookies(), httpauth.CSRFCookieName)
	req = authReq(http.MethodPost, "/api/v1/personal-access-tokens", `{"name":"reset session pat"}`)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: resetSession})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: resetCSRF})
	req.Header.Set(httpauth.CSRFHeaderName, resetCSRF)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reset session PAT status=%d body=%s", rec.Code, rec.Body.String())
	}
	if changed := waitStringEmail(t, emailSender.changed); changed != email {
		t.Fatalf("password reset notice to=%q", changed)
	}
	req = authReq(
		http.MethodPost,
		"/api/auth/password/reset",
		`{"token":"`+reset.token+`","password":"Third Correct Horse 3!"}`,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("reused reset token status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid or expired token") {
		t.Fatalf("reused reset token body=%s, want token error", rec.Body.String())
	}
	req = authReq(
		http.MethodPost,
		"/api/auth/email/verify",
		`{"token":"missing-verify-`+runKey+`","password":"`+initialPassword+`","display_name":"Missing"}`,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing verify token status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid or expired token") {
		t.Fatalf("missing verify token body=%s, want token error", rec.Body.String())
	}
	req = authReq(
		http.MethodPost,
		"/api/auth/login",
		`{"email":"`+loginEmail+`","password":"`+initialPassword+`"}`,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old password after reset status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = authReq(
		http.MethodPost,
		"/api/auth/login",
		`{"email":"`+loginEmail+`","password":"`+resetPassword+`"}`,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("new password login status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPasswordAuthNoEnumerationResponseShapes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	emailSender := newCaptureAuthEmailSender()
	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://omnara.test"),
		WithAuthRateLimiter(allowAllAuthLimiter{}),
		WithEmailSender(emailSender),
	)
	now := time.Now().UTC()
	runKey := identitystore.HashBearerToken(t.Name() + now.Format(time.RFC3339Nano))[:12]
	email := "known-" + runKey + "@example.com"
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: email, DisplayName: "Known"})
	if err != nil {
		t.Fatalf("create verified user: %v", err)
	}
	hash, err := authn.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO user_credentials(user_id, password_hash, password_changed_at, created_at, updated_at) VALUES ($1, $2, $3, $3, $3)`,
		user.ID,
		hash,
		now,
	); err != nil {
		t.Fatalf("create credential: %v", err)
	}
	authReq := func(body string) *http.Request {
		req := newAuthJSONRequest(http.MethodPost, "/api/auth/signup", body)
		req.RemoteAddr = "enum-signup-" + runKey
		return req
	}
	signupBodies := []string{
		`{"email":"` + email + `"}`,
		`{"email":"new-` + runKey + `@example.com"}`,
		`{"email":"not an email"}`,
	}
	var signupBody string
	for i, body := range signupBodies {
		rec := performRequest(handler, authReq(body))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("signup %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
		assertAuthNoSessionCookies(t, rec)
		if i == 0 {
			signupBody = rec.Body.String()
		} else if rec.Body.String() != signupBody {
			t.Fatalf("signup %d body=%q want %q", i, rec.Body.String(), signupBody)
		}
	}
	if got := waitStringEmail(t, emailSender.account); got != email {
		t.Fatalf("existing-account signup notice to=%q want %q", got, email)
	}
	if got := waitAuthEmail(t, emailSender.verification); got.to != "new-"+runKey+"@example.com" {
		t.Fatalf("new-account verification to=%q", got.to)
	}
	assertNoAuthEmail(t, emailSender.verification)
	assertNoStringEmail(t, emailSender.account)

	loginBodies := []string{
		`{"email":"` + email + `","password":"wrong horse battery staple"}`,
		`{"email":"missing-` + runKey + `@example.com","password":"wrong horse battery staple"}`,
		`{"email":"not an email","password":"wrong horse battery staple"}`,
	}
	var loginBody string
	for i, body := range loginBodies {
		req := newAuthJSONRequest(http.MethodPost, "/api/auth/login", body)
		req.RemoteAddr = "enum-login-" + runKey
		rec := performRequest(handler, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("login %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
		assertAuthNoSessionCookies(t, rec)
		if i == 0 {
			loginBody = rec.Body.String()
		} else if rec.Body.String() != loginBody {
			t.Fatalf("login %d body=%q want %q", i, rec.Body.String(), loginBody)
		}
	}

	resetBodies := []string{
		`{"email":"` + email + `"}`,
		`{"email":"missing-reset-` + runKey + `@example.com"}`,
		`{"email":"not an email"}`,
	}
	var resetBody string
	for i, body := range resetBodies {
		req := newAuthJSONRequest(http.MethodPost, "/api/auth/password/reset/request", body)
		req.RemoteAddr = "enum-reset-" + runKey
		rec := performRequest(handler, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("reset %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
		assertAuthNoSessionCookies(t, rec)
		if i == 0 {
			resetBody = rec.Body.String()
		} else if rec.Body.String() != resetBody {
			t.Fatalf("reset %d body=%q want %q", i, rec.Body.String(), resetBody)
		}
	}
	if got := waitAuthEmail(t, emailSender.reset); got.to != email {
		t.Fatalf("known-account reset to=%q want %q", got.to, email)
	}
	assertNoAuthEmail(t, emailSender.reset)
}

func TestPasswordAuthChangeAndRevokeAllRequireSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	const (
		currentPassword = "Correct Horse Battery 1!"
		newPassword     = "New Correct Horse 2!"
	)

	emailSender := newCaptureAuthEmailSender()
	redisClient := integrationredis.OpenClient(t)
	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://omnara.test"),
		WithRedisBackedAuth(redisClient),
		WithEmailSender(emailSender),
	)
	store := storage.NewStore(pool)

	req := newAuthJSONRequest(
		http.MethodPost,
		"/api/auth/password/change",
		`{"current_password":"old","new_password":"new password phrase"}`,
	)
	rec := performRequest(handler, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated password change status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = newAuthJSONRequest(http.MethodPost, "/api/auth/security/revoke-all", `{"current_password":"old"}`)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated revoke-all status=%d body=%s", rec.Code, rec.Body.String())
	}

	start, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: "change@example.com"},
	)
	if err != nil {
		t.Fatalf("start signup: %v", err)
	}
	oldHash, err := authn.HashPassword(currentPassword)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	completed, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{
			Token:        start.Token,
			PasswordHash: oldHash,
			DisplayName:  "Change User",
		},
	)
	if err != nil || !completed.Verified {
		t.Fatalf("complete signup: record=%+v err=%v", completed, err)
	}
	sessionToken, csrfToken, err := sCreateBrowserSessionForTest(ctx, store, completed.User.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	req = newAuthJSONRequest(
		http.MethodPost,
		"/api/auth/password/change",
		`{"current_password":"`+currentPassword+`","new_password":"`+newPassword+`"}`,
	)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: csrfToken})
	rec = performRequest(handler, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("password change without csrf status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = newAuthJSONRequest(
		http.MethodPost,
		"/api/auth/password/change",
		`{"current_password":"`+currentPassword+`","new_password":"`+newPassword+`"}`,
	)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: csrfToken})
	req.Header.Set(httpauth.CSRFHeaderName, "wrong-csrf")
	rec = performRequest(handler, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("password change wrong csrf status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = newAuthJSONRequest(
		http.MethodPost,
		"/api/auth/password/change",
		`{"current_password":"`+currentPassword+`","new_password":"no uppercase 1!"}`,
	)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: csrfToken})
	req.Header.Set(httpauth.CSRFHeaderName, csrfToken)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("weak changed password status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "one uppercase letter") {
		t.Fatalf("weak changed password body=%s, want password policy error", rec.Body.String())
	}

	req = newAuthJSONRequest(
		http.MethodPost,
		"/api/auth/password/change",
		`{"current_password":"`+currentPassword+`","new_password":"`+newPassword+`"}`,
	)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: csrfToken})
	req.Header.Set(httpauth.CSRFHeaderName, csrfToken)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("password change status=%d body=%s", rec.Code, rec.Body.String())
	}
	waitStringEmail(t, emailSender.changed)
	if _, _, err := store.Identity().AuthenticateBrowserSession(
		ctx,
		sessionToken,
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("old password-change session auth error=%v, want unauthorized", err)
	}
	freshSession := cookieValue(rec.Result().Cookies(), httpauth.BrowserSessionCookieName)
	freshCSRF := cookieValue(rec.Result().Cookies(), httpauth.CSRFCookieName)
	if freshSession == "" || freshCSRF == "" {
		t.Fatalf("password change did not rotate session cookies: %+v", rec.Result().Cookies())
	}

	pat, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: completed.User.ID,
			Name:   "compromise",
		},
	)
	if err != nil {
		t.Fatalf("create pat: %v", err)
	}
	req = newAuthJSONRequest(
		http.MethodPost,
		"/api/auth/password/change",
		`{"current_password":"`+newPassword+`","new_password":"Pat Should Not Work 3!"}`,
	)
	req.Header.Set("Authorization", "Bearer "+pat.Token)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PAT password change status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = newAuthJSONRequest(
		http.MethodPost,
		"/api/auth/security/revoke-all",
		`{"current_password":"`+newPassword+`"}`,
	)
	req.Header.Set("Authorization", "Bearer "+pat.Token)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PAT revoke-all status=%d body=%s", rec.Code, rec.Body.String())
	}
	orgID := httpTestID("auth-revoke-all-org")
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at)
		 VALUES ($1, 'Auth Revoke Org', 'auth-revoke-org', now(), now())`,
		orgID,
	); err != nil {
		t.Fatalf("create revoke-all org: %v", err)
	}
	machine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          orgID,
			DisplayName:    "Auth Revoke Machine",
			IdempotencyKey: "auth-revoke-machine",
		},
	)
	if err != nil {
		t.Fatalf("create revoke-all machine: %v", err)
	}
	daemonToken, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     orgID,
			MachineID: machine.ID,
			Name:      "revoke-all",
		},
	)
	if err != nil {
		t.Fatalf("create revoke-all machine token: %v", err)
	}
	req = newAuthJSONRequest(
		http.MethodPost,
		"/api/auth/security/revoke-all",
		`{"current_password":"`+newPassword+`"}`,
	)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: freshSession})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: freshCSRF})
	rec = performRequest(handler, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("revoke-all without csrf status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = newAuthJSONRequest(
		http.MethodPost,
		"/api/auth/security/revoke-all",
		`{"current_password":"`+newPassword+`"}`,
	)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: freshSession})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: freshCSRF})
	req.Header.Set(httpauth.CSRFHeaderName, "wrong-csrf")
	rec = performRequest(handler, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("revoke-all wrong csrf status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = newAuthJSONRequest(http.MethodPost, "/api/auth/security/revoke-all", `{}`)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: freshSession})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: freshCSRF})
	req.Header.Set(httpauth.CSRFHeaderName, freshCSRF)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("password-backed revoke-all without password status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = newAuthJSONRequest(
		http.MethodPost,
		"/api/auth/security/revoke-all",
		`{"current_password":"`+newPassword+`"}`,
	)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: freshSession})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: freshCSRF})
	req.Header.Set(httpauth.CSRFHeaderName, freshCSRF)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke-all status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := store.Identity().AuthenticatePersonalAccessToken(
		ctx,
		pat.Token,
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("pat after revoke-all error=%v, want unauthorized", err)
	}
	if _, err := store.Execution().AuthenticateMachineDaemonToken(
		ctx,
		daemonToken.Token,
	); err != nil {
		t.Fatalf("machine daemon token after revoke-all: %v", err)
	}
	if _, _, err := store.Identity().AuthenticateBrowserSession(
		ctx,
		freshSession,
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("session after revoke-all error=%v, want unauthorized", err)
	}
}

func TestPasswordlessUserCanRevokeAllAuthTokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool, WithPublicURL("http://omnara.test"), WithAuthRateLimiter(allowAllAuthLimiter{}))
	store := integrationStoreForHandler(t, handler)
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email:       "passwordless-revoke@example.com",
		DisplayName: "Passwordless Revoke",
	},
	)
	if err != nil {
		t.Fatalf("create passwordless user: %v", err)
	}
	sessionToken, csrfToken, err := sCreateBrowserSessionForTest(ctx, store, user.ID)
	if err != nil {
		t.Fatalf("create passwordless session: %v", err)
	}
	pat, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: user.ID,
			Name:   "passwordless compromise",
		},
	)
	if err != nil {
		t.Fatalf("create passwordless PAT: %v", err)
	}
	orgID := httpTestID("passwordless-revoke-org")
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at)
		 VALUES ($1, 'Passwordless Revoke Org', 'passwordless-revoke-org', now(), now())`,
		orgID,
	); err != nil {
		t.Fatalf("create passwordless revoke org: %v", err)
	}
	machine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          orgID,
			DisplayName:    "Passwordless Revoke Machine",
			IdempotencyKey: "passwordless-revoke-machine",
		},
	)
	if err != nil {
		t.Fatalf("create passwordless revoke machine: %v", err)
	}
	daemonToken, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     orgID,
			MachineID: machine.ID,
			Name:      "passwordless revoke",
		},
	)
	if err != nil {
		t.Fatalf("create passwordless daemon token: %v", err)
	}

	req := newAuthJSONRequest(http.MethodPost, "/api/auth/security/revoke-all", `{}`)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: csrfToken})
	req.Header.Set(httpauth.CSRFHeaderName, csrfToken)
	rec := performRequest(handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("passwordless revoke-all status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := store.Identity().AuthenticatePersonalAccessToken(
		ctx,
		pat.Token,
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("passwordless PAT after revoke-all error=%v, want unauthorized", err)
	}
	if _, err := store.Execution().AuthenticateMachineDaemonToken(
		ctx,
		daemonToken.Token,
	); err != nil {
		t.Fatalf("passwordless daemon token after revoke-all: %v", err)
	}
	if _, _, err := store.Identity().AuthenticateBrowserSession(
		ctx,
		sessionToken,
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("passwordless session after revoke-all error=%v, want unauthorized", err)
	}
}

func TestPasswordAuthOriginAndLimiterGuards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	emailSender := newCaptureAuthEmailSender()
	runKey := identitystore.HashBearerToken(t.Name() + time.Now().UTC().Format(time.RFC3339Nano))[:12]
	email := "limited+" + runKey + "@example.com"
	clientAddr := "client-" + runKey
	otherClientAddr := "client-other-" + runKey
	noLimiter := newIntegrationServer(pool, WithPublicURL("http://omnara.test"), WithEmailSender(emailSender))
	req := newAuthJSONRequest(http.MethodPost, "/api/auth/signup", `{"email":"`+email+`"}`)
	req.RemoteAddr = clientAddr
	rec := performRequest(noLimiter, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing limiter status=%d body=%s", rec.Code, rec.Body.String())
	}

	redisClient := integrationredis.OpenClient(t)
	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://omnara.test"),
		WithRedisBackedAuth(redisClient),
		WithEmailSender(emailSender),
	)
	req = newJSONRequest(http.MethodPost, "http://omnara.test/api/auth/signup", `{"email":"`+email+`"}`)
	req.RemoteAddr = clientAddr
	req.Header.Set("Origin", "http://evil.example")
	rec = performRequest(handler, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin signup status=%d body=%s", rec.Code, rec.Body.String())
	}
	for i := 0; i < httpauth.SignupLimit; i++ {
		req = newAuthJSONRequest(http.MethodPost, "/api/auth/signup", `{"email":"`+email+`"}`)
		req.RemoteAddr = clientAddr
		rec = performRequest(handler, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("signup %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
	}
	req = newAuthJSONRequest(http.MethodPost, "/api/auth/signup", `{"email":"`+email+`"}`)
	req.RemoteAddr = clientAddr
	rec = performRequest(handler, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited signup status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = newAuthJSONRequest(http.MethodPost, "/api/auth/signup", `{"email":"`+email+`"}`)
	req.RemoteAddr = otherClientAddr
	rec = performRequest(handler, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("subject-wide signup limit status=%d body=%s", rec.Code, rec.Body.String())
	}
	for i := 0; i < httpauth.SignupClientLimit-httpauth.SignupLimit; i++ {
		req = newAuthJSONRequest(
			http.MethodPost,
			"/api/auth/signup",
			fmt.Sprintf(`{"email":"limited-client-%d-%s@example.com"}`, i, runKey),
		)
		req.RemoteAddr = clientAddr
		rec = performRequest(handler, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("client-wide setup signup %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
	}
	req = newAuthJSONRequest(
		http.MethodPost,
		"/api/auth/signup",
		`{"email":"limited-client-final-`+runKey+`@example.com"}`,
	)
	req.RemoteAddr = clientAddr
	rec = performRequest(handler, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("client-wide signup limit status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPasswordAuthRateLimitsPublicCredentialSurfaces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	emailSender := newCaptureAuthEmailSender()
	redisClient := integrationredis.OpenClient(t)
	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://omnara.test"),
		WithRedisBackedAuth(redisClient),
		WithEmailSender(emailSender),
	)
	runKey := identitystore.HashBearerToken(t.Name() + time.Now().UTC().Format(time.RFC3339Nano))[:12]

	cases := []struct {
		name       string
		path       string
		body       string
		limit      int
		wantBefore int
	}{
		{
			name:       "login",
			path:       "/api/auth/login",
			body:       `{"email":"missing-login-` + runKey + `@example.com","password":"wrong horse battery staple"}`,
			limit:      httpauth.LoginLimit,
			wantBefore: http.StatusUnauthorized,
		},
		{
			name:       "reset_request",
			path:       "/api/auth/password/reset/request",
			body:       `{"email":"missing-reset-limit-` + runKey + `@example.com"}`,
			limit:      httpauth.ResetLimit,
			wantBefore: http.StatusAccepted,
		},
		{
			name:       "email_verify_token",
			path:       "/api/auth/email/verify",
			body:       `{"token":"verify-limit-` + runKey + `","password":"correct horse battery staple"}`,
			limit:      httpauth.TokenConsumeLimit,
			wantBefore: http.StatusUnauthorized,
		},
		{
			name:       "reset_token",
			path:       "/api/auth/password/reset",
			body:       `{"token":"reset-limit-` + runKey + `","password":"correct horse battery staple"}`,
			limit:      httpauth.TokenConsumeLimit,
			wantBefore: http.StatusUnauthorized,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < tc.limit; i++ {
				req := newAuthJSONRequest(http.MethodPost, tc.path, tc.body)
				req.RemoteAddr = "rate-" + tc.name + "-" + runKey
				rec := performRequest(handler, req)
				if rec.Code != tc.wantBefore {
					t.Fatalf("%s request %d status=%d want=%d body=%s", tc.name, i, rec.Code, tc.wantBefore, rec.Body.String())
				}
			}
			req := newAuthJSONRequest(http.MethodPost, tc.path, tc.body)
			req.RemoteAddr = "rate-" + tc.name + "-" + runKey
			rec := performRequest(handler, req)
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("%s rate limit status=%d body=%s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDeviceAuthFlowApprovesBrowserSessionAndMintsPAT(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://app.omnara.test"),
		WithAuthRateLimiter(allowAllAuthLimiter{}),
	)
	store := integrationStoreForHandler(t, handler)
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "device-http@example.com", DisplayName: "Device HTTP"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	session, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    user.ID,
			Token:     "device-http-session",
			CSRFToken: "device-http-csrf",
			TTL:       time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}

	req := newJSONRequest(
		http.MethodPost,
		"http://app.omnara.test/api/auth/device/code",
		`{"client_name":"CLI\u202eApp","token_name":"CLI token"}`,
	)
	rec := performRequest(handler, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "client_name contains an unsupported") {
		t.Fatalf("invalid device client name status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = newJSONRequest(
		http.MethodPost,
		"http://app.omnara.test/api/auth/device/code",
		`{"client_name":"CLI","token_name":" CLI token "}`,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "token_name must not start or end with whitespace") {
		t.Fatalf("invalid device token name status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = newJSONRequest(
		http.MethodPost,
		"http://app.omnara.test/api/auth/device/code",
		`{"client_name":"CLI","token_name":"CLI token"}`,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("device code status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertAuthNoSessionCookies(t, rec)
	var started struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		Interval                int    `json:"interval"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode device code response: %v", err)
	}
	if started.DeviceCode == "" || started.UserCode == "" || started.VerificationURI != "http://app.omnara.test/device" ||
		!strings.Contains(started.VerificationURIComplete, url.QueryEscape(started.UserCode)) ||
		started.Interval <= 0 {
		t.Fatalf("device code response = %+v", started)
	}

	pat, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: user.ID,
			Name:   "device pending PAT",
		},
	)
	if err != nil {
		t.Fatalf("create pending PAT: %v", err)
	}
	req = httptest.NewRequest(
		http.MethodGet,
		"http://app.omnara.test/api/auth/device/pending?user_code="+url.QueryEscape(started.UserCode),
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+pat.Token)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("pending with PAT status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(
		http.MethodGet,
		"http://app.omnara.test/api/auth/device/pending?user_code="+url.QueryEscape(started.UserCode),
		nil,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("pending without session status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(
		http.MethodGet,
		"http://app.omnara.test/api/auth/device/pending?user_code="+url.QueryEscape(started.UserCode),
		nil,
	)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "device-http-session"})
	rec = performRequest(handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pending with session status=%d body=%s", rec.Code, rec.Body.String())
	}
	var pendingFlow struct {
		ClientName string    `json:"client_name"`
		TokenName  string    `json:"token_name"`
		CreatedAt  time.Time `json:"created_at"`
		ExpiresAt  time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pendingFlow); err != nil {
		t.Fatalf("decode pending flow: %v", err)
	}
	if pendingFlow.ClientName != "CLI" || pendingFlow.TokenName != "CLI token" || pendingFlow.CreatedAt.IsZero() ||
		pendingFlow.ExpiresAt.IsZero() {
		t.Fatalf("pending flow = %+v", pendingFlow)
	}
	req = httptest.NewRequest(http.MethodGet, "http://app.omnara.test/api/auth/device/pending?user_code=missing", nil)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "device-http-session"})
	rec = performRequest(handler, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid or expired device code") {
		t.Fatalf("pending invalid code status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = newJSONRequest(
		http.MethodPost,
		"http://app.omnara.test/api/auth/device/token",
		`{"device_code":"`+started.DeviceCode+`"}`,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusAccepted ||
		!strings.Contains(rec.Body.String(), string(identitystore.DeviceAuthFlowStatusPending)) {
		t.Fatalf("pending device token status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertAuthNoSessionCookies(t, rec)

	req = newJSONRequest(
		http.MethodPost,
		"http://app.omnara.test/api/auth/device/approve",
		`{"user_code":"`+started.UserCode+`"}`,
	)
	req.Header.Set("Origin", "http://app.omnara.test")
	rec = performRequest(handler, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("approve without session status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(
		http.MethodPost,
		"http://app.omnara.test/api/auth/device/approve",
		strings.NewReader(`{"user_code":"`+started.UserCode+`"}`),
	)
	req.Header.Set("Origin", "http://app.omnara.test")
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "device-http-session"})
	req.Header.Set(httpauth.CSRFHeaderName, "device-http-csrf")
	rec = performRequest(handler, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("approve without json content type status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(
		http.MethodPost,
		"http://app.omnara.test/api/auth/device/deny",
		strings.NewReader(`{"user_code":"`+started.UserCode+`"}`),
	)
	req.Header.Set("Origin", "http://app.omnara.test")
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "device-http-session"})
	req.Header.Set(httpauth.CSRFHeaderName, "device-http-csrf")
	rec = performRequest(handler, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("deny without json content type status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = newJSONRequest(
		http.MethodPost,
		"http://app.omnara.test/api/auth/device/approve",
		`{"user_code":"`+started.UserCode+`"}`,
	)
	req.Header.Set("Origin", "http://app.omnara.test")
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "device-http-session"})
	req.Header.Set(httpauth.CSRFHeaderName, "device-http-csrf")
	rec = performRequest(handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s session=%s", rec.Code, rec.Body.String(), session.ID)
	}
	req = httptest.NewRequest(
		http.MethodGet,
		"http://app.omnara.test/api/auth/device/pending?user_code="+url.QueryEscape(started.UserCode),
		nil,
	)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "device-http-session"})
	rec = performRequest(handler, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid or expired device code") {
		t.Fatalf("pending approved flow status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = newJSONRequest(
		http.MethodPost,
		"http://app.omnara.test/api/auth/device/token",
		`{"device_code":"`+started.DeviceCode+`"}`,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approved device token status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertAuthNoSessionCookies(t, rec)
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tokenResponse); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if err := bearertoken.Validate(tokenResponse.AccessToken, bearertoken.KindPersonalAccess); err != nil ||
		tokenResponse.TokenType != "Bearer" {
		t.Fatalf("token response = %+v", tokenResponse)
	}
	principal, err := store.Identity().AuthenticatePersonalAccessToken(ctx, tokenResponse.AccessToken)
	if err != nil {
		t.Fatalf("authenticate device PAT: %v", err)
	}
	if principal.Type != identitystore.PrincipalTypeUser || principal.ID != user.ID ||
		principal.PersonalAccessTokenID == storage.NilID {
		t.Fatalf("device PAT principal = %+v", principal)
	}
	req = newJSONRequest(
		http.MethodPost,
		"http://app.omnara.test/api/auth/device/token",
		`{"device_code":"`+started.DeviceCode+`"}`,
	)
	rec = performRequest(handler, req)
	if rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), string(identitystore.DeviceAuthFlowStatusExpired)) {
		t.Fatalf("replayed device token status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeviceAuthTokenPollingAllowsAdvertisedIntervalBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	redisClient := integrationredis.OpenClient(t)
	handler := newIntegrationServer(pool, WithPublicURL("http://omnara.test"), WithRedisBackedAuth(redisClient))
	runKey := identitystore.HashBearerToken(t.Name() + time.Now().UTC().Format(time.RFC3339Nano))[:12]

	req := newJSONRequest(http.MethodPost, "/api/auth/device/code", `{"client_name":"CLI","token_name":"CLI token"}`)
	req.RemoteAddr = "device-poll-start-" + runKey
	rec := performRequest(handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("device code status=%d body=%s", rec.Code, rec.Body.String())
	}
	var started struct {
		DeviceCode string `json:"device_code"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode device code response: %v", err)
	}
	if started.DeviceCode == "" || started.ExpiresIn <= 0 || started.Interval <= 0 {
		t.Fatalf("device code response = %+v", started)
	}

	polls := started.ExpiresIn / started.Interval
	for i := 0; i < polls; i++ {
		req = newJSONRequest(http.MethodPost, "/api/auth/device/token", `{"device_code":"`+started.DeviceCode+`"}`)
		req.RemoteAddr = "device-poll-client-" + runKey
		rec = performRequest(handler, req)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("device token poll %d was rate-limited body=%s", i, rec.Body.String())
		}
		if rec.Code != http.StatusAccepted {
			t.Fatalf("device token poll %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
	}
}

func TestDeviceAuthApprovalRateLimitsUserCodeGuesses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	redisClient := integrationredis.OpenClient(t)
	handler := newIntegrationServer(pool, WithPublicURL("http://omnara.test"), WithRedisBackedAuth(redisClient))
	store := integrationStoreForHandler(t, handler)
	now := time.Now().UTC()
	runKey := identitystore.HashBearerToken(t.Name() + now.Format(time.RFC3339Nano))[:12]
	guessCode := strings.ToUpper(runKey[:5] + "-" + runKey[5:10])
	clientBucket := "device-rate-client-" + runKey
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "device-rate@example.com", DisplayName: "Device Rate"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    user.ID,
			Token:     "device-rate-session",
			CSRFToken: "device-rate-csrf",
			TTL:       time.Hour,
		},
	); err != nil {
		t.Fatalf("create browser session: %v", err)
	}

	for i := 0; i < httpauth.TokenConsumeLimit; i++ {
		req := newAuthJSONRequest(http.MethodPost, "/api/auth/device/approve", `{"user_code":"`+guessCode+`"}`)
		req.RemoteAddr = clientBucket
		req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "device-rate-session"})
		req.Header.Set(httpauth.CSRFHeaderName, "device-rate-csrf")
		rec := performRequest(handler, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("device approval guess %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
	}
	req := newAuthJSONRequest(http.MethodPost, "/api/auth/device/approve", `{"user_code":"`+guessCode+`"}`)
	req.RemoteAddr = clientBucket
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: "device-rate-session"})
	req.Header.Set(httpauth.CSRFHeaderName, "device-rate-csrf")
	rec := performRequest(handler, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited device approval status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthConnectorsRouteListsEnabledConnectorsWithoutDecryptingSecrets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool, WithPublicURL("http://omnara.test"))
	store := integrationStoreForHandler(t, handler)
	github, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:             "github",
		Kind:             identitystore.AuthConnectorKindGitHub,
		DisplayName:      "GitHub",
		Issuer:           "https://github.com",
		AuthorizationURL: "https://github.com/login/oauth/authorize",
		TokenURL:         "https://github.com/login/oauth/access_token",
		UserinfoURL:      "https://api.github.com/user",
		ClientID:         "github-client",
		ClientSecret:     "github-secret",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("create github connector: %v", err)
	}
	disabled, err := store.Identity().UpsertAuthConnector(
		ctx,
		identitystore.CreateAuthConnectorInput{
			Slug:         "disabled-sso",
			Kind:         identitystore.AuthConnectorKindOIDC,
			DisplayName:  "Disabled SSO",
			Issuer:       "https://disabled.example.com",
			ClientID:     "disabled-client",
			ClientSecret: "disabled-secret",
			Enabled:      false,
		},
	)
	if err != nil {
		t.Fatalf("create disabled connector: %v", err)
	}
	for _, connectorID := range []storage.ID{github.ID, disabled.ID} {
		if _, err := pool.Exec(
			ctx,
			`UPDATE auth_connectors SET encrypted_client_secret = '{}'::jsonb WHERE id = $1`,
			connectorID,
		); err != nil {
			t.Fatalf("corrupt connector secret envelope: %v", err)
		}
	}

	rec := performRequest(handler, httptest.NewRequest(http.MethodGet, "http://omnara.test/api/auth/connectors", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("connectors status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "client_id") {
		t.Fatalf("connector response leaked private data: %s", rec.Body.String())
	}
	var response struct {
		Connectors []struct {
			Slug        string `json:"slug"`
			Kind        string `json:"kind"`
			DisplayName string `json:"display_name"`
			LoginURL    string `json:"login_url"`
		} `json:"connectors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode connectors response: %v body=%s", err, rec.Body.String())
	}
	if len(response.Connectors) != 1 {
		t.Fatalf("connectors = %+v, want one enabled connector", response.Connectors)
	}
	if response.Connectors[0].Slug != "github" || response.Connectors[0].Kind != identitystore.AuthConnectorKindGitHub ||
		response.Connectors[0].DisplayName != "GitHub" ||
		response.Connectors[0].LoginURL != "/api/auth/connectors/github/login" {
		t.Fatalf("connector response = %+v", response.Connectors[0])
	}
	rec = performRequest(
		handler,
		httptest.NewRequest(http.MethodGet, "http://omnara.test/api/auth/connectors/disabled-sso/login", nil),
	)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled connector login status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOAuthLoginGitHubConnectorMintsBrowserSessionAndRejectsReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	var gotCodeVerifier string
	var gotAuthorizationState string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/authorize":
			if r.URL.Query().Get("code_challenge") == "" || r.URL.Query().Get("code_challenge_method") != "S256" {
				t.Fatalf("authorize query missing PKCE: %s", r.URL.RawQuery)
			}
			gotAuthorizationState = r.URL.Query().Get("state")
			http.Redirect(
				w,
				r,
				r.URL.Query().Get("redirect_uri")+"?code=oauth-code&state="+url.QueryEscape(gotAuthorizationState),
				http.StatusFound,
			)
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			gotCodeVerifier = r.Form.Get("code_verifier")
			if gotCodeVerifier == "" || r.Form.Get("code") != "oauth-code" {
				t.Fatalf("token form = %v", r.Form)
			}
			writeJSON(w, http.StatusOK, map[string]any{"access_token": "provider-access-token", "token_type": "bearer"})
		case "/user":
			if r.Header.Get("Authorization") != "Bearer provider-access-token" {
				t.Fatalf("user authorization header = %q", r.Header.Get("Authorization"))
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": 12345, "login": "octo", "name": "Octo User"})
		case "/user/emails":
			writeJSON(w, http.StatusOK, []map[string]any{{"email": "octo@example.com", "primary": true, "verified": true}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://omnara.test"),
		WithAuthHTTPClient(provider.Client()),
		withRedisOAuthStateAndAllowAllLimiter(t),
	)
	store := integrationStoreForHandler(t, handler)
	connector, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:             "github-test",
		Kind:             identitystore.AuthConnectorKindGitHub,
		DisplayName:      "GitHub Test",
		Issuer:           provider.URL,
		AuthorizationURL: provider.URL + "/api/authorize",
		TokenURL:         provider.URL + "/token",
		UserinfoURL:      provider.URL + "/user",
		ClientID:         "client-id",
		ClientSecret:     "client-secret",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("create auth connector: %v", err)
	}

	cancelReq := httptest.NewRequest(
		http.MethodGet,
		"http://omnara.test/api/auth/connectors/github-test/login?return_to=/device%3Fuser_code%3DABCD",
		nil,
	)
	cancelRec := performRequest(handler, cancelReq)
	if cancelRec.Code != http.StatusFound {
		t.Fatalf("cancel login redirect status=%d body=%s", cancelRec.Code, cancelRec.Body.String())
	}
	authorizationURL, err := url.Parse(cancelRec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse cancel authorization URL: %v", err)
	}
	if got := authorizationURL.Query().Get("prompt"); got != "select_account" {
		t.Fatalf("authorization prompt = %q, want select_account", got)
	}
	cancelState := authorizationURL.Query().Get("state")
	if cancelState == "" {
		t.Fatal("cancel authorization URL missing state")
	}
	cancelFlowCookies := cancelRec.Result().Cookies()
	invalidCancelReq := httptest.NewRequest(
		http.MethodGet,
		"http://omnara.test/api/auth/connectors/github-test/callback?error=access_denied&state=bogus",
		nil,
	)
	for _, cookie := range cancelFlowCookies {
		invalidCancelReq.AddCookie(cookie)
	}
	invalidCancelRec := performRequest(handler, invalidCancelReq)
	if invalidCancelRec.Code != http.StatusFound ||
		invalidCancelRec.Header().Get("Location") != "/login?auth_error=invalid_state" {
		t.Fatalf(
			"invalid cancel callback status=%d location=%q body=%s",
			invalidCancelRec.Code,
			invalidCancelRec.Header().Get("Location"),
			invalidCancelRec.Body.String(),
		)
	}
	if setCookies := invalidCancelRec.Header().Values("Set-Cookie"); len(setCookies) != 0 {
		t.Fatalf("invalid cancel callback set cookies: %v", setCookies)
	}

	cancelCallbackURL := "http://omnara.test/api/auth/connectors/github-test/callback?error=access_denied&state=" +
		url.QueryEscape(cancelState)
	cancelCallbackReq := httptest.NewRequest(
		http.MethodGet,
		cancelCallbackURL,
		nil,
	)
	for _, cookie := range cancelFlowCookies {
		cancelCallbackReq.AddCookie(cookie)
	}
	cancelRec = performRequest(handler, cancelCallbackReq)
	if cancelRec.Code != http.StatusFound ||
		cancelRec.Header().Get("Location") !=
			"/login?auth_error=access_denied&return_to=%2Fdevice%3Fuser_code%3DABCD" {
		t.Fatalf(
			"cancel callback status=%d location=%q body=%s",
			cancelRec.Code,
			cancelRec.Header().Get("Location"),
			cancelRec.Body.String(),
		)
	}
	clearedFlowCookies := map[string]bool{}
	for _, cookie := range cancelRec.Result().Cookies() {
		if cookie.MaxAge < 0 {
			clearedFlowCookies[cookie.Name] = true
		}
	}
	if !clearedFlowCookies["omnara_oauth_flow"] || !clearedFlowCookies["__Host-omnara_oauth_flow"] {
		t.Fatalf("cancel callback did not clear OAuth flow cookies: %+v", cancelRec.Result().Cookies())
	}
	cancelReplayReq := httptest.NewRequest(http.MethodGet, cancelCallbackURL, nil)
	for _, cookie := range cancelFlowCookies {
		cancelReplayReq.AddCookie(cookie)
	}
	cancelReplayRec := performRequest(handler, cancelReplayReq)
	if cancelReplayRec.Code != http.StatusFound ||
		cancelReplayRec.Header().Get("Location") != "/login?auth_error=invalid_state" {
		t.Fatalf(
			"cancel replay status=%d location=%q body=%s",
			cancelReplayRec.Code,
			cancelReplayRec.Header().Get("Location"),
			cancelReplayRec.Body.String(),
		)
	}

	req := httptest.NewRequest(
		http.MethodGet,
		"http://omnara.test/api/auth/connectors/github-test/login?return_to=/after",
		nil,
	)
	rec := performRequest(handler, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("login redirect status=%d body=%s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, provider.URL+"/api/authorize?") {
		t.Fatalf("login redirect location = %q", location)
	}
	flowCookies := rec.Result().Cookies()
	if len(flowCookies) == 0 {
		t.Fatal("login redirect did not set oauth flow cookie")
	}
	providerRec := httptest.NewRecorder()
	provider.Config.Handler.ServeHTTP(providerRec, httptest.NewRequest(http.MethodGet, location, nil))
	callback := providerRec.Header().Get("Location")
	if callback == "" {
		t.Fatalf("provider callback location missing")
	}
	missingCookie := performRequest(handler, httptest.NewRequest(http.MethodGet, callback, nil))
	if missingCookie.Code != http.StatusFound ||
		!strings.Contains(missingCookie.Header().Get("Location"), "auth_error=invalid_state") {
		t.Fatalf(
			"callback without flow cookie status=%d location=%q body=%s",
			missingCookie.Code,
			missingCookie.Header().Get("Location"),
			missingCookie.Body.String(),
		)
	}
	callbackReq := httptest.NewRequest(http.MethodGet, callback, nil)
	for _, cookie := range flowCookies {
		callbackReq.AddCookie(cookie)
	}
	rec = performRequest(handler, callbackReq)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/after" {
		t.Fatalf("callback status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if gotCodeVerifier == "" {
		t.Fatal("token exchange did not send code verifier")
	}
	sessionToken := cookieValue(rec.Result().Cookies(), httpauth.BrowserSessionCookieName)
	if sessionToken == "" {
		t.Fatalf("oauth callback did not set browser session cookies: %+v", rec.Result().Cookies())
	}
	if method := cookieValue(rec.Result().Cookies(), "omnara_last_login_method"); method != "connector:github-test" {
		t.Fatalf("last login method=%q want connector:github-test", method)
	}
	principal, _, err := store.Identity().AuthenticateBrowserSession(ctx, sessionToken)
	if err != nil {
		t.Fatalf("authenticate oauth browser session: %v", err)
	}
	if principal.Type != identitystore.PrincipalTypeUser || principal.BrowserSessionID == storage.NilID {
		t.Fatalf("oauth principal = %+v", principal)
	}
	var linkedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_auth_identities WHERE auth_connector_id = $1 AND subject = $2`, connector.ID, strconv.FormatInt(12345, 10)).
		Scan(&linkedCount); err != nil {
		t.Fatalf("count linked identity: %v", err)
	}
	if linkedCount != 1 {
		t.Fatalf("linked identity count = %d, want 1", linkedCount)
	}
	replay := performRequest(handler, httptest.NewRequest(http.MethodGet, callback, nil))
	if replay.Code != http.StatusFound || !strings.Contains(replay.Header().Get("Location"), "auth_error=invalid_state") {
		t.Fatalf(
			"replay callback status=%d location=%q body=%s",
			replay.Code,
			replay.Header().Get("Location"),
			replay.Body.String(),
		)
	}
}

func TestOAuthLoginGitHubRequiresPrimaryVerifiedEmail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/authorize":
			http.Redirect(
				w,
				r,
				r.URL.Query().Get("redirect_uri")+"?code=oauth-code&state="+url.QueryEscape(r.URL.Query().Get("state")),
				http.StatusFound,
			)
		case "/token":
			writeJSON(w, http.StatusOK, map[string]any{"access_token": "provider-access-token", "token_type": "bearer"})
		case "/user":
			writeJSON(
				w,
				http.StatusOK,
				map[string]any{"id": 12345, "login": "octo", "name": "Octo User", "email": "public@example.com"},
			)
		case "/user/emails":
			writeJSON(
				w,
				http.StatusOK,
				[]map[string]any{
					{"email": "secondary@example.com", "primary": false, "verified": true},
					{"email": "primary@example.com", "primary": true, "verified": false},
				},
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://omnara.test"),
		WithAuthHTTPClient(provider.Client()),
		withRedisOAuthStateAndAllowAllLimiter(t),
	)
	store := integrationStoreForHandler(t, handler)
	connector, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:             "github-primary-required",
		Kind:             identitystore.AuthConnectorKindGitHub,
		DisplayName:      "GitHub Primary Required",
		Issuer:           provider.URL,
		AuthorizationURL: provider.URL + "/api/authorize",
		TokenURL:         provider.URL + "/token",
		UserinfoURL:      provider.URL + "/user",
		ClientID:         "client-id",
		ClientSecret:     "client-secret",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("create auth connector: %v", err)
	}

	rec := performRequest(
		handler,
		httptest.NewRequest(
			http.MethodGet,
			"http://omnara.test/api/auth/connectors/github-primary-required/login?return_to=/after",
			nil,
		),
	)
	if rec.Code != http.StatusFound {
		t.Fatalf("login redirect status=%d body=%s", rec.Code, rec.Body.String())
	}
	flowCookies := rec.Result().Cookies()
	providerRec := httptest.NewRecorder()
	provider.Config.Handler.ServeHTTP(providerRec, httptest.NewRequest(http.MethodGet, rec.Header().Get("Location"), nil))
	callbackReq := httptest.NewRequest(http.MethodGet, providerRec.Header().Get("Location"), nil)
	for _, cookie := range flowCookies {
		callbackReq.AddCookie(cookie)
	}
	rec = performRequest(handler, callbackReq)
	if rec.Code != http.StatusFound ||
		rec.Header().Get("Location") != "/login?auth_error=identity_failed&return_to=%2Fafter" {
		t.Fatalf("callback status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if sessionToken := cookieValue(rec.Result().Cookies(), httpauth.BrowserSessionCookieName); sessionToken != "" {
		t.Fatalf("github login without primary verified email created session token %q", sessionToken)
	}
	var linkedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_auth_identities WHERE auth_connector_id = $1`, connector.ID).
		Scan(&linkedCount); err != nil {
		t.Fatalf("count linked identities: %v", err)
	}
	if linkedCount != 0 {
		t.Fatalf("linked identity count = %d, want 0", linkedCount)
	}
}

func TestOAuthLoginRateLimitsStateCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	redisClient := integrationredis.OpenClient(t)
	handler := newIntegrationServer(pool, WithPublicURL("http://omnara.test"), WithRedisBackedAuth(redisClient))
	store := integrationStoreForHandler(t, handler)
	runKey := identitystore.HashBearerToken(t.Name() + time.Now().UTC().Format(time.RFC3339Nano))[:12]
	clientBucket := "oauth-rate-client-" + runKey
	otherClientBucket := "oauth-rate-other-client-" + runKey
	if _, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:             "github-rate",
		Kind:             identitystore.AuthConnectorKindGitHub,
		DisplayName:      "GitHub Rate",
		Issuer:           "https://github.com",
		AuthorizationURL: "https://github.com/login/oauth/authorize",
		TokenURL:         "https://github.com/login/oauth/access_token",
		UserinfoURL:      "https://api.github.com/user",
		ClientID:         "client-id",
		ClientSecret:     "client-secret",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("create auth connector: %v", err)
	}
	for i := 0; i < 30; i++ {
		req := httptest.NewRequest(http.MethodGet, "http://omnara.test/api/auth/connectors/github-rate/login", nil)
		req.RemoteAddr = clientBucket
		rec := performRequest(handler, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("oauth login %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
	}
	otherClientReq := httptest.NewRequest(http.MethodGet, "http://omnara.test/api/auth/connectors/github-rate/login", nil)
	otherClientReq.RemoteAddr = otherClientBucket
	otherClientRec := performRequest(handler, otherClientReq)
	if otherClientRec.Code != http.StatusFound {
		t.Fatalf("other client oauth login status=%d body=%s", otherClientRec.Code, otherClientRec.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "http://omnara.test/api/auth/connectors/github-rate/login", nil)
	req.RemoteAddr = clientBucket
	rec := performRequest(handler, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited oauth login status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSSOOIDCConnectorValidatesIDTokenAndNonce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate oidc key: %v", err)
	}
	var issuer string
	var gotNonce string
	var gotCodeVerifier string
	var discoveryCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			discoveryCalls.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{
				"issuer":                                issuer,
				"authorization_endpoint":                issuer + "/api/authorize",
				"token_endpoint":                        issuer + "/token",
				"userinfo_endpoint":                     issuer + "/userinfo",
				"jwks_uri":                              issuer + "/jwks",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/api/authorize":
			if r.URL.Query().Get("code_challenge") == "" || r.URL.Query().Get("code_challenge_method") != "S256" {
				t.Fatalf("authorize query missing PKCE: %s", r.URL.RawQuery)
			}
			gotNonce = r.URL.Query().Get("nonce")
			if gotNonce == "" {
				t.Fatalf("authorize query missing nonce: %s", r.URL.RawQuery)
			}
			redirectURI := r.URL.Query().Get("redirect_uri")
			state := r.URL.Query().Get("state")
			http.Redirect(w, r, redirectURI+"?code=oidc-code&state="+url.QueryEscape(state), http.StatusFound)
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			gotCodeVerifier = r.Form.Get("code_verifier")
			if gotCodeVerifier == "" || r.Form.Get("code") != "oidc-code" {
				t.Fatalf("token form = %v", r.Form)
			}
			idToken := signedOIDCTestToken(t, issuer, "client-id", "oidc-subject", gotNonce, key)
			writeJSON(
				w,
				http.StatusOK,
				map[string]any{"access_token": "oidc-access-token", "token_type": "bearer", "id_token": idToken},
			)
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer oidc-access-token" {
				t.Fatalf("userinfo authorization header = %q", r.Header.Get("Authorization"))
			}
			writeJSON(
				w,
				http.StatusOK,
				map[string]any{
					"sub":            "oidc-subject",
					"email":          "oidc-user@example.com",
					"email_verified": true,
					"name":           "OIDC User",
				},
			)
		case "/jwks":
			writeJSON(
				w,
				http.StatusOK,
				jose.JSONWebKeySet{
					Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig"}},
				},
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	issuer = provider.URL

	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://omnara.test"),
		WithAuthHTTPClient(provider.Client()),
		withRedisOAuthStateAndAllowAllLimiter(t),
	)
	store := integrationStoreForHandler(t, handler)
	connector, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:             "corp-sso",
		Kind:             identitystore.AuthConnectorKindOIDC,
		DisplayName:      "Corp SSO",
		Issuer:           issuer,
		ClientID:         "client-id",
		ClientSecret:     "client-secret",
		EmailTrustPolicy: identitystore.AuthConnectorEmailTrustPolicyVerifiedEmail,
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("create auth connector: %v", err)
	}

	rec := performRequest(
		handler,
		httptest.NewRequest(http.MethodGet, "http://omnara.test/api/auth/connectors/corp-sso/login?return_to=/sso-done", nil),
	)
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), issuer+"/api/authorize?") {
		t.Fatalf("sso login status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	flowCookies := rec.Result().Cookies()
	if len(flowCookies) == 0 {
		t.Fatal("sso login did not set oauth flow cookie")
	}
	providerRec := httptest.NewRecorder()
	provider.Config.Handler.ServeHTTP(providerRec, httptest.NewRequest(http.MethodGet, rec.Header().Get("Location"), nil))
	callback := providerRec.Header().Get("Location")
	if callback == "" {
		t.Fatal("provider did not redirect to callback")
	}
	callbackReq := httptest.NewRequest(http.MethodGet, callback, nil)
	for _, cookie := range flowCookies {
		callbackReq.AddCookie(cookie)
	}
	rec = performRequest(handler, callbackReq)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/sso-done" {
		t.Fatalf("sso callback status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if gotCodeVerifier == "" {
		t.Fatal("oidc token exchange did not send code verifier")
	}
	if got := discoveryCalls.Load(); got != 2 {
		t.Fatalf("oidc discovery calls = %d, want 2", got)
	}
	sessionToken := cookieValue(rec.Result().Cookies(), httpauth.BrowserSessionCookieName)
	if sessionToken == "" {
		t.Fatalf("oidc callback did not set session cookies: %+v", rec.Result().Cookies())
	}
	principal, _, err := store.Identity().AuthenticateBrowserSession(ctx, sessionToken)
	if err != nil {
		t.Fatalf("authenticate oidc browser session: %v", err)
	}
	if principal.Type != identitystore.PrincipalTypeUser || principal.BrowserSessionID == storage.NilID {
		t.Fatalf("oidc principal = %+v", principal)
	}
	var linkedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_auth_identities WHERE auth_connector_id = $1 AND subject = 'oidc-subject'`, connector.ID).
		Scan(&linkedCount); err != nil {
		t.Fatalf("count oidc linked identity: %v", err)
	}
	if linkedCount != 1 {
		t.Fatalf("linked oidc identity count = %d, want 1", linkedCount)
	}
	var verifiedEmail string
	if err := pool.QueryRow(ctx, `
		SELECT email
		FROM user_emails
		WHERE user_id = $1 AND verified_at IS NOT NULL
	`, principal.ID).Scan(&verifiedEmail); err != nil {
		t.Fatalf("load oidc verified email: %v", err)
	}
	if verifiedEmail != "oidc-user@example.com" {
		t.Fatalf("verified email = %q, want userinfo email", verifiedEmail)
	}
}

func TestSSOOIDCConnectorUsesVerifiedIDTokenEmailWithoutUserInfo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate oidc key: %v", err)
	}
	var issuer string
	var gotNonce string
	var userInfoCalled atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(w, http.StatusOK, map[string]any{
				"issuer":                                issuer,
				"authorization_endpoint":                issuer + "/api/authorize",
				"token_endpoint":                        issuer + "/token",
				"userinfo_endpoint":                     issuer + "/userinfo",
				"jwks_uri":                              issuer + "/jwks",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/api/authorize":
			gotNonce = r.URL.Query().Get("nonce")
			redirectURI := r.URL.Query().Get("redirect_uri")
			state := r.URL.Query().Get("state")
			http.Redirect(w, r, redirectURI+"?code=oidc-code&state="+url.QueryEscape(state), http.StatusFound)
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			idToken := signedOIDCTestToken(
				t,
				issuer,
				"client-id",
				"oidc-subject",
				gotNonce,
				key,
				map[string]any{"email": "id-token@example.com", "email_verified": true, "name": "ID Token User"},
			)
			writeJSON(
				w,
				http.StatusOK,
				map[string]any{"access_token": "oidc-access-token", "token_type": "bearer", "id_token": idToken},
			)
		case "/userinfo":
			userInfoCalled.Store(true)
			http.Error(w, "unexpected userinfo", http.StatusInternalServerError)
		case "/jwks":
			writeJSON(
				w,
				http.StatusOK,
				jose.JSONWebKeySet{
					Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig"}},
				},
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	issuer = provider.URL

	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://omnara.test"),
		WithAuthHTTPClient(provider.Client()),
		withRedisOAuthStateAndAllowAllLimiter(t),
	)
	store := integrationStoreForHandler(t, handler)
	connector, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:             "id-token-sso",
		Kind:             identitystore.AuthConnectorKindOIDC,
		DisplayName:      "ID Token SSO",
		Issuer:           issuer,
		ClientID:         "client-id",
		ClientSecret:     "client-secret",
		EmailTrustPolicy: identitystore.AuthConnectorEmailTrustPolicyVerifiedEmail,
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("create auth connector: %v", err)
	}

	rec := performRequest(
		handler,
		httptest.NewRequest(
			http.MethodGet,
			"http://omnara.test/api/auth/connectors/id-token-sso/login?return_to=/sso-done",
			nil,
		),
	)
	if rec.Code != http.StatusFound {
		t.Fatalf("sso login status=%d body=%s", rec.Code, rec.Body.String())
	}
	flowCookies := rec.Result().Cookies()
	providerRec := httptest.NewRecorder()
	provider.Config.Handler.ServeHTTP(providerRec, httptest.NewRequest(http.MethodGet, rec.Header().Get("Location"), nil))
	callbackReq := httptest.NewRequest(http.MethodGet, providerRec.Header().Get("Location"), nil)
	for _, cookie := range flowCookies {
		callbackReq.AddCookie(cookie)
	}
	rec = performRequest(handler, callbackReq)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/sso-done" {
		t.Fatalf("sso callback status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	sessionToken := cookieValue(rec.Result().Cookies(), httpauth.BrowserSessionCookieName)
	if sessionToken == "" {
		t.Fatalf("oidc callback did not set session cookies: %+v", rec.Result().Cookies())
	}
	principal, _, err := store.Identity().AuthenticateBrowserSession(ctx, sessionToken)
	if err != nil {
		t.Fatalf("authenticate oidc browser session: %v", err)
	}
	var verifiedEmail string
	if err := pool.QueryRow(ctx, `
		SELECT email
		FROM user_emails
		WHERE user_id = $1 AND verified_at IS NOT NULL
	`, principal.ID).Scan(&verifiedEmail); err != nil {
		t.Fatalf("load oidc verified email: %v", err)
	}
	if verifiedEmail != "id-token@example.com" {
		t.Fatalf("verified email = %q, want id token email", verifiedEmail)
	}
	if userInfoCalled.Load() {
		t.Fatal("userinfo endpoint was called despite verified ID-token email")
	}
	var linkedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_auth_identities WHERE auth_connector_id = $1 AND subject = 'oidc-subject'`, connector.ID).
		Scan(&linkedCount); err != nil {
		t.Fatalf("count linked identity: %v", err)
	}
	if linkedCount != 1 {
		t.Fatalf("linked identity count = %d, want 1", linkedCount)
	}
}

func TestSSOOIDCConnectorRejectsMismatchedAuthorizedParty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate oidc key: %v", err)
	}
	var issuer string
	var gotNonce string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(w, http.StatusOK, map[string]any{
				"issuer":                                issuer,
				"authorization_endpoint":                issuer + "/api/authorize",
				"token_endpoint":                        issuer + "/token",
				"userinfo_endpoint":                     issuer + "/userinfo",
				"jwks_uri":                              issuer + "/jwks",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/api/authorize":
			gotNonce = r.URL.Query().Get("nonce")
			redirectURI := r.URL.Query().Get("redirect_uri")
			state := r.URL.Query().Get("state")
			http.Redirect(w, r, redirectURI+"?code=oidc-code&state="+url.QueryEscape(state), http.StatusFound)
		case "/token":
			idToken := signedOIDCTestToken(
				t,
				issuer,
				"client-id",
				"oidc-subject",
				gotNonce,
				key,
				map[string]any{"azp": "other-client", "email": "id-token@example.com", "email_verified": true},
			)
			writeJSON(
				w,
				http.StatusOK,
				map[string]any{"access_token": "oidc-access-token", "token_type": "bearer", "id_token": idToken},
			)
		case "/jwks":
			writeJSON(
				w,
				http.StatusOK,
				jose.JSONWebKeySet{
					Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig"}},
				},
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	issuer = provider.URL

	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://omnara.test"),
		WithAuthHTTPClient(provider.Client()),
		withRedisOAuthStateAndAllowAllLimiter(t),
	)
	store := integrationStoreForHandler(t, handler)
	if _, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:         "azp-sso",
		Kind:         identitystore.AuthConnectorKindOIDC,
		DisplayName:  "AZP SSO",
		Issuer:       issuer,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Enabled:      true,
	}); err != nil {
		t.Fatalf("create auth connector: %v", err)
	}

	rec := performRequest(
		handler,
		httptest.NewRequest(http.MethodGet, "http://omnara.test/api/auth/connectors/azp-sso/login?return_to=/sso-done", nil),
	)
	if rec.Code != http.StatusFound {
		t.Fatalf("sso login status=%d body=%s", rec.Code, rec.Body.String())
	}
	flowCookies := rec.Result().Cookies()
	providerRec := httptest.NewRecorder()
	provider.Config.Handler.ServeHTTP(providerRec, httptest.NewRequest(http.MethodGet, rec.Header().Get("Location"), nil))
	callbackReq := httptest.NewRequest(http.MethodGet, providerRec.Header().Get("Location"), nil)
	for _, cookie := range flowCookies {
		callbackReq.AddCookie(cookie)
	}
	rec = performRequest(handler, callbackReq)
	if rec.Code != http.StatusFound ||
		rec.Header().Get("Location") != "/login?auth_error=identity_failed&return_to=%2Fsso-done" {
		t.Fatalf("sso callback status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if sessionToken := cookieValue(rec.Result().Cookies(), httpauth.BrowserSessionCookieName); sessionToken != "" {
		t.Fatalf("mismatched azp minted session token %q", sessionToken)
	}
}

func TestSSOOIDCConnectorRejectsMismatchedUserInfoEmailVerification(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate oidc key: %v", err)
	}
	var issuer string
	var gotNonce string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(w, http.StatusOK, map[string]any{
				"issuer":                                issuer,
				"authorization_endpoint":                issuer + "/api/authorize",
				"token_endpoint":                        issuer + "/token",
				"userinfo_endpoint":                     issuer + "/userinfo",
				"jwks_uri":                              issuer + "/jwks",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/api/authorize":
			gotNonce = r.URL.Query().Get("nonce")
			redirectURI := r.URL.Query().Get("redirect_uri")
			state := r.URL.Query().Get("state")
			http.Redirect(w, r, redirectURI+"?code=oidc-code&state="+url.QueryEscape(state), http.StatusFound)
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			idToken := signedOIDCTestToken(
				t,
				issuer,
				"client-id",
				"oidc-subject",
				gotNonce,
				key,
				map[string]any{"email": "id-token@example.com"},
			)
			writeJSON(
				w,
				http.StatusOK,
				map[string]any{"access_token": "oidc-access-token", "token_type": "bearer", "id_token": idToken},
			)
		case "/userinfo":
			writeJSON(
				w,
				http.StatusOK,
				map[string]any{"sub": "oidc-subject", "email": "userinfo@example.com", "email_verified": true, "name": "OIDC User"},
			)
		case "/jwks":
			writeJSON(
				w,
				http.StatusOK,
				jose.JSONWebKeySet{
					Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig"}},
				},
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	issuer = provider.URL

	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://omnara.test"),
		WithAuthHTTPClient(provider.Client()),
		withRedisOAuthStateAndAllowAllLimiter(t),
	)
	store := integrationStoreForHandler(t, handler)
	connector, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:         "corp-sso",
		Kind:         identitystore.AuthConnectorKindOIDC,
		DisplayName:  "Corp SSO",
		Issuer:       issuer,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("create auth connector: %v", err)
	}

	rec := performRequest(
		handler,
		httptest.NewRequest(http.MethodGet, "http://omnara.test/api/auth/connectors/corp-sso/login?return_to=/sso-done", nil),
	)
	if rec.Code != http.StatusFound {
		t.Fatalf("sso login status=%d body=%s", rec.Code, rec.Body.String())
	}
	flowCookies := rec.Result().Cookies()
	providerRec := httptest.NewRecorder()
	provider.Config.Handler.ServeHTTP(providerRec, httptest.NewRequest(http.MethodGet, rec.Header().Get("Location"), nil))
	callbackReq := httptest.NewRequest(http.MethodGet, providerRec.Header().Get("Location"), nil)
	for _, cookie := range flowCookies {
		callbackReq.AddCookie(cookie)
	}
	rec = performRequest(handler, callbackReq)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "auth_error=identity_failed") {
		t.Fatalf("sso callback status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if sessionToken := cookieValue(rec.Result().Cookies(), httpauth.BrowserSessionCookieName); sessionToken != "" {
		t.Fatalf("oidc callback created session after mismatched userinfo email: %q", sessionToken)
	}
	var linkedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_auth_identities WHERE auth_connector_id = $1`, connector.ID).
		Scan(&linkedCount); err != nil {
		t.Fatalf("count linked identities: %v", err)
	}
	if linkedCount != 0 {
		t.Fatalf("linked identity count = %d, want 0", linkedCount)
	}
}

func TestSSOOIDCConnectorRejectsSubjectOnlyFirstLogin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate oidc key: %v", err)
	}
	var issuer string
	var gotNonce string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(w, http.StatusOK, map[string]any{
				"issuer":                                issuer,
				"authorization_endpoint":                issuer + "/api/authorize",
				"token_endpoint":                        issuer + "/token",
				"jwks_uri":                              issuer + "/jwks",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/api/authorize":
			gotNonce = r.URL.Query().Get("nonce")
			redirectURI := r.URL.Query().Get("redirect_uri")
			state := r.URL.Query().Get("state")
			http.Redirect(w, r, redirectURI+"?code=oidc-code&state="+url.QueryEscape(state), http.StatusFound)
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			idToken := signedOIDCTestToken(t, issuer, "client-id", "oidc-subject", gotNonce, key)
			writeJSON(
				w,
				http.StatusOK,
				map[string]any{"access_token": "oidc-access-token", "token_type": "bearer", "id_token": idToken},
			)
		case "/jwks":
			writeJSON(
				w,
				http.StatusOK,
				jose.JSONWebKeySet{
					Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig"}},
				},
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	issuer = provider.URL

	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://omnara.test"),
		WithAuthHTTPClient(provider.Client()),
		withRedisOAuthStateAndAllowAllLimiter(t),
	)
	store := integrationStoreForHandler(t, handler)
	connector, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:         "subject-only-sso",
		Kind:         identitystore.AuthConnectorKindOIDC,
		DisplayName:  "Subject Only SSO",
		Issuer:       issuer,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("create auth connector: %v", err)
	}

	rec := performRequest(
		handler,
		httptest.NewRequest(
			http.MethodGet,
			"http://omnara.test/api/auth/connectors/subject-only-sso/login?return_to=/sso-done",
			nil,
		),
	)
	if rec.Code != http.StatusFound {
		t.Fatalf("sso login status=%d body=%s", rec.Code, rec.Body.String())
	}
	flowCookies := rec.Result().Cookies()
	providerRec := httptest.NewRecorder()
	provider.Config.Handler.ServeHTTP(providerRec, httptest.NewRequest(http.MethodGet, rec.Header().Get("Location"), nil))
	callbackReq := httptest.NewRequest(http.MethodGet, providerRec.Header().Get("Location"), nil)
	for _, cookie := range flowCookies {
		callbackReq.AddCookie(cookie)
	}
	rec = performRequest(handler, callbackReq)
	if rec.Code != http.StatusFound ||
		rec.Header().Get("Location") != "/login?auth_error=identity_failed&return_to=%2Fsso-done" {
		t.Fatalf("sso callback status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	sessionToken := cookieValue(rec.Result().Cookies(), httpauth.BrowserSessionCookieName)
	if sessionToken != "" {
		t.Fatalf("subject-only first login created session token %q", sessionToken)
	}
	var verifiedEmailCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM user_emails
		WHERE verified_at IS NOT NULL
	`).Scan(&verifiedEmailCount); err != nil {
		t.Fatalf("count verified emails: %v", err)
	}
	if verifiedEmailCount != 0 {
		t.Fatalf("verified email count = %d, want 0", verifiedEmailCount)
	}
	var linkedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_auth_identities WHERE auth_connector_id = $1 AND subject = 'oidc-subject'`, connector.ID).
		Scan(&linkedCount); err != nil {
		t.Fatalf("count linked identity: %v", err)
	}
	if linkedCount != 0 {
		t.Fatalf("linked identity count = %d, want 0", linkedCount)
	}
}

func signedOIDCTestToken(
	t *testing.T,
	issuer, audience, subject, nonce string,
	key *rsa.PrivateKey,
	extraClaims ...map[string]any,
) string {
	t.Helper()
	claims := map[string]any{
		"iss":   issuer,
		"aud":   audience,
		"sub":   subject,
		"nonce": nonce,
		"iat":   time.Now().Add(-time.Minute).Unix(),
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
	for _, extra := range extraClaims {
		for name, value := range extra {
			claims[name] = value
		}
	}
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal oidc claims: %v", err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"),
	)
	if err != nil {
		t.Fatalf("create oidc signer: %v", err)
	}
	jws, err := signer.Sign(body)
	if err != nil {
		t.Fatalf("sign oidc token: %v", err)
	}
	token, err := jws.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize oidc token: %v", err)
	}
	return token
}

func sCreateBrowserSessionForTest(
	ctx context.Context,
	store *storage.Store,
	userID storage.ID,
) (string, string, error) {
	sessionToken := "session-" + identitystore.HashBearerToken(userID.String())[:24]
	csrfToken := "csrf-" + identitystore.HashBearerToken(userID.String())[:24]
	_, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    userID,
			Token:     sessionToken,
			CSRFToken: csrfToken,
			TTL:       time.Hour,
		},
	)
	return sessionToken, csrfToken, err
}

func TestPublicURLControlsBrowserCookieSecurity(t *testing.T) {
	t.Parallel()
	server := &Server{publicURL: "https://omnara.test"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://omnara.test/api/auth/login", nil)
	httpauth.SetBrowserSessionCookies(rec, req, server.publicURL, "session-token", "csrf-token", time.Hour)
	sessionCookie := rec.Result().Cookies()[0]
	if sessionCookie.Name != httpauth.BrowserSessionHostCookieName || !sessionCookie.Secure || !sessionCookie.HttpOnly ||
		sessionCookie.MaxAge != int(time.Hour/time.Second) {
		t.Fatalf("expected secure host session cookie, got %+v", sessionCookie)
	}
	csrfCookie := rec.Result().Cookies()[1]
	if csrfCookie.Name != httpauth.CSRFHostCookieName || !csrfCookie.Secure || csrfCookie.HttpOnly ||
		csrfCookie.MaxAge != int(time.Hour/time.Second) {
		t.Fatalf("expected secure readable host csrf cookie, got %+v", csrfCookie)
	}
}

func TestMachineRoutesRequireMachineAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := newIntegrationStore(pool)
	project := bootstrapPublicHTTPProject(t, handler, "org-machine-auth")

	viewer, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "machine-viewer@example.com", DisplayName: "Machine Viewer"})
	if err != nil {
		t.Fatalf("create viewer user: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID:  project.OrgUUID,
		UserID: viewer.ID,
		Role:   "member",
	}); err != nil {
		t.Fatalf("add member org membership: %v", err)
	}
	viewerPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: viewer.ID,
			Name:   "viewer",
		},
	)
	if err != nil {
		t.Fatalf("create viewer token: %v", err)
	}
	viewerToken := viewerPAT.Token
	creator, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "machine-creator@example.com", DisplayName: "Machine Creator"})
	if err != nil {
		t.Fatalf("create creator user: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID:  project.OrgUUID,
		UserID: creator.ID,
		Role:   "admin",
	}); err != nil {
		t.Fatalf("add creator admin membership: %v", err)
	}
	creatorPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: creator.ID,
			Name:   "creator",
		},
	)
	if err != nil {
		t.Fatalf("create creator token: %v", err)
	}
	creatorToken := creatorPAT.Token

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		`{"display_name":"viewer machine"}`,
		"idem-viewer-machine",
		http.StatusForbidden,
		authHeaders(viewerToken),
	)

	machine := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		`{"display_name":"admin machine"}`,
		"idem-admin-machine",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	machineID := machine["id"].(string)
	missingMachineID := testPublicID(t, publicid.KindMachine, httpTestID("missing-machine"))
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+missingMachineID,
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	otherMachine := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		`{"display_name":"other admin machine"}`,
		"idem-other-admin-machine",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	otherMachineID := otherMachine["id"].(string)
	creatorMachine := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		`{"display_name":"creator machine"}`,
		"idem-creator-machine",
		http.StatusCreated,
		authHeaders(creatorToken),
	)
	creatorMachineID := creatorMachine["id"].(string)
	membershiplessCreatorMachine := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		`{"display_name":"membershipless creator machine"}`,
		"idem-membershipless-creator-machine",
		http.StatusCreated,
		authHeaders(creatorToken),
	)
	membershiplessCreatorMachineID := membershiplessCreatorMachine["id"].(string)
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: project.OrgUUID, UserID: creator.ID, Role: "member"},
	); err != nil {
		t.Fatalf("demote creator to member: %v", err)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+creatorMachineID,
		"",
		"",
		http.StatusForbidden,
		authHeaders(creatorToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+creatorMachineID,
		"",
		"",
		http.StatusForbidden,
		authHeaders(creatorToken),
	)
	if _, err := pool.Exec(ctx, `
		DELETE FROM org_memberships
		WHERE org_id = $1 AND user_id = $2
	`, project.OrgUUID, creator.ID); err != nil {
		t.Fatalf("remove creator org membership: %v", err)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+membershiplessCreatorMachineID,
		"",
		"",
		http.StatusForbidden,
		authHeaders(creatorToken),
	)
	token := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+machineID+"/daemon-tokens",
		`{"name":"daemon"}`,
		"",
		http.StatusCreated,
		project.adminBrowserAuthHeaders(),
	)
	tokenRecord := token["token_record"].(map[string]any)
	tokenID := tokenRecord["id"].(string)
	daemonToken := token["token"].(string)
	if len(token) != 2 {
		t.Fatalf("unexpected daemon token response: %+v", token)
	}
	otherToken := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+otherMachineID+"/daemon-tokens",
		`{"name":"daemon"}`,
		"",
		http.StatusCreated,
		project.adminBrowserAuthHeaders(),
	)
	otherDaemonToken := otherToken["token"].(string)
	var installationUUID storage.ID
	if err := pool.QueryRow(ctx, `SELECT id FROM installation WHERE singleton_key = 1`).Scan(&installationUUID); err != nil {
		t.Fatalf("get installation: %v", err)
	}
	installationID := testPublicID(t, publicid.KindInstallation, installationUUID)

	profile := createPublicHTTPAgent(t, handler, project, "daemon-forbidden", project.AdminToken)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"profile":"`+profile["id"].(string)+`","config":"`+profile["current_config"].(map[string]any)["id"].(string)+`"}`,
		"",
		http.StatusForbidden,
		authHeaders(daemonToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+machineID+"/daemon-tokens/"+tokenID+"/revoke",
		"",
		"",
		http.StatusForbidden,
		authHeaders(viewerToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/daemon/runtimes",
		`{"daemon_instance_id":`+quote(httpTestID("di-machine-authority").String())+`,"daemon_version":"1.0.0","processes":[]}`,
		"",
		http.StatusForbidden,
		authHeaders(project.AdminToken),
	)

	bootstrap := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/daemon/bootstrap",
		"",
		"",
		http.StatusOK,
		authHeaders(daemonToken),
	)
	if len(bootstrap) != 2 || bootstrap["installation_id"] != installationID || bootstrap["machine_id"] != machineID {
		t.Fatalf("unexpected daemon bootstrap response: %+v", bootstrap)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/daemon/runtimes",
		`{"daemon_instance_id":`+quote(httpTestID("daemon-unsupported-protocol").String())+
			`,"protocol_version":"unsupported"}`,
		"",
		http.StatusBadRequest,
		authHeaders(daemonToken),
	)
	runtime := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/daemon/runtimes",
		`{"daemon_instance_id":`+quote(httpTestID("daemon-identity-workflow").String())+`,"daemon_version":"1.0.0","processes":[]}`,
		"",
		http.StatusCreated,
		authHeaders(daemonToken),
	)
	runtimeRecord := runtime["runtime"].(map[string]any)
	runtimeID := runtimeRecord["id"].(string)
	if runtimeRecord["next_heartbeat_after_ms"].(float64) <= 0 {
		t.Fatalf(
			"runtime response missing positive next_heartbeat_after_ms: %+v",
			runtimeRecord,
		)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/daemon/runtimes/"+runtimeID+"/socket",
		"",
		"",
		http.StatusServiceUnavailable,
		authHeaders(daemonToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/daemon/runtimes/"+runtimeID+"/end",
		"",
		"",
		http.StatusGone,
		authHeaders(otherDaemonToken),
	)
}
