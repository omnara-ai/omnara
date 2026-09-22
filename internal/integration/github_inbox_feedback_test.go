package integration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type githubFeedbackFixture struct {
	mu       sync.Mutex
	app      integrationstore.ProjectAppRecord
	version  uuid.UUID
	payload  secrets.Payload
	revoked  bool
	beforePR func()
	requests []string
	posts    []string
	postCode int
	repoID   int64
}

func (f *githubFeedbackFixture) GetProjectApp(_ context.Context, projectID, appID uuid.UUID) (
	integrationstore.ProjectAppRecord, error,
) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if projectID != f.app.ProjectID || appID != f.app.ID {
		return integrationstore.ProjectAppRecord{}, storeerr.ErrUnauthorized
	}
	return f.app, nil
}

func (f *githubFeedbackFixture) ReadProjectAvailableSecretPayload(_ context.Context,
	input secretstore.ReadProjectAvailableSecretPayloadInput,
) (secretstore.SecretPayloadRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if input.ProjectID != f.app.ProjectID || input.OrgID != f.app.OrgID ||
		input.SecretID != f.app.CredentialSecretID || input.Kind != secrets.KindGitHubAppCredentials || f.revoked {
		return secretstore.SecretPayloadRecord{}, storeerr.ErrUnauthorized
	}
	return secretstore.SecretPayloadRecord{CurrentVersionID: f.version, Payload: f.payload}, nil
}

func (f *githubFeedbackFixture) GetProjectAvailableSecret(_ context.Context, orgID, projectID, secretID uuid.UUID) (
	secretstore.ProjectSecretAccessRecord, error,
) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if projectID != f.app.ProjectID || orgID != f.app.OrgID || secretID != f.app.CredentialSecretID || f.revoked {
		return secretstore.ProjectSecretAccessRecord{}, storeerr.ErrUnauthorized
	}
	return secretstore.ProjectSecretAccessRecord{Secret: secretstore.SecretRecord{
		Kind: secrets.KindGitHubAppCredentials, CurrentVersionID: f.version,
	}}, nil
}

func newGitHubFeedbackFixture(t *testing.T) (*githubFeedbackFixture, *GitHubAppInboxProvider) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	f := &githubFeedbackFixture{app: githubEventApp(), version: uuid.New(), repoID: 1001,
		payload: secrets.Payload{
			secrets.KeyAppID: "123", secrets.KeyPrivateKey: string(privateKey), secrets.KeyWebhookSecret: "webhook-secret",
		}}
	f.app.OrgID, f.app.CredentialSecretID, f.app.SetupRevision = uuid.New(), uuid.New(), 3
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/app/installations/456/access_tokens" {
			assert.Equal(t, http.MethodPost, r.Method)
			parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
			if assert.Len(t, parts, 3) {
				claims, err := base64.RawURLEncoding.DecodeString(parts[1])
				assert.NoError(t, err)
				var decoded struct {
					Issuer string `json:"iss"`
				}
				assert.NoError(t, json.Unmarshal(claims, &decoded))
				assert.Equal(t, "123", decoded.Issuer)
			}
			var grant struct {
				Repositories []int64           `json:"repository_ids"`
				Permissions  map[string]string `json:"permissions"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&grant))
			assert.Equal(t, []int64{1001}, grant.Repositories)
			assert.Equal(t, map[string]string{"pull_requests": "write"}, grant.Permissions)
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"token": "scoped-token", "expires_at": time.Now().Add(time.Hour),
			}))
			return
		}
		assert.Equal(t, "Bearer scoped-token", r.Header.Get("Authorization"))
		repo := github.Repository{ID: f.repoID, Name: "repository", Owner: github.User{Login: "owner"}}
		var response any
		switch r.URL.Path {
		case "/installation/repositories":
			response = map[string]any{"total_count": 1, "repositories": []github.Repository{repo}}
		case "/repos/owner/repository/pulls/42":
			if f.beforePR != nil {
				f.beforePR()
			}
			response = github.PullRequest{ID: 2001, Number: 42, Base: github.Branch{Repo: &repo}}
		case "/repos/owner/repository/pulls/comments/3001", "/repos/owner/repository/pulls/comments/3000":
			comment := github.ReviewComment{DiscussionComment: github.DiscussionComment{ID: 3000},
				PullRequestURL: "http://" + r.Host + "/repos/owner/repository/pulls/42"}
			if strings.HasSuffix(r.URL.Path, "/3001") {
				comment.ID, comment.InReplyToID = 3001, 3000
			}
			response = comment
		case "/repos/owner/repository/issues/42/comments", "/repos/owner/repository/pulls/42/comments/3000/replies":
			assert.Equal(t, http.MethodPost, r.Method)
			f.posts = append(f.posts, r.URL.Path)
			var body map[string]string
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, map[string]string{"body": inboxFailureMessage}, body)
			if f.postCode != 0 {
				w.WriteHeader(f.postCode)
				return
			}
			response = map[string]int{"id": 9001}
		default:
			t.Errorf("unexpected GitHub feedback request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	t.Cleanup(server.Close)
	provider := NewGitHubAppInboxProvider(github.Config{HTTPClient: server.Client(), APIURL: server.URL}, f, f)
	return f, provider
}

func TestGitHubNotifyInboxFailureHumanSources(t *testing.T) {
	for _, eventType := range []string{"issue_comment", "pull_request_review_comment", "pull_request_review"} {
		for _, source := range []string{"unplanned mention", "planned followup"} {
			t.Run(eventType+"/"+source, func(t *testing.T) {
				f, p := newGitHubFeedbackFixture(t)
				event := githubEventFixture(eventType)
				if source == "planned followup" {
					if event.Comment != nil {
						event.Comment.Body = "ordinary follow-up"
					} else {
						event.Review.Body = "ordinary follow-up"
					}
				}
				receipt := feedbackReceipt(f.app, githubEventJSON(t, event))
				if source == "planned followup" {
					normalized, ok, err := NormalizeGitHubAppEvent(f.app, receipt.Payload)
					require.NoError(t, err)
					require.True(t, ok)
					require.False(t, normalized.Event.Mentioned)
					receipt.Plan = feedbackPlan(t, normalized.Event.Scope)
				}
				require.NoError(t, p.NotifyInboxFailure(t.Context(), f.app, receipt, inboxFailureMessage))
				f.mu.Lock()
				defer f.mu.Unlock()
				want := "/repos/owner/repository/issues/42/comments"
				if eventType == "pull_request_review_comment" {
					want = "/repos/owner/repository/pulls/42/comments/3000/replies"
				}
				require.Equal(t, []string{want}, f.posts)
			})
		}
	}
}

func TestGitHubNotifyInboxFailureSkipsUnplannedHumanComments(t *testing.T) {
	app := githubEventApp()
	p := GitHubAppInboxProvider{}
	for _, eventType := range []string{"issue_comment", "pull_request_review_comment", "pull_request_review"} {
		t.Run(eventType, func(t *testing.T) {
			event := githubEventFixture(eventType)
			if event.Comment != nil {
				event.Comment.Body = "ordinary follow-up"
			} else {
				event.Review.Body = "ordinary follow-up"
			}
			receipt := feedbackReceipt(app, githubEventJSON(t, event))
			require.NoError(t, p.NotifyInboxFailure(t.Context(), app, receipt, inboxFailureMessage))
		})
	}
}

func TestGitHubNotifyInboxFailureSkipsAutomaticAndBotEvents(t *testing.T) {
	app := githubEventApp()
	p := GitHubAppInboxProvider{}
	for _, action := range []string{"opened", "synchronize"} {
		event := githubEventFixture("pull_request")
		event.Action, event.Before, event.After = action, strings.Repeat("a", 40), strings.Repeat("b", 40)
		receipt := feedbackReceipt(app, githubEventJSON(t, event))
		require.NoError(t, p.NotifyInboxFailure(t.Context(), app, receipt, inboxFailureMessage))
		normalized, ok, err := NormalizeGitHubAppEvent(app, receipt.Payload)
		require.NoError(t, err)
		require.True(t, ok)
		receipt.Plan = feedbackPlan(t, normalized.Event.Scope)
		require.NoError(t, p.NotifyInboxFailure(t.Context(), app, receipt, inboxFailureMessage))
	}
	event := githubEventFixture("issue_comment")
	event.Comment.User.Type = "Bot"
	require.NoError(t,
		p.NotifyInboxFailure(t.Context(), app, feedbackReceipt(app, githubEventJSON(t, event)), inboxFailureMessage))
}

func TestGitHubNotifyInboxFailureAuthorityAndScope(t *testing.T) {
	for _, change := range []string{"revision", "credential version", "revoked", "wrong App", "wrong repository"} {
		t.Run(change, func(t *testing.T) {
			f, p := newGitHubFeedbackFixture(t)
			app := f.app
			switch change {
			case "wrong App":
				f.payload[secrets.KeyAppID] = "999"
			case "wrong repository":
				f.repoID = 999
			default:
				f.beforePR = func() {
					switch change {
					case "revision":
						f.app.SetupRevision++
					case "credential version":
						f.version = uuid.New()
					default:
						f.revoked = true
					}
				}
			}
			err := p.NotifyInboxFailure(t.Context(), app,
				feedbackReceipt(app, githubEventJSON(t, githubEventFixture("issue_comment"))), inboxFailureMessage)
			require.Error(t, err)
			f.mu.Lock()
			defer f.mu.Unlock()
			require.Empty(t, f.posts)
			if change == "wrong App" {
				require.Empty(t, f.requests)
			}
		})
	}
}

func TestGitHubNotifyInboxFailureDoesNotRetryUnknownSend(t *testing.T) {
	f, p := newGitHubFeedbackFixture(t)
	f.postCode = http.StatusServiceUnavailable
	err := p.NotifyInboxFailure(t.Context(), f.app,
		feedbackReceipt(f.app, githubEventJSON(t, githubEventFixture("issue_comment"))), inboxFailureMessage)
	var apiErr *github.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, github.DeliveryUnknown, apiErr.Code)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.posts, 1)
}
