package integrationstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// FindIntegrationInstall resolves an existing connection, including disabled
// connections, when setup needs to preserve its behavior during reauthorization.
// UpsertIntegrationInstall rechecks identity and authority under transaction locks.
func (s *Store) FindIntegrationInstall(
	ctx context.Context, projectID, appID uuid.UUID, tenantID, accountRef string,
) (IntegrationInstallRecord, error) {
	row, err := s.q.GetIntegrationInstallByAppProviderAccount(ctx, dbsqlc.GetIntegrationInstallByAppProviderAccountParams{
		IntegrationAppID: appID, ProviderTenantID: storeutil.TextFromEmpty(tenantID), ProviderAccountRef: accountRef,
	})
	if err != nil {
		return IntegrationInstallRecord{}, integrationChannelReadError("find integration installation", err)
	}
	if row.ProjectID != projectID {
		return IntegrationInstallRecord{}, storeerr.ErrConflict
	}
	return integrationInstallRecordFromSQLC(row), nil
}
