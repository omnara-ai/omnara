//go:build integration

package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelPriceCatalogKeepsOneSharedCopy(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	models := NewStore(openIntegrationDB(t, ctx)).Models()

	version, data, err := models.ModelPriceCatalog(ctx)
	require.NoError(t, err)
	require.Empty(t, version)
	require.Nil(t, data)
	data, err = models.ChangedModelPriceCatalog(ctx, "builtin")
	require.NoError(t, err)
	require.Nil(t, data, "workers keep the bundled prices until maintenance stores a copy")

	require.NoError(t, models.SaveModelPriceCatalog(ctx, []byte(`["first"]`), "v1"))
	data, err = models.ChangedModelPriceCatalog(ctx, "builtin")
	require.NoError(t, err)
	require.Equal(t, []byte(`["first"]`), data)
	data, err = models.ChangedModelPriceCatalog(ctx, "v1")
	require.NoError(t, err)
	require.Nil(t, data, "a worker on the stored version gets nothing back")

	require.NoError(t, models.SaveModelPriceCatalog(ctx, []byte(`["second"]`), "v2"))
	version, data, err = models.ModelPriceCatalog(ctx)
	require.NoError(t, err)
	require.Equal(t, "v2", version)
	require.Equal(t, []byte(`["second"]`), data)
	data, err = models.ChangedModelPriceCatalog(ctx, "v1")
	require.NoError(t, err)
	require.Equal(t, []byte(`["second"]`), data)
}
