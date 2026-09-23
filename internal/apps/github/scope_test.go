package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func scopeOperations() []struct {
	name string
	run  func(context.Context, *Client, Scope) error
} {
	return []struct {
		name string
		run  func(context.Context, *Client, Scope) error
	}{
		{"metadata", func(ctx context.Context, c *Client, s Scope) error {
			_, err := c.GetPullRequest(ctx, s)
			return err
		}},
		{"diff", func(ctx context.Context, c *Client, s Scope) error {
			_, err := c.GetDiff(ctx, s)
			return err
		}},
		{"files", func(ctx context.Context, c *Client, s Scope) error {
			_, err := c.ListFiles(ctx, s, PageOptions{})
			return err
		}},
		{"discussion-read", func(ctx context.Context, c *Client, s Scope) error {
			_, err := c.ListDiscussionComments(ctx, s, PageOptions{})
			return err
		}},
		{"review-read", func(ctx context.Context, c *Client, s Scope) error {
			_, err := c.ListReviewComments(ctx, s, PageOptions{})
			return err
		}},
		{"discussion-post", func(ctx context.Context, c *Client, s Scope) error {
			_, err := c.CreateDiscussionComment(ctx, s, "comment")
			return err
		}},
		{"inline", func(ctx context.Context, c *Client, s Scope) error {
			_, err := c.CreateInlineComment(ctx, s, InlineCommentArgs{
				Body: "inline", CommitID: "commit", Path: "file.go", Line: 1, Side: "RIGHT",
			})
			return err
		}},
		{"reply", func(ctx context.Context, c *Client, s Scope) error {
			_, err := c.Reply(ctx, s, 8, "reply")
			return err
		}},
	}
}

func TestRepositoryIdentitySurvivesRenameAndNameReuse(t *testing.T) {
	var mu sync.Mutex
	repository := testRepository()
	var tokens, resolutions, oldNameRequests int
	grants := make(map[string]string)
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/app/installations/456/access_tokens" {
			var input struct {
				RepositoryIDs []int64           `json:"repository_ids"`
				Repositories  []string          `json:"repositories"`
				Permissions   map[string]string `json:"permissions"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.RepositoryIDs) != 1 ||
				input.RepositoryIDs[0] != 789 || input.Repositories != nil || len(input.Permissions) != 1 {
				t.Error("token grant used names or another repository")
			}
			tokens++
			token := fmt.Sprintf("id-token-%d", tokens)
			grants[token] = input.Permissions["pull_requests"]
			fmt.Fprintf(w, `{"token":%q,"expires_at":"2099-01-01T00:00:00Z"}`, token)
			return
		}
		grant := grants[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if grant != "read" && grant != "write" {
			t.Error("missing installation token")
		}
		if r.URL.Path == "/installation/repositories" {
			resolutions++
			if r.Method != http.MethodGet || r.URL.Query().Get("page") != "1" ||
				r.URL.Query().Get("per_page") != "100" {
				t.Error("unbounded repository lookup")
			}
			json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "repositories": []Repository{repository}})
			return
		}
		if repository.Owner.Login != testRepository().Owner.Login &&
			strings.HasPrefix(r.URL.Path, repoPath(testRepository())+"/") {
			oldNameRequests++
			fmt.Fprint(w, `{"id":99,"number":42,"base":{"repo":{"id":999}},"body":"wrong repository"}`)
			return
		}
		prPath := pullPath(repository, 42)
		if r.Method == http.MethodPost {
			if grant != "write" || (r.URL.Path != repoPath(repository)+"/issues/42/comments" &&
				r.URL.Path != prPath+"/comments" && r.URL.Path != prPath+"/comments/7/replies") {
				t.Error("mutation escaped the resolved repository/PR")
			}
			fmt.Fprint(w, `{"id":10,"in_reply_to_id":7}`)
			return
		}
		switch r.URL.Path {
		case prPath:
			if r.Header.Get("Accept") == "application/vnd.github.diff" {
				fmt.Fprint(w, "diff --git a/file b/file\n+change\n")
			} else {
				json.NewEncoder(w).Encode(PullRequest{ID: 7, Number: 42, Base: Branch{Repo: &repository}})
			}
		case prPath + "/files", prPath + "/comments", repoPath(repository) + "/issues/42/comments":
			fmt.Fprint(w, `[]`)
		case repoPath(repository) + "/pulls/comments/8":
			fmt.Fprintf(w, `{"id":8,"in_reply_to_id":7,"pull_request_url":%q}`, "http://"+r.Host+prPath)
		case repoPath(repository) + "/pulls/comments/7":
			fmt.Fprintf(w, `{"id":7,"pull_request_url":%q}`, "http://"+r.Host+prPath)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	operations := scopeOperations()
	for phase := range 3 {
		if phase == 1 {
			mu.Lock()
			repository.Owner.Login, repository.Name = "new-owner", "renamed.repo_2"
			mu.Unlock()
		}
		scope := testScope()
		if phase == 2 {
			scope.Owner, scope.Repository = "//other.example", "../../repo?token=secret#fragment"
		}
		for _, operation := range operations {
			if err := operation.run(t.Context(), client, scope); err != nil {
				t.Fatalf("phase %d, %s: %v", phase, operation.name, err)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if oldNameRequests != 0 || tokens != 2 || resolutions != 3*len(operations) {
		t.Fatalf("old name requests=%d, tokens=%d, resolutions=%d", oldNameRequests, tokens, resolutions)
	}
}

func TestAllOperationsRejectWrongPullIdentity(t *testing.T) {
	for _, fixture := range []struct {
		body string
		code ErrorCode
	}{
		{`{"id":7,"number":42}`, InvalidResponse},
		{`{"id":7,"number":42,"base":{"repo":{"id":0}}}`, InvalidResponse},
		{`{"id":0,"number":42,"base":{"repo":{"id":789}}}`, InvalidResponse},
		{`{"id":7,"number":0,"base":{"repo":{"id":789}}}`, InvalidResponse},
		{`{"id":7,"number":42,"base":{"repo":{"id":999}}}`, ScopeMismatch},
		{`{"id":7,"number":43,"base":{"repo":{"id":789}}}`, ScopeMismatch},
	} {
		for _, operation := range scopeOperations() {
			t.Run(operation.name+fixture.body, func(t *testing.T) {
				client, _ := testClient(t, withToken(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.Path != pullPath(testRepository(), 42) ||
						r.Header.Get("Accept") != jsonMediaType {
						t.Error("performed operation beyond failed PR identity check")
					}
					fmt.Fprint(w, fixture.body)
				}))
				requireAPIError(t, operation.run(t.Context(), client, testScope()), fixture.code)
				pr, err := client.GetPullRequest(t.Context(), testScope())
				if err == nil || pr.ID != 0 || pr.Base.Repo != nil {
					t.Fatal("returned unverified PR metadata")
				}
			})
		}
	}
}

func TestRepositoryResolverRejectsMissingOrAmbiguousIdentity(t *testing.T) {
	for _, fixture := range []struct {
		body string
		link string
		code ErrorCode
	}{
		{`null`, "", InvalidResponse},
		{`{"repositories":[]}`, "", InvalidResponse},
		{`{"total_count":-1,"repositories":[]}`, "", InvalidResponse},
		{`{"total_count":1,"repositories":null}`, "", InvalidResponse},
		{`{"total_count":101,"repositories":[` + strings.Repeat(`{"id":789},`, 100) + `{"id":789}]}`, "", InvalidResponse},
		{`{"total_count":0,"repositories":[]}`, "", ScopeMismatch},
		{`{"total_count":1,"repositories":[{"id":999}]}`, "", ScopeMismatch},
		{`{"total_count":2,"repositories":[{"id":789},{"id":999}]}`, "", ScopeMismatch},
		{`{"total_count":1,"repositories":[{"id":789}]}`, "", InvalidResponse},
		{testRepositoryListing, `<https://attacker.example/steal?page=2&per_page=100>; rel="next"`, InvalidResponse},
		{testRepositoryListing, "next", ScopeMismatch},
		{testRepositoryListing, `<$ORIGIN/repositories/789?per_page=100&page=2>; rel="next"`, InvalidResponse},
	} {
		t.Run(fixture.body+fixture.link, func(t *testing.T) {
			var resolutions atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/app/installations/456/access_tokens" {
					tokenResponse(w)
					return
				}
				if r.URL.Path != "/installation/repositories" {
					t.Error("followed URL or used unverified repository")
				}
				resolutions.Add(1)
				link := strings.ReplaceAll(fixture.link, "$ORIGIN", "http://"+r.Host)
				if link == "next" {
					link = "<http://" + r.Host + `/installation/repositories?page=2&per_page=100>; rel="next"`
				}
				w.Header().Set("Link", link)
				fmt.Fprint(w, fixture.body)
			})
			_, err := client.CreateDiscussionComment(t.Context(), testScope(), "body")
			requireAPIError(t, err, fixture.code)
			if resolutions.Load() != 1 {
				t.Fatal("resolver escaped its single page bound")
			}
		})
	}
}

func TestResolvedRepositoryPathSegments(t *testing.T) {
	for _, value := range []string{"", ".", "..", "../other", "a/b", "%2f", "name?query", "name#fragment",
		"name\\folder", "name\r\nInjected:true", "🐙", strings.Repeat("a", 256)} {
		for _, field := range []string{"owner", "name"} {
			t.Run(field+value, func(t *testing.T) {
				repository := testRepository()
				if field == "owner" {
					repository.Owner.Login = value
				} else {
					repository.Name = value
				}
				client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/app/installations/456/access_tokens" {
						tokenResponse(w)
						return
					}
					if r.URL.Path != "/installation/repositories" {
						t.Error("used unsafe provider path segment")
					}
					json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "repositories": []Repository{repository}})
				})
				_, err := client.GetPullRequest(t.Context(), testScope())
				requireAPIError(t, err, InvalidResponse)
			})
		}
	}
}

func TestRepositoryTransferLosingInstallationAccess(t *testing.T) {
	var removed atomic.Bool
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/456/access_tokens" {
			tokenResponse(w)
			return
		}
		if removed.Load() {
			if r.URL.Path != "/installation/repositories" {
				t.Error("used stale name or tried to discover another installation")
			}
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if !servePreparedPull(w, r) {
			fmt.Fprint(w, `{"id":9}`)
		}
	})
	if _, err := client.CreateDiscussionComment(t.Context(), testScope(), "before transfer"); err != nil {
		t.Fatal(err)
	}
	removed.Store(true)
	_, err := client.CreateDiscussionComment(t.Context(), testScope(), "after transfer")
	requireAPIError(t, err, PermanentFailure)
}

func TestPreflightFailuresDoNotAttemptMutation(t *testing.T) {
	for _, stage := range []string{"/installation/repositories", pullPath(testRepository(), 42)} {
		t.Run(stage, func(t *testing.T) {
			var attempts atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/app/installations/456/access_tokens" {
					tokenResponse(w)
					return
				}
				if r.URL.Path == stage {
					attempts.Add(1)
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if !servePreparedPull(w, r) {
					t.Error("attempted mutation after failed preflight")
				}
			})
			_, err := client.CreateDiscussionComment(t.Context(), testScope(), "body")
			requireAPIError(t, err, TransientFailure)
			if attempts.Load() != 3 {
				t.Fatal("preflight retry exceeded its bound")
			}
		})
	}
}
