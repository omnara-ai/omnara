package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

// ConfirmedAppFollow is produced only after a provider confirms the post. It is
// not a grant: the model request's config and current config must both permit it.
type ConfirmedAppFollow struct {
	ResourceKey  string
	ConnectionID uuid.UUID
	Scope        appdefinition.Scope
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
	if follow.ResourceKey == "" || follow.ConnectionID == uuid.Nil || input.Outcome != ToolResultOutcomeSucceeded {
		return ToolCallRecord{}, storeerr.InvalidRequest(
			errors.New("confirmed follow requires a resource, connection and successful post"),
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
	if err := integrationstore.LockAppConnectionsTx(ctx, tx, input.ProjectID, nil, follow.ConnectionID); err != nil {
		return ToolCallRecord{}, err
	}
	if err := integrationstore.LockConversationTx(ctx, tx, input.ProjectID, follow.ConnectionID, address); err != nil {
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
	if !toolcatalog.AppToolSupportsFollow(tool.Name) {
		return storeerr.InvalidRequest(errors.New("this tool does not support following replies"))
	}
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
	current, err := loadAgentConfigTx(ctx, q, tool.ProjectID, agent.CurrentConfigID)
	if err != nil {
		return err
	}
	originalContract, err := launchableRuntimeContract(original)
	if err != nil {
		return err
	}
	currentContract, err := launchableRuntimeContract(current)
	if err != nil {
		return err
	}
	authority, err := agentconfig.ResolveAppToolAuthority(
		originalContract,
		currentContract,
		tool.Name,
		follow.ResourceKey,
	)
	if err != nil {
		return storeerr.ErrUnauthorized
	}
	connectionID, err := publicid.Decode(publicid.KindIntegrationConnection, authority.Original.ConnectionID)
	if err != nil || connectionID != follow.ConnectionID || !authority.AllowsScope(follow.Scope) ||
		!authority.AllowsFollowingReplies() {
		return storeerr.ErrUnauthorized
	}
	if _, err := s.integrations.EnsureConversationTargetTx(ctx, tx, integrationstore.EnsureConversationTargetInput{
		ProjectID:    tool.ProjectID,
		AgentID:      tool.AgentID,
		ConnectionID: follow.ConnectionID,
		Address:      address,
		Role:         integrationstore.TargetFollowed,
	}); err != nil {
		return err
	}
	if _, err := q.UpsertAgentListener(ctx, dbsqlc.UpsertAgentListenerParams{
		ProjectID:      tool.ProjectID,
		AgentID:        tool.AgentID,
		ConnectionID:   follow.ConnectionID,
		ResourceKey:    follow.ResourceKey,
		ScopeKind:      address.Kind,
		ScopeRef:       address.Ref,
		Events:         []string{"message"},
		SourceConfigID: agent.CurrentConfigID,
		ToolCallID:     &tool.ID,
	}); err != nil {
		return err
	}
	limits, err := resourceguard.ResolveLimits(ctx, q, agent.OrgID)
	if err != nil {
		return err
	}
	count, err := q.CountActiveAgentListeners(
		ctx,
		dbsqlc.CountActiveAgentListenersParams{ProjectID: tool.ProjectID, AgentID: tool.AgentID},
	)
	if err != nil {
		return err
	}
	if count > limits.MaxActiveAppListenersPerAgent {
		return fmt.Errorf(
			"app listeners limit of %d reached: %w",
			limits.MaxActiveAppListenersPerAgent,
			storeerr.ErrConflict,
		)
	}
	return nil
}
