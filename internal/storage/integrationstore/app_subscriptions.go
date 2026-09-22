package integrationstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateAppSubscription(
	ctx context.Context, input CreateAppSubscriptionInput,
) (AppSubscriptionRecord, error) {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil || input.AppID == uuid.Nil || input.AgentID == uuid.Nil {
		return AppSubscriptionRecord{}, storeerr.InvalidRequest(
			errors.New("organization, project, app and agent are required"),
		)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AppSubscriptionRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return AppSubscriptionRecord{}, err
	}
	if err := LockAppsTx(ctx, tx, input.ProjectID, nil, input.AppID); err != nil {
		return AppSubscriptionRecord{}, err
	}
	prepared, err := PrepareAppSubscriptionTx(ctx, tx, input.ProjectID, AppSubscriptionAttachment{
		AppID: input.AppID, Type: input.Type, Conversation: input.Conversation, Events: input.Events,
	})
	if err != nil {
		return AppSubscriptionRecord{}, err
	}
	prepared.AgentID = input.AgentID
	if err := LockConversationTx(ctx, tx, input.ProjectID, input.AppID, prepared.Address); err != nil {
		return AppSubscriptionRecord{}, err
	}
	if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{
		{ProjectID: input.ProjectID, AgentID: input.AgentID},
	}); err != nil {
		return AppSubscriptionRecord{}, err
	}
	record, err := RegisterAppSubscriptionTx(ctx, tx, prepared)
	if err != nil {
		return AppSubscriptionRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AppSubscriptionRecord{}, err
	}
	return record, nil
}

func (s *Store) DeleteAppSubscription(ctx context.Context, orgID, projectID, appID, id uuid.UUID) error {
	if orgID == uuid.Nil || projectID == uuid.Nil || appID == uuid.Nil || id == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("organization, project, app and subscription are required"))
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
	if err := q.LockProjectAppLifecycleShared(ctx, dbsqlc.LockProjectAppLifecycleSharedParams{AppID: appID}); err != nil {
		return err
	}
	if _, err := getProjectApp(ctx, q, projectID, appID); err != nil {
		return err
	}
	row, err := q.GetAppSubscription(ctx, dbsqlc.GetAppSubscriptionParams{ProjectID: projectID, AppID: appID, ID: id})
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
	if err := q.DeleteAppSubscription(ctx, dbsqlc.DeleteAppSubscriptionParams{
		ProjectID: projectID, AppID: appID, ID: id,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListAppSubscriptions(
	ctx context.Context, input ListAppSubscriptionsInput,
) (ListAppSubscriptionsResult, error) {
	if input.ProjectID == uuid.Nil || input.AppID == uuid.Nil || input.Limit < 1 || input.Limit > 100 {
		return ListAppSubscriptionsResult{}, storeerr.InvalidRequest(
			errors.New("project, app and limit between 1 and 100 are required"),
		)
	}
	app, err := getProjectApp(ctx, s.q, input.ProjectID, input.AppID)
	if err != nil {
		return ListAppSubscriptionsResult{}, err
	}
	rows, err := s.q.ListAppSubscriptions(ctx, dbsqlc.ListAppSubscriptionsParams{
		ProjectID: input.ProjectID, AppID: input.AppID, RowLimit: int32(input.Limit + 1),
		CursorSet: input.After.Set, CursorCreatedAt: input.After.CreatedAt, CursorID: input.After.ID,
	})
	if err != nil {
		return ListAppSubscriptionsResult{}, err
	}
	result := ListAppSubscriptionsResult{
		Subscriptions: make([]AppSubscriptionRecord, 0, min(len(rows), input.Limit)), HasMore: len(rows) > input.Limit,
	}
	if result.HasMore {
		rows = rows[:input.Limit]
	}
	for _, row := range rows {
		record := appSubscriptionRecord(dbsqlc.AppSubscription{
			ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID, AppID: row.AppID,
			SubscriptionType: row.SubscriptionType, ScopeKind: row.ScopeKind, ScopeRef: row.ScopeRef,
			Events: row.Events, CreatedAt: row.CreatedAt,
		})
		record.AgentName = row.AgentName
		scope, err := appdefinition.ParseConversation(app.Provider, row.ScopeKind, row.ScopeRef)
		if err != nil {
			return ListAppSubscriptionsResult{}, err
		}
		record.Conversation, err = scope.ConversationJSON()
		if err != nil {
			return ListAppSubscriptionsResult{}, err
		}
		result.Subscriptions = append(result.Subscriptions, record)
	}
	if result.HasMore {
		last := rows[len(rows)-1]
		result.Next = listing.KeysetCursor{Set: true, CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return result, nil
}
