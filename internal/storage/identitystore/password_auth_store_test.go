package identitystore

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func TestSelectPasswordLoginPreservesExactLegacyIdentity(t *testing.T) {
	t.Parallel()
	unicodeUser := ID{1}
	canonicalUser := ID{2}
	logins := []dbsqlc.ListPasswordLoginsByVerifiedEmailsRow{
		{UserID: unicodeUser, NormalizedEmail: "user@bücher.example"},
		{UserID: canonicalUser, NormalizedEmail: "user@xn--bcher-kva.example"},
	}
	for _, test := range []struct {
		name  string
		email string
		want  ID
	}{
		{name: "unicode", email: "User@BÜCHER.example", want: unicodeUser},
		{name: "punycode", email: "User@XN--BCHER-KVA.example", want: canonicalUser},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, ok := selectPasswordLogin(logins, test.email)
			if !ok || got.UserID != test.want {
				t.Fatalf("select password login = (%s, %t), want (%s, true)", got.UserID, ok, test.want)
			}
		})
	}
}

func TestSelectPasswordLoginRejectsAmbiguousIdentity(t *testing.T) {
	t.Parallel()
	logins := []dbsqlc.ListPasswordLoginsByVerifiedEmailsRow{
		{UserID: ID{1}, NormalizedEmail: "user@bücher.example"},
		{UserID: ID{2}, NormalizedEmail: "user@xn--bcher-kva.example"},
	}
	if got, ok := selectPasswordLogin(logins, "user@bu\u0308cher.example"); ok {
		t.Fatalf("select password login = (%+v, true), want no unambiguous identity", got)
	}
}
