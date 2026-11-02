package integration

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func TestGitHubCheckGiveUp(t *testing.T) {
	now := time.Now()
	transient := &github.APIError{Code: github.TransientFailure}
	day := 24 * time.Hour
	exhausted := integrationstore.IntegrationInboxMaxAttempts
	for _, test := range []struct {
		name  string
		err   error
		retry githubCheckRetry
		open  bool
		want  bool
	}{
		{"missing permission", github.ErrChecksPermissionRequired, githubCheckRetry{attempts: 1, since: now}, true, true},
		{"early failure", transient, githubCheckRetry{attempts: exhausted - 1, since: now}, false, false},
		{"exhausted without open run", transient, githubCheckRetry{attempts: exhausted, since: now}, false, true},
		{"open run within window", transient, githubCheckRetry{attempts: 100, since: now.Add(time.Hour - day)}, true, false},
		{"open run past window", errors.New("down"), githubCheckRetry{attempts: 300, since: now.Add(-day)}, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := githubCheckGiveUp(test.err, test.retry, test.open, now); got != test.want {
				t.Fatalf("githubCheckGiveUp() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestGitHubCheckCurrent(t *testing.T) {
	running, waiting := executionstore.SubagentStateRunning, executionstore.SubagentStateWaitingOnInteraction
	for _, test := range []struct {
		name  string
		run   integrationstore.GitHubCheckRun
		state string
		want  bool
	}{
		{"running with open run", integrationstore.GitHubCheckRun{Run: 1}, running, true},
		{"running after new commit", integrationstore.GitHubCheckRun{Run: 1, Restart: true}, running, false},
		{"running without run", integrationstore.GitHubCheckRun{}, running, false},
		{"running after waiting", integrationstore.GitHubCheckRun{Run: 1, Waiting: true}, running, false},
		{"waiting shown", integrationstore.GitHubCheckRun{Run: 1, Waiting: true}, waiting, true},
		{"waiting after new commit", integrationstore.GitHubCheckRun{Waiting: true, Restart: true}, waiting, false},
		{"idle", integrationstore.GitHubCheckRun{Run: 1}, executionstore.SubagentStateIdle, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := githubCheckCurrent(test.run, test.state); got != test.want {
				t.Fatalf("githubCheckCurrent() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestGitHubCheckState(t *testing.T) {
	idle, running := executionstore.SubagentStateIdle, executionstore.SubagentStateRunning
	waiting, archived := executionstore.SubagentStateWaitingOnInteraction, executionstore.SubagentStateArchived
	busy := executionstore.AgentActivityIDs{Count: 1}
	for _, test := range []struct {
		name     string
		activity executionstore.AgentActivity
		want     string
	}{
		{"idle", executionstore.AgentActivity{State: idle}, idle},
		{"idle with a running process", executionstore.AgentActivity{
			State: idle, AgentActivityWork: executionstore.AgentActivityWork{RunningProcesses: busy},
		}, running},
		{"idle with a working subagent", executionstore.AgentActivity{
			State: idle, AgentActivityWork: executionstore.AgentActivityWork{WorkingSubagents: busy},
		}, running},
		{"waiting with a running process", executionstore.AgentActivity{
			State: waiting, AgentActivityWork: executionstore.AgentActivityWork{RunningProcesses: busy},
		}, waiting},
		{"running", executionstore.AgentActivity{State: running}, running},
		{"archived", executionstore.AgentActivity{State: archived}, archived},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := githubCheckState(test.activity); got != test.want {
				t.Fatalf("githubCheckState() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestGitHubCheckName(t *testing.T) {
	setup := integrationstore.IntegrationRecord{ID: uuid.New(), Name: "reviews", ProviderAgentDisplayName: "my-app"}
	for _, test := range []struct {
		name   string
		setup  integrationstore.IntegrationRecord
		oldest uuid.UUID
		want   string
	}{
		{"only integration on the App", setup, setup.ID, "my-app"},
		{"later integration sharing the App", setup, uuid.New(), "my-app (reviews)"},
		{"sharing lookup unavailable", setup, uuid.Nil, "my-app"},
		{"App without a display name", integrationstore.IntegrationRecord{ID: setup.ID}, setup.ID, "Omnara"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := githubCheckName(test.setup, test.oldest); got != test.want {
				t.Fatalf("githubCheckName() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestGitHubCheckDue(t *testing.T) {
	now := time.Now()
	open := integrationstore.GitHubCheckRun{Run: 1}
	unfinished := integrationstore.GitHubCheckRun{Run: 1, Unfinished: true}
	running := executionstore.SubagentStateRunning
	for _, test := range []struct {
		name  string
		run   integrationstore.GitHubCheckRun
		retry githubCheckRetry
		want  bool
	}{
		{"current", open, githubCheckRetry{}, false},
		{"stale", integrationstore.GitHubCheckRun{}, githubCheckRetry{}, true},
		{"current but unfinished", unfinished, githubCheckRetry{attempts: 1, at: now.Add(-time.Second)}, true},
		{"unfinished after a restart", unfinished, githubCheckRetry{}, true},
		{"backing off", unfinished, githubCheckRetry{attempts: 1, at: now.Add(time.Second)}, false},
		{"stale while backing off", integrationstore.GitHubCheckRun{}, githubCheckRetry{at: now.Add(time.Second)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := githubCheckDue(test.run, running, test.retry, now); got != test.want {
				t.Fatalf("githubCheckDue() = %v, want %v", got, test.want)
			}
		})
	}
}
