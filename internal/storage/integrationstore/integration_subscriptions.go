package integrationstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrAgentConfigChanged = errors.New("agent config changed")

func (s *Store) CreateIntegrationSubscription(
	ctx context.Context, input CreateIntegrationSubscriptionInput,
) (IntegrationSubscriptionRecord, error) {
	return s.createIntegrationSubscription(ctx, input, nil)
}

func (s *Store) CreateAssignedIntegrationSubscription(
	ctx context.Context, input CreateAssignedIntegrationSubscriptionInput,
) (IntegrationSubscriptionRecord, error) {
	subscription := input.CreateIntegrationSubscriptionInput
	return s.createIntegrationSubscription(ctx, subscription, func(tx pgx.Tx, address ConversationAddress) error {
		q := dbsqlc.New(tx)
		authority, err := q.GetAgentAssignmentAuthority(ctx, dbsqlc.GetAgentAssignmentAuthorityParams{
			ConfigID: input.ExpectedConfigID, RuntimeLockID: input.RuntimeLockID,
			ProjectID: subscription.ProjectID, AgentID: subscription.AgentID,
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
		if err := s.claimConversationOwnershipTx(ctx, tx, subscription, address, input.DisplayName); err != nil {
			return err
		}
		return s.AssignAgentIntegrationConversationTx(
			ctx, tx, subscription.ProjectID, subscription.AgentID, subscription.IntegrationID, address,
		)
	})
}

func (s *Store) claimConversationOwnershipTx(
	ctx context.Context, tx pgx.Tx, input CreateIntegrationSubscriptionInput,
	address ConversationAddress, displayName string,
) error {
	q := dbsqlc.New(tx)
	claim, err := json.Marshal(integrationLaunchIdentity{IntegrationID: input.IntegrationID, Address: address})
	if err != nil {
		return err
	}
	reserved, err := q.FindInboxLaunchClaims(ctx, dbsqlc.FindInboxLaunchClaimsParams{
		ProjectID: input.ProjectID, IntegrationID: input.IntegrationID, ReceiptID: uuid.Nil, LaunchClaim: claim,
	})
	if err != nil || len(reserved) != 0 {
		return err
	}
	owners, err := q.ListConversationLaunchOwners(ctx, dbsqlc.ListConversationLaunchOwnersParams{
		ProjectID: input.ProjectID, IntegrationID: input.IntegrationID, Kind: address.Kind, Ref: address.Ref,
	})
	if err != nil || len(owners) != 0 {
		return err
	}
	existing, err := q.GetAgentConversationTarget(ctx, dbsqlc.GetAgentConversationTargetParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID, IntegrationID: input.IntegrationID,
		Kind: address.Kind, Ref: address.Ref,
	})
	if err == nil {
		return q.PromoteIntegrationConversationTarget(ctx, dbsqlc.PromoteIntegrationConversationTargetParams{
			LaunchKey: integrationdefinition.ProfileLaunchKey, ProjectID: input.ProjectID, ID: existing.ID,
		})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	_, err = s.EnsureConversationTargetTx(ctx, tx, EnsureConversationTargetInput{
		ProjectID: input.ProjectID, AgentID: input.AgentID, IntegrationID: input.IntegrationID,
		Address: address, DisplayName: displayName, LaunchKey: integrationdefinition.ProfileLaunchKey,
	})
	return err
}

func (s *Store) createIntegrationSubscription(
	ctx context.Context, input CreateIntegrationSubscriptionInput,
	assign func(pgx.Tx, ConversationAddress) error,
) (IntegrationSubscriptionRecord, error) {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil ||
		input.IntegrationID == uuid.Nil || input.AgentID == uuid.Nil {
		return IntegrationSubscriptionRecord{}, storeerr.InvalidRequest(
			errors.New("organization, project, integration and agent are required"),
		)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationSubscriptionRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return IntegrationSubscriptionRecord{}, err
	}
	if err := LockIntegrationsTx(ctx, tx, input.ProjectID, nil, input.IntegrationID); err != nil {
		return IntegrationSubscriptionRecord{}, err
	}
	if assign != nil {
		if err := LockIntegrationCredentialAccessTx(ctx, tx, input.ProjectID, input.IntegrationID); err != nil {
			return IntegrationSubscriptionRecord{}, err
		}
	}
	prepared, err := PrepareIntegrationSubscriptionTx(ctx, tx, input.ProjectID, IntegrationSubscriptionAttachment{
		IntegrationID: input.IntegrationID, Conversation: input.Conversation,
	})
	if err != nil {
		return IntegrationSubscriptionRecord{}, err
	}
	prepared.AgentID = input.AgentID
	if err := LockConversationTx(ctx, tx, input.ProjectID, input.IntegrationID, prepared.Address); err != nil {
		return IntegrationSubscriptionRecord{}, err
	}
	if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{
		{ProjectID: input.ProjectID, AgentID: input.AgentID},
	}); err != nil {
		return IntegrationSubscriptionRecord{}, err
	}
	record, err := RegisterIntegrationSubscriptionTx(ctx, tx, prepared)
	if err != nil {
		return IntegrationSubscriptionRecord{}, err
	}
	if assign != nil {
		if err := assign(tx, prepared.Address); err != nil {
			return IntegrationSubscriptionRecord{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationSubscriptionRecord{}, err
	}
	return record, nil
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
