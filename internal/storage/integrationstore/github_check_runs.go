package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const githubCheckRunStateKind = "github_check_run"

type GitHubCheckRun struct {
	ID            uuid.UUID
	ProjectID     uuid.UUID
	IntegrationID uuid.UUID
	AgentID       uuid.UUID
	Address       ConversationAddress
	Run           int64
	Waiting       bool
	Done          bool
	Restart       bool
	Unfinished    bool
	revision      int64
}

type githubCheckRunData struct {
	AgentID uuid.UUID `json:"agent_id"`
	Run     int64     `json:"run,omitempty"`
	Waiting bool      `json:"waiting,omitempty"`
	Done    bool      `json:"done"`
	Restart bool      `json:"restart,omitempty"`
	Claim   string    `json:"claim,omitempty"`
}

func (s *Store) MarkGitHubCheckRun(
	ctx context.Context, projectID, integrationID, agentID uuid.UUID, address ConversationAddress, restart bool,
) error {
	data, err := encodeIntegrationState(githubCheckRunData{AgentID: agentID, Restart: restart})
	if err != nil {
		return err
	}
	return s.q.MergeIntegrationState(ctx, dbsqlc.MergeIntegrationStateParams{
		ProjectID: projectID, IntegrationID: integrationID, Kind: githubCheckRunStateKind,
		Key: agentID.String() + " " + address.Ref, ScopeKind: &address.Kind, ScopeRef: &address.Ref, Data: data,
		Force: restart,
	})
}

func (s *Store) RestartGitHubCheckRuns(
	ctx context.Context, projectID, integrationID uuid.UUID, address ConversationAddress,
) error {
	if err := s.q.MergeIntegrationStatesInScope(ctx, dbsqlc.MergeIntegrationStatesInScopeParams{
		Data: json.RawMessage(`{"restart":true,"done":false}`), ProjectID: projectID, IntegrationID: integrationID,
		Kind: githubCheckRunStateKind, ScopeKind: &address.Kind, ScopeRef: &address.Ref,
	}); err != nil {
		return err
	}
	owners, err := s.q.ListConversationLaunchOwners(ctx, dbsqlc.ListConversationLaunchOwnersParams{
		ProjectID: projectID, IntegrationID: integrationID, Kind: address.Kind, Ref: address.Ref,
	})
	if err != nil {
		return err
	}
	for _, owner := range owners {
		if owner.DeletedAt != nil {
			continue
		}
		if err := s.MarkGitHubCheckRun(ctx, projectID, integrationID, owner.AgentID, address, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListGitHubCheckRuns(
	ctx context.Context, claim, after uuid.UUID, limit int32,
) ([]GitHubCheckRun, error) {
	states, err := s.q.ListActiveGitHubCheckRunStates(ctx, dbsqlc.ListActiveGitHubCheckRunStatesParams{
		AfterID: after, Claim: claim.String(), RowLimit: limit,
	})
	if err != nil {
		return nil, err
	}
	runs := make([]GitHubCheckRun, 0, len(states))
	for _, state := range states {
		var data githubCheckRunData
		if err := json.Unmarshal(state.Data, &data); err != nil {
			return nil, err
		}
		runs = append(runs, GitHubCheckRun{
			ID: state.ID, ProjectID: state.ProjectID, IntegrationID: state.IntegrationID, AgentID: data.AgentID,
			Address: ConversationAddress{
				Kind: storeutil.TextOrEmpty(state.ScopeKind), Ref: storeutil.TextOrEmpty(state.ScopeRef),
			},
			Run: data.Run, Waiting: data.Waiting, Done: data.Done, Restart: data.Restart, Unfinished: data.Claim != "",
			revision: state.Revision,
		})
	}
	return runs, nil
}

func (s *Store) ClaimGitHubCheckRun(
	ctx context.Context, run *GitHubCheckRun, claim uuid.UUID, lease time.Duration,
) error {
	revision, err := s.q.ClaimIntegrationState(ctx, dbsqlc.ClaimIntegrationStateParams{
		Claim: claim.String(), LeaseMilliseconds: lease.Milliseconds(), ProjectID: run.ProjectID,
		IntegrationID: run.IntegrationID, Kind: githubCheckRunStateKind, ID: run.ID, ExpectedRevision: run.revision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrConflict
	}
	if err == nil {
		run.revision = revision
	}
	return err
}

func (s *Store) SaveGitHubCheckRun(ctx context.Context, run GitHubCheckRun) error {
	data, err := encodeIntegrationState(githubCheckRunData{
		AgentID: run.AgentID, Run: run.Run, Waiting: run.Waiting, Done: run.Done,
	})
	if err != nil {
		return err
	}
	_, err = s.q.ReplaceIntegrationState(ctx, dbsqlc.ReplaceIntegrationStateParams{
		Data: data, ProjectID: run.ProjectID, IntegrationID: run.IntegrationID, Kind: githubCheckRunStateKind,
		ID: run.ID, ExpectedRevision: run.revision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrConflict
	}
	return err
}

func (s *Store) DeleteGitHubCheckRun(ctx context.Context, run GitHubCheckRun) error {
	deleted, err := s.q.DeleteIntegrationState(ctx, dbsqlc.DeleteIntegrationStateParams{
		ProjectID: run.ProjectID, IntegrationID: run.IntegrationID, Kind: githubCheckRunStateKind,
		ID: run.ID, ExpectedRevision: run.revision,
	})
	if err == nil && deleted == 0 {
		return storeerr.ErrConflict
	}
	return err
}
