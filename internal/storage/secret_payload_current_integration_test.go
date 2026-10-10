//go:build integration

package storage

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestCurrentOrgSecretPayloadTracksRotationAndChecksAuthority(t *testing.T) {
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Payload owner", "admin")
	record, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg, Name: "current-payload",
		Material: secrets.GenericMaterial{Value: "initial"}, Actor: userPrincipal(admin.ID),
	})
	require.NoError(t, err)
	input := secretstore.ReadOrgOwnedSecretPayloadInput{
		OrgID: testOrgID, SecretID: record.ID, ManagementKind: management.Tenant, Kind: secretstore.SecretKindGeneric,
	}
	before := pool.Stat().AcquireCount()
	payload, err := store.Secrets().ReadOrgOwnedSecretPayload(ctx, input)
	require.NoError(t, err)
	require.EqualValues(t, 1, pool.Stat().AcquireCount()-before)
	require.Equal(t, "initial", payload.Payload[secrets.KeyValue])
	require.Equal(t, record.CurrentVersionID, payload.CurrentVersionID)
	_, version, err := store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
		OrgID: testOrgID, SecretID: record.ID,
		Material: secrets.GenericMaterial{Value: "rotated"}, Actor: userPrincipal(admin.ID),
	})
	require.NoError(t, err)
	payload, err = store.Secrets().ReadOrgOwnedSecretPayload(ctx, input)
	require.NoError(t, err)
	require.Equal(t, version.ID, payload.CurrentVersionID)
	require.Equal(t, "rotated", payload.Payload[secrets.KeyValue])
	for _, test := range []struct {
		name   string
		mutate func(*secretstore.ReadOrgOwnedSecretPayloadInput)
		want   error
	}{
		{"organization", func(i *secretstore.ReadOrgOwnedSecretPayloadInput) { i.OrgID = uuid.New() }, storeerr.ErrNotFound},
		{"management", func(i *secretstore.ReadOrgOwnedSecretPayloadInput) {
			i.ManagementKind = management.Cluster
		}, storeerr.ErrNotFound},
		{"kind", func(i *secretstore.ReadOrgOwnedSecretPayloadInput) {
			i.Kind = secretstore.SecretKindOAuthTokenSet
		}, storeerr.ErrInvalidSecretRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := input
			test.mutate(&invalid)
			_, err := store.Secrets().ReadOrgOwnedSecretPayload(ctx, invalid)
			require.ErrorIs(t, err, test.want)
		})
	}
	_, err = store.Secrets().DeleteSecret(ctx, secretstore.DeleteSecretInput{
		OrgID: testOrgID, SecretID: record.ID, Actor: userPrincipal(admin.ID),
	})
	require.NoError(t, err)
	_, err = store.Secrets().ReadOrgOwnedSecretPayload(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}
