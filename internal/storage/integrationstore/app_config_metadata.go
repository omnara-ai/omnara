package integrationstore

import (
	"context"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

// ResolveAppDefinitions performs one project-scoped read for model preparation.
// Unavailable apps are omitted; callers can still prepare unrelated capabilities.
// This metadata never authorizes provider I/O or returns credentials.
func (s *Store) ResolveAppDefinitions(
	ctx context.Context,
	projectID uuid.UUID,
	refs []string,
) (map[string]agentconfig.AppResolution, error) {
	byID := make(map[uuid.UUID]string, len(refs))
	for _, ref := range refs {
		id, err := publicid.Decode(publicid.KindProjectApp, ref)
		if err != nil {
			return nil, err
		}
		byID[id] = ref
	}
	result := make(map[string]agentconfig.AppResolution, len(byID))
	if len(byID) == 0 {
		return result, nil
	}
	rows, err := s.q.ListProjectAppMetadataByIDs(
		ctx,
		dbsqlc.ListProjectAppMetadataByIDsParams{ProjectID: projectID, Ids: slices.Collect(maps.Keys(byID))},
	)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.DeletedAt != nil || row.State != string(ProjectAppStateActive) {
			continue
		}
		ref := byID[row.ID]
		result[ref] = agentconfig.AppResolution{AppID: ref, AppType: appdefinition.Type(row.AppType)}
	}
	return result, nil
}
