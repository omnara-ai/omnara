package identitystore_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func TestPrincipalConstructors(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("0198f0e5-eeb4-7000-8000-000000000001")
	orgID := uuid.MustParse("0198f0e5-eeb4-7000-8000-000000000002")
	machineID := uuid.MustParse("0198f0e5-eeb4-7000-8000-000000000003")
	credentialID := uuid.MustParse("0198f0e5-eeb4-7000-8000-000000000004")

	tests := []struct {
		name string
		got  identitystore.PrincipalRecord
		want identitystore.PrincipalRecord
	}{
		{
			name: "user",
			got:  identitystore.NewUserPrincipal(userID),
			want: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: userID},
		},
		{
			name: "personal access token",
			got:  identitystore.NewPersonalAccessTokenPrincipal(userID, credentialID),
			want: identitystore.PrincipalRecord{
				Type:                  identitystore.PrincipalTypeUser,
				ID:                    userID,
				PersonalAccessTokenID: credentialID,
			},
		},
		{
			name: "browser session",
			got:  identitystore.NewBrowserSessionPrincipal(userID, credentialID),
			want: identitystore.PrincipalRecord{
				Type:             identitystore.PrincipalTypeUser,
				ID:               userID,
				BrowserSessionID: credentialID,
			},
		},
		{
			name: "organization API key",
			got:  identitystore.NewOrgAPIKeyPrincipal(orgID, credentialID),
			want: identitystore.PrincipalRecord{
				Type:        identitystore.PrincipalTypeOrgAPIKey,
				ID:          credentialID,
				OrgID:       orgID,
				OrgAPIKeyID: credentialID,
			},
		},
		{
			name: "machine daemon",
			got:  identitystore.NewMachineDaemonPrincipal(orgID, machineID, credentialID),
			want: identitystore.PrincipalRecord{
				Type:                 identitystore.PrincipalTypeMachineDaemon,
				ID:                   machineID,
				OrgID:                orgID,
				MachineDaemonTokenID: credentialID,
			},
		},
		{
			name: "channel connector",
			got: identitystore.NewChannelConnectorPrincipal(
				"primary",
				[]channelconnector.Capability{{ConnectorKey: "chat_sdk_v1", Provider: "discord"}},
			),
			want: identitystore.PrincipalRecord{
				Type: identitystore.PrincipalTypeChannelConnector, ChannelConnectorID: "primary",
				ChannelConnectorCapabilities: []channelconnector.Capability{{
					ConnectorKey: "chat_sdk_v1", Provider: "discord",
				}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if !reflect.DeepEqual(test.got, test.want) {
				t.Fatalf("principal = %+v, want %+v", test.got, test.want)
			}
		})
	}
}

func TestAccountPrincipalIDs(t *testing.T) {
	t.Parallel()
	id := uuid.MustParse("0198f0e5-eeb4-7000-8000-000000000001")
	tests := []struct {
		name        string
		principal   identitystore.PrincipalRecord
		wantUser    bool
		wantAPIKey  bool
		wantAccount bool
	}{
		{
			name:        "user",
			principal:   identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: id},
			wantUser:    true,
			wantAccount: true,
		},
		{
			name:        "organization API key",
			principal:   identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeOrgAPIKey, ID: id},
			wantAPIKey:  true,
			wantAccount: true,
		},
		{
			name:      "nil user ID",
			principal: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser},
		},
		{
			name:      "machine daemon is not an account principal",
			principal: identitystore.NewMachineDaemonPrincipal(id, id, id),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			userID, orgAPIKeyID := identitystore.AccountPrincipalIDs(test.principal)
			if (userID != nil) != test.wantUser {
				t.Fatalf("user id presence = %v, want %v", userID != nil, test.wantUser)
			}
			if userID != nil && *userID != id {
				t.Fatalf("user id = %s, want %s", *userID, id)
			}
			if (orgAPIKeyID != nil) != test.wantAPIKey {
				t.Fatalf("organization API key id presence = %v, want %v", orgAPIKeyID != nil, test.wantAPIKey)
			}
			if orgAPIKeyID != nil && *orgAPIKeyID != id {
				t.Fatalf("organization API key id = %s, want %s", *orgAPIKeyID, id)
			}
			if got := identitystore.IsAccountPrincipal(test.principal); got != test.wantAccount {
				t.Fatalf("account principal = %v, want %v", got, test.wantAccount)
			}
		})
	}
}
