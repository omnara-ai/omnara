//go:build integration

package identitystore

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/authn"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"golang.org/x/crypto/argon2"
)

func TestMain(m *testing.M) {
	integrationdb.RunTestMain(m)
}

func TestPasswordLoginRehashesOutdatedCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	store := New(pool, nil, nil)
	password := "correct horse battery staple"

	start, err := store.StartPasswordSignup(ctx, PasswordSignupStartInput{Email: "rehash@example.com"})
	if err != nil {
		t.Fatalf("start signup: %v", err)
	}
	oldHash := legacyArgon2IDHash(password)
	needsRehash, err := authn.PasswordHashNeedsRehash(oldHash)
	if err != nil {
		t.Fatalf("inspect legacy hash: %v", err)
	}
	if !needsRehash {
		t.Fatal("legacy hash should need rehash")
	}
	completed, err := store.CompletePasswordSignup(
		ctx,
		CompletePasswordSignupInput{Token: start.Token, PasswordHash: oldHash},
	)
	if err != nil {
		t.Fatalf("complete signup: %v", err)
	}

	if _, err := store.AuthenticatePasswordAndCreateSession(ctx, PasswordLoginSessionInput{
		Email:            "rehash@example.com",
		Password:         password,
		SessionToken:     "rehash-session",
		SessionCSRFToken: "rehash-csrf",
		SessionTTL:       time.Hour,
	}); err != nil {
		t.Fatalf("authenticate password: %v", err)
	}
	credential, err := store.q.GetPasswordCredentialByUserForUpdate(
		ctx,
		dbsqlc.GetPasswordCredentialByUserForUpdateParams{UserID: completed.User.ID},
	)
	if err != nil {
		t.Fatalf("load credential: %v", err)
	}
	if credential.PasswordHash == oldHash {
		t.Fatal("password hash was not upgraded")
	}
	needsRehash, err = authn.PasswordHashNeedsRehash(credential.PasswordHash)
	if err != nil {
		t.Fatalf("inspect upgraded hash: %v", err)
	}
	if needsRehash {
		t.Fatalf("upgraded hash still needs rehash: %s", credential.PasswordHash)
	}
	ok, err := authn.VerifyPassword(password, credential.PasswordHash)
	if err != nil {
		t.Fatalf("verify upgraded hash: %v", err)
	}
	if !ok {
		t.Fatal("upgraded hash does not verify original password")
	}

	currentHash := credential.PasswordHash
	store.rehashPasswordAfterLogin(ctx, completed.User.ID, oldHash, password)
	credential, err = store.q.GetPasswordCredentialByUserForUpdate(
		ctx,
		dbsqlc.GetPasswordCredentialByUserForUpdateParams{UserID: completed.User.ID},
	)
	if err != nil {
		t.Fatalf("reload credential: %v", err)
	}
	if credential.PasswordHash != currentHash {
		t.Fatal("stale previous_password_hash overwrote a concurrent credential change")
	}
}

func legacyArgon2IDHash(password string) string {
	salt := []byte("legacy-pw-salt!!")
	key := argon2.IDKey([]byte(password), salt, 1, 8192, 1, 32)
	return fmt.Sprintf(
		"$argon2id$v=19$m=8192,t=1,p=1$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
}
