package executionstore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// GitHubReviewError contains fixed recovery facts for the sending agent. Native
// response bodies and references belonging to another agent never appear here.
type GitHubReviewError struct {
	Code     channelconnector.OperationFailureCode
	ReviewID string
	CommitID string
}

func (e *GitHubReviewError) Error() string { return string(e.Code) }

type githubReviewParams struct {
	ReviewComment bool   `json:"review_comment"`
	PublishReview bool   `json:"publish_review"`
	ReviewID      string `json:"review_id"`
	CommitID      string `json:"commit_id"`
}

// Only this built-in behavior owns staged reviews. Other connector definitions
// keep their own params and lifecycle; channel kind alone never selects a workflow.
func (s *Store) prepareGitHubReviewTx(
	ctx context.Context,
	tx pgx.Tx,
	prepared PreparedChannelOperation,
	raw json.RawMessage,
) error {
	var params githubReviewParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return storeerr.InvalidRequest(err)
	}
	if !params.ReviewComment && !params.PublishReview {
		if params.ReviewID != "" {
			return storeerr.InvalidRequest(errors.New("review_id requires a review action"))
		}
		return nil // Timeline comments never acquire or alter a pending review.
	}
	if params.ReviewComment && params.PublishReview {
		return storeerr.InvalidRequest(errors.New("review actions are mutually exclusive"))
	}
	if params.ReviewComment {
		commit, err := hex.DecodeString(params.CommitID)
		if err != nil || len(commit) != 20 {
			return storeerr.InvalidRequest(errors.New("review finding requires a commit ID"))
		}
		params.CommitID = strings.ToLower(params.CommitID)
	}
	q := dbsqlc.New(tx)
	owner := prepared.owner
	if params.ReviewID != "" {
		review, err := q.GetGitHubReviewByProviderID(ctx, dbsqlc.GetGitHubReviewByProviderIDParams{
			ProjectID: owner.ProjectID, IntegrationInstallID: prepared.access.IntegrationInstallID,
			ProviderReviewID: &params.ReviewID,
		})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil &&
			(review.AgentID != owner.AgentID || review.PrChannelID != owner.ChannelID)) {
			return &GitHubReviewError{Code: channelconnector.FailureReviewNotOwned}
		}
		if err != nil {
			return err
		}
		if params.ReviewComment && params.CommitID != review.CommitID {
			return &GitHubReviewError{
				Code: channelconnector.FailureReviewCommitMismatch, ReviewID: params.ReviewID, CommitID: review.CommitID,
			}
		}
		return nil
	}
	running, err := q.HasRunningGitHubReviewCreation(ctx, dbsqlc.HasRunningGitHubReviewCreationParams{
		ProjectID: owner.ProjectID, AgentID: owner.AgentID, PrChannelID: owner.ChannelID,
		IssuingToolCallID: owner.ToolCallID,
	})
	if err != nil {
		return err
	}
	if running {
		return &GitHubReviewError{Code: channelconnector.FailureReviewCreationInProgress}
	}
	if params.PublishReview {
		return nil // Summary-only submission creates no draft to own or continue.
	}
	_, err = q.InsertGitHubReviewCreator(ctx, dbsqlc.InsertGitHubReviewCreatorParams{
		ProjectID: owner.ProjectID, AgentID: owner.AgentID, CreatingToolCallID: owner.ToolCallID,
		IntegrationInstallID: prepared.access.IntegrationInstallID, PrChannelID: owner.ChannelID,
		CreatingBindingID: prepared.binding.ID, CommitID: params.CommitID,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	review, err := q.GetGitHubReviewCreator(ctx, dbsqlc.GetGitHubReviewCreatorParams{
		ProjectID: owner.ProjectID, AgentID: owner.AgentID, CreatingToolCallID: owner.ToolCallID,
	})
	if err != nil {
		return err
	}
	if review.IntegrationInstallID != prepared.access.IntegrationInstallID ||
		review.PrChannelID != owner.ChannelID || review.CreatingBindingID != prepared.binding.ID ||
		review.CommitID != params.CommitID {
		return storeerr.ErrIdempotencyConflict
	}
	if review.ProviderReviewID != nil {
		return &GitHubReviewError{
			Code:     channelconnector.FailureReviewCreationAlreadyRecorded,
			ReviewID: *review.ProviderReviewID, CommitID: review.CommitID,
		}
	}
	return nil
}
