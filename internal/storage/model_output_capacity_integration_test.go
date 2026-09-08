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

func TestOutputCapacityConcurrentDiscoveryAndImmutableClear(t *testing.T) {
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
			providerID := testDefaultProviderConfigID()
			if format == modelprotocol.APIFormatAnthropicMessages {
				provider, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
					OrgID: testOrgID, Name: "capacity-messages", APIFormat: format,
					BaseURL: "https://example.test", CredentialSecretID: testDefaultProviderCredentialSecretID,
				})
				require.NoError(t, err)
				providerID = provider.ID
			}
			type creation struct {
				model modelstore.ConfiguredModelRecord
				err   error
			}
			results := make(chan creation, 2)
			start := make(chan struct{})
			for _, hint := range []int{64000, 96000} {
				go func() {
					<-start
					record, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
						OrgID:                     testOrgID,
						ModelProviderConfigID:     providerID,
						Name:                      "discovered-model",
						ProviderModelSlug:         "test-model",
						ContextWindowTokens:       128000,
						DiscoveredMaxOutputTokens: new(hint),
					})
					results <- creation{record, err}
				}()
			}
			close(start)
			first, second := <-results, <-results
			if first.err != nil || second.err != nil {
				t.Fatalf("concurrent errors=%v / %v", first.err, second.err)
			}
			if first.model.ID != second.model.ID ||
				first.model.CurrentRevisionID != second.model.CurrentRevisionID ||
				first.model.Created == second.model.Created ||
				first.model.MaxOutputTokens == nil ||
				second.model.MaxOutputTokens == nil ||
				first.model.DefaultMaxOutputTokens != nil ||
				second.model.DefaultMaxOutputTokens != nil ||
				*first.model.MaxOutputTokens != *second.model.MaxOutputTokens {
				t.Fatal("concurrent omitted-capacity creation did not converge")
			}
			old := first.model
			grant, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
				OrgID: testOrgID, ProjectID: testProjectID, ConfiguredModelID: old.ID,
				ContextWindowTokens: new(32000),
			})
			if err != nil {
				t.Fatalf("narrow context with inherited capacity: %v", err)
			}
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
			if format == modelprotocol.APIFormatAnthropicMessages {
				clearInput.Name = new("rejected-rename")
			}
			cleared, err := store.Models().PatchConfiguredModel(ctx, clearInput)
			if format == modelprotocol.APIFormatAnthropicMessages {
				require.ErrorIs(t, err, storeerr.ErrInvalidModelProviderConfig)
				current, err := store.Models().GetConfiguredModelByName(ctx, testOrgID, providerID, old.Name)
				require.NoError(t, err)
				require.Equal(t, old.CurrentRevisionID, current.CurrentRevisionID)
				require.Equal(t, old.MaxOutputTokens, current.MaxOutputTokens)
				require.Equal(t, old.Name, current.Name)
				var revisions int
				require.NoError(t, pool.QueryRow(ctx,
					`SELECT count(*) FROM configured_model_revisions WHERE configured_model_id=$1`, old.ID,
				).Scan(&revisions))
				require.Equal(t, 1, revisions, "rejected capacity clearing must not append a revision")
				var missingCapacityRevision ID
				require.NoError(t, pool.QueryRow(ctx, `
WITH revision AS (
    INSERT INTO configured_model_revisions(
        org_id, configured_model_id, model_provider_config_id, provider_model_slug,
        context_window_tokens, max_output_tokens, created_at
    ) VALUES ($1, $2, $3, 'test-model', 128000, NULL, statement_timestamp())
    RETURNING id
)
UPDATE configured_models SET current_revision_id=revision.id
FROM revision WHERE configured_models.id=$2 RETURNING revision.id`, testOrgID, old.ID, providerID,
				).Scan(&missingCapacityRevision))
				_, err = store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
					OrgID: testOrgID, ModelProviderConfigID: providerID, Name: old.Name,
					ProviderModelSlug: old.ProviderModelSlug, ContextWindowTokens: old.ContextWindowTokens,
				})
				require.ErrorIs(t, err, storeerr.ErrInvalidModelProviderConfig)
				for _, name := range []*string{nil, new("rejected-legacy-rename")} {
					_, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
						OrgID: testOrgID, ModelProviderConfigID: providerID, ID: old.ID, Name: name,
					})
					require.ErrorIs(t, err, storeerr.ErrInvalidModelProviderConfig)
				}
				current, err = store.Models().GetConfiguredModelByName(ctx, testOrgID, providerID, old.Name)
				require.NoError(t, err)
				require.Equal(t, missingCapacityRevision, current.CurrentRevisionID)
				require.Equal(t, old.Name, current.Name)
				require.Nil(t, current.MaxOutputTokens)
				require.NoError(t, pool.QueryRow(ctx,
					`SELECT count(*) FROM configured_model_revisions WHERE configured_model_id=$1`, old.ID,
				).Scan(&revisions))
				require.Equal(t, 2, revisions, "invalid replay and patches must not append revisions")
				return
			}
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
