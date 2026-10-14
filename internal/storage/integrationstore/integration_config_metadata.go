package integrationstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func (s *Store) ResolveIntegrationDefinitions(
	ctx context.Context,
	projectID uuid.UUID,
	ids []uuid.UUID,
) (map[uuid.UUID]agentconfig.IntegrationResolution, error) {
	result := make(map[uuid.UUID]agentconfig.IntegrationResolution, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := s.q.ListIntegrationMetadataByIDs(
		ctx,
		dbsqlc.ListIntegrationMetadataByIDsParams{ProjectID: projectID, Ids: ids},
	)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.DeletedAt != nil || row.State != string(IntegrationStateActive) {
			continue
		}
		result[row.ID] = agentconfig.IntegrationResolution{
			IntegrationID: row.ID, IntegrationKind: integrationdefinition.Kind(row.IntegrationKind),
		}
	}
	return result, nil
}
