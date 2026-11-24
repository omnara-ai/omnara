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

	save := func(data, version, expected string) bool {
		saved, err := models.SaveModelPriceCatalog(ctx, []byte(data), version, expected)
		require.NoError(t, err)
		return saved
	}
	require.True(t, save(`["first"]`, "v1", ""))
	require.False(t, save(`["late first"]`, "v0", ""), "a second first save loses to the one already stored")
	data, err = models.ChangedModelPriceCatalog(ctx, "builtin")
	require.NoError(t, err)
	require.Equal(t, []byte(`["first"]`), data)
	data, err = models.ChangedModelPriceCatalog(ctx, "v1")
	require.NoError(t, err)
	require.Nil(t, data, "a worker on the stored version gets nothing back")

	require.True(t, save(`["second"]`, "v2", "v1"))
	require.False(t, save(`["stale"]`, "v3", "v1"), "a save validated against an older copy changes nothing")
	version, data, err = models.ModelPriceCatalog(ctx)
	require.NoError(t, err)
	require.Equal(t, "v2", version)
	require.Equal(t, []byte(`["second"]`), data)
	data, err = models.ChangedModelPriceCatalog(ctx, "v1")
	require.NoError(t, err)
	require.Equal(t, []byte(`["second"]`), data)
}
