package integrationstore

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestNormalizeIntegrationInstaller(t *testing.T) {
	t.Parallel()
	orgID, subjectID, credentialID := uuid.New(), uuid.New(), uuid.New()
	user := identitystore.NewUserPrincipal(subjectID)
	key := identitystore.NewOrgAPIKeyPrincipal(orgID, subjectID)
	for _, principal := range []identitystore.PrincipalRecord{
		user,
		identitystore.NewBrowserSessionPrincipal(subjectID, credentialID),
		identitystore.NewPersonalAccessTokenPrincipal(subjectID, credentialID),
		identitystore.NewOAuthAccessTokenPrincipal(subjectID, credentialID),
		key,
		{Type: identitystore.PrincipalTypeOrgAPIKey, ID: subjectID},
	} {
		got, err := normalizeIntegrationInstaller(orgID, principal)
		if err != nil {
			t.Fatalf("normalize %s installer: %v", principal.Type, err)
		}
		want := user
		if principal.Type == identitystore.PrincipalTypeOrgAPIKey {
			want = key
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("normalized installer = %+v, want %+v", got, want)
		}
	}

	for name, principal := range map[string]identitystore.PrincipalRecord{
		"empty":                           {},
		"user without id":                 {Type: identitystore.PrincipalTypeUser},
		"key without id":                  {Type: identitystore.PrincipalTypeOrgAPIKey, OrgID: orgID},
		"other org":                       identitystore.NewOrgAPIKeyPrincipal(uuid.New(), subjectID),
		"mismatched key":                  {Type: identitystore.PrincipalTypeOrgAPIKey, ID: subjectID, OrgAPIKeyID: credentialID},
		"user with key credential":        {Type: identitystore.PrincipalTypeUser, ID: subjectID, OrgAPIKeyID: credentialID},
		"key with user credential":        {Type: identitystore.PrincipalTypeOrgAPIKey, ID: subjectID, BrowserSessionID: credentialID},
		"machine":                         identitystore.NewMachineDaemonPrincipal(orgID, subjectID, credentialID),
		"connector":                       identitystore.NewChannelConnectorPrincipal("gateway", []channelconnector.Capability{{ConnectorKey: "omnara", Provider: "slack"}}),
		"user with connector credentials": {Type: identitystore.PrincipalTypeUser, ID: subjectID, ChannelConnectorID: "gateway"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := normalizeIntegrationInstaller(orgID, principal); !errors.Is(err, storeerr.ErrUnauthorized) {
				t.Fatalf("installer error = %v, want unauthorized", err)
			}
		})
	}
}
