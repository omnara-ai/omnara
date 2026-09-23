package apps

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/apps/github"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func githubEventApp() appstore.ProjectAppRecord {
	return appstore.ProjectAppRecord{
		ID: uuid.New(), ProjectID: uuid.New(), Provider: "github", ProviderTenantID: "123", ProviderAccountRef: "456",
		State:            appstore.ProjectAppStateActive,
		ProviderIdentity: json.RawMessage(`{"bot_user_id":999,"bot_login":"helper[bot]"}`),
	}
}

func githubEventFixture(eventType string) githubEventPayload {
	human := github.User{ID: 71, Login: "human", Type: "User"}
	repo := github.Repository{ID: 1001, FullName: "owner/repository"}
	result := githubEventPayload{Webhook: github.Webhook{
		Action: "created", Sender: human, Installation: github.Installation{ID: 456}, Repository: repo,
	}}
	if eventType != "issue_comment" {
		result.PullRequest = &github.PullRequest{
			ID: 2001, Number: 42, Title: "Change", Body: "@helper review this",
			Base: github.Branch{Repo: &repo}, Head: github.Branch{SHA: strings.Repeat("b", 40)},
		}
	}
	switch eventType {
	case "issue_comment":
		result.Issue = &github.Issue{ID: 2001, Number: 42, PullRequest: &struct {
			URL string `json:"url"`
		}{URL: "https://api.github.com/repos/owner/repository/pulls/42"}}
		fallthrough
	case "pull_request_review_comment":
		result.Comment = &github.ReviewComment{
			DiscussionComment: github.DiscussionComment{ID: 3001, User: human, Body: "@helper please fix this"},
			Path:              "a.go", Side: "RIGHT", Line: new(15), StartLine: new(12), StartSide: "RIGHT", InReplyToID: 3000,
		}
	case "pull_request_review":
		result.Action = "submitted"
		result.Review = &githubReview{ID: 4001, User: human, State: "changes_requested", Body: "@helper please fix this"}
	case "pull_request":
		result.Action = "opened"
	}
	return result
}

func githubEventJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestGitHubNormalizeAppEvents(t *testing.T) {
	t.Parallel()
	definition, _ := appdefinition.Lookup(appdefinition.GitHubPR)
	for _, tc := range []struct {
		eventType string
		kind      string
		key       string
		steering  bool
	}{
		{"issue_comment", "discussion_comment", "github:1001:discussion_comment:3001:created", true},
		{"pull_request_review_comment", "review_comment", "github:1001:review_comment:3001:created", true},
		{"pull_request_review", "review_comment", "github:1001:review:4001:submitted", true},
		{"pull_request", "pull_request_opened", "github:1001:pull_request:2001:opened", false},
	} {
		t.Run(tc.eventType, func(t *testing.T) {
			t.Parallel()
			appSetup := githubEventApp()
			event, ok, err := NormalizeGitHubAppEvent(appSetup, githubEventJSON(t, githubEventFixture(tc.eventType)))
			if err != nil || !ok {
				t.Fatalf("normalize: ok=%v err=%v", ok, err)
			}
			if event.Event.Kind != tc.kind || event.SemanticKey != tc.key || event.CancelOpenInteractions != tc.steering {
				t.Fatalf("event: %+v", event)
			}
			if event.Event.Scope.GitHub.RepositoryID != 1001 || event.Event.Scope.GitHub.PullRequest != 42 {
				t.Fatalf("scope: %+v", event.Event.Scope)
			}
			if definition.MatchesLauncher(event.Event, "mention") != tc.steering ||
				definition.MatchesLauncher(event.Event, "pull_request_opened") == tc.steering {
				t.Fatalf("trigger: %+v", event.Event)
			}
			mode := executionstore.DeliveryModeQueued
			if tc.steering {
				mode = executionstore.DeliveryModeSteering
			}
			if event.DeliveryMode != mode || event.Actor.ProviderUserID != "71" ||
				event.Actor.Provider != executionstore.ActorProviderApp ||
				event.Actor.ProviderTenantID != appTestActor(t, appSetup.ID, "").ProviderTenantID {
				t.Fatalf("delivery/actor: %+v", event)
			}
			var blocks []map[string]string
			if json.Unmarshal(event.ContentBlocks, &blocks) != nil || len(blocks) != 1 || blocks[0]["type"] != "text" {
				t.Fatalf("blocks: %s", event.ContentBlocks)
			}
			var metadata GitHubEventMetadata
			if json.Unmarshal(event.Metadata, &metadata) != nil || metadata.RepositoryID != 1001 ||
				metadata.PullRequest != 42 || metadata.EventType != tc.eventType {
				t.Fatalf("metadata: %s", event.Metadata)
			}
			if tc.eventType == "pull_request_review_comment" &&
				(metadata.Line == nil || *metadata.Line != 15 || metadata.StartLine == nil || *metadata.StartLine != 12 ||
					metadata.Path != "a.go" || metadata.ReplyToID != 3000) {
				t.Fatalf("inline metadata: %s", event.Metadata)
			}
		})
	}
}

func TestGitHubSynchronizeQueuesAndUsesTransitionIdentity(t *testing.T) {
	t.Parallel()
	payload := githubEventFixture("pull_request")
	payload.Action, payload.Before, payload.After = "synchronize", strings.Repeat("a", 40), strings.Repeat("b", 40)
	event, ok, err := NormalizeGitHubAppEvent(githubEventApp(), githubEventJSON(t, payload))
	if err != nil || !ok || event.DeliveryMode != executionstore.DeliveryModeQueued ||
		event.CancelOpenInteractions || event.Event.Mentioned || event.Event.Kind != "commit" {
		t.Fatalf("commit: %+v %v %v", event, ok, err)
	}
	payload.Before = strings.Repeat("c", 40)
	other, ok, err := NormalizeGitHubAppEvent(githubEventApp(), githubEventJSON(t, payload))
	if err != nil || !ok || event.SemanticKey == other.SemanticKey {
		t.Fatalf("force-push transition identity: %+v %v %v", other, ok, err)
	}
	payload.Before = strings.Repeat("a", 40)
	payload.PullRequest.UpdatedAt = time.Unix(1000, 0)
	other, ok, err = NormalizeGitHubAppEvent(githubEventApp(), githubEventJSON(t, payload))
	if err != nil || !ok || event.SemanticKey == other.SemanticKey {
		t.Fatalf("repeated force-push transition: %+v %v %v", other, ok, err)
	}
}

func TestGitHubNormalizeIgnoresNonInputs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*githubEventPayload)
	}{
		{"edited", func(p *githubEventPayload) { p.Action = "edited" }},
		{"deleted", func(p *githubEventPayload) { p.Action = "deleted" }},
		{"issue only", func(p *githubEventPayload) { p.Issue.PullRequest = nil }},
		{"self sender", func(p *githubEventPayload) { p.Sender.ID = 999 }},
		{"self author", func(p *githubEventPayload) { p.Comment.User.ID = 999 }},
		{"self login", func(p *githubEventPayload) { p.Sender.Login = "HELPER[bot]" }},
		{"other bot", func(p *githubEventPayload) { p.Comment.User.Type = "Bot" }},
		{"bot sender", func(p *githubEventPayload) { p.Sender.Type = "Bot" }},
		{"ambiguous objects", func(p *githubEventPayload) { p.PullRequest = &github.PullRequest{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			payload := githubEventFixture("issue_comment")
			tc.edit(&payload)
			_, ok, err := NormalizeGitHubAppEvent(githubEventApp(), githubEventJSON(t, payload))
			if err != nil || ok {
				t.Fatalf("ignored input: ok=%v err=%v", ok, err)
			}
		})
	}
	for _, raw := range []string{
		`{"zen":"Keep it logically awesome","hook":{"id":123}}`,
		`{"action":"deleted","installation":{"id":456}}`,
		`{"ref":"refs/heads/main","commits":[{"id":"abc"}]}`,
	} {
		appSetup := githubEventApp()
		appSetup.ProviderIdentity = nil
		_, ok, err := NormalizeGitHubAppEvent(appSetup, []byte(raw))
		if err != nil || ok {
			t.Fatalf("unsupported input: ok=%v err=%v", ok, err)
		}
	}
}

func TestGitHubNormalizeRejectsWrongIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*githubEventPayload, *appstore.ProjectAppRecord)
	}{
		{"installation", func(p *githubEventPayload, _ *appstore.ProjectAppRecord) {
			p.Installation.ID++
		}},
		{"app", func(p *githubEventPayload, _ *appstore.ProjectAppRecord) {
			p.Installation.AppID = 321
		}},
		{"repository", func(p *githubEventPayload, _ *appstore.ProjectAppRecord) { p.Repository.ID++ }},
		{"missing repository", func(p *githubEventPayload, _ *appstore.ProjectAppRecord) {
			p.PullRequest.Base.Repo = nil
		}},
		{"PR number", func(p *githubEventPayload, _ *appstore.ProjectAppRecord) {
			p.PullRequest.Number = 0
		}},
		{"comment ID", func(p *githubEventPayload, _ *appstore.ProjectAppRecord) { p.Comment.ID = 0 }},
		{"sender", func(p *githubEventPayload, _ *appstore.ProjectAppRecord) { p.Sender.ID++ }},
		{"bot identity absent", func(_ *githubEventPayload, c *appstore.ProjectAppRecord) {
			c.ProviderIdentity = nil
		}},
		{"wrong provider", func(_ *githubEventPayload, c *appstore.ProjectAppRecord) {
			c.Provider = "slack"
		}},
		{"noncanonical app", func(_ *githubEventPayload, c *appstore.ProjectAppRecord) {
			c.ProviderTenantID = "0123"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			appSetup := githubEventApp()
			payload := githubEventFixture("pull_request_review_comment")
			tc.edit(&payload, &appSetup)
			_, ok, err := NormalizeGitHubAppEvent(appSetup, githubEventJSON(t, payload))
			if err == nil || ok {
				t.Fatalf("invalid input: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestGitHubNormalizationStableOnRenameAndHeaderReplay(t *testing.T) {
	t.Parallel()
	appSetup := githubEventApp()
	payload := githubEventFixture("pull_request_review_comment")
	before, _, err := NormalizeGitHubAppEvent(appSetup, githubEventJSON(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	payload.Repository.FullName = "new-owner/new-name"
	raw := githubEventJSON(t, payload)
	raw = append([]byte(`{"event_type":"pull_request","delivery_id":"different",`), raw[1:]...)
	after, ok, err := NormalizeGitHubAppEvent(appSetup, raw)
	if err != nil || !ok || before.SemanticKey != after.SemanticKey ||
		*before.Event.Scope.GitHub != *after.Event.Scope.GitHub || before.Event.Mentioned != after.Event.Mentioned {
		t.Fatalf("rename/replay changed identity: %+v %v", after, err)
	}
	if after.Event.Kind != "review_comment" || !strings.HasPrefix(after.DisplayName, "new-owner/new-name#") {
		t.Fatalf("renamed input: %+v", after)
	}
	payload.Repository.ID++
	payload.PullRequest.Base.Repo.ID = payload.Repository.ID
	payload.Repository.FullName = "owner/repository"
	other, ok, err := NormalizeGitHubAppEvent(appSetup, githubEventJSON(t, payload))
	if err != nil || !ok || other.Event.Scope.GitHub.RepositoryID == before.Event.Scope.GitHub.RepositoryID {
		t.Fatalf("name reuse: %+v %v", other, err)
	}
}

func TestGitHubMentionBoundaries(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		"@helper fix", "(@HELPER)", "@helper[bot]: fix", "hello\n@helper!", "@helper", "@helper.",
	} {
		if !githubMentionsBot(body, "helper[bot]") {
			t.Errorf("missing mention: %q", body)
		}
	}
	for _, body := range []string{"helper", "a@helper", "@helper-else", "@helper_else", "@helper[wrong]", "@helper.com"} {
		if githubMentionsBot(body, "helper[bot]") {
			t.Errorf("false mention: %q", body)
		}
	}
}

func TestGitHubInboxAdapter(t *testing.T) {
	t.Parallel()
	provider := GitHubAppInboxProvider{}
	appSetup := githubEventApp()
	expansion, err := provider.Expand(t.Context(), appSetup, githubEventJSON(t, githubEventFixture("issue_comment")))
	if err != nil || len(expansion.Events) != 1 || len(expansion.Files) != 0 {
		t.Fatalf("expansion: %+v %v", expansion, err)
	}
	if _, err = provider.DownloadFile(t.Context(), appSetup, nil, "file"); err == nil {
		t.Fatal("unexpected file download")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = provider.Expand(ctx, appSetup, []byte(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled expansion: %v", err)
	}
	for _, raw := range [][]byte{nil, []byte("null"), []byte("[]"), []byte("{"), {0xff},
		[]byte(strings.Repeat(" ", appstore.AppInboxMaxPayloadBytes+1))} {
		if _, _, err = NormalizeGitHubAppEvent(appSetup, raw); err == nil {
			t.Fatal("accepted invalid JSON")
		}
	}
}

func TestGitHubReviewAndCommitPolicies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		eventType string
		edit      func(*githubEventPayload)
		wantEvent bool
		wantError bool
	}{
		{"empty approval", "pull_request_review", func(p *githubEventPayload) {
			p.Review.State, p.Review.Body = "approved", ""
		}, true, false},
		{"empty changes requested", "pull_request_review", func(p *githubEventPayload) {
			p.Review.State, p.Review.Body = "changes_requested", ""
		}, true, false},
		{"empty commented", "pull_request_review", func(p *githubEventPayload) {
			p.Review.State, p.Review.Body = "commented", ""
		}, false, false},
		{"whitespace commented", "pull_request_review", func(p *githubEventPayload) {
			p.Review.State, p.Review.Body = "commented", " \n\t\r"
		}, false, false},
		{"commented with body", "pull_request_review", func(p *githubEventPayload) {
			p.Review.State = "commented"
		}, true, false},
		{"blank review wrong sender", "pull_request_review", func(p *githubEventPayload) {
			p.Review.State, p.Review.Body = "commented", ""
			p.Sender.ID++
		}, false, true},
		{"pending review", "pull_request_review", func(p *githubEventPayload) {
			p.Action, p.Review.State = "edited", "pending"
		}, false, false},
		{"bad submitted state", "pull_request_review", func(p *githubEventPayload) {
			p.Review.State = "pending"
		}, false, true},
		{"review without identity", "pull_request_review", func(p *githubEventPayload) {
			p.Review.ID = 0
		}, false, true},
		{"PR closed", "pull_request", func(p *githubEventPayload) { p.Action = "closed" }, false, false},
		{"self PR open", "pull_request", func(p *githubEventPayload) { p.Sender.ID = 999 }, false, false},
		{"other bot PR open", "pull_request", func(p *githubEventPayload) { p.Sender.Type = "Bot" }, true, false},
		{"invalid commit", "pull_request", func(p *githubEventPayload) {
			p.Action, p.Before, p.After = "synchronize", "bad", strings.Repeat("b", 40)
		}, false, true},
		{"commit mismatches head", "pull_request", func(p *githubEventPayload) {
			p.Action, p.Before, p.After = "synchronize", strings.Repeat("a", 40), strings.Repeat("c", 40)
		}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			payload := githubEventFixture(tc.eventType)
			tc.edit(&payload)
			event, ok, err := NormalizeGitHubAppEvent(githubEventApp(), githubEventJSON(t, payload))
			if ok != tc.wantEvent || (err != nil) != tc.wantError {
				t.Fatalf("policy: event=%+v ok=%v err=%v", event, ok, err)
			}
			if tc.wantEvent && tc.eventType == "pull_request_review" {
				if !strings.Contains(string(event.ContentBlocks), "Review "+payload.Review.State+":") ||
					event.DeliveryMode != executionstore.DeliveryModeSteering || !event.CancelOpenInteractions {
					t.Fatalf("review lacks useful steering content: %+v", event)
				}
			}
		})
	}
}
