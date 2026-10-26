package integration

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
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
	githubCheckClaimLease         = time.Minute
	githubCheckClaimDeadline      = 45 * time.Second
	githubCheckRetryForgetAfter   = time.Hour
)

type GitHubCheckReconciler struct {
	Store     *storage.Store
	GitHub    github.Config
	PublicURL string
	claim     uuid.UUID
	after     uuid.UUID
	pageSize  int32
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
	if r.claim == uuid.Nil {
		r.claim, r.retries = uuid.New(), make(map[uuid.UUID]githubCheckRetry)
	}
	pageSize := cmp.Or(r.pageSize, githubCheckRunBatchSize)
	runs, err := r.Store.Integrations().ListGitHubCheckRuns(ctx, r.claim, r.after, pageSize)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		r.after = uuid.Nil
		return nil
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
	r.after = runs[len(runs)-1].ID
	if len(runs) < int(pageSize) {
		r.after = uuid.Nil
	}
	now := time.Now()
	maps.DeleteFunc(r.retries, func(_ uuid.UUID, retry githubCheckRetry) bool {
		return now.Sub(retry.at) > githubCheckRetryForgetAfter
	})
	var work []githubCheckWork
	for _, run := range runs {
		retry, state := r.retries[run.ID], activity[run.AgentID].State
		if githubCheckDue(run, state, retry, now) {
			work = append(work, githubCheckWork{run: run, state: state, retry: retry})
		}
	}
	var mu sync.Mutex
	var failures []error
	var group errgroup.Group
	group.SetLimit(githubCheckRunConcurrency)
	for _, item := range work {
		group.Go(func() error {
			err := r.reconcile(ctx, &item.run, item.state)
			if err == nil {
				mu.Lock()
				defer mu.Unlock()
				delete(r.retries, item.run.ID)
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
			if giveUp {
				delete(r.retries, item.run.ID)
			} else {
				retry.at = failedAt.Add(integrationInboxRetryDelay(retry.attempts, err))
				r.retries[item.run.ID] = retry
			}
			if !errors.Is(err, github.ErrChecksPermissionRequired) {
				failures = append(failures, fmt.Errorf("github check for agent %s: %w", item.run.AgentID, err))
			}
			return nil
		})
	}
	_ = group.Wait()
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
	return !now.Before(retry.at) && (run.Unfinished || !githubCheckCurrent(run, state))
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
	ctx context.Context, tracked *integrationstore.GitHubCheckRun, state string,
) error {
	running := state == executionstore.SubagentStateRunning
	waiting := state == executionstore.SubagentStateWaitingOnInteraction
	if !running && !waiting && tracked.Done && !tracked.Restart && !tracked.Unfinished {
		return ignoreConflict(r.Store.Integrations().DeleteGitHubCheckRun(ctx, *tracked))
	}
	if err := r.Store.Integrations().ClaimGitHubCheckRun(ctx, tracked, r.claim, githubCheckClaimLease); err != nil {
		return ignoreConflict(err)
	}
	ctx, cancel := context.WithTimeout(ctx, githubCheckClaimDeadline)
	defer cancel()
	run := *tracked
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
