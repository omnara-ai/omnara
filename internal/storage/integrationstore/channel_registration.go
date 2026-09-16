package integrationstore

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// RegisteredChannel is a setup projection. Its presence grants no agent access
// and does not expose provider messages or unbounded provider metadata.
type RegisteredChannel struct {
	ID, ChannelDefinitionID, ParentChannelID uuid.UUID
	ProviderRef, ProviderRefKind, Name       string
	CreatedAt                                time.Time
}

type ListRegisteredChannelsInput struct {
	ProjectID, IntegrationInstallID uuid.UUID
	After                           listing.KeysetCursor
	Limit                           int
}

type RegisteredChannelsPage struct {
	Channels []RegisteredChannel
	Next     listing.KeysetCursor
}

func (s *Store) ListRegisteredChannels(
	ctx context.Context, input ListRegisteredChannelsInput,
) (RegisteredChannelsPage, error) {
	var page RegisteredChannelsPage
	if input.ProjectID == uuid.Nil || input.IntegrationInstallID == uuid.Nil || input.Limit < 1 || input.Limit > 100 {
		return page, storeerr.InvalidRequest(errors.New("project, installation and a limit from 1 to 100 are required"))
	}
	if input.After.Set && (input.After.ID == uuid.Nil || input.After.CreatedAt.IsZero()) {
		return page, storeerr.InvalidRequest(errors.New("invalid registered channel cursor"))
	}
	if _, err := s.GetIntegrationInstall(ctx, input.ProjectID, input.IntegrationInstallID); err != nil {
		return page, err
	}
	rows, err := s.q.ListRegisteredChannels(ctx, dbsqlc.ListRegisteredChannelsParams{
		ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID,
		CursorSet: input.After.Set, CursorID: input.After.ID, CursorCreatedAt: input.After.CreatedAt,
		RowLimit: int32(input.Limit + 1),
	})
	if err != nil {
		return page, integrationChannelReadError("list registered channels", err)
	}
	if len(rows) > input.Limit {
		rows = rows[:input.Limit]
		last := rows[len(rows)-1]
		page.Next = listing.KeysetCursor{Set: true, ID: last.ID, CreatedAt: last.CreatedAt}
	}
	page.Channels = make([]RegisteredChannel, 0, len(rows))
	for _, row := range rows {
		page.Channels = append(page.Channels, RegisteredChannel{
			ID: row.ID, ChannelDefinitionID: row.ChannelDefinitionID,
			ParentChannelID: storeutil.IDFromPtr(row.ParentChannelID),
			ProviderRef:     row.ProviderRef, ProviderRefKind: row.ProviderRefKind,
			Name: row.DisplayName, CreatedAt: row.CreatedAt,
		})
	}
	return page, nil
}
