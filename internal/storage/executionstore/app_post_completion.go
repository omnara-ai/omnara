package executionstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

// ConfirmedAppFollow is produced only after a provider confirms the post. The
// original config must authorize the send and follow; the live app and agent
// govern registration. Subsequent sender edits cannot revoke that receipt.
type ConfirmedAppFollow struct {
	SubscriptionType string
	AppID            uuid.UUID
	Scope            appdefinition.Scope
}

func (s *Store) CompleteAppPostToolCall(
	ctx context.Context,
	input CompleteRuntimeToolCallInput,
	follow ConfirmedAppFollow,
) (ToolCallRecord, error) {
	parts, err := s.prepareToolResult(ctx, input.ProjectID, input.AgentID, input.ID, input.ResultContentParts)
	if err != nil {
		return ToolCallRecord{}, err
	}
	input.ResultContentParts = parts
	record, err := s.completeAppPostOnce(ctx, input, follow)
	var read *toolResultArtifactReadRequiredError
	if !errors.As(err, &read) {
		return record, err
	}
	ctx, err = s.loadToolResultForReplay(ctx, read)
	if err != nil {
		return ToolCallRecord{}, err
	}
	return s.completeAppPostOnce(ctx, input, follow)
}

func (s *Store) completeAppPostOnce(
	ctx context.Context,
	input CompleteRuntimeToolCallInput,
	follow ConfirmedAppFollow,
) (ToolCallRecord, error) {
	if follow.SubscriptionType == "" || follow.AppID == uuid.Nil || input.Outcome != ToolResultOutcomeSucceeded {
		return ToolCallRecord{}, storeerr.InvalidRequest(
			errors.New("confirmed follow requires a subscription type, app and successful post"),
		)
	}
	kind, ref, err := follow.Scope.Conversation()
	if err != nil {
		return ToolCallRecord{}, storeerr.InvalidRequest(err)
	}
	address := integrationstore.ConversationAddress{Kind: kind, Ref: ref}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ToolCallRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	// A completed replay must never reactivate a later revoked subscription.
	prior, err := getToolCallTx(ctx, tx, input.ProjectID, input.AgentID, input.ID)
	if err != nil {
		return ToolCallRecord{}, err
	}
	if prior.State == ToolCallStateCompleted {
		_ = tx.Rollback(ctx)
		return s.CompleteRuntimeToolCall(ctx, input)
	}
	project, err := loadProjectTx(ctx, q, input.ProjectID)
	if err != nil {
		return ToolCallRecord{}, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, project.OrgID, input.ProjectID); err != nil {
		return ToolCallRecord{}, err
	}
	if err := integrationstore.LockAppsTx(ctx, tx, input.ProjectID, nil, follow.AppID); err != nil {
		return ToolCallRecord{}, err
	}
	if err := integrationstore.LockConversationTx(ctx, tx, input.ProjectID, follow.AppID, address); err != nil {
		return ToolCallRecord{}, err
	}
	if err := lockAgentRuntimeForOwnedMutationTx(
		ctx,
		q,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	); err != nil {
		return ToolCallRecord{}, err
	}
	// Re-read under the lock: another completion may have won while we waited.
	prior, err = getToolCallTx(ctx, tx, input.ProjectID, input.AgentID, input.ID)
	if err != nil {
		return ToolCallRecord{}, err
	}
	notifications := s.newTxNotifications()
	if prior.State != ToolCallStateCompleted {
		if err := s.registerAppFollowTx(ctx, tx, prior, follow, address); err != nil {
			return ToolCallRecord{}, err
		}
	}
	record, err := completeRuntimeToolCallTx(ctx, notifications, tx, input)
	if err != nil {
		return ToolCallRecord{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, notifications, "complete app post and follow"); err != nil {
		return ToolCallRecord{}, err
	}
	return record, nil
}

func (s *Store) registerAppFollowTx(
	ctx context.Context,
	tx pgx.Tx,
	tool ToolCallRecord,
	follow ConfirmedAppFollow,
	address integrationstore.ConversationAddress,
) error {
	q := dbsqlc.New(tx)
	model, err := loadModelCallContextByIDTx(ctx, tx, tool.ProjectID, tool.AgentID, tool.ModelCallContextID)
	if err != nil {
		return err
	}
	agent, err := q.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{ProjectID: tool.ProjectID, ID: tool.AgentID})
	if err != nil {
		return err
	}
	original, err := loadAgentConfigTx(ctx, q, tool.ProjectID, model.AgentConfigID)
	if err != nil {
		return err
	}
	originalContract, err := launchableRuntimeContract(original)
	if err != nil {
		return err
	}
	app, err := s.integrations.GetProjectAppByIDTx(ctx, tx, follow.AppID)
	if err != nil {
		return err
	}
	appRef, err := publicid.Encode(publicid.KindProjectApp, app.ID)
	if err != nil {
		return err
	}
	// Provider I/O already checked the current sender. Completion validates the
	// original send's provenance; future tool edits do not revoke a confirmed post.
	authority, err := agentconfig.ResolveAppToolAuthority(originalContract, originalContract, tool.Name,
		map[string]agentconfig.AppResolution{appRef: {AppID: appRef, Definition: app.DefinitionID}})
	if err != nil {
		return storeerr.ErrUnauthorized
	}
	name, _, ok := toolcatalog.SplitAppToolName(tool.Name)
	if !ok || app.ProjectID != tool.ProjectID || app.Name != name || authority.Definition.FollowSubscription == "" ||
		follow.SubscriptionType != authority.Definition.FollowSubscription {
		return storeerr.ErrUnauthorized
	}
	arguments, err := authority.Definition.ResolveArgs(authority.Tool.Config, tool.Input)
	if err != nil || !arguments.FollowReplies {
		return storeerr.ErrUnauthorized
	}
	if err := follow.Scope.Validate(app.Provider); err != nil {
		return storeerr.InvalidRequest(err)
	}
	if _, err := s.integrations.EnsureConversationTargetTx(ctx, tx, integrationstore.EnsureConversationTargetInput{
		ProjectID: tool.ProjectID,
		AgentID:   tool.AgentID,
		AppID:     follow.AppID,
		Address:   address,
		Role:      integrationstore.TargetFollowed,
	}); err != nil {
		return err
	}
	_, err = integrationstore.RegisterAppSubscriptionTx(ctx, tx, integrationstore.RegisterAppSubscriptionInput{
		OrgID: agent.OrgID, ProjectID: tool.ProjectID, AgentID: tool.AgentID,
		AppID: follow.AppID, Type: follow.SubscriptionType, Address: address, ToolCallID: &tool.ID,
	})
	return err
}
