package integrationstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) GetConversationDisplayName(
	ctx context.Context,
	projectID, appID uuid.UUID,
	address ConversationAddress,
) (string, error) {
	if projectID == uuid.Nil || appID == uuid.Nil {
		return "", storeerr.InvalidRequest(errors.New("project and app are required"))
	}
	if err := address.Validate(); err != nil {
		return "", err
	}
	name, err := s.q.GetConversationDisplayName(ctx, dbsqlc.GetConversationDisplayNameParams{
		ProjectID: projectID, AppID: appID, Kind: address.Kind, Ref: address.Ref,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get conversation display name: %w", err)
	}
	return name, nil
}
