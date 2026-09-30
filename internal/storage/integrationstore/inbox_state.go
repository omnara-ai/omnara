package integrationstore

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func enqueueIntegrationStateWork(ctx context.Context, q *dbsqlc.Queries, projectID, integrationID uuid.UUID,
	receiptKey string, stateID uuid.UUID, reserve *ConversationAddress) (IntegrationInboxRecord, error) {
	if projectID == uuid.Nil || integrationID == uuid.Nil || stateID == uuid.Nil || receiptKey == "" ||
		len(receiptKey) > IntegrationInboxMaxReceiptKeyBytes ||
		!utf8.ValidString(receiptKey) || strings.ContainsRune(receiptKey, 0) {
		return IntegrationInboxRecord{}, inboxInvalid("state work requires project, integration, state and receipt key")
	}
	var kind, ref *string
	if reserve != nil {
		if err := reserve.Validate(); err != nil {
			return IntegrationInboxRecord{}, err
		}
		kind, ref = &reserve.Kind, &reserve.Ref
	}
	row, err := q.InsertIntegrationStateWork(ctx, dbsqlc.InsertIntegrationStateWorkParams{
		ProjectID: projectID, IntegrationID: integrationID, ReceiptKey: receiptKey, SourceStateID: &stateID,
		ReservedScopeKind: kind, ReservedScopeRef: ref,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = q.GetIntegrationInboxReceiptByKey(ctx, dbsqlc.GetIntegrationInboxReceiptByKeyParams{
			ProjectID: projectID, IntegrationID: integrationID, ReceiptKey: receiptKey,
		})
		if err == nil && (row.Source != string(IntegrationInboxSourceState) ||
			row.SourceStateID == nil || *row.SourceStateID != stateID ||
			storeutil.TextOrEmpty(row.ReservedScopeKind) != storeutil.TextOrEmpty(kind) ||
			storeutil.TextOrEmpty(row.ReservedScopeRef) != storeutil.TextOrEmpty(ref)) {
			err = storeerr.ErrIdempotencyConflict
		}
	}
	return inboxRecord(row), err
}
