package identitystore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

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
			name:      "unsupported principal",
			principal: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeAgent, ID: id},
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
