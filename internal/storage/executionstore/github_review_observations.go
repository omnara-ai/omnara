package executionstore

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const MaxGitHubReviewObservations = 100

type GitHubReviewObservation struct {
	ReviewID           string
	CommitID           string    // Optional native observation; never substitutes for the immutable creator pin.
	CreatingToolCallID uuid.UUID // Only from an exact Omnara marker in the native review body.
}

type GitHubReviewOwnership string

const (
	GitHubReviewOwned      GitHubReviewOwnership = "owned"
	GitHubReviewOtherAgent GitHubReviewOwnership = "other_agent"
	GitHubReviewUnknown    GitHubReviewOwnership = "unknown"
)

type GitHubReviewObservationResult struct {
	ReviewID           string
	Ownership          GitHubReviewOwnership
	CreatingToolCallID uuid.UUID
	CommitID           string
}

// LookupGitHubReviewObservations classifies a bounded provider read without
// changing identity or native state. Only this agent's creator references leave
// storage. A missing match is unknown, never proof that a draft does not exist.
func (s *Store) LookupGitHubReviewObservations(
	ctx context.Context,
	input GitHubReviewOperationScope,
	observations []GitHubReviewObservation,
) ([]GitHubReviewObservationResult, error) {
	if len(observations) > MaxGitHubReviewObservations {
		return nil, storeerr.InvalidRequest(errors.New("too many GitHub review observations"))
	}
	nativeIDs := make([]string, 0, len(observations))
	creatorIDs := make([]uuid.UUID, 0, len(observations))
	seen := make(map[string]bool, len(observations))
	for _, observation := range observations {
		if !validGitHubReviewObservation(observation) || seen[observation.ReviewID] {
			return nil, storeerr.InvalidRequest(errors.New("invalid or duplicate GitHub review observation"))
		}
		seen[observation.ReviewID] = true
		nativeIDs = append(nativeIDs, observation.ReviewID)
		if observation.CreatingToolCallID != uuid.Nil {
			creatorIDs = append(creatorIDs, observation.CreatingToolCallID)
		}
	}
	scope, err := s.githubReviewOperationScope(ctx, input)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListGitHubReviewObservations(ctx, dbsqlc.ListGitHubReviewObservationsParams{
		ProjectID: scope.projectID, IntegrationInstallID: input.IntegrationInstallID, PrChannelID: scope.prID,
		AgentID: input.AgentID, ProviderReviewIds: nativeIDs, CreatingToolCallIds: creatorIDs,
	})
	if err != nil {
		return nil, err
	}
	byNative := make(map[string]dbsqlc.GithubPrReview, len(rows))
	byCreator := make(map[uuid.UUID]dbsqlc.GithubPrReview, len(rows))
	for _, review := range rows {
		if review.ProviderReviewID != nil {
			byNative[*review.ProviderReviewID] = review
		}
		if review.AgentID == input.AgentID {
			byCreator[review.CreatingToolCallID] = review
		}
	}
	results := make([]GitHubReviewObservationResult, 0, len(observations))
	for _, observation := range observations {
		result := GitHubReviewObservationResult{ReviewID: observation.ReviewID, Ownership: GitHubReviewUnknown}
		review, found := byNative[observation.ReviewID]
		if !found && observation.CreatingToolCallID != uuid.Nil {
			review, found = byCreator[observation.CreatingToolCallID]
			found = found && (review.ProviderReviewID == nil || *review.ProviderReviewID == observation.ReviewID)
		}
		if found {
			if review.AgentID == input.AgentID {
				result.Ownership = GitHubReviewOwned
				result.CreatingToolCallID, result.CommitID = review.CreatingToolCallID, review.CommitID
			} else {
				result.Ownership = GitHubReviewOtherAgent
			}
		}
		results = append(results, result)
	}
	return results, nil
}

type GitHubReviewIdentityEvidence string

const (
	GitHubReviewCreateResponse GitHubReviewIdentityEvidence = "create_response"
	GitHubReviewMarker         GitHubReviewIdentityEvidence = "marker"
)

type RecordGitHubReviewIdentityInput struct {
	Scope       GitHubReviewOperationScope
	Observation GitHubReviewObservation
	Evidence    GitHubReviewIdentityEvidence
}

type RecordGitHubReviewIdentityResult struct {
	Recorded bool
	Continue bool
}

// RecordGitHubReviewIdentity is a write-once factual acknowledgment. Its single
// update deliberately commits before checking continuation: a stopped agent or
// revoked binding must not erase the native identity of an already-created draft.
func (s *Store) RecordGitHubReviewIdentity(
	ctx context.Context,
	input RecordGitHubReviewIdentityInput,
) (RecordGitHubReviewIdentityResult, error) {
	var result RecordGitHubReviewIdentityResult
	observation := input.Observation
	if !validGitHubReviewObservation(observation) || observation.CreatingToolCallID == uuid.Nil ||
		(input.Evidence != GitHubReviewCreateResponse && input.Evidence != GitHubReviewMarker) {
		return result, storeerr.InvalidRequest(errors.New("GitHub review identity evidence is required"))
	}
	scope, err := s.githubReviewOperationScope(ctx, input.Scope)
	if err != nil {
		return result, err
	}
	if scope.toolName != toolcatalog.ToolNameSendChannelMessage {
		return result, storeerr.ErrUnauthorized
	}
	if input.Evidence == GitHubReviewCreateResponse &&
		(input.Scope.RequestID != observation.CreatingToolCallID || !scope.params.ReviewComment ||
			scope.params.ReviewID != "" || !strings.EqualFold(scope.params.CommitID, observation.CommitID)) {
		return result, storeerr.ErrUnauthorized
	}
	review, err := s.q.GetGitHubReviewCreator(ctx, dbsqlc.GetGitHubReviewCreatorParams{
		ProjectID: scope.projectID, AgentID: input.Scope.AgentID, CreatingToolCallID: observation.CreatingToolCallID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return result, storeerr.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if review.IntegrationInstallID != input.Scope.IntegrationInstallID || review.PrChannelID != scope.prID ||
		(input.Evidence == GitHubReviewCreateResponse && !strings.EqualFold(review.CommitID, observation.CommitID)) {
		return result, storeerr.ErrUnauthorized
	}
	review, err = s.q.RecordGitHubReviewIdentity(ctx, dbsqlc.RecordGitHubReviewIdentityParams{
		ProjectID: scope.projectID, AgentID: input.Scope.AgentID,
		CreatingToolCallID: observation.CreatingToolCallID, ProviderReviewID: &observation.ReviewID,
	})
	if errors.Is(err, pgx.ErrNoRows) || storeutil.IsUniqueViolation(err) {
		return result, storeerr.ErrIdempotencyConflict
	}
	if err != nil {
		return result, err
	}
	result.Recorded = true
	result.Continue, err = s.githubReviewCreationMayContinue(ctx, input.Scope, review)
	return result, err
}

func validGitHubReviewObservation(observation GitHubReviewObservation) bool {
	if observation.CommitID != "" {
		commit, err := hex.DecodeString(observation.CommitID)
		if err != nil || len(commit) != 20 {
			return false
		}
	}
	return observation.ReviewID != "" && len(observation.ReviewID) <= 512 &&
		strings.TrimSpace(observation.ReviewID) == observation.ReviewID &&
		strings.IndexFunc(observation.ReviewID, unicode.IsControl) == -1 && dbsafe.Text(observation.ReviewID) == nil
}
