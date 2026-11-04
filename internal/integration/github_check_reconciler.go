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
	Store       *storage.Store
	GitHub      github.Config
	PublicURL   string
	claim       uuid.UUID
	after       uuid.UUID
	pageSize    int32
	retries     map[uuid.UUID]githubCheckRetry
	paused      map[string]time.Time
	passStarted time.Time
}

type GitHubCheckPass struct {
	Worked   bool
	FullPass time.Duration
	Retrying int
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

func (r *GitHubCheckReconciler) Reconcile(ctx context.Context) (GitHubCheckPass, error) {
	if r.claim == uuid.Nil {
		r.claim, r.retries, r.paused = uuid.New(), make(map[uuid.UUID]githubCheckRetry), make(map[string]time.Time)
	}
	now := time.Now()
	if r.after == uuid.Nil {
		r.passStarted = now
	}
	pageSize := cmp.Or(r.pageSize, githubCheckRunBatchSize)
	runs, err := r.Store.Integrations().ListGitHubCheckRuns(ctx, r.claim, r.after, pageSize)
	if err != nil {
		return GitHubCheckPass{Retrying: len(r.retries)}, err
	}
	maps.DeleteFunc(r.retries, func(_ uuid.UUID, retry githubCheckRetry) bool {
		return now.Sub(retry.at) > githubCheckRetryForgetAfter
	})
	maps.DeleteFunc(r.paused, func(_ string, until time.Time) bool { return !now.Before(until) })
	var ready []integrationstore.GitHubCheckRun
	for _, run := range runs {
		if !now.Before(r.retries[run.ID].at) && !now.Before(r.paused[run.Installation]) {
			ready = append(ready, run)
		}
	}
	var states map[uuid.UUID]string
	if len(ready) > 0 {
		projectIDs := make([]uuid.UUID, 0, len(ready))
		agentIDs := make([]uuid.UUID, 0, len(ready))
		for _, run := range ready {
			projectIDs = append(projectIDs, run.ProjectID)
			agentIDs = append(agentIDs, run.AgentID)
		}
		if states, err = r.Store.Execution().ListAgentTreeStates(ctx, projectIDs, agentIDs); err != nil {
			return GitHubCheckPass{Retrying: len(r.retries)}, err
		}
	}
	finished := len(runs) < int(pageSize)
	if finished {
		r.after = uuid.Nil
	} else {
		r.after = runs[len(runs)-1].ID
	}
	var work []githubCheckWork
	for _, run := range ready {
		retry, state := r.retries[run.ID], states[run.AgentID]
		if githubCheckDue(run, state) {
			work = append(work, githubCheckWork{run: run, state: state, retry: retry})
		}
	}
	var mu sync.Mutex
	var failures []error
	var group errgroup.Group
	group.SetLimit(githubCheckRunConcurrency)
	for _, item := range work {
		group.Go(func() error {
			mu.Lock()
			paused := time.Now().Before(r.paused[item.run.Installation])
			mu.Unlock()
			if paused {
				return nil
			}
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
				var apiErr *github.APIError
				if errors.As(err, &apiErr) && apiErr.Code == github.RateLimited &&
					retry.at.After(r.paused[item.run.Installation]) {
					r.paused[item.run.Installation] = retry.at
				}
			}
			if !errors.Is(err, github.ErrChecksPermissionRequired) {
				failures = append(failures, fmt.Errorf("github check for agent %s: %w", item.run.AgentID, err))
			}
			return nil
		})
	}
	_ = group.Wait()
	pass := GitHubCheckPass{Worked: len(work) > 0, Retrying: len(r.retries)}
	if finished {
		pass.FullPass = time.Since(r.passStarted)
	}
	return pass, errors.Join(failures...)
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

func githubCheckDue(run integrationstore.GitHubCheckRun, state string) bool {
	return run.Unfinished || !githubCheckCurrent(run, state)
}

func githubCheckCurrent(run integrationstore.GitHubCheckRun, state string) bool {
	switch state {
	case executionstore.SubagentStateRunning:
		return run.Run != 0 && !run.Waiting && !run.Restart
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
	ctx, cancel := context.WithTimeout(ctx, githubCheckClaimDeadline)
	defer cancel()
	if err := r.Store.Integrations().ClaimGitHubCheckRun(ctx, tracked, r.claim, githubCheckClaimLease); err != nil {
		return ignoreConflict(err)
	}
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
	finish := func(next githubCheckOutcome) (int64, error) {
		if run.Run != 0 && !run.Restart {
			return run.Run, client.CompleteCheckRun(ctx, scope, run.Run, check(next))
		}
		return replace(next)
	}
	switch {
	case running:
		checkRunID, err := replace(githubCheckWorking)
		if err != nil {
			return err
		}
		run.Run, run.Waiting, run.Done = checkRunID, false, false
		err = r.Store.Integrations().SaveGitHubCheckRun(ctx, run)
		if errors.Is(err, storeerr.ErrConflict) {
			return client.CompleteCheckRun(ctx, scope, checkRunID, check(githubCheckSuperseded))
		}
		return err
	case waiting:
		checkRunID, err := finish(githubCheckWaiting)
		if err != nil {
			return err
		}
		run.Run, run.Waiting, run.Done = checkRunID, true, false
		return ignoreConflict(r.Store.Integrations().SaveGitHubCheckRun(ctx, run))
	default:
		if _, err := finish(githubCheckFinished); err != nil {
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
	check := github.CheckRun{
		Name: cmp.Or(strings.TrimSpace(setup.ProviderAgentDisplayName), "Omnara") + " (" + setup.Name + ")",
	}
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
