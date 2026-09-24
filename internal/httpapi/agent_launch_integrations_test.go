package httpapi

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/stretchr/testify/require"
)

func TestPublicLaunchInitialInputAttribution(t *testing.T) {
	t.Parallel()
	project := identitystore.ProjectRecord{ID: uuid.New(), OrgID: uuid.New()}
	principal := identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeOrgAPIKey, ID: uuid.New()}
	body := openapi.AgentLaunchInitialInput{
		ContentBlocks: []openapi.TextContentBlock{{Type: "text", Text: "First"}, {Type: "text", Text: "Second"}},
		Actor:         &openapi.ExternalActorParams{ProviderUserId: "customer-7"},
	}
	initial, err := publicLaunchInitialInput(project, principal, body)
	require.NoError(t, err)
	require.JSONEq(t, `[{"type":"text","text":"First"},{"type":"text","text":"Second"}]`, string(initial.ContentBlocks))
	require.Nil(t, initial.Origin)
	require.Equal(t, executionstore.ActorProviderExternal, initial.Actor.Provider)
	require.Equal(t, "customer-7", initial.Actor.ProviderUserID)
	principal.Type = identitystore.PrincipalTypeUser
	_, err = publicLaunchInitialInput(project, principal, body)
	require.ErrorContains(t, err, "actor cannot be combined with user authentication")
	body.Actor = nil
	initial, err = publicLaunchInitialInput(project, principal, body)
	require.NoError(t, err)
	require.Equal(t, executionstore.ActorProviderOmnara, initial.Actor.Provider)
}

func TestPublicLaunchInitialInputExcludesMessage(t *testing.T) {
	t.Parallel()
	body := openapi.AgentLaunchInitialInput{ContentBlocks: []openapi.TextContentBlock{{Type: "text", Text: "First"}}}
	server := strictOpenAPIServer{}
	project := identitystore.ProjectRecord{ID: uuid.New(), OrgID: uuid.New()}
	principal := identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: uuid.New()}
	empty := ""
	_, err := server.preparePublicAgentLaunch(t.Context(), project, principal,
		openapi.CreateAgentRequest{Message: &empty, InitialInput: &body}, executionstore.LaunchAgentInput{})
	require.ErrorContains(t, err, "initial_input is mutually exclusive with message")
}
