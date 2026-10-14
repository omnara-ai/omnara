//go:build integration

package executionstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestIntegrationActorUnchangedLabelDoesNotLockSharedActor(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	params, err := executionstore.IntegrationActorParams(integrationstore.IntegrationRecord{
		IntegrationKind: integrationdefinition.SlackThread, Provider: "slack", ProviderTenantID: "T123",
	}, "U123", nil)
	require.NoError(t, err)
	id, err := executionstore.IntegrationResolveActorTx(ctx, store.q, testProjectID, &params)
	require.NoError(t, err)
	holder := integrationdb.BeginTx(t, ctx, pool)
	_, err = holder.Exec(ctx, `SELECT id FROM actors WHERE id=$1 FOR NO KEY UPDATE`, id)
	require.NoError(t, err)
	repeated := integrationdb.RunAsync(func() (uuid.UUID, error) {
		return executionstore.IntegrationResolveActorTx(ctx, store.q, testProjectID, &params)
	})
	require.Equal(t, id, integrationdb.AwaitSuccess(t, repeated, "reuse actor without locking its unchanged label"))
}

func TestIntegrationActorSharingAndIsolation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	otherProject := uuid.New()
	storagefixture.InsertProject(t, ctx, pool, testOrgID, otherProject, "Other project", "actor-project", time.Now())
	ids := map[string]uuid.UUID{}
	for _, tc := range []struct {
		kind                integrationdefinition.Kind
		project             uuid.UUID
		tenant, user, group string
	}{
		{integrationdefinition.SlackThread, testProjectID, "T_SHARED", "123", "slack"},
		{integrationdefinition.SlackThread, testProjectID, "T_SHARED", "123", "slack"},
		{integrationdefinition.SlackThread, testProjectID, "T_OTHER", "123", "other workspace"},
		{integrationdefinition.SlackThread, testProjectID, "T_SHARED", "456", "other user"},
		{integrationdefinition.SlackThread, otherProject, "T_SHARED", "123", "other project"},
		{integrationdefinition.DiscordThread, testProjectID, "app-1", "123", "discord"},
		{integrationdefinition.DiscordThread, testProjectID, "app-2", "123", "discord"},
		{integrationdefinition.GitHubPR, testProjectID, "install-1", "123", "github"},
		{integrationdefinition.GitHubPR, testProjectID, "install-2", "123", "github"},
	} {
		integration := integrationstore.IntegrationRecord{
			ID: uuid.New(), ProjectID: tc.project, IntegrationKind: tc.kind,
			Provider: integrationdefinition.ProviderForKind(tc.kind), ProviderTenantID: tc.tenant,
			ProviderAccountRef: uuid.NewString(),
		}
		params, err := executionstore.IntegrationActorParams(integration, tc.user, nil)
		require.NoError(t, err)
		id, err := executionstore.IntegrationResolveActorTx(ctx, store.q, tc.project, &params)
		require.NoError(t, err)
		if previous, exists := ids[tc.group]; exists {
			require.Equal(t, previous, id, tc.group)
		} else {
			for group, previous := range ids {
				require.NotEqual(t, previous, id, "%s must be isolated from %s", tc.group, group)
			}
			ids[tc.group] = id
		}
		actor, err := store.Execution().GetActor(ctx, tc.project, id)
		require.NoError(t, err)
		metadata, err := resourcemeta.FromJSON(actor.Metadata)
		require.NoError(t, err)
		require.Equal(t, params.Metadata, metadata)
	}
}

func TestInternalActorMetadataPersistsAndPreservesHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	name := "Historical sender"
	params := executionstore.ActorParams{
		Provider: executionstore.ActorProviderIntegration, ProviderTenantID: "slack:T_HISTORY",
		ProviderUserID: "U_HISTORY", DisplayName: &name,
		Metadata: resourcemeta.Metadata{"retained": "history"},
	}
	actorID, err := executionstore.IntegrationResolveActorTx(ctx, store.q, testProjectID, &params)
	require.NoError(t, err)
	actor, err := store.Execution().GetActor(ctx, testProjectID, actorID)
	require.NoError(t, err)
	require.JSONEq(t, `{"retained":"history"}`, string(actor.Metadata))

	params.Metadata = resourcemeta.Metadata{"source_label": "Slack"}
	withLabelID, err := executionstore.IntegrationResolveActorTx(ctx, store.q, testProjectID, &params)
	require.NoError(t, err)
	require.Equal(t, actorID, withLabelID)
	withLabel, err := store.Execution().GetActor(ctx, testProjectID, actorID)
	require.NoError(t, err)
	require.JSONEq(t, `{"retained":"history","source_label":"Slack"}`, string(withLabel.Metadata))

	_, err = executionstore.IntegrationResolveActorTx(ctx, store.q, testProjectID, &params)
	require.NoError(t, err)
	repeated, err := store.Execution().GetActor(ctx, testProjectID, actorID)
	require.NoError(t, err)
	require.Equal(t, withLabel.UpdatedAt, repeated.UpdatedAt)

	params.Metadata = nil
	params.DisplayName = nil
	_, err = executionstore.IntegrationResolveActorTx(ctx, store.q, testProjectID, &params)
	require.NoError(t, err)
	omitted, err := store.Execution().GetActor(ctx, testProjectID, actorID)
	require.NoError(t, err)
	require.Equal(t, withLabel, omitted)

	params.Metadata = resourcemeta.Metadata{"source_label": "Updated source"}
	_, err = executionstore.IntegrationResolveActorTx(ctx, store.q, testProjectID, &params)
	require.NoError(t, err)
	updated, err := store.Execution().GetActor(ctx, testProjectID, actorID)
	require.NoError(t, err)
	require.JSONEq(t, `{"retained":"history","source_label":"Updated source"}`, string(updated.Metadata))
	require.Equal(t, name, updated.DisplayName)
	require.Equal(t, actor.CreatedAt, updated.CreatedAt)

	params.ProviderTenantID = "discord"
	params.Metadata = resourcemeta.Metadata{"source_label": "Discord"}
	discordID, err := executionstore.IntegrationResolveActorTx(ctx, store.q, testProjectID, &params)
	require.NoError(t, err)
	require.NotEqual(t, actorID, discordID)
	discordActor, err := store.Execution().GetActor(ctx, testProjectID, discordID)
	require.NoError(t, err)
	require.JSONEq(t, `{"source_label":"Discord"}`, string(discordActor.Metadata))
}
