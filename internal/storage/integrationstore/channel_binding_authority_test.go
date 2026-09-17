package integrationstore

import (
	"fmt"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestReplyChannelGrantsRequireSendAndNonemptyTuple(t *testing.T) {
	for bits := range 8 {
		for _, parentSend := range []bool{false, true} {
			t.Run(fmt.Sprintf("tuple_%d_parent_send_%t", bits, parentSend), func(t *testing.T) {
				grants := ChannelGrants{
					ReceiveAllowed: bits&1 != 0, ReadAllowed: bits&2 != 0, SendAllowed: bits&4 != 0,
				}
				input := CreateIntegrationTargetBindingInput{
					ProjectID: uuid.New(), AgentID: uuid.New(), IntegrationInstallID: uuid.New(),
					IntegrationTargetID: uuid.New(), Source: "setup", ReadAllowed: true,
					SendAllowed: parentSend, ReplyChannelGrants: &grants,
				}
				normalized, err := normalizeCreateIntegrationTargetBindingInput(input)
				if bits == 0 || !parentSend {
					require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
					return
				}
				require.NoError(t, err)
				require.Equal(t, grants, *normalized.ReplyChannelGrants)
				grants = ChannelGrants{}
				require.NotEqual(t, grants, *normalized.ReplyChannelGrants, "normalization owns its tuple")
			})
		}
	}
}

func TestBindingInvalidInputsAreInvalidRequests(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CreateIntegrationTargetBindingInput)
	}{
		{"missing_project", func(in *CreateIntegrationTargetBindingInput) { in.ProjectID = uuid.Nil }},
		{"missing_agent", func(in *CreateIntegrationTargetBindingInput) { in.AgentID = uuid.Nil }},
		{"missing_installation", func(in *CreateIntegrationTargetBindingInput) { in.IntegrationInstallID = uuid.Nil }},
		{"missing_target", func(in *CreateIntegrationTargetBindingInput) { in.IntegrationTargetID = uuid.Nil }},
		{"empty_grants", func(in *CreateIntegrationTargetBindingInput) { in.ReceiveAllowed = false }},
		{"empty_source", func(in *CreateIntegrationTargetBindingInput) { in.Source = " \t\n" }},
		{"oversized_source", func(in *CreateIntegrationTargetBindingInput) { in.Source = strings.Repeat("a", 129) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := CreateIntegrationTargetBindingInput{
				ProjectID: uuid.New(), AgentID: uuid.New(), IntegrationInstallID: uuid.New(),
				IntegrationTargetID: uuid.New(), ReceiveAllowed: true, Source: "api",
			}
			tt.mutate(&input)
			_, err := normalizeCreateIntegrationTargetBindingInput(input)
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
		})
	}
}

func TestBindingReceiveGrantDoesNotRequireRoute(t *testing.T) {
	input := CreateIntegrationTargetBindingInput{
		ProjectID: uuid.New(), AgentID: uuid.New(), IntegrationInstallID: uuid.New(),
		IntegrationTargetID: uuid.New(), ReceiveAllowed: true, Source: " api ",
	}
	normalized, err := normalizeCreateIntegrationTargetBindingInput(input)
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, normalized.IntegrationRouteID)
	require.True(t, normalized.ReceiveAllowed)
	require.False(t, normalized.ReadAllowed)
	require.False(t, normalized.SendAllowed)
	require.Nil(t, normalized.ReplyChannelGrants)
	require.Equal(t, "api", normalized.Source)
}
