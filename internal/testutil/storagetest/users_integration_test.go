//go:build integration

package storagetest

import (
	"context"
	"testing"

	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestCreateVerifiedUserRollsBackUserWhenEmailConflicts(t *testing.T) {
	ctx := context.Background()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	if _, err := CreateVerifiedUser(ctx, pool, CreateVerifiedUserInput{
		DisplayName: "Existing",
		Email:       "person@example.com",
	}); err != nil {
		t.Fatalf("create existing verified user: %v", err)
	}
	if _, err := CreateVerifiedUser(ctx, pool, CreateVerifiedUserInput{
		DisplayName: "Rejected",
		Email:       " Person@Example.com ",
	}); err == nil {
		t.Fatal("expected duplicate verified email to fail")
	}
	var rejectedUsers int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM users WHERE display_name = 'Rejected'`,
	).Scan(&rejectedUsers); err != nil {
		t.Fatalf("count rejected users: %v", err)
	}
	if rejectedUsers != 0 {
		t.Fatalf("rejected users = %d, want 0", rejectedUsers)
	}
}
