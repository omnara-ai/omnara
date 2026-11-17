package modelstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func (s *Store) ModelPriceCatalog(ctx context.Context) (string, []byte, error) {
	row, err := s.q.GetModelPriceCatalog(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	return row.Version, row.Data, nil
}

func (s *Store) ChangedModelPriceCatalog(ctx context.Context, version string) ([]byte, error) {
	data, err := s.q.GetChangedModelPriceCatalog(ctx, dbsqlc.GetChangedModelPriceCatalogParams{Version: version})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return data, err
}

func (s *Store) SaveModelPriceCatalog(ctx context.Context, data []byte, version string) error {
	return s.q.SaveModelPriceCatalog(ctx, dbsqlc.SaveModelPriceCatalogParams{Data: data, Version: version})
}
