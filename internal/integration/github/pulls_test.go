package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadPagesAndDiff(t *testing.T) {
	var requests atomic.Int32
	client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer installation-token" {
			t.Error("wrong read request")
		}
		if r.URL.Path == pullPath(testRepository(), 42) {
			if r.Header.Get("Accept") != "application/vnd.github.diff" {
				t.Error("wrong diff media type")
			}
			fmt.Fprint(w, "diff --git a/file b/file\n+new line\n")
			return
		}
		if r.URL.Query().Get("per_page") != "1" {
			t.Error("incorrect page bounds")
		}
		switch r.URL.Query().Get("page") {
		case "1":
			path := "/repositories/789" + strings.TrimPrefix(r.URL.Path, repoPath(testRepository()))
			w.Header().Set("Link", fmt.Sprintf(
				`<http://%s%s?per_page=1&page=2>; rel="next", <http://%s%s?per_page=1&page=2>; rel="last"`,
				r.Host, path, r.Host, path))
		case "2":
			if r.URL.Path != pullPath(testRepository(), 42)+"/files" {
				t.Error("next page did not use the resolved repository path")
			}
			fmt.Fprint(w, `[]`)
			return
		default:
			t.Error("incorrect page bounds")
		}
		switch r.URL.Path {
		case pullPath(testRepository(), 42) + "/files":
			fmt.Fprint(w, `[{"filename":"a b/#?.go","patch":"@@ -1 +1 @@\n-old\n+new"}]`)
		case pullPath(testRepository(), 42) + "/comments":
			fmt.Fprint(w, `[{"id":9,"body":"inline","user":{"id":4,"type":"Bot"},
				"line":null,"original_line":3,"in_reply_to_id":7}]`)
		case repoPath(testRepository()) + "/issues/42/comments":
			fmt.Fprint(w, `[{"id":8,"body":"discussion","user":{"id":3,"login":"author","type":"User"}}]`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	options := PageOptions{PerPage: 1}
	files, err := client.ListFiles(t.Context(), testScope(), options)
	if err != nil || files.NextPage != 2 || len(files.Files) != 1 || files.Files[0].Patch == nil {
		t.Fatalf("files = %+v, err = %v", files, err)
	}
	lastFiles, err := client.ListFiles(t.Context(), testScope(), PageOptions{Page: files.NextPage, PerPage: 1})
	if err != nil || lastFiles.NextPage != 0 || len(lastFiles.Files) != 0 {
		t.Fatalf("last files = %+v, err = %v", lastFiles, err)
	}
	comments, err := client.ListDiscussionComments(t.Context(), testScope(), options)
	if err != nil || comments.NextPage != 2 || len(comments.Comments) != 1 || comments.Comments[0].User.Login != "author" {
		t.Fatalf("discussion = %+v, err = %v", comments, err)
	}
	reviews, err := client.ListReviewComments(t.Context(), testScope(), options)
	if err != nil || reviews.NextPage != 2 || len(reviews.Comments) != 1 || reviews.Comments[0].Line != nil ||
		reviews.Comments[0].OriginalLine == nil || reviews.Comments[0].InReplyToID != 7 {
		t.Fatalf("reviews = %+v, err = %v", reviews, err)
	}
	diff, err := client.GetDiff(t.Context(), testScope())
	if err != nil || !strings.HasPrefix(diff.Text, "diff --git") || requests.Load() != 5 {
		t.Fatalf("diff = %+v, err = %v, requests = %d", diff, err, requests.Load())
	}
}

func TestPaginationCannotChangeScope(t *testing.T) {
	var link string
	var calls atomic.Int32
	client, server := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Link", link)
		fmt.Fprint(w, `[]`)
	}))
	for _, path := range []string{pullPath(testRepository(), 42) + "/comments", "/repositories/789/pulls/42/comments"} {
		for _, next := range []string{
			"https://attacker.example" + path + "?page=2&per_page=30",
			server.URL + "/repos/other/repo/pulls/42/comments?page=2&per_page=30",
			server.URL + "/repositories/790/pulls/42/comments?page=2&per_page=30",
			server.URL + "/repositories/0789/pulls/42/comments?page=2&per_page=30",
			server.URL + "/repositories/789/issues/42/comments?page=2&per_page=30",
			server.URL + "/repositories/789/pulls/42/files?page=2&per_page=30",
			server.URL + "/installation/repositories?page=2&per_page=30",
			server.URL + strings.Replace(path, "/42/", "/43/", 1) + "?page=2&per_page=30",
			server.URL + strings.Replace(path, "/pulls/", "/%70ulls/", 1) + "?page=2&per_page=30",
			strings.Replace(server.URL, "http://", "http://token@", 1) + path + "?page=2&per_page=30",
			server.URL + path + "?page=2&per_page=100",
			server.URL + path + "?page=2&page=3&per_page=30",
			server.URL + path + "?page=2&per_page=30&per_page=30",
			server.URL + path + "?page=2&per_page=30&extra=scope",
			server.URL + path + "?page=1&per_page=30",
			server.URL + path + "?page=3&per_page=30",
			server.URL + path + "?page=-1&per_page=30",
			server.URL + path + "?page=9223372036854775808&per_page=30",
			server.URL + path + "?page=2&per_page=30#fragment",
			"//attacker.example" + path + "?page=2&per_page=30",
		} {
			link = "<" + next + `>; rel="next"`
			before := calls.Load()
			_, err := client.ListReviewComments(t.Context(), testScope(), PageOptions{})
			requireAPIError(t, err, InvalidResponse)
			if calls.Load() != before+1 {
				t.Fatal("followed provider URL")
			}
		}
		valid := "<" + server.URL + path + `?page=2&per_page=30>; rel="next"`
		link = valid + ", " + valid
		_, err := client.ListReviewComments(t.Context(), testScope(), PageOptions{})
		requireAPIError(t, err, InvalidResponse)
		link = valid
		page, err := client.ListReviewComments(t.Context(), testScope(), PageOptions{})
		if err != nil || page.NextPage != 2 {
			t.Fatalf("valid pagination rejected: %v", err)
		}
	}
	link = "<" + server.URL + pullPath(testRepository(), 42) + `/comments?per_page=30&page=2>; rel="next", <` +
		server.URL + `/repositories/789/pulls/42/comments?per_page=30&page=2>; rel="next"`
	_, err := client.ListReviewComments(t.Context(), testScope(), PageOptions{})
	requireAPIError(t, err, InvalidResponse)
}

func TestScopesAndPageBoundsRejectedBeforeIO(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, scope := range []Scope{
		{RepositoryID: 0, PullRequest: 42},
		{RepositoryID: -1, PullRequest: 42},
		{RepositoryID: 789, PullRequest: 0},
		{RepositoryID: 789, PullRequest: -1},
	} {
		if _, err := client.GetPullRequest(t.Context(), scope); err == nil {
			t.Fatal("accepted invalid scope")
		}
		if _, err := client.CreateDiscussionComment(t.Context(), scope, "body"); err == nil {
			t.Fatal("accepted invalid mutation scope")
		}
		if _, err := client.GetDiff(t.Context(), scope); err == nil {
			t.Fatal("accepted invalid diff scope")
		}
	}
	for _, options := range []PageOptions{{PerPage: 101}, {PerPage: -1}, {Page: -1}} {
		if _, err := client.ListReviewComments(t.Context(), testScope(), options); err == nil {
			t.Fatal("accepted invalid page bounds")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid scope/page made a request")
	}
}

func TestPaginationResponseBounds(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `[{},{}]`, `[] []`} {
		client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		_, err := client.ListReviewComments(t.Context(), testScope(), PageOptions{PerPage: 1})
		requireAPIError(t, err, InvalidResponse)
	}
}

func TestCommentRequests(t *testing.T) {
	var received []map[string]any
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if servePreparedPull(w, r) {
			return
		}
		var input map[string]any
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			t.Error("invalid request JSON")
			return
		}
		if r.URL.Path == "/app/installations/456/access_tokens" {
			permissions, ok := input["permissions"].(map[string]any)
			if !ok || len(permissions) != 1 || permissions["pull_requests"] != "write" {
				t.Error("comment did not use PR write permission")
			}
			tokenResponse(w)
			return
		}
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" ||
			r.Header.Get("Authorization") != "Bearer installation-token" {
			t.Error("incorrect comment request headers/method")
		}
		if len(received) == 0 && r.URL.Path != repoPath(testRepository())+"/issues/42/comments" {
			t.Error("discussion comment used wrong endpoint")
		}
		if len(received) > 0 && r.URL.Path != pullPath(testRepository(), 42)+"/comments" {
			t.Error("inline comment used wrong endpoint")
		}
		received = append(received, input)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":99,"body":"saved","html_url":"https://github.com/org/repo/pull/42#comment"}`)
	})
	comment, err := client.CreateDiscussionComment(t.Context(), testScope(), "discussion\nwith Unicode 🐙")
	if err != nil || comment.ID != 99 || comment.HTMLURL == "" {
		t.Fatalf("discussion receipt = %+v, err = %v", comment, err)
	}
	args := InlineCommentArgs{
		Body: "inline", CommitID: strings.Repeat("a", 40), Path: "dir/a b#?%.go", Line: 10, Side: "RIGHT",
	}
	if _, err := client.CreateInlineComment(t.Context(), testScope(), args); err != nil {
		t.Fatal(err)
	}
	start := 8
	args.StartLine, args.StartSide = &start, "RIGHT"
	if _, err := client.CreateInlineComment(t.Context(), testScope(), args); err != nil {
		t.Fatal(err)
	}
	if len(received) != 3 || len(received[0]) != 1 || len(received[1]) != 5 || len(received[2]) != 7 ||
		received[1]["path"] != args.Path || received[2]["start_line"] != float64(8) || received[2]["start_side"] != "RIGHT" {
		t.Fatalf("incorrect tool argument mapping: %+v", received)
	}
}

func TestInlineValidationBeforeIO(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, change := range []func(*InlineCommentArgs){
		func(a *InlineCommentArgs) { a.Body = " " },
		func(a *InlineCommentArgs) { a.Body = strings.Repeat("x", CommentMaxBytes+1) },
		func(a *InlineCommentArgs) { a.Body = string([]byte{0xff}) },
		func(a *InlineCommentArgs) { a.CommitID = "" },
		func(a *InlineCommentArgs) { a.Path = "" },
		func(a *InlineCommentArgs) { a.Line = 0 },
		func(a *InlineCommentArgs) { a.Side = "right" },
		func(a *InlineCommentArgs) { n := 1; a.StartLine = &n },
		func(a *InlineCommentArgs) { a.StartSide = "RIGHT" },
		func(a *InlineCommentArgs) { n := -1; a.StartLine, a.StartSide = &n, "RIGHT" },
		func(a *InlineCommentArgs) { n := 5; a.StartLine, a.StartSide = &n, "RIGHT" },
	} {
		args := InlineCommentArgs{Body: "body", CommitID: "sha", Path: "file", Line: 5, Side: "RIGHT"}
		change(&args)
		if _, err := client.CreateInlineComment(t.Context(), testScope(), args); err == nil {
			t.Fatal("accepted invalid inline args")
		}
	}
	if _, err := client.Reply(t.Context(), testScope(), 0, "body"); err == nil {
		t.Fatal("accepted nonpositive reply ID")
	}
	if _, err := client.Reply(t.Context(), testScope(), 1, ""); err == nil {
		t.Fatal("accepted empty reply")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid args made a request")
	}
}

func TestReplyChecksScopeAndNormalizesRoot(t *testing.T) {
	for _, mode := range []string{
		"root", "reply", "wrong-pr", "foreign-url", "nested-root", "wrong-root-pr", "wrong-repo", "wrong-root-repo",
	} {
		t.Run(mode, func(t *testing.T) {
			var posts atomic.Int32
			client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
					if r.URL.Path != pullPath(testRepository(), 42)+"/comments/7/replies" {
						t.Error("reply did not target root on selected PR")
					}
					var args map[string]string
					if json.NewDecoder(r.Body).Decode(&args) != nil || len(args) != 1 || args["body"] != "reply" {
						t.Error("incorrect reply body")
					}
					fmt.Fprint(w, `{"id":10,"in_reply_to_id":7}`)
					return
				}
				id, root := 7, 0
				if strings.HasSuffix(r.URL.Path, "/8") {
					id, root = 8, 7
				}
				url := "http://" + r.Host + pullPath(testRepository(), 42)
				if mode == "wrong-pr" || (mode == "wrong-root-pr" && id == 7) {
					url = "http://" + r.Host + repoPath(testRepository()) + "/pulls/43"
				}
				if mode == "foreign-url" {
					url = "https://attacker.example" + pullPath(testRepository(), 42)
				}
				if mode == "wrong-repo" || (mode == "wrong-root-repo" && id == 7) {
					url = "http://" + r.Host + "/repos/other/repository/pulls/42"
				}
				if mode == "nested-root" && id == 7 {
					root = 6
				}
				fmt.Fprintf(w, `{"id":%d,"in_reply_to_id":%d,"pull_request_url":%q}`, id, root, url)
			}))
			id := int64(7)
			if mode == "reply" || mode == "nested-root" || mode == "wrong-root-pr" || mode == "wrong-root-repo" {
				id = 8
			}
			comment, err := client.Reply(t.Context(), testScope(), id, "reply")
			if mode == "root" || mode == "reply" {
				if err != nil || posts.Load() != 1 || comment.InReplyToID != 7 {
					t.Fatalf("reply failed: %v", err)
				}
			} else if err == nil || posts.Load() != 0 {
				t.Fatal("unsafe reply was posted")
			}
		})
	}
}

func TestInlineCommentValidationFailureGuidesCorrection(t *testing.T) {
	var posts atomic.Int32
	client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"message":"Validation Failed","errors":[{"resource":"PullRequestReviewComment",`+
			`"field":"line","code":"invalid","message":"private-comment installation-token"}]}`)
	}))
	_, err := client.CreateInlineComment(t.Context(), testScope(), InlineCommentArgs{
		Body: "comment", CommitID: "commit", Path: "file.go", Line: 5, Side: "RIGHT",
	})
	apiErr := requireAPIError(t, err, PermanentFailure)
	if apiErr.StatusCode != http.StatusUnprocessableEntity || posts.Load() != 1 {
		t.Fatalf("validation failure = %v, posts = %d", err, posts.Load())
	}
	for _, text := range []string{"body", "commit_id", "path", "line", "side", "pull request diff"} {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("missing guidance %q: %v", text, err)
		}
	}
	if strings.Contains(err.Error(), "private-comment") || strings.Contains(err.Error(), "installation-token") {
		t.Fatalf("validation error exposed provider details: %v", err)
	}
}
