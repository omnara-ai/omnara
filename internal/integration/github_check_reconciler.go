package integration

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"golang.org/x/sync/errgroup"
)

const (
	githubCheckRunBatchSize       = 1000
	githubCheckRunConcurrency     = 16
	githubCheckOpenRunRetryWindow = 24 * time.Hour
)

type GitHubCheckReconciler struct {
	Store     *storage.Store
	GitHub    github.Config
	PublicURL string
	retries   map[uuid.UUID]githubCheckRetry
}

type githubCheckRetry struct {
	attempts int
	since    time.Time
	at       time.Time
}

type githubCheckWork struct {
	run   integrationstore.GitHubCheckRun
	state string
	retry githubCheckRetry
}

func (r *GitHubCheckReconciler) Reconcile(ctx context.Context) error {
	now := time.Now()
	retries := make(map[uuid.UUID]githubCheckRetry)
	var work []githubCheckWork
	var after uuid.UUID
	for {
		runs, err := r.Store.Integrations().ListGitHubCheckRuns(ctx, after, githubCheckRunBatchSize)
		if err != nil {
			return err
		}
		if len(runs) == 0 {
			break
		}
		projectIDs := make([]uuid.UUID, 0, len(runs))
		agentIDs := make([]uuid.UUID, 0, len(runs))
		for _, run := range runs {
			projectIDs = append(projectIDs, run.ProjectID)
			agentIDs = append(agentIDs, run.AgentID)
		}
		activity, err := r.Store.Execution().ListAgentTreeActivity(ctx, projectIDs, agentIDs)
		if err != nil {
			return err
		}
		for _, run := range runs {
			retry, state := r.retries[run.ID], activity[run.AgentID].State
			if githubCheckDue(run, state, retry, now) {
				work = append(work, githubCheckWork{run: run, state: state, retry: retry})
			} else if now.Before(retry.at) {
				retries[run.ID] = retry
			}
		}
		if len(runs) < githubCheckRunBatchSize {
			break
		}
		after = runs[len(runs)-1].ID
	}
	var mu sync.Mutex
	var failures []error
	var group errgroup.Group
	group.SetLimit(githubCheckRunConcurrency)
	for _, item := range work {
		group.Go(func() error {
			err := r.reconcile(ctx, item.run, item.state)
			if err == nil {
				return nil
			}
			failedAt, retry := time.Now(), item.retry
			if retry.attempts == 0 {
				retry.since = failedAt
			}
			retry.attempts++
			giveUp := githubCheckGiveUp(err, retry, item.run.Run != 0, failedAt)
			if giveUp {
				err = errors.Join(err, ignoreConflict(r.Store.Integrations().DeleteGitHubCheckRun(ctx, item.run)))
			}
			mu.Lock()
			defer mu.Unlock()
			if !giveUp {
				retry.at = failedAt.Add(integrationInboxRetryDelay(retry.attempts, err))
				retries[item.run.ID] = retry
			}
			if !errors.Is(err, github.ErrChecksPermissionRequired) {
				failures = append(failures, fmt.Errorf("github check for agent %s: %w", item.run.AgentID, err))
			}
			return nil
		})
	}
	_ = group.Wait()
	r.retries = retries
	return errors.Join(failures...)
}

func githubCheckGiveUp(err error, retry githubCheckRetry, open bool, now time.Time) bool {
	switch {
	case errors.Is(err, github.ErrChecksPermissionRequired):
		return true
	case retry.attempts < integrationstore.IntegrationInboxMaxAttempts:
		return false
	default:
		return !open || now.Sub(retry.since) >= githubCheckOpenRunRetryWindow
	}
}

func githubCheckDue(run integrationstore.GitHubCheckRun, state string, retry githubCheckRetry, now time.Time) bool {
	return !now.Before(retry.at) && (retry.attempts > 0 || !githubCheckCurrent(run, state))
}

func githubCheckCurrent(run integrationstore.GitHubCheckRun, state string) bool {
	switch state {
	case executionstore.SubagentStateRunning:
		return run.Run != 0 && !run.Restart
	case executionstore.SubagentStateWaitingOnInteraction:
		return run.Waiting && !run.Restart
	default:
		return false
	}
}

type githubCheckOutcome struct{ conclusion, title string }

var (
	githubCheckWorking    = githubCheckOutcome{"", "Working"}
	githubCheckWaiting    = githubCheckOutcome{"action_required", "Waiting for input"}
	githubCheckFinished   = githubCheckOutcome{"neutral", "Finished"}
	githubCheckSuperseded = githubCheckOutcome{"neutral", "Superseded"}
)

func (r *GitHubCheckReconciler) reconcile(
	ctx context.Context, run integrationstore.GitHubCheckRun, state string,
) error {
	running := state == executionstore.SubagentStateRunning
	waiting := state == executionstore.SubagentStateWaitingOnInteraction
	if !running && !waiting && run.Done && !run.Restart {
		return ignoreConflict(r.Store.Integrations().DeleteGitHubCheckRun(ctx, run))
	}
	client, scope, setup, err := r.client(ctx, run)
	if err != nil {
		return err
	}
	base := r.check(ctx, run, setup)
	check := func(outcome githubCheckOutcome) github.CheckRun {
		base.Conclusion, base.Title = outcome.conclusion, outcome.title
		return base
	}
	replace := func(next githubCheckOutcome) (int64, error) {
		if run.Run != 0 {
			if err := client.CompleteCheckRun(ctx, scope, run.Run, check(githubCheckSuperseded)); err != nil {
				return 0, err
			}
		}
		return client.CreateCheckRun(ctx, scope, check(next))
	}
	finish := func(next githubCheckOutcome) error {
		if run.Run != 0 && !run.Restart {
			return client.CompleteCheckRun(ctx, scope, run.Run, check(next))
		}
		_, err := replace(next)
		return err
	}
	switch {
	case running:
		checkRunID, err := replace(githubCheckWorking)
		if err != nil {
			return err
		}
		run.Run, run.Waiting, run.Done = checkRunID, false, false
		if err := r.Store.Integrations().SaveGitHubCheckRun(ctx, run); err != nil {
			return errors.Join(ignoreConflict(err),
				client.CompleteCheckRun(ctx, scope, checkRunID, check(githubCheckSuperseded)))
		}
		return nil
	case waiting:
		if err := finish(githubCheckWaiting); err != nil {
			return err
		}
		run.Run, run.Waiting, run.Done = 0, true, false
		return ignoreConflict(r.Store.Integrations().SaveGitHubCheckRun(ctx, run))
	default:
		if err := finish(githubCheckFinished); err != nil {
			return err
		}
		run.Run, run.Waiting, run.Done = 0, false, true
		return ignoreConflict(r.Store.Integrations().SaveGitHubCheckRun(ctx, run))
	}
}

func (r *GitHubCheckReconciler) client(
	ctx context.Context, run integrationstore.GitHubCheckRun,
) (*github.Client, github.Scope, integrationstore.IntegrationRecord, error) {
	setup, err := r.Store.Integrations().GetIntegration(ctx, run.ProjectID, run.IntegrationID)
	if err != nil {
		return nil, github.Scope{}, setup, err
	}
	conversation, err := integrationdefinition.ParseConversation(setup.Provider, run.Address.Kind, run.Address.Ref)
	if err != nil {
		return nil, github.Scope{}, setup, err
	}
	client, err := NewGitHubIntegrationInboxProvider(r.GitHub, r.Store.Secrets(), r.Store.Integrations()).
		requestAccess(ctx, setup)
	if err != nil {
		return nil, github.Scope{}, setup, err
	}
	return client, github.Scope{
		RepositoryID: conversation.GitHub.RepositoryID, PullRequest: conversation.GitHub.PullRequest,
	}, setup, nil
}

func (r *GitHubCheckReconciler) check(
	ctx context.Context, run integrationstore.GitHubCheckRun, setup integrationstore.IntegrationRecord,
) github.CheckRun {
	check := github.CheckRun{Name: cmp.Or(strings.TrimSpace(setup.ProviderAgentDisplayName), "Omnara")}
	project, _ := publicid.Encode(publicid.KindProject, run.ProjectID)
	agentID, _ := publicid.Encode(publicid.KindAgent, run.AgentID)
	facts := []string{"- **Agent:** `" + agentID + "`"}
	if agent, err := r.Store.Execution().GetAgentInProject(ctx, run.ProjectID, run.AgentID); err == nil {
		if profileID, err := publicid.Encode(publicid.KindAgentProfile, agent.AgentProfileID); err == nil {
			facts = append(facts, "- **Profile:** `"+profileID+"`")
		}
	}
	facts = append(facts, "- **Project:** `"+project+"`", "- **Integration:** `"+setup.Name+"`")
	if r.PublicURL != "" {
		check.DetailsURL = r.PublicURL + "/projects/" + project + "/agents/" + agentID + "/chat"
		facts = append([]string{"Open in Omnara: " + check.DetailsURL, ""}, facts...)
	}
	check.Summary = strings.Join(facts, "\n")
	return check
}

func ignoreConflict(err error) error {
	if errors.Is(err, storeerr.ErrConflict) {
		return nil
	}
	return err
}
