package integrationstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrAgentConfigChanged = errors.New("agent config changed")

func (s *Store) ListAgentIntegrationSubscriptions(
	ctx context.Context, projectID, agentID, integrationID uuid.UUID,
) ([]ConversationAddress, error) {
	rows, err := s.q.ListAgentIntegrationSubscriptions(ctx, dbsqlc.ListAgentIntegrationSubscriptionsParams{
		ProjectID: projectID, AgentID: agentID, IntegrationID: integrationID,
	})
	if err != nil {
		return nil, fmt.Errorf("list agent integration subscriptions: %w", err)
	}
	addresses := make([]ConversationAddress, 0, len(rows))
	for _, row := range rows {
		addresses = append(addresses, ConversationAddress{Kind: row.ScopeKind, Ref: row.ScopeRef})
	}
	return addresses, nil
}

func (s *Store) CreateIntegrationSubscription(
	ctx context.Context, input CreateIntegrationSubscriptionInput,
) (IntegrationSubscriptionRecord, error) {
	var record IntegrationSubscriptionRecord
	err := s.withConversationTx(ctx, input, false, func(tx pgx.Tx, prepared RegisterIntegrationSubscriptionInput) error {
		var err error
		record, err = RegisterIntegrationSubscriptionTx(ctx, tx, prepared)
		return err
	})
	return record, err
}

func (s *Store) AttachAgentConversation(ctx context.Context, input AgentConversationInput) error {
	return s.withConversationTx(ctx, input.CreateIntegrationSubscriptionInput, true,
		func(tx pgx.Tx, prepared RegisterIntegrationSubscriptionInput) error {
			if err := checkAgentToolAuthorityTx(ctx, dbsqlc.New(tx), input); err != nil {
				return err
			}
			_, err := RegisterIntegrationSubscriptionTx(ctx, tx, prepared)
			return err
		})
}

func (s *Store) DetachAgentConversation(ctx context.Context, input AgentConversationInput) error {
	return s.withConversationTx(ctx, input.CreateIntegrationSubscriptionInput, false,
		func(tx pgx.Tx, prepared RegisterIntegrationSubscriptionInput) error {
			q := dbsqlc.New(tx)
			if err := checkAgentToolAuthorityTx(ctx, q, input); err != nil {
				return err
			}
			assigned, found, err := getAgentIntegrationConversation(
				ctx, q, input.ProjectID, input.AgentID, input.IntegrationID,
			)
			if err != nil {
				return err
			}
			if found && assigned == prepared.Address {
				return storeerr.InvalidRequest(errors.New("an agent can't detach the conversation it was launched for"))
			}
			return q.DeleteAgentIntegrationSubscription(ctx,
				dbsqlc.DeleteAgentIntegrationSubscriptionParams{
					ProjectID: input.ProjectID, AgentID: input.AgentID, IntegrationID: input.IntegrationID,
					ScopeKind: prepared.Address.Kind, ScopeRef: prepared.Address.Ref,
				})
		})
}

func checkAgentToolAuthorityTx(ctx context.Context, q *dbsqlc.Queries, input AgentConversationInput) error {
	authority, err := q.GetAgentToolAuthority(ctx, dbsqlc.GetAgentToolAuthorityParams{
		ConfigID: input.ExpectedConfigID, RuntimeLockID: input.RuntimeLockID,
		ProjectID: input.ProjectID, AgentID: input.AgentID,
	})
	if err != nil {
		return err
	}
	if !authority.RuntimeActive {
		return storeerr.ErrRuntimeLockInactive
	}
	if !authority.ConfigCurrent {
		return ErrAgentConfigChanged
	}
	return nil
}

func (s *Store) withConversationTx(
	ctx context.Context, input CreateIntegrationSubscriptionInput, lockCredential bool,
	fn func(pgx.Tx, RegisterIntegrationSubscriptionInput) error,
) error {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil ||
		input.IntegrationID == uuid.Nil || input.AgentID == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("organization, project, integration and agent are required"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return err
	}
	if err := LockIntegrationsTx(ctx, tx, input.ProjectID, nil, input.IntegrationID); err != nil {
		return err
	}
	if lockCredential {
		if err := LockIntegrationCredentialAccessTx(ctx, tx, input.ProjectID, input.IntegrationID); err != nil {
			return err
		}
	}
	prepared, err := PrepareIntegrationSubscriptionTx(ctx, tx, input.ProjectID, IntegrationSubscriptionAttachment{
		IntegrationID: input.IntegrationID, Conversation: input.Conversation,
	})
	if err != nil {
		return err
	}
	prepared.AgentID = input.AgentID
	if err := LockConversationTx(ctx, tx, input.ProjectID, input.IntegrationID, prepared.Address); err != nil {
		return err
	}
	if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{
		{ProjectID: input.ProjectID, AgentID: input.AgentID},
	}); err != nil {
		return err
	}
	if err := fn(tx, prepared); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteIntegrationSubscription(
	ctx context.Context,
	orgID, projectID, integrationID, id uuid.UUID,
) error {
	if orgID == uuid.Nil || projectID == uuid.Nil || integrationID == uuid.Nil || id == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("organization, project, integration and subscription are required"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, orgID, projectID); err != nil {
		return err
	}
	q := dbsqlc.New(tx)
	if err := q.LockIntegrationLifecycleShared(ctx, dbsqlc.LockIntegrationLifecycleSharedParams{
		IntegrationID: integrationID,
	}); err != nil {
		return err
	}
	if _, err := getIntegration(ctx, q, projectID, integrationID); err != nil {
		return err
	}
	row, err := q.GetIntegrationSubscription(ctx, dbsqlc.GetIntegrationSubscriptionParams{
		ProjectID: projectID, IntegrationID: integrationID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{
		{ProjectID: projectID, AgentID: row.AgentID},
	}); err != nil {
		return err
	}
	if err := q.DeleteIntegrationSubscription(ctx, dbsqlc.DeleteIntegrationSubscriptionParams{
		ProjectID: projectID, IntegrationID: integrationID, ID: id,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListIntegrationSubscriptions(
	ctx context.Context, input ListIntegrationSubscriptionsInput,
) (ListIntegrationSubscriptionsResult, error) {
	if input.ProjectID == uuid.Nil || input.IntegrationID == uuid.Nil || input.Limit < 1 || input.Limit > 100 {
		return ListIntegrationSubscriptionsResult{}, storeerr.InvalidRequest(
			errors.New("project, integration and limit between 1 and 100 are required"),
		)
	}
	integration, err := getIntegration(ctx, s.q, input.ProjectID, input.IntegrationID)
	if err != nil {
		return ListIntegrationSubscriptionsResult{}, err
	}
	rows, err := s.q.ListIntegrationSubscriptions(ctx, dbsqlc.ListIntegrationSubscriptionsParams{
		ProjectID: input.ProjectID, IntegrationID: input.IntegrationID, RowLimit: int32(input.Limit + 1),
		CursorSet: input.After.Set, CursorCreatedAt: input.After.CreatedAt, CursorID: input.After.ID,
	})
	if err != nil {
		return ListIntegrationSubscriptionsResult{}, err
	}
	result := ListIntegrationSubscriptionsResult{
		Subscriptions: make([]IntegrationSubscriptionRecord, 0, min(len(rows), input.Limit)),
		HasMore:       len(rows) > input.Limit,
	}
	if result.HasMore {
		rows = rows[:input.Limit]
	}
	for _, row := range rows {
		record, err := listedIntegrationSubscriptionRecord(integration.Provider, row)
		if err != nil {
			return ListIntegrationSubscriptionsResult{}, err
		}
		result.Subscriptions = append(result.Subscriptions, record)
	}
	if result.HasMore {
		last := rows[len(rows)-1]
		result.Next = listing.KeysetCursor{Set: true, CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return result, nil
}

func (s *Store) GetIntegrationSubscription(
	ctx context.Context,
	projectID, integrationID, id uuid.UUID,
) (IntegrationSubscriptionRecord, error) {
	if projectID == uuid.Nil || integrationID == uuid.Nil || id == uuid.Nil {
		return IntegrationSubscriptionRecord{}, storeerr.InvalidRequest(
			errors.New("project, integration and subscription are required"),
		)
	}
	integration, err := getIntegration(ctx, s.q, projectID, integrationID)
	if err != nil {
		return IntegrationSubscriptionRecord{}, err
	}
	row, err := s.q.GetIntegrationSubscriptionDisplay(ctx, dbsqlc.GetIntegrationSubscriptionDisplayParams{
		ProjectID: projectID, IntegrationID: integrationID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationSubscriptionRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return IntegrationSubscriptionRecord{}, err
	}
	return listedIntegrationSubscriptionRecord(integration.Provider, dbsqlc.ListIntegrationSubscriptionsRow(row))
}

func listedIntegrationSubscriptionRecord(
	provider integrationdefinition.Provider,
	row dbsqlc.ListIntegrationSubscriptionsRow,
) (IntegrationSubscriptionRecord, error) {
	record := integrationSubscriptionRecord(dbsqlc.IntegrationSubscription{
		ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID, IntegrationID: row.IntegrationID,
		ScopeKind: row.ScopeKind, ScopeRef: row.ScopeRef,
		CreatedAt: row.CreatedAt,
	})
	record.AgentName = row.AgentName
	record.ConversationName = row.ConversationName
	scope, err := integrationdefinition.ParseConversation(provider, row.ScopeKind, row.ScopeRef)
	if err != nil {
		return IntegrationSubscriptionRecord{}, err
	}
	record.Conversation, err = scope.ConversationJSON()
	if err != nil {
		return IntegrationSubscriptionRecord{}, err
	}
	return record, nil
}
