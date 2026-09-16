package executionstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type GitHubReviewOperationScope struct {
	IntegrationAppID     uuid.UUID
	IntegrationInstallID uuid.UUID
	AgentID              uuid.UUID
	ChannelID            uuid.UUID
	RequestID            uuid.UUID
	Capabilities         []channelconnector.Capability
}

type githubReviewScope struct {
	projectID uuid.UUID
	prID      uuid.UUID
	params    githubReviewParams
	toolName  string
}

// Correlate observations to an admitted send or running history read. Historical
// send scope survives cancellation so late provider responses can record facts;
// a separate live check controls further dispatch.
func (s *Store) githubReviewOperationScope(
	ctx context.Context,
	input GitHubReviewOperationScope,
) (githubReviewScope, error) {
	var scope githubReviewScope
	if input.IntegrationAppID == uuid.Nil || input.IntegrationInstallID == uuid.Nil ||
		input.AgentID == uuid.Nil || input.ChannelID == uuid.Nil || input.RequestID == uuid.Nil {
		return scope, storeerr.InvalidRequest(errors.New("GitHub review operation scope is required"))
	}
	row, err := s.q.GetGitHubReviewOperationScope(ctx, dbsqlc.GetGitHubReviewOperationScopeParams{
		IntegrationAppID: input.IntegrationAppID, IntegrationInstallID: input.IntegrationInstallID,
		AgentID: input.AgentID, ChannelID: input.ChannelID, RequestID: input.RequestID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return scope, storeerr.ErrNotFound
	}
	if err != nil {
		return scope, err
	}
	authorized := false
	for _, capability := range input.Capabilities {
		if capability.ConnectorKey == row.ConnectorKey && capability.Provider == row.Provider {
			authorized = true
			break
		}
	}
	if !authorized || row.Provider != integrationstore.IntegrationProviderGitHub {
		return scope, storeerr.ErrUnauthorized
	}
	var args struct {
		ChannelID string          `json:"channel_id"`
		Params    json.RawMessage `json:"params"`
	}
	if json.Unmarshal(row.Input, &args) != nil {
		return scope, storeerr.ErrUnauthorized
	}
	channelID, err := publicid.Decode(publicid.KindIntegrationTarget, args.ChannelID)
	if err != nil || channelID != input.ChannelID {
		return scope, storeerr.ErrUnauthorized
	}
	if len(args.Params) != 0 && json.Unmarshal(args.Params, &scope.params) != nil {
		return scope, storeerr.ErrUnauthorized
	}
	scope.projectID = row.ProjectID
	scope.toolName = row.ToolName
	switch row.ImplementationKey {
	case "github_pr":
		if row.ToolName == toolcatalog.ToolNameSendChannelMessage &&
			scope.params.ReviewComment == scope.params.PublishReview {
			return scope, storeerr.ErrUnauthorized
		}
		scope.prID = input.ChannelID
	case "github_review_thread":
		if row.ParentImplementationKey != "github_pr" || row.ParentChannelID == nil {
			return scope, storeerr.ErrUnauthorized
		}
		scope.prID = *row.ParentChannelID
	default:
		return scope, storeerr.ErrUnauthorized
	}
	return scope, nil
}

func (s *Store) githubReviewCreationMayContinue(
	ctx context.Context,
	input GitHubReviewOperationScope,
	review dbsqlc.GithubPrReview,
) (bool, error) {
	if input.RequestID != review.CreatingToolCallID {
		return false, nil
	}
	call, err := s.GetToolCall(ctx, review.ProjectID, review.AgentID, review.CreatingToolCallID)
	if err != nil {
		return false, err
	}
	if call.State != ToolCallStateRunning || call.RuntimeLockID == uuid.Nil {
		return false, nil
	}
	access, err := s.integrations.GetAgentChannelAccess(ctx, review.ProjectID, review.AgentID, review.PrChannelID)
	if err != nil {
		return githubReviewContinuationError(err)
	}
	binding, err := s.integrations.GetIntegrationTargetBinding(ctx, review.ProjectID, review.CreatingBindingID)
	if err != nil {
		return githubReviewContinuationError(err)
	}
	prepared := PreparedChannelOperation{
		store: s, access: access, binding: binding,
		owner: PrepareChannelOperationInput{
			ExecuteToolCallInput: ExecuteToolCallInput{
				ProjectID: review.ProjectID, AgentID: review.AgentID,
				ToolCallID: review.CreatingToolCallID, RuntimeLockID: call.RuntimeLockID,
			},
			TurnID: call.TurnID, ChannelID: review.PrChannelID, Operation: integrationstore.ChannelBindingOperationSend,
		},
		input: integrationstore.PrepareChannelBindingInput{
			ProjectID: review.ProjectID, AgentID: review.AgentID,
			IntegrationInstallID: review.IntegrationInstallID, IntegrationTargetID: review.PrChannelID,
			Operation: integrationstore.ChannelBindingOperationSend,
		},
	}
	// Reuse the exact original binding pin, even if a replacement now exists.
	// No provider I/O is performed while these ordinary dispatch locks are held.
	_, err = s.RecheckChannelOperation(ctx, prepared)
	if err != nil {
		return githubReviewContinuationError(err)
	}
	return true, nil
}

func githubReviewContinuationError(err error) (bool, error) {
	if errors.Is(err, storeerr.ErrNotFound) || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, storeerr.ErrUnauthorized) ||
		errors.Is(err, storeerr.ErrStateTransitionConflict) || errors.Is(err, storeerr.ErrRuntimeLockInactive) ||
		errors.Is(err, storeerr.ErrConflict) {
		return false, nil
	}
	return false, err
}
