package integrationstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func (s *Store) ResolveAppDefinitions(
	ctx context.Context,
	projectID uuid.UUID,
	ids []uuid.UUID,
) (map[uuid.UUID]agentconfig.AppResolution, error) {
	result := make(map[uuid.UUID]agentconfig.AppResolution, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := s.q.ListProjectAppMetadataByIDs(
		ctx,
		dbsqlc.ListProjectAppMetadataByIDsParams{ProjectID: projectID, Ids: ids},
	)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.DeletedAt != nil || row.State != string(ProjectAppStateActive) {
			continue
		}
		result[row.ID] = agentconfig.AppResolution{AppID: row.ID, AppType: appdefinition.Type(row.AppType)}
	}
	return result, nil
}
