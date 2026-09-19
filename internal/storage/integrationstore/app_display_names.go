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

// GetConversationDisplayName returns the latest nonempty label from a live
// target at this exact address, or an empty string when no label is known.
// It neither chooses an agent nor grants any authority to receive or send.
func (s *Store) GetConversationDisplayName(
	ctx context.Context,
	projectID, connectionID uuid.UUID,
	address ConversationAddress,
) (string, error) {
	if projectID == uuid.Nil || connectionID == uuid.Nil {
		return "", storeerr.InvalidRequest(errors.New("project and connection are required"))
	}
	if err := address.Validate(); err != nil {
		return "", err
	}
	name, err := s.q.GetConversationDisplayName(ctx, dbsqlc.GetConversationDisplayNameParams{
		ProjectID: projectID, ConnectionID: connectionID, Kind: address.Kind, Ref: address.Ref,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get conversation display name: %w", err)
	}
	return name, nil
}
