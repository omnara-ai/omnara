//go:build integration

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func TestListOrgMembershipsForUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC)

	user, err := store.CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{Email: "member@example.com", DisplayName: "Member"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	alphaOrgID := testID("org_alpha")
	if _, err := store.pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, created_at, updated_at) VALUES ($1, 'Alpha Org', $2, $2)`,
		alphaOrgID,
		now,
	); err != nil {
		t.Fatalf("seed alpha org: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: user.ID, Role: "owner"},
	); err != nil {
		t.Fatalf("add owner membership: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: alphaOrgID, UserID: user.ID, Role: "member"},
	); err != nil {
		t.Fatalf("add member membership: %v", err)
	}

	other, err := store.CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{Email: "elsewhere@example.com", DisplayName: "Elsewhere"},
	)
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	otherOrgID := testID("org_other")
	if _, err := store.pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, created_at, updated_at) VALUES ($1, 'Zeta Org', $2, $2)`,
		otherOrgID,
		now,
	); err != nil {
		t.Fatalf("seed other org: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: otherOrgID, UserID: other.ID, Role: "owner"},
	); err != nil {
		t.Fatalf("add other membership: %v", err)
	}

	memberships, err := store.Identity().ListOrgMembershipsForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("list org memberships: %v", err)
	}
	if len(memberships) != 2 {
		t.Fatalf("expected exactly the caller's 2 memberships, got %+v", memberships)
	}
	if memberships[0].OrgName != "Alpha Org" || memberships[0].Role != "member" || memberships[0].OrgID != alphaOrgID {
		t.Fatalf("first membership = %+v, want Alpha Org/member/%s", memberships[0], alphaOrgID)
	}
	if memberships[1].OrgName != "Test Org" || memberships[1].Role != "owner" || memberships[1].OrgID != testOrgID {
		t.Fatalf("second membership = %+v, want Test Org/owner/%s", memberships[1], testOrgID)
	}
}

func TestGetUserAndPrimaryVerifiedUserEmail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)

	withEmail, err := store.CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{Email: "primary@example.com", DisplayName: "Primary"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	got, err := store.Identity().GetUser(ctx, withEmail.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.ID != withEmail.ID || got.DisplayName != "Primary" {
		t.Fatalf("unexpected user: %+v", got)
	}
	email, err := store.Identity().PrimaryVerifiedUserEmail(ctx, withEmail.ID)
	if err != nil {
		t.Fatalf("primary email: %v", err)
	}
	if email != "primary@example.com" {
		t.Fatalf("primary email = %q, want primary@example.com", email)
	}

	noEmail, err := store.Identity().CreateUser(ctx, identitystore.CreateUserInput{DisplayName: "No Email"})
	if err != nil {
		t.Fatalf("create no-email user: %v", err)
	}
	email, err = store.Identity().PrimaryVerifiedUserEmail(ctx, noEmail.ID)
	if err != nil {
		t.Fatalf("primary email (none): %v", err)
	}
	if email != "" {
		t.Fatalf("expected empty primary email, got %q", email)
	}

	splitUser, err := store.Identity().CreateUser(ctx, identitystore.CreateUserInput{DisplayName: "Split"})
	if err != nil {
		t.Fatalf("create split user: %v", err)
	}
	if _, err := store.Identity().CreateUserEmail(ctx, identitystore.CreateUserEmailInput{
		UserID:    splitUser.ID,
		Email:     "unverified-primary@example.com",
		IsPrimary: true,
	}); err != nil {
		t.Fatalf("create unverified primary email: %v", err)
	}
	if _, err := store.Identity().CreateUserEmail(ctx, identitystore.CreateUserEmailInput{
		UserID:    splitUser.ID,
		Email:     "verified-secondary@example.com",
		Verified:  true,
		IsPrimary: false,
	}); err != nil {
		t.Fatalf("create verified secondary email: %v", err)
	}
	email, err = store.Identity().PrimaryVerifiedUserEmail(ctx, splitUser.ID)
	if err != nil {
		t.Fatalf("primary email (unverified primary): %v", err)
	}
	if email != "" {
		t.Fatalf("expected empty email when primary is unverified, got %q", email)
	}
	emails, err := store.Identity().PrimaryVerifiedUserEmails(ctx, []ID{withEmail.ID, noEmail.ID, splitUser.ID})
	if err != nil {
		t.Fatalf("primary emails batch: %v", err)
	}
	if emails[withEmail.ID] != "primary@example.com" {
		t.Fatalf("primary emails batch withEmail = %q, want primary@example.com", emails[withEmail.ID])
	}
	if _, ok := emails[noEmail.ID]; ok {
		t.Fatalf("primary emails batch should omit no-email user, got %+v", emails)
	}
	if _, ok := emails[splitUser.ID]; ok {
		t.Fatalf("primary emails batch should omit user without verified primary, got %+v", emails)
	}
	if _, err := store.Identity().PrimaryVerifiedUserEmails(ctx, []ID{withEmail.ID, NilID}); err == nil {
		t.Fatal("expected primary emails batch to reject nil user id")
	}

	if _, err := store.Identity().GetUser(ctx, testID("missing-user")); err == nil {
		t.Fatalf("expected error for missing user")
	}
}
