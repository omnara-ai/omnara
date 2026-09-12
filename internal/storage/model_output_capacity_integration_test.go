//go:build integration

package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/patch"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestOutputCapacityConcurrentCreationAndImmutableClear(t *testing.T) {
	t.Parallel()
	for _, format := range []modelprotocol.APIFormat{
		modelprotocol.APIFormatOpenAIResponses, modelprotocol.APIFormatAnthropicMessages,
	} {
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newIntegrationStore(pool)
			provider := requireSingleConcurrentCreate(t, func() (modelstore.ModelProviderConfigRecord, error) {
				return store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
					OrgID: testOrgID, Name: "capacity-provider", APIFormat: format,
					BaseURL: "https://example.test", CredentialSecretID: testDefaultProviderCredentialSecretID,
				})
			})
			providerID := provider.ID
			created := requireSingleConcurrentCreate(t, func() (modelstore.ConfiguredModelRecord, error) {
				return store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
					OrgID: testOrgID, ModelProviderConfigID: providerID, Name: "optional-capacity-model",
					ProviderModelSlug: "test-model", ContextWindowTokens: 128000,
				})
			})
			require.Nil(t, created.MaxOutputTokens)
			require.Nil(t, created.DefaultMaxOutputTokens)
			old, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
				OrgID: testOrgID, ModelProviderConfigID: providerID, ID: created.ID,
				MaxOutputTokens: patch.NullableInt{Set: true, Value: new(64000)},
			})
			require.NoError(t, err)
			grant := requireSingleConcurrentCreate(t, func() (modelstore.ProjectModelGrantRecord, error) {
				return store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
					OrgID: testOrgID, ProjectID: testProjectID, ConfiguredModelID: old.ID,
					ContextWindowTokens: new(32000),
				})
			})
			if grant.ContextWindowTokens == nil || *grant.ContextWindowTokens != 32000 || grant.MaxOutputTokens != nil {
				t.Fatalf("grant bounds changed: %+v", grant)
			}
			_, err = store.Models().UpdateProjectModelGrant(ctx, modelstore.UpdateProjectModelGrantInput{
				OrgID: testOrgID, ProjectID: testProjectID, ID: grant.ID,
				MaxOutputTokens: patch.NullableInt{Set: true, Value: new(64000)},
			})
			if !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
				t.Fatalf("explicit grant bounds must fail application validation: %v", err)
			}
			clearInput := modelstore.PatchConfiguredModelInput{
				OrgID: testOrgID, ModelProviderConfigID: providerID, ID: old.ID, MaxOutputTokens: patch.NullableInt{Set: true},
			}
			cleared, err := store.Models().PatchConfiguredModel(ctx, clearInput)
			require.NoError(t, err)
			historical, err := store.Models().GetConfiguredModelRevisionForUse(ctx, testOrgID, old.CurrentRevisionID)
			require.NoError(t, err)
			current, err := store.Models().GetConfiguredModelRevisionForUse(ctx, testOrgID, cleared.CurrentRevisionID)
			require.NoError(t, err)
			if cleared.MaxOutputTokens != nil ||
				current.MaxOutputTokens != nil ||
				current.DefaultMaxOutputTokens != nil ||
				historical.MaxOutputTokens == nil ||
				*historical.MaxOutputTokens != *old.MaxOutputTokens ||
				old.CurrentRevisionID == cleared.CurrentRevisionID {
				t.Fatal("nullable revision changed immutable history")
			}
			_, err = store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
				OrgID: testOrgID, ModelProviderConfigID: providerID, Name: old.Name,
				ProviderModelSlug: old.ProviderModelSlug, ContextWindowTokens: old.ContextWindowTokens,
			})
			require.ErrorIs(t, err, storeerr.ErrConflict)
			unchanged, err := store.Models().GetConfiguredModelByName(ctx, testOrgID, providerID, old.Name)
			require.NoError(t, err)
			require.Equal(t, cleared, unchanged, "duplicate creation must preserve cleared capacity and current revision")
			for _, name := range []*string{nil, new("renamed-null-capacity")} {
				updated, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
					OrgID: testOrgID, ModelProviderConfigID: providerID, ID: old.ID, Name: name,
				})
				require.NoError(t, err)
				if updated.CurrentRevisionID != cleared.CurrentRevisionID || updated.MaxOutputTokens != nil {
					t.Fatal("no-op or name-only update changed cleared capacity")
				}
				if name != nil && updated.Name != *name {
					t.Fatalf("name = %q, want %q", updated.Name, *name)
				}
			}
			var revisions int
			require.NoError(t, pool.QueryRow(ctx,
				`SELECT count(*) FROM configured_model_revisions WHERE configured_model_id=$1`, old.ID,
			).Scan(&revisions))
			require.Equal(t, 3, revisions, "only creation, setting, and clearing capacity append revisions")
			for _, tc := range []struct {
				name                string
				context             int
				capacity, allowance *int
			}{
				{name: "one-token context", context: 1},
				{name: "allowance exhausts unknown capacity context", context: 128000, allowance: new(128000)},
				{name: "zero capacity", context: 128000, capacity: new(0)},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					_, err := pool.Exec(
						ctx,
						`INSERT INTO configured_model_revisions(org_id,configured_model_id,model_provider_config_id,provider_model_slug,context_window_tokens,max_output_tokens,default_max_output_tokens,created_at) VALUES($1,$2,$3,'test-model',$4,$5,$6,statement_timestamp())`,
						testOrgID,
						old.ID,
						providerID,
						tc.context,
						tc.capacity,
						tc.allowance,
					)
					var pgErr *pgconn.PgError
					if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
						t.Fatalf("invalid nullable limits error=%v", err)
					}
				})
			}
		})
	}
}

func requireSingleConcurrentCreate[T any](t *testing.T, create func() (T, error)) T {
	t.Helper()
	type result struct {
		record T
		err    error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			record, err := create()
			results <- result{record, err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil {
		first, second = second, first
	}
	require.NoError(t, first.err, "one concurrent creation must succeed")
	require.ErrorIs(t, second.err, storeerr.ErrConflict, "the duplicate must conflict")
	return first.record
}
