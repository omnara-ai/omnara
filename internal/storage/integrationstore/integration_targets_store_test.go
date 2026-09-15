package integrationstore

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestChannelRegistrationRequiresDefinitionBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()
	store := New(nil, nil)
	input := CreateIntegrationTargetInput{
		ProjectID: uuid.New(), IntegrationInstallID: uuid.New(), ProviderRef: "thread", ProviderRefKind: "thread",
	}
	_, err := store.CreateIntegrationTarget(context.Background(), input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	// Every DB operation on this transaction would panic: rejection must precede
	// SQL even when registration composes with a caller-owned transaction.
	tx := &struct{ pgx.Tx }{}
	_, err = store.CreateIntegrationTargetTx(context.Background(), tx, input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
}

func TestChannelRegistrationRejectsInvalidIdentifiersBeforeSQL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*CreateIntegrationTargetInput)
	}{
		{"missing_project", func(in *CreateIntegrationTargetInput) { in.ProjectID = NilID }},
		{"missing_installation", func(in *CreateIntegrationTargetInput) { in.IntegrationInstallID = NilID }},
		{"blank_reference", func(in *CreateIntegrationTargetInput) { in.ProviderRef = " \t\n" }},
		{"blank_reference_kind", func(in *CreateIntegrationTargetInput) { in.ProviderRefKind = " \t\n" }},
		{"reference_byte_limit", func(in *CreateIntegrationTargetInput) { in.ProviderRef = strings.Repeat("é", 1025) }},
		{"reference_kind_byte_limit", func(in *CreateIntegrationTargetInput) {
			in.ProviderRefKind = strings.Repeat("é", 65)
		}},
		{"name_byte_limit", func(in *CreateIntegrationTargetInput) { in.DisplayName = strings.Repeat("é", 257) }},
		{"reference_nul", func(in *CreateIntegrationTargetInput) { in.ProviderRef = "thread\x00suffix" }},
		{"reference_kind_nul", func(in *CreateIntegrationTargetInput) { in.ProviderRefKind = "thread\x00" }},
		{"name_nul", func(in *CreateIntegrationTargetInput) { in.DisplayName = "name\x00" }},
		{"reference_invalid_utf8", func(in *CreateIntegrationTargetInput) { in.ProviderRef = "thread\xff" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := CreateIntegrationTargetInput{
				ProjectID: uuid.New(), IntegrationInstallID: uuid.New(), ChannelDefinitionID: uuid.New(),
				ProviderRef: "thread", ProviderRefKind: "thread", DisplayName: "Name",
			}
			tt.mutate(&input)
			// Any SQL on this transaction panics. Input rejection must happen
			// before authority lookup or row creation in the shared Tx path.
			tx := &struct{ pgx.Tx }{}
			_, err := New(nil, nil).CreateIntegrationTargetTx(context.Background(), tx, input)
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
		})
	}
}
