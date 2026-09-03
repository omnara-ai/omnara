//go:build integration

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/authn"
	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

const testDeviceOAuthClientID = "test-device-client"

func TestCanonicalBearerCredentialsPersistOnlyFullTokenDigests(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := NewStore(pool)
	seedDefaultProject(t, ctx, store)

	user := mustCreateIdentityUser(t, ctx, store, "canonical-tokens@example.com", "Canonical Tokens")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: user.ID, Role: "admin"},
	); err != nil {
		t.Fatalf("add token creator membership: %v", err)
	}
	pat, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: user.ID, Name: "canonical PAT"},
	)
	if err != nil {
		t.Fatalf("create personal access token: %v", err)
	}
	orgKey, err := store.Identity().CreateOrgAPIKeyWithPlaintext(
		ctx,
		identitystore.CreateOrgAPIKeyInput{
			OrgID:           testOrgID,
			CreatedByUserID: user.ID,
			Name:            "canonical org key",
			OrgRole:         "member",
		},
	)
	if err != nil {
		t.Fatalf("create organization API key: %v", err)
	}
	machine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Canonical Token Machine",
			IdempotencyKey: "canonical-token-machine",
		},
	)
	if err != nil {
		t.Fatalf("create BYO machine: %v", err)
	}
	daemon, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     testOrgID,
			MachineID: machine.ID,
			Name:      "canonical daemon token",
		},
	)
	if err != nil {
		t.Fatalf("create daemon token: %v", err)
	}

	for _, credential := range []struct {
		name  string
		token string
		kind  bearertoken.Kind
	}{
		{name: "personal", token: pat.Token, kind: bearertoken.KindPersonalAccess},
		{name: "organization", token: orgKey.Token, kind: bearertoken.KindOrganization},
		{name: "daemon", token: daemon.Token, kind: bearertoken.KindDaemon},
	} {
		if err := bearertoken.Validate(credential.token, credential.kind); err != nil {
			t.Fatalf("validate %s token: %v", credential.name, err)
		}
	}
	for name, managementID := range map[string]string{
		"personal":     pat.Record.TokenID,
		"organization": orgKey.Record.TokenID,
	} {
		if managementID == "" || strings.Contains(
			map[string]string{"personal": pat.Token, "organization": orgKey.Token}[name],
			managementID,
		) {
			t.Fatalf("%s management token id %q is empty or embedded in bearer", name, managementID)
		}
	}

	assertStoredToken := func(table string, id ID, token, recordTokenID string) {
		t.Helper()
		var tokenHash string
		if recordTokenID == "" {
			if err := pool.QueryRow(ctx, `SELECT token_hash FROM `+table+` WHERE id = $1`, id).Scan(&tokenHash); err != nil {
				t.Fatalf("load %s token hash: %v", table, err)
			}
		} else {
			var tokenID string
			if err := pool.QueryRow(
				ctx,
				`SELECT token_id, token_hash FROM `+table+` WHERE id = $1`,
				id,
			).Scan(&tokenID, &tokenHash); err != nil {
				t.Fatalf("load %s token fields: %v", table, err)
			}
			if tokenID != recordTokenID {
				t.Fatalf("%s token id = %q, want %q", table, tokenID, recordTokenID)
			}
		}
		digest := sha256.Sum256([]byte(token))
		wantHash := hex.EncodeToString(digest[:])
		if tokenHash != wantHash || tokenHash == token || len(tokenHash) != 64 {
			t.Fatalf("%s stored token hash = %q, want SHA-256 %q", table, tokenHash, wantHash)
		}
	}
	assertStoredToken("personal_access_tokens", pat.Record.ID, pat.Token, pat.Record.TokenID)
	assertStoredToken("org_api_keys", orgKey.Record.ID, orgKey.Token, orgKey.Record.TokenID)
	assertStoredToken("machine_daemon_tokens", daemon.Record.ID, daemon.Token, "")

	pool.Close()
	if _, err := store.Identity().AuthenticatePersonalAccessToken(ctx, pat.Token); err == nil || errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("valid PAT against closed pool error = %v, want storage error", err)
	}
	for name, authenticate := range map[string]func() error{
		"corrupt PAT checksum": func() error {
			_, err := store.Identity().AuthenticatePersonalAccessToken(ctx, corruptBearerChecksum(pat.Token))
			return err
		},
		"corrupt org checksum": func() error {
			_, err := store.Identity().AuthenticateOrgAPIKey(ctx, corruptBearerChecksum(orgKey.Token))
			return err
		},
		"corrupt daemon checksum": func() error {
			_, err := store.Execution().AuthenticateMachineDaemonToken(ctx, corruptBearerChecksum(daemon.Token))
			return err
		},
	} {
		if err := authenticate(); !errors.Is(err, storeerr.ErrUnauthorized) {
			t.Fatalf("%s error = %v, want unauthorized before database access", name, err)
		}
	}
	for name, authenticate := range map[string]func() error{
		"org token as PAT": func() error {
			_, err := store.Identity().AuthenticatePersonalAccessToken(ctx, orgKey.Token)
			return err
		},
		"daemon token as org key": func() error {
			_, err := store.Identity().AuthenticateOrgAPIKey(ctx, daemon.Token)
			return err
		},
		"PAT as daemon token": func() error {
			_, err := store.Execution().AuthenticateMachineDaemonToken(ctx, pat.Token)
			return err
		},
		"legacy PAT": func() error {
			_, err := store.Identity().AuthenticatePersonalAccessToken(ctx, "omnara_pat_old_secret")
			return err
		},
		"legacy org key": func() error {
			_, err := store.Identity().AuthenticateOrgAPIKey(ctx, "omnara_org_old_secret")
			return err
		},
		"legacy daemon token": func() error {
			_, err := store.Execution().AuthenticateMachineDaemonToken(ctx, "omnara_daemon_old")
			return err
		},
	} {
		if err := authenticate(); !errors.Is(err, storeerr.ErrUnauthorized) {
			t.Fatalf("%s error = %v, want unauthorized before database access", name, err)
		}
	}
}

func corruptBearerChecksum(token string) string {
	replacement := byte('0')
	if token[len(token)-1] == replacement {
		replacement = '1'
	}
	return token[:len(token)-1] + string(replacement)
}

func TestProjectAuthorizationAndPersonalAccessTokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	viewer := mustCreateIdentityUser(t, ctx, store, "viewer@example.com", "Viewer")
	developer := mustCreateIdentityUser(t, ctx, store, "developer@example.com", "Developer")
	orgAdmin := mustCreateIdentityUser(t, ctx, store, "org-admin@example.com", "Org Admin")
	orgOwner := mustCreateIdentityUser(t, ctx, store, "org-owner@example.com", "Org Owner")
	operator := mustCreateIdentityUser(t, ctx, store, "operator@example.com", "Operator")
	outsider := mustCreateIdentityUser(t, ctx, store, "outsider@example.com", "Outsider")

	for _, membership := range []identitystore.AddProjectMembershipInput{
		{OrgID: testOrgID, ProjectID: testProjectID, UserID: viewer.ID, Role: "viewer"},
		{OrgID: testOrgID, ProjectID: testProjectID, UserID: developer.ID, Role: "developer"},
		{OrgID: testOrgID, ProjectID: testProjectID, UserID: operator.ID, Role: "operator"},
	} {
		if _, err := store.Identity().AddOrgMembership(
			ctx,
			identitystore.AddOrgMembershipInput{OrgID: membership.OrgID, UserID: membership.UserID, Role: "member"},
		); err != nil {
			t.Fatalf("add org membership %s: %v", membership.UserID, err)
		}
		if _, err := store.Identity().AddProjectMembership(ctx, membership); err != nil {
			t.Fatalf("add project membership %s: %v", membership.UserID, err)
		}
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: orgAdmin.ID, Role: "admin"},
	); err != nil {
		t.Fatalf("add org membership: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: orgOwner.ID, Role: "owner"},
	); err != nil {
		t.Fatalf("add org owner membership: %v", err)
	}
	developerToken, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: developer.ID,
			Name:   "Developer token",
		},
	)
	if err != nil {
		t.Fatalf("create pat: %v", err)
	}
	principal, err := store.Identity().AuthenticatePersonalAccessToken(ctx, developerToken.Token)
	if err != nil {
		t.Fatalf("authenticate pat: %v", err)
	}
	if principal.Type != identitystore.PrincipalTypeUser || principal.ID != developer.ID ||
		principal.PersonalAccessTokenID != developerToken.Record.ID ||
		principal.OrgID != NilID {
		t.Fatalf("unexpected principal: %+v", principal)
	}
	if _, err := store.Identity().AuthenticatePersonalAccessToken(ctx, "missing-token"); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("expected unauthorized for missing token, got %v", err)
	}

	assertProjectAllowed(t, ctx, store, identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: viewer.ID}, identitystore.ProjectActionRead, true)
	assertProjectAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: viewer.ID},
		identitystore.ProjectActionManage,
		false,
	)
	assertProjectAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: developer.ID},
		identitystore.ProjectActionManage,
		true,
	)
	assertProjectAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: developer.ID},
		identitystore.AgentActionOperate,
		true,
	)
	assertProjectAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: operator.ID},
		identitystore.AgentActionOperate,
		true,
	)
	assertProjectAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: operator.ID},
		identitystore.ProjectActionManage,
		false,
	)
	assertProjectAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: orgAdmin.ID},
		identitystore.ProjectActionAccessManage,
		true,
	)
	assertProjectAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: orgOwner.ID},
		identitystore.ProjectActionAccessManage,
		true,
	)
	assertProjectAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: outsider.ID},
		identitystore.ProjectActionRead,
		false,
	)
	assertOrgAllowed(t, ctx, store, identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: orgAdmin.ID}, identitystore.OrgActionManage, true)
	assertOrgAllowed(t, ctx, store, identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: orgOwner.ID}, identitystore.OrgActionManage, true)
	assertOrgAllowed(t, ctx, store, identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: developer.ID}, identitystore.OrgActionManage, false)
	assertOrgAllowed(t, ctx, store, identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: viewer.ID}, identitystore.OrgActionManage, false)
	assertOrgAllowed(t, ctx, store, identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: outsider.ID}, identitystore.OrgActionManage, false)
	assertOrgAllowed(t, ctx, store, identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: orgOwner.ID}, identitystore.OrgActionOwn, true)
	assertOrgAllowed(t, ctx, store, identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: orgAdmin.ID}, identitystore.OrgActionOwn, false)
	assertOrgAllowed(t, ctx, store, identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: developer.ID}, identitystore.OrgActionOwn, false)
	allowed, err := store.Identity().AuthorizeProject(
		ctx,
		identitystore.AuthorizeProjectInput{
			Principal: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: orgAdmin.ID},
			OrgID:     testOrgID,
			ProjectID: testID("missing-project"),
			Action:    identitystore.ProjectActionRead,
		},
	)
	if err != nil {
		t.Fatalf("authorize missing project: %v", err)
	}
	if allowed {
		t.Fatal("org admin must not authorize nonexistent project")
	}
	noGrantMember := mustCreateIdentityUser(t, ctx, store, "member-nogrant@example.com", "Member No Grant")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: noGrantMember.ID, Role: "member"},
	); err != nil {
		t.Fatalf("add no-grant org member: %v", err)
	}
	assertProjectAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: noGrantMember.ID},
		identitystore.ProjectActionRead,
		false,
	)
	for _, column := range []string{"id", "org_id"} {
		_, err := pool.Exec(
			ctx,
			fmt.Sprintf("UPDATE projects SET %s = $1 WHERE org_id = $2 AND id = $3", column),
			testID("changed-project-"+column),
			testOrgID,
			testProjectID,
		)
		if !isPgCode(err, "25006") {
			t.Fatalf("update project %s error = %v, want SQLSTATE 25006", column, err)
		}
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE projects SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
		 WHERE org_id = $1 AND id = $2`,
		testOrgID,
		testProjectID,
	); err != nil {
		t.Fatalf("soft-delete project for authorization check: %v", err)
	}
	assertProjectAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: viewer.ID},
		identitystore.ProjectActionRead,
		false,
	)
	assertProjectAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: orgAdmin.ID},
		identitystore.ProjectActionRead,
		false,
	)
	inactiveOrgOwner := mustCreateIdentityUser(t, ctx, store, "inactive-org-owner@example.com", "Inactive Org Owner")
	inactiveOrg, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID: inactiveOrgOwner.ID, Name: "Inactive Org", IdempotencyKey: "inactive-org",
	})
	if err != nil {
		t.Fatalf("create organization for inactive-parent authorization check: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE orgs SET deleted_at = statement_timestamp(), updated_at = statement_timestamp()
		 WHERE id = $1`,
		inactiveOrg.Org.ID,
	); err != nil {
		t.Fatalf("soft-delete organization for authorization check: %v", err)
	}
	allowed, err = store.Identity().AuthorizeProject(ctx, identitystore.AuthorizeProjectInput{
		Principal: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: inactiveOrgOwner.ID},
		OrgID:     inactiveOrg.Org.ID,
		ProjectID: inactiveOrg.Project.ID,
		Action:    identitystore.ProjectActionRead,
	})
	if err != nil {
		t.Fatalf("authorize project in deleted organization: %v", err)
	}
	if allowed {
		t.Fatal("project authorization must not survive organization deletion")
	}
}

func TestCreateOrgForUserCreatesOwnerAndDefaultProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	user := mustCreateIdentityUser(t, ctx, store, "owner@example.com", "Owner")

	created, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{UserID: user.ID, Name: "Owner Org", IdempotencyKey: "owner-org"},
	)
	if err != nil {
		t.Fatalf("create org for user: %v", err)
	}
	if !created.Created || created.Membership.Role != "owner" {
		t.Fatalf("unexpected created org record: %+v", created)
	}
	storedOrg, err := store.Identity().GetOrg(ctx, created.Org.ID)
	if err != nil || storedOrg.ID != created.Org.ID {
		t.Fatalf("stored organization = %+v err=%v, want ID %s", storedOrg, err, created.Org.ID)
	}
	replayed, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{UserID: user.ID, Name: "Owner Org", IdempotencyKey: "owner-org"},
	)
	if err != nil {
		t.Fatalf("replay org for user: %v", err)
	}
	if replayed.Created || replayed.Org.ID != created.Org.ID || replayed.Project.ID != created.Project.ID {
		t.Fatalf("unexpected replay: %+v", replayed)
	}
	preflight, found, err := store.Identity().GetOrgCreationReplay(ctx, identitystore.GetOrgCreationReplayInput{
		UserID: user.ID, Name: "Owner Org", IdempotencyKey: "owner-org",
	})
	if err != nil || !found || preflight.Org.ID != created.Org.ID {
		t.Fatalf("organization replay preflight = %+v found=%v err=%v", preflight, found, err)
	}
	secondOwner := mustCreateIdentityUser(t, ctx, store, "second-owner@example.com", "Second Owner")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: created.Org.ID, UserID: secondOwner.ID, Role: "owner"},
	); err != nil {
		t.Fatalf("add second owner: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: created.Org.ID, UserID: user.ID, Role: "member"},
	); err != nil {
		t.Fatalf("demote bootstrap owner: %v", err)
	}
	allowed, err := store.Identity().AuthorizeProject(
		ctx,
		identitystore.AuthorizeProjectInput{
			Principal: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: user.ID},
			OrgID:     created.Org.ID,
			ProjectID: created.Project.ID,
			Action:    identitystore.ProjectActionAccessManage,
		},
	)
	if err != nil {
		t.Fatalf("authorize bootstrap project creator: %v", err)
	}
	if !allowed {
		t.Fatal("expected bootstrap project creator to keep explicit project admin after org demotion")
	}
	other := mustCreateIdentityUser(t, ctx, store, "other-owner@example.com", "Other")
	otherCreated, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{UserID: other.ID, Name: "Other Org", IdempotencyKey: "owner-org"},
	)
	if err != nil {
		t.Fatalf("create same idempotency key for different user: %v", err)
	}
	if otherCreated.Org.ID == created.Org.ID {
		t.Fatal("expected idempotency keys to be scoped by user")
	}
}

func TestCreateOrgForUserRejectsDefaultPoolSecretEnv(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user := mustCreateIdentityUser(t, ctx, store, "default-pool-secret@example.com", "Cluster Pool Secret")
	missingSecretID := secretPublicIDForTest(t, testID("default-pool-missing-secret"))
	_, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{
			UserID:         user.ID,
			Name:           "Secret Pool Org",
			IdempotencyKey: "default-pool-secret-org",
			DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{
				defaultMachinePoolTemplateWithDefaultMachineForTest(executionstore.DefaultMachinePoolTemplate{
					Name:               "hosted-pool-secret",
					Provider:           "unikraft",
					DefaultCwd:         "/workspace",
					ProviderConfig:     json.RawMessage(`{}`),
					ProviderAuthEnvVar: "HOSTED_POOL_SECRET_TOKEN",
					MaxTotalMachines:   1,
					MaxTotalCPU:        intPtrForMachinePoolTest(1),
					MaxTotalMemoryMB:   intPtrForMachinePoolTest(1024),
					MaxMachineCPU:      intPtrForMachinePoolTest(1),
					MaxMachineMemoryMB: intPtrForMachinePoolTest(1024),
				}, defaultMachineFieldsForTest{
					DefaultMachineCPU:             1,
					DefaultMachineMemoryMB:        1024,
					DefaultMachineSecretEnv:       json.RawMessage(`{"API_TOKEN":"` + missingSecretID + `"}`),
					DefaultMachineProviderOptions: json.RawMessage(`{"image":"registry.example.com/agent:latest","metro":"sfo"}`),
				}),
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "secret is not found or not org-owned") {
		t.Fatalf("create org with secret_env default pool = %v, want missing secret rejection", err)
	}
}

func TestCreateOrgForUserCreatesDefaultMachinePool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defaultPools := []executionstore.DefaultMachinePoolTemplate{
		defaultMachinePoolTemplateWithDefaultMachineForTest(executionstore.DefaultMachinePoolTemplate{
			Name:                     "hosted-pool",
			Description:              "Cluster pool",
			Provider:                 "unikraft",
			DefaultCwd:               "/workspace",
			ProviderConfig:           json.RawMessage(`{"api_base_url":"https://api.kraft.cloud"}`),
			ProviderAuthEnvVar:       "HOSTED_POOL_TOKEN",
			RuntimeProtectionEnabled: true,
			MaxTotalMachines:         5,
			MaxTotalCPU:              intPtrForMachinePoolTest(5),
			MaxTotalMemoryMB:         intPtrForMachinePoolTest(5120),
			MaxMachineCPU:            intPtrForMachinePoolTest(1),
			MaxMachineMemoryMB:       intPtrForMachinePoolTest(1024),
			Metadata:                 resourcemeta.Metadata{"source": "test"},
		}, defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        1024,
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"registry.example.com/agent:latest","metro":"sfo"}`),
		}),
		defaultMachinePoolTemplateWithDefaultMachineForTest(executionstore.DefaultMachinePoolTemplate{
			Name:               "hosted-pool-large",
			Description:        "Large cluster pool",
			Provider:           "unikraft",
			DefaultCwd:         "/workspace",
			ProviderConfig:     json.RawMessage(`{"api_base_url":"https://api.kraft.cloud"}`),
			ProviderAuthEnvVar: "HOSTED_POOL_LARGE_TOKEN",
			MaxTotalMachines:   3,
			MaxTotalCPU:        intPtrForMachinePoolTest(6),
			MaxTotalMemoryMB:   intPtrForMachinePoolTest(6144),
			MaxMachineCPU:      intPtrForMachinePoolTest(2),
			MaxMachineMemoryMB: intPtrForMachinePoolTest(2048),
			Metadata:           resourcemeta.Metadata{"source": "test"},
		}, defaultMachineFieldsForTest{
			DefaultMachineCPU:             2,
			DefaultMachineMemoryMB:        2048,
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"registry.example.com/agent:latest","metro":"iad"}`),
		}),
	}
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user := mustCreateIdentityUser(t, ctx, store, "default-pool-owner@example.com", "Cluster Pool Owner")

	created, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{
			UserID:              user.ID,
			Name:                "Cluster Pool Org",
			IdempotencyKey:      "default-pool-org",
			DefaultMachinePools: defaultPools,
		},
	)
	if err != nil {
		t.Fatalf("create org for user: %v", err)
	}
	for _, defaultPool := range defaultPools {
		poolRecord, err := testQueries(store).GetMachinePoolByName(
			ctx,
			dbsqlc.GetMachinePoolByNameParams{OrgID: created.Org.ID, Name: defaultPool.Name},
		)
		if err != nil {
			t.Fatalf("get default machine pool %q: %v", defaultPool.Name, err)
		}
		if poolRecord.Name != defaultPool.Name || poolRecord.ManagementKind != string(management.Cluster) ||
			poolRecord.Provider != "unikraft" ||
			poolRecord.RuntimeProtectionEnabled != defaultPool.RuntimeProtectionEnabled {
			t.Fatalf("unexpected default pool: %+v", poolRecord)
		}
		assertJSONRawEqual(t, poolRecord.ProviderConfig, string(defaultPool.ProviderConfig))
		grant, err := testQueries(store).GetActiveProjectMachinePoolGrantForMachinePool(
			ctx,
			dbsqlc.GetActiveProjectMachinePoolGrantForMachinePoolParams{
				ProjectID:     created.Project.ID,
				MachinePoolID: poolRecord.ID,
			},
		)
		if err != nil {
			t.Fatalf("get default project machine pool grant for %q: %v", defaultPool.Name, err)
		}
		if grant.IdempotencyKey != "" {
			t.Fatalf("unexpected default project machine pool grant: %+v", grant)
		}
		assertJSONRawEqual(t, grant.Metadata, `{}`)
	}
	createdProject, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID:          created.Org.ID,
			Creator:        userPrincipal(user.ID),
			Name:           "Manual Project",
			IdempotencyKey: "manual-project",
		},
	)
	if err != nil {
		t.Fatalf("create manual project: %v", err)
	}
	var manualProjectGrantCount int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM project_machine_pool_grants pool_grant JOIN machine_pools pool ON pool.org_id = pool_grant.org_id AND pool.id = pool_grant.machine_pool_id WHERE pool_grant.project_id = $1 AND pool.management_kind = 'cluster'`, createdProject.ID).
		Scan(&manualProjectGrantCount); err != nil {
		t.Fatalf("count manual project default pool grants: %v", err)
	}
	if manualProjectGrantCount != 0 {
		t.Fatalf("manual project default pool grants = %d, want 0", manualProjectGrantCount)
	}
}

func TestCreateOrgForUserAllowsZeroCapClusterPool(t *testing.T) {
	t.Setenv("TEST_ZERO_CAP_POOL_TOKEN", "blaxel-test-token")
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user := mustCreateIdentityUser(t, ctx, store, "zero-cap-owner@example.com", "Zero Cap Owner")

	memoryFloorMB := 128
	zeroMemoryMB := 0
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID:         user.ID,
		Name:           "Zero Cap Org",
		IdempotencyKey: "zero-cap-org",
		DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{{
			Name:                          "hosted-default-pool",
			Provider:                      "blaxel",
			ProviderAuthEnvVar:            "TEST_ZERO_CAP_POOL_TOKEN",
			DefaultMachineMemoryMB:        &memoryFloorMB,
			DefaultMachineEnv:             json.RawMessage(`{}`),
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"blaxel/base-image:latest","region":"us-pdx-1"}`),
			ProviderConfig:                json.RawMessage(`{"workspace":"omnara"}`),
			MaxTotalMachines:              0,
			MaxTotalMemoryMB:              &zeroMemoryMB,
			MaxMachineMemoryMB:            &memoryFloorMB,
		}},
	})
	if err != nil {
		t.Fatalf("create org with zero-cap pool: %v", err)
	}
	assertClusterPoolCaps(t, ctx, pool, created.Org.ID, 0, 0)

	if _, err := pool.Exec(ctx, `
		UPDATE machine_pools
		SET max_total_machines = 8, max_total_memory_mb = 1024, updated_at = now()
		WHERE org_id = $1 AND management_kind = 'cluster' AND deleted_at IS NULL
	`, created.Org.ID); err != nil {
		t.Fatalf("raise cluster pool caps: %v", err)
	}
	assertClusterPoolCaps(t, ctx, pool, created.Org.ID, 8, 1024)
}

func assertClusterPoolCaps(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	orgID ID,
	wantMachines, wantMemoryMB int32,
) {
	t.Helper()
	var maxMachines int32
	var maxMemoryMB *int32
	if err := pool.QueryRow(ctx, `
		SELECT max_total_machines, max_total_memory_mb
		FROM machine_pools
		WHERE org_id = $1
		  AND management_kind = 'cluster'
		  AND deleted_at IS NULL
	`, orgID).Scan(&maxMachines, &maxMemoryMB); err != nil {
		t.Fatalf("read cluster pool caps: %v", err)
	}
	if maxMachines != wantMachines || maxMemoryMB == nil || *maxMemoryMB != wantMemoryMB {
		t.Fatalf(
			"cluster pool caps machines=%d memory=%v, want %d and %d",
			maxMachines,
			maxMemoryMB,
			wantMachines,
			wantMemoryMB,
		)
	}
}

func TestDefaultModelProviderProvisioningCreatesClusterManagedResourcesAtomically(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()

	store := newSecretIntegrationStore(pool)
	user := mustCreateIdentityUser(t, ctx, store, "default-model-provider-owner@example.com", "Default Model Provider Owner")
	template := modelstore.DefaultModelProviderTemplate{
		Provisioner:          "openrouter",
		Name:                 "omnara-openrouter",
		CredentialSecretName: "omnara-openrouter-key",
		APIFormat:            modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant:           modelprotocol.APIVariantOpenRouter,
		BaseURL:              "https://openrouter.ai/api/v1",
		EndpointPath:         "/chat/completions",
		AuthKind:             modelstore.ModelProviderAuthKindBearerToken,
		Models: []modelstore.DefaultConfiguredModelTemplate{{
			Name:                "claude-sonnet-4.5",
			ProviderModelSlug:   "anthropic/claude-sonnet-4.5",
			ContextWindowTokens: 200000,
			MaxOutputTokens:     64000,
			SupportsReasoning:   true,
			InputModalities:     []string{"text", "image"},
			OutputModalities:    []string{"text"},
		}},
	}
	orgID := uuid.New()
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		OrgID:                         orgID,
		UserID:                        user.ID,
		Name:                          "Cluster Model Provider Org",
		IdempotencyKey:                "cluster-model-provider-org",
		ProvisionDefaultModelProvider: true,
	})
	if err != nil {
		t.Fatalf("create org for user: %v", err)
	}
	if _, err := store.Models().GetModelProviderConfigByName(ctx, created.Org.ID, template.Name); !storeerr.IsNotFound(err) {
		t.Fatalf("get provider before post-commit provisioning error = %v, want not found", err)
	}
	mustCompleteDefaultModelProviderProvisioning(
		t,
		ctx,
		store,
		created.Org.ID,
		template,
		"sk-openrouter-org",
	)
	provider, err := store.Models().GetModelProviderConfigByName(ctx, created.Org.ID, template.Name)
	if err != nil {
		t.Fatalf("get default model provider: %v", err)
	}
	if provider.ManagementKind != management.Cluster {
		t.Fatalf("unexpected default model provider: %+v", provider)
	}
	credential, err := store.Secrets().GetSecret(ctx, created.Org.ID, provider.CredentialSecretID)
	if err != nil {
		t.Fatalf("get default model provider credential: %v", err)
	}
	if credential.ManagementKind != management.Cluster || credential.OwnerKind != secretstore.SecretOwnerOrg {
		t.Fatalf("unexpected default model provider credential: %+v", credential)
	}
	if _, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              created.Org.ID,
		Name:               "tenant-provider-with-cluster-secret",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: credential.ID,
	}); !errors.Is(err, storeerr.ErrInvalidSecretRequest) {
		t.Fatalf("tenant provider with cluster credential error = %v, want invalid secret request", err)
	}
	if _, err := store.Execution().CreateMachinePool(ctx, completeMachinePoolInputForTest(executionstore.CreateMachinePoolInput{
		OrgID:                created.Org.ID,
		Name:                 "tenant-pool-with-cluster-secret",
		Provider:             "unikraft",
		ProviderAuthSecretID: credential.ID,
		MaxTotalMachines:     1,
	})); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("tenant machine pool with cluster credential error = %v, want not found", err)
	}
	credentialPublicID, err := publicid.Encode(publicid.KindSecret, credential.ID)
	if err != nil {
		t.Fatalf("encode cluster credential id: %v", err)
	}
	tenantCredential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     created.Org.ID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "tenant-machine-provider-auth",
		Material:  secrets.GenericMaterial{Value: "tenant-token"},
		Actor:     userPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create tenant machine provider credential: %v", err)
	}
	if _, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolInputForTest(executionstore.CreateMachinePoolInput{
			OrgID:                   created.Org.ID,
			Name:                    "tenant-pool-with-cluster-env-secret",
			Provider:                "unikraft",
			ProviderAuthSecretID:    tenantCredential.ID,
			DefaultMachineSecretEnv: mustTestRawJSON(t, map[string]string{"OPENROUTER_API_KEY": credentialPublicID}),
			MaxTotalMachines:        1,
		}),
	); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("tenant machine environment with cluster credential error = %v, want not found", err)
	}
	payload, err := store.Secrets().ReadOrgOwnedSecretPayload(ctx, secretstore.ReadOrgOwnedSecretPayloadInput{
		OrgID:          created.Org.ID,
		SecretID:       credential.ID,
		ManagementKind: management.Cluster,
		Kind:           secretstore.SecretKindGeneric,
	})
	if err != nil {
		t.Fatalf("read default model provider credential: %v", err)
	}
	if payload.Payload[secrets.KeyValue] != "sk-openrouter-org" {
		t.Fatalf("credential value = %q, want provisioned value", payload.Payload[secrets.KeyValue])
	}
	models, err := store.Models().ListConfiguredModels(ctx, modelstore.ListConfiguredModelsInput{
		OrgID: created.Org.ID, ProviderConfigID: provider.ID, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list default configured models: %v", err)
	}
	if len(models.Models) != 1 || models.Models[0].Name != template.Models[0].Name ||
		models.Models[0].ManagementKind != management.Cluster {
		t.Fatalf("unexpected default configured models: %+v", models.Models)
	}
	if _, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		created.Org.ID,
		created.Project.ID,
		models.Models[0].ID,
	); err != nil {
		t.Fatalf("get default project model grant: %v", err)
	}
	if _, err := store.Models().PatchModelProviderConfig(ctx, modelstore.PatchModelProviderConfigInput{
		OrgID: created.Org.ID, ID: provider.ID,
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("patch cluster-managed provider error = %v, want state transition conflict", err)
	}
	if _, err := store.Models().DeleteModelProviderConfig(
		ctx,
		created.Org.ID,
		provider.ID,
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("archive cluster-managed provider error = %v, want state transition conflict", err)
	}
	tenantModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 created.Org.ID,
		ModelProviderConfigID: provider.ID,
		Name:                  "tenant-added-model",
		ProviderModelSlug:     "example/model",
		ContextWindowTokens:   8192,
		MaxOutputTokens:       1024,
	})
	if err != nil {
		t.Fatalf("create tenant model under cluster-managed provider: %v", err)
	}
	if tenantModel.ManagementKind != management.Tenant {
		t.Fatalf("tenant model management kind = %q, want tenant", tenantModel.ManagementKind)
	}
	tenantModelName := "tenant-renamed-model"
	tenantModel, err = store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID: created.Org.ID, ModelProviderConfigID: provider.ID, ID: tenantModel.ID, Name: &tenantModelName,
	})
	if err != nil || tenantModel.Name != tenantModelName {
		t.Fatalf("patch tenant model under cluster-managed provider = %+v, %v", tenantModel, err)
	}
	if _, err := store.Models().DeleteConfiguredModel(ctx, created.Org.ID, tenantModel.ID); err != nil {
		t.Fatalf("delete tenant model under cluster-managed provider: %v", err)
	}
	if _, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID: created.Org.ID, ModelProviderConfigID: provider.ID, ID: models.Models[0].ID,
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("patch cluster-managed configured model error = %v, want state transition conflict", err)
	}
	if _, err := store.Models().DeleteConfiguredModel(
		ctx,
		created.Org.ID,
		models.Models[0].ID,
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("archive cluster-managed configured model error = %v, want state transition conflict", err)
	}
	if _, err := store.Secrets().UpdateSecretMetadata(ctx, secretstore.UpdateSecretMetadataInput{
		OrgID: created.Org.ID, SecretID: credential.ID, Name: credential.Name,
		Metadata: resourcemeta.Metadata{}, Actor: userPrincipal(user.ID),
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("update cluster-managed secret error = %v, want state transition conflict", err)
	}
	if _, _, err := store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
		OrgID: created.Org.ID, SecretID: credential.ID,
		Material: secrets.GenericMaterial{Value: "sk-replacement"}, Actor: userPrincipal(user.ID),
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("rotate cluster-managed secret error = %v, want state transition conflict", err)
	}
	if _, err := store.Secrets().CreateSecretGrant(ctx, secretstore.CreateSecretGrantInput{
		OrgID: created.Org.ID, SecretID: credential.ID, TargetProjectID: created.Project.ID,
		Actor: userPrincipal(user.ID),
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("grant cluster-managed secret error = %v, want state transition conflict", err)
	}
	if _, err := store.Secrets().DeleteSecret(ctx, secretstore.DeleteSecretInput{
		OrgID: created.Org.ID, SecretID: credential.ID, Actor: userPrincipal(user.ID),
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("delete cluster-managed secret error = %v, want state transition conflict", err)
	}

	replayed, found, err := store.Identity().GetOrgCreationReplay(ctx, identitystore.GetOrgCreationReplayInput{
		UserID:         user.ID,
		Name:           "Cluster Model Provider Org",
		IdempotencyKey: "cluster-model-provider-org",
	})
	if err != nil {
		t.Fatalf("load completed org replay: %v", err)
	}
	if !found {
		t.Fatal("completed cluster model-provider org was not replayable")
	}
	if replayed.Org.ID != created.Org.ID {
		t.Fatalf("replayed org id = %s, want %s", replayed.Org.ID, created.Org.ID)
	}
	var providerCount int
	if err := pool.QueryRow(ctx, `
			SELECT count(*)::int
			FROM model_provider_configs
			WHERE org_id = $1 AND management_kind = 'cluster' AND name = $2
		`, created.Org.ID, template.Name).Scan(&providerCount); err != nil {
		t.Fatalf("count default model providers: %v", err)
	}
	if providerCount != 1 {
		t.Fatalf("default model provider count after replay = %d, want 1", providerCount)
	}
}

func TestConflictingProviderSupersedesDefaultProviderProvisioning(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	store := newSecretIntegrationStore(pool)
	user := mustCreateIdentityUser(t, ctx, store, "default-model-provider-rollback@example.com", "Rollback Owner")
	orgID, err := newSecretUUID()
	if err != nil {
		t.Fatalf("generate org id: %v", err)
	}
	baseTemplate := modelstore.DefaultModelProviderTemplate{
		Provisioner:          "openrouter",
		Name:                 "provider-one",
		CredentialSecretName: "shared-cluster-credential",
		APIFormat:            modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant:           modelprotocol.APIVariantOpenRouter,
		BaseURL:              "https://openrouter.ai/api/v1",
		AuthKind:             modelstore.ModelProviderAuthKindBearerToken,
		Models: []modelstore.DefaultConfiguredModelTemplate{{
			Name: "model-one", ProviderModelSlug: "example/model-one",
			ContextWindowTokens: 8192, MaxOutputTokens: 1024,
		}},
	}
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		OrgID:                         orgID,
		UserID:                        user.ID,
		Name:                          "Durable Provider Org",
		IdempotencyKey:                "durable-default-provider",
		ProvisionDefaultModelProvider: true,
		DefaultMachinePools: []executionstore.DefaultMachinePoolTemplate{
			defaultMachinePoolTemplateWithDefaultMachineForTest(executionstore.DefaultMachinePoolTemplate{
				Name:               "rollback-cluster-pool",
				Provider:           "unikraft",
				ProviderAuthEnvVar: "ROLLBACK_CLUSTER_POOL_TOKEN",
				MaxTotalMachines:   1,
				MaxTotalCPU:        intPtrForMachinePoolTest(1),
				MaxTotalMemoryMB:   intPtrForMachinePoolTest(1024),
				MaxMachineCPU:      intPtrForMachinePoolTest(1),
				MaxMachineMemoryMB: intPtrForMachinePoolTest(1024),
			}, defaultMachineFieldsForTest{
				DefaultMachineCPU:             1,
				DefaultMachineMemoryMB:        1024,
				DefaultMachineProviderOptions: json.RawMessage(`{}`),
			}),
		},
	})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	tenantCredential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: created.Org.ID, OwnerKind: secretstore.SecretOwnerOrg, Name: "tenant-provider-credential",
		Material: secrets.GenericMaterial{Value: "tenant-value"}, Actor: userPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create tenant credential: %v", err)
	}
	tenantProvider, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID: created.Org.ID, Name: baseTemplate.Name, APIFormat: baseTemplate.APIFormat,
		BaseURL: baseTemplate.BaseURL, CredentialSecretID: tenantCredential.ID,
	})
	if err != nil {
		t.Fatalf("create conflicting tenant provider: %v", err)
	}
	claim, found, err := store.Organizations().ClaimDefaultModelProviderProvisioning(ctx)
	if err != nil || !found {
		t.Fatalf("claim provisioning: found=%t err=%v", found, err)
	}
	if err := store.Organizations().CompleteDefaultModelProviderProvisioning(
		ctx,
		orglifecycle.CompleteDefaultModelProviderProvisioningInput{
			Claim:           claim,
			Template:        baseTemplate,
			CredentialValue: "sk-one",
		},
	); !errors.Is(err, orglifecycle.ErrDefaultModelProviderProvisioningSuperseded) {
		t.Fatalf("complete conflicting default provider error = %v, want superseded", err)
	}
	if persisted, err := store.Identity().GetOrg(ctx, orgID); err != nil || persisted.ID != created.Org.ID {
		t.Fatalf("get preserved organization = %+v err=%v", persisted, err)
	}
	persistedProvider, err := store.Models().GetModelProviderConfigByName(ctx, orgID, baseTemplate.Name)
	if err != nil || persistedProvider.ID != tenantProvider.ID {
		t.Fatalf("get preserved tenant provider = %+v err=%v", persistedProvider, err)
	}
	var jobCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM default_model_provider_provisioning_jobs
		WHERE organization_id = $1
	`, orgID).Scan(&jobCount); err != nil {
		t.Fatalf("count provisioning jobs: %v", err)
	}
	if jobCount != 0 {
		t.Fatalf("provisioning jobs after provider conflict = %d, want 0", jobCount)
	}
}

func TestCreateOrgForUserRejectsDefaultMachinePoolNULDefaultCwd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	user := mustCreateIdentityUser(t, ctx, store, "default-pool-nul@example.com", "Cluster Pool NUL")
	defaultPools := []executionstore.DefaultMachinePoolTemplate{
		defaultMachinePoolTemplateWithDefaultMachineForTest(executionstore.DefaultMachinePoolTemplate{
			Name:               "nul-default-pool",
			Provider:           "unikraft",
			DefaultCwd:         "/workspace\x00bad",
			ProviderConfig:     json.RawMessage(`{"api_base_url":"https://api.kraft.cloud"}`),
			MaxTotalMachines:   5,
			MaxTotalCPU:        intPtrForMachinePoolTest(5),
			MaxTotalMemoryMB:   intPtrForMachinePoolTest(5120),
			MaxMachineCPU:      intPtrForMachinePoolTest(1),
			MaxMachineMemoryMB: intPtrForMachinePoolTest(1024),
		}, defaultMachineFieldsForTest{
			DefaultMachineCPU:             1,
			DefaultMachineMemoryMB:        1024,
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"registry.example.com/agent:latest","metro":"sfo"}`),
		}),
	}

	_, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{
			UserID:              user.ID,
			Name:                "Cluster Pool NUL Org",
			IdempotencyKey:      "default-pool-nul-org",
			DefaultMachinePools: defaultPools,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "default_cwd cannot contain NUL") {
		t.Fatalf("create org for user error = %v, want default_cwd NUL rejection", err)
	}
}

func TestUserOrgMembershipRowReusedAcrossMembershipWrites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	user := mustCreateIdentityUser(t, ctx, store, "membership-reuse@example.com", "Membership Reuse")

	invite, err := store.Identity().CreateOrgInvitation(
		ctx,
		identitystore.CreateOrgInvitationInput{OrgID: testOrgID, Email: "membership-reuse@example.com", Role: "member"},
	)
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	if _, err := store.Identity().AcceptOrgInvitation(
		ctx,
		identitystore.AcceptOrgInvitationInput{ID: invite.ID, UserID: user.ID},
	); err != nil {
		t.Fatalf("accept invitation: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: user.ID, Role: "admin"},
	); err != nil {
		t.Fatalf("update org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{OrgID: testOrgID, ProjectID: testProjectID, UserID: user.ID, Role: "developer"},
	); err != nil {
		t.Fatalf("add project membership: %v", err)
	}

	var membershipID ID
	var membershipCount int
	var role string
	if err := pool.QueryRow(ctx, `
SELECT id, role, count(*) OVER ()
FROM org_memberships
WHERE org_id = $1 AND user_id = $2
`, testOrgID, user.ID).
		Scan(&membershipID, &role, &membershipCount); err != nil {
		t.Fatalf("load org membership: %v", err)
	}
	if membershipCount != 1 {
		t.Fatalf("expected one org membership row, got %d", membershipCount)
	}
	if role != "admin" {
		t.Fatalf("org membership role = %s, want admin", role)
	}
	var projectMembershipID ID
	if err := pool.QueryRow(ctx, `
SELECT org_membership_id
FROM project_memberships
WHERE org_id = $1 AND project_id = $2 AND role = 'developer'
`, testOrgID, testProjectID).
		Scan(&projectMembershipID); err != nil {
		t.Fatalf("load project membership: %v", err)
	}
	if projectMembershipID != membershipID {
		t.Fatalf("project membership used org membership %s, want %s", projectMembershipID, membershipID)
	}
}

func TestCreateProjectForPrincipalRejectsNonOrgMemberCreator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 4, 29, 13, 42, 0, 0, time.UTC)
	orgID := testID("project_creator_org")
	user := mustCreateIdentityUser(t, ctx, store, "non-member-creator@example.com", "Non Member")
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, created_at, updated_at) VALUES ($1, 'Project Creator Org', $2, $2)`,
		orgID,
		now,
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	_, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID:          orgID,
			Creator:        userPrincipal(user.ID),
			Name:           "Rejected Project",
			IdempotencyKey: "rejected-project",
		},
	)
	if !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("expected unauthorized for non-org-member creator, got %v", err)
	}
	var projectCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projects WHERE org_id = $1 AND idempotency_key = 'rejected-project'`, orgID).
		Scan(&projectCount); err != nil {
		t.Fatalf("count rejected projects: %v", err)
	}
	if projectCount != 0 {
		t.Fatalf("expected rejected project insert to roll back, got %d rows", projectCount)
	}
}

func TestAuthorizeMachineRequiresAdminOrProjectVisibility(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	admin := mustCreateIdentityUser(t, ctx, store, "machine-admin@example.com", "Machine Admin")
	creator := mustCreateIdentityUser(t, ctx, store, "machine-current-creator@example.com", "Machine Creator")
	member := mustCreateIdentityUser(t, ctx, store, "machine-member@example.com", "Machine Member")
	removedCreator := mustCreateIdentityUser(t, ctx, store, "machine-removed-creator@example.com", "Removed Creator")
	for _, membership := range []identitystore.AddOrgMembershipInput{
		{OrgID: testOrgID, UserID: admin.ID, Role: "admin"},
		{OrgID: testOrgID, UserID: creator.ID, Role: "member"},
		{OrgID: testOrgID, UserID: member.ID, Role: "member"},
		{OrgID: testOrgID, UserID: removedCreator.ID, Role: "member"},
	} {
		if _, err := store.Identity().AddOrgMembership(ctx, membership); err != nil {
			t.Fatalf("add machine auth org membership %s: %v", membership.UserID, err)
		}
	}
	machine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Creator Machine",
			IdempotencyKey: "machine-auth-creator",
		},
	)
	if err != nil {
		t.Fatalf("create creator machine: %v", err)
	}
	removedMachine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Removed Creator Machine",
			IdempotencyKey: "machine-auth-removed",
		},
	)
	if err != nil {
		t.Fatalf("create removed creator machine: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM org_memberships
		WHERE org_id = $1
		  AND user_id = $2
	`, testOrgID, removedCreator.ID); err != nil {
		t.Fatalf("remove creator org membership: %v", err)
	}

	assertMachineAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: admin.ID},
		machine.ID,
		executionstore.MachineActionManage,
		true,
	)
	assertMachineAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: member.ID},
		machine.ID,
		executionstore.MachineActionManage,
		false,
	)
	assertMachineAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: creator.ID},
		machine.ID,
		executionstore.MachineActionManage,
		false,
	)
	assertMachineAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: removedCreator.ID},
		removedMachine.ID,
		executionstore.MachineActionRead,
		false,
	)
	assertMachineAllowed(
		t,
		ctx,
		store,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: admin.ID},
		testID("missing-machine"),
		executionstore.MachineActionRead,
		false,
	)
}

func TestVisibleProjectsForUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	admin := mustCreateIdentityUser(t, ctx, store, "visible-projects-admin@example.com", "Admin")
	viewer := mustCreateIdentityUser(t, ctx, store, "visible-projects-viewer@example.com", "Viewer")
	operator := mustCreateIdentityUser(t, ctx, store, "visible-projects-operator@example.com", "Operator")
	member := mustCreateIdentityUser(t, ctx, store, "visible-projects-member@example.com", "Member")
	for _, membership := range []identitystore.AddOrgMembershipInput{
		{OrgID: testOrgID, UserID: admin.ID, Role: "admin"},
		{OrgID: testOrgID, UserID: viewer.ID, Role: "member"},
		{OrgID: testOrgID, UserID: operator.ID, Role: "member"},
		{OrgID: testOrgID, UserID: member.ID, Role: "member"},
	} {
		if _, err := store.Identity().AddOrgMembership(ctx, membership); err != nil {
			t.Fatalf("add org membership: %v", err)
		}
	}
	privateProject, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID:          testOrgID,
			Creator:        userPrincipal(admin.ID),
			Name:           "Private Project",
			IdempotencyKey: "visible-private",
		},
	)
	if err != nil {
		t.Fatalf("create private project: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     testOrgID,
			ProjectID: privateProject.ID,
			UserID:    viewer.ID,
			Role:      "viewer",
		},
	); err != nil {
		t.Fatalf("add viewer project membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     testOrgID,
			ProjectID: privateProject.ID,
			UserID:    operator.ID,
			Role:      "operator",
		},
	); err != nil {
		t.Fatalf("add operator project membership: %v", err)
	}
	adminPage, err := store.Identity().ListVisibleProjectsForPrincipal(
		ctx,
		identitystore.ListVisibleProjectsForPrincipalInput{OrgID: testOrgID, Principal: userPrincipal(admin.ID), Limit: 10},
	)
	if err != nil {
		t.Fatalf("list admin visible projects: %v", err)
	}
	adminProjects := adminPage.Projects
	if len(adminProjects) != 2 {
		t.Fatalf("admin should see every project, got %+v", adminProjects)
	}
	viewerPage, err := store.Identity().ListVisibleProjectsForPrincipal(
		ctx,
		identitystore.ListVisibleProjectsForPrincipalInput{OrgID: testOrgID, Principal: userPrincipal(viewer.ID), Limit: 10},
	)
	if err != nil {
		t.Fatalf("list viewer visible projects: %v", err)
	}
	viewerProjects := viewerPage.Projects
	if len(viewerProjects) != 1 || viewerProjects[0].Project.ID != privateProject.ID ||
		!identitystore.ProjectRolesAllow(viewerProjects[0].Roles, identitystore.ProjectActionRead) ||
		identitystore.ProjectRolesAllow(viewerProjects[0].Roles, identitystore.ProjectActionManage) ||
		identitystore.ProjectRolesAllow(viewerProjects[0].Roles, identitystore.AgentActionOperate) {
		t.Fatalf("unexpected viewer projects: %+v", viewerProjects)
	}
	operatorPage, err := store.Identity().ListVisibleProjectsForPrincipal(
		ctx,
		identitystore.ListVisibleProjectsForPrincipalInput{OrgID: testOrgID, Principal: userPrincipal(operator.ID), Limit: 10},
	)
	if err != nil {
		t.Fatalf("list operator visible projects: %v", err)
	}
	operatorProjects := operatorPage.Projects
	if len(operatorProjects) != 1 || operatorProjects[0].Project.ID != privateProject.ID ||
		!identitystore.ProjectRolesAllow(operatorProjects[0].Roles, identitystore.AgentActionOperate) ||
		identitystore.ProjectRolesAllow(operatorProjects[0].Roles, identitystore.ProjectActionAccessManage) {
		t.Fatalf("unexpected operator projects: %+v", operatorProjects)
	}
	memberPage, err := store.Identity().ListVisibleProjectsForPrincipal(
		ctx,
		identitystore.ListVisibleProjectsForPrincipalInput{OrgID: testOrgID, Principal: userPrincipal(member.ID), Limit: 10},
	)
	if err != nil {
		t.Fatalf("list member visible projects: %v", err)
	}
	memberProjects := memberPage.Projects
	if len(memberProjects) != 0 {
		t.Fatalf("member without project grant should see no projects, got %+v", memberProjects)
	}
}

func TestVisibleMachinesForUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	seedDefaultProject(t, ctx, store)
	now := time.Date(2026, 4, 29, 14, 10, 0, 0, time.UTC)
	admin := mustCreateIdentityUser(t, ctx, store, "visible-machines-admin@example.com", "Admin")
	viewer := mustCreateIdentityUser(t, ctx, store, "visible-machines-viewer@example.com", "Viewer")
	creator := mustCreateIdentityUser(t, ctx, store, "visible-machines-creator@example.com", "Creator")
	removedCreator := mustCreateIdentityUser(t, ctx, store, "visible-machines-removed@example.com", "Removed")
	noProjectAccess := mustCreateIdentityUser(t, ctx, store, "visible-machines-no-project@example.com", "No Project")
	for _, membership := range []identitystore.AddOrgMembershipInput{
		{OrgID: testOrgID, UserID: admin.ID, Role: "admin"},
		{OrgID: testOrgID, UserID: viewer.ID, Role: "member"},
		{OrgID: testOrgID, UserID: creator.ID, Role: "member"},
		{OrgID: testOrgID, UserID: removedCreator.ID, Role: "member"},
		{OrgID: testOrgID, UserID: noProjectAccess.ID, Role: "member"},
	} {
		if _, err := store.Identity().AddOrgMembership(ctx, membership); err != nil {
			t.Fatalf("add org membership: %v", err)
		}
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{OrgID: testOrgID, ProjectID: testProjectID, UserID: viewer.ID, Role: "viewer"},
	); err != nil {
		t.Fatalf("add viewer project membership: %v", err)
	}
	grantedMachine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Granted Machine",
			IdempotencyKey: "visible-granted",
		},
	)
	if err != nil {
		t.Fatalf("create granted machine: %v", err)
	}
	creatorMachine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Creator Machine",
			IdempotencyKey: "visible-creator",
		},
	)
	if err != nil {
		t.Fatalf("create creator machine: %v", err)
	}
	creatorOverlapProject, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID:          testOrgID,
			Creator:        userPrincipal(admin.ID),
			Name:           "Creator Overlap",
			IdempotencyKey: "visible-creator-overlap",
		},
	)
	if err != nil {
		t.Fatalf("create creator overlap project: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     testOrgID,
			ProjectID: creatorOverlapProject.ID,
			UserID:    creator.ID,
			Role:      "viewer",
		},
	); err != nil {
		t.Fatalf("add creator overlap project membership: %v", err)
	}
	creatorOverlapGrant, _, err := store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      creatorOverlapProject.ID,
			MachineID:      creatorMachine.ID,
			IdempotencyKey: "visible-creator-overlap-grant",
		},
	)
	if err != nil {
		t.Fatalf("create creator overlap machine grant: %v", err)
	}
	if _, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Removed Machine",
			IdempotencyKey: "visible-removed",
		},
	); err != nil {
		t.Fatalf("create removed machine: %v", err)
	}
	grant, _, err := store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      grantedMachine.ID,
			IdempotencyKey: "visible-grant",
		},
	)
	if err != nil {
		t.Fatalf("create machine grant: %v", err)
	}
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			machinePoolInputWithDefaultMachineForTest(
				executionstore.CreateMachinePoolInput{
					OrgID:            testOrgID,
					Name:             "Visible Pool",
					Provider:         "test.provider",
					MaxTotalMachines: 1,
				},
				defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`)},
			),
		))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "visible-pool-grant",
		})

	if err != nil {
		t.Fatalf("create machine pool grant: %v", err)
	}
	var poolMachineID ID
	if err := pool.QueryRow(ctx, `
			INSERT INTO machines(
				org_id, machine_pool_id, source_kind, display_name, provider, lifecycle_state,
				lifecycle_changed_at,
				provider_resource_id, provider_provision_attempted_at,
				cpu, memory_mb, cwd, env, secret_env, provider_options, metadata, created_at, updated_at
			)
			VALUES ($1, $2, 'pool', 'Pool Allocation', $3, 'active',
				$4,
				'pool-allocation-resource', $4,
				1, 1024, '', '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, $4, $4)
			RETURNING id
		`, testOrgID, machinePool.ID, machinePool.Provider, now).Scan(&poolMachineID); err != nil {
		t.Fatalf("insert pool allocation machine: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_machine_grants(
			org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id,
			metadata, created_at, updated_at
		)
		VALUES ($1, $2, $3, 'pool', $4, '{}'::jsonb, $5, $5)
	`, testOrgID, testProjectID, poolMachineID, poolGrant.ID, now); err != nil {
		t.Fatalf("insert pool allocation grant: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM org_memberships
		WHERE org_id = $1
		  AND user_id = $2
	`, testOrgID, removedCreator.ID); err != nil {
		t.Fatalf("remove creator org membership: %v", err)
	}

	adminPage, err := store.Execution().ListVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListVisibleMachinesForPrincipalInput{OrgID: testOrgID, Principal: userPrincipal(admin.ID), Limit: 10},
	)
	if err != nil {
		t.Fatalf("list admin visible machines: %v", err)
	}
	adminMachines := adminPage.Machines
	if len(adminMachines) != 4 {
		t.Fatalf("admin should see every active machine, got %+v", adminMachines)
	}
	adminBYOPage, err := store.Execution().ListVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListVisibleMachinesForPrincipalInput{
			OrgID: testOrgID, Principal: userPrincipal(admin.ID),
			Filters: executionstore.MachineListFilters{SourceKinds: []executionstore.MachineSourceKind{executionstore.MachineSourceKindBYO}}, Limit: 10,
		},
	)
	if err != nil {
		t.Fatalf("list admin visible BYO machines: %v", err)
	}
	adminBYOMachines := adminBYOPage.Machines
	if len(adminBYOMachines) != 3 {
		t.Fatalf("admin should be able to filter to BYO machines, got %+v", adminBYOMachines)
	}
	adminPoolPage, err := store.Execution().ListVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListVisibleMachinesForPrincipalInput{
			OrgID: testOrgID, Principal: userPrincipal(admin.ID),
			Filters: executionstore.MachineListFilters{SourceKinds: []executionstore.MachineSourceKind{executionstore.MachineSourceKindPool}}, Limit: 10,
		},
	)
	if err != nil {
		t.Fatalf("list admin visible pool machines: %v", err)
	}
	adminPoolMachines := adminPoolPage.Machines
	if len(adminPoolMachines) != 1 || adminPoolMachines[0].Machine.ID != poolMachineID {
		t.Fatalf("admin should be able to filter to pool machines, got %+v", adminPoolMachines)
	}
	viewerPage, err := store.Execution().ListVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListVisibleMachinesForPrincipalInput{OrgID: testOrgID, Principal: userPrincipal(viewer.ID), Limit: 10},
	)
	if err != nil {
		t.Fatalf("list viewer visible machines: %v", err)
	}
	viewerMachines := viewerPage.Machines
	if len(viewerMachines) != 2 || viewerMachines[0].CanManage || viewerMachines[1].CanManage {
		t.Fatalf("unexpected viewer machines: %+v", viewerMachines)
	}
	var viewerGranted, viewerPool bool
	for _, visible := range viewerMachines {
		switch visible.Machine.ID {
		case grantedMachine.ID:
			viewerGranted = true
			if len(visible.Sources) != 1 || visible.Sources[0].Kind != "project_machine_grant" ||
				visible.Sources[0].ProjectID != testProjectID ||
				visible.Sources[0].GrantID != grant.ID ||
				visible.Sources[0].GrantSourceKind != "explicit" {
				t.Fatalf("unexpected viewer granted machine sources: %+v", visible.Sources)
			}
		case poolMachineID:
			viewerPool = true
			if len(visible.Sources) != 1 || visible.Sources[0].Kind != "project_machine_grant" ||
				visible.Sources[0].ProjectID != testProjectID ||
				visible.Sources[0].GrantSourceKind != "pool" {
				t.Fatalf("unexpected viewer pool machine sources: %+v", visible.Sources)
			}
		default:
			t.Fatalf("unexpected viewer machine: %+v", visible)
		}
	}
	if !viewerGranted || !viewerPool {
		t.Fatalf("viewer should see explicit BYO and pool machines, got %+v", viewerMachines)
	}
	assertVisibleMachineManageMatchesAuthorize(t, ctx, store, viewer.ID, viewerMachines)

	fullViewerMachines := make(map[ID]executionstore.VisibleMachineRecord, len(viewerMachines))
	for _, machine := range viewerMachines {
		fullViewerMachines[machine.Machine.ID] = machine
	}
	pagedViewerMachines := make(map[ID]bool, len(viewerMachines))
	afterMachine := listing.Cursor{}
	for {
		page, err := store.Execution().ListVisibleMachinesForPrincipal(
			ctx,
			executionstore.ListVisibleMachinesForPrincipalInput{
				OrgID: testOrgID, Principal: userPrincipal(viewer.ID), Limit: 1,
				List: listing.Options{After: afterMachine},
			},
		)
		if err != nil {
			t.Fatalf("list paged viewer visible machines: %v", err)
		}
		if len(page.Machines) != 1 {
			t.Fatalf("paged viewer visible machines returned %d rows, want 1", len(page.Machines))
		}
		machine := page.Machines[0]
		full, ok := fullViewerMachines[machine.Machine.ID]
		if !ok {
			t.Fatalf("paged viewer visible machine not present in full list: %+v", machine)
		}
		if pagedViewerMachines[machine.Machine.ID] {
			t.Fatalf("paged viewer visible machine repeated: %+v", machine)
		}
		if len(machine.Sources) != len(full.Sources) {
			t.Fatalf(
				"paged viewer visible machine %s split sources across pages: got %+v want %+v",
				machine.Machine.ID,
				machine.Sources,
				full.Sources,
			)
		}
		pagedViewerMachines[machine.Machine.ID] = true
		if !page.HasMore {
			break
		}
		afterMachine = page.Next
	}
	if len(pagedViewerMachines) != len(fullViewerMachines) {
		t.Fatalf(
			"paged viewer visible machines covered %d machines, want %d",
			len(pagedViewerMachines),
			len(fullViewerMachines),
		)
	}

	fullAdminMachines := make(map[ID]executionstore.VisibleMachineRecord, len(adminMachines))
	for _, machine := range adminMachines {
		fullAdminMachines[machine.Machine.ID] = machine
	}
	pagedAdminMachines := make(map[ID]bool, len(adminMachines))
	afterMachine = listing.Cursor{}
	for {
		page, err := store.Execution().ListVisibleMachinesForPrincipal(
			ctx,
			executionstore.ListVisibleMachinesForPrincipalInput{
				OrgID: testOrgID, Principal: userPrincipal(admin.ID), Limit: 1,
				List: listing.Options{After: afterMachine},
			},
		)
		if err != nil {
			t.Fatalf("list paged admin visible machines: %v", err)
		}
		if len(page.Machines) != 1 {
			t.Fatalf("paged admin visible machines returned %d rows, want 1", len(page.Machines))
		}
		machine := page.Machines[0]
		full, ok := fullAdminMachines[machine.Machine.ID]
		if !ok {
			t.Fatalf("paged admin visible machine not present in full list: %+v", machine)
		}
		if pagedAdminMachines[machine.Machine.ID] {
			t.Fatalf("paged admin visible machine repeated: %+v", machine)
		}
		if len(machine.Sources) != len(full.Sources) {
			t.Fatalf(
				"paged admin visible machine %s split sources across pages: got %+v want %+v",
				machine.Machine.ID,
				machine.Sources,
				full.Sources,
			)
		}
		pagedAdminMachines[machine.Machine.ID] = true
		if !page.HasMore {
			break
		}
		afterMachine = page.Next
	}
	if len(pagedAdminMachines) != len(fullAdminMachines) {
		t.Fatalf("paged admin visible machines covered %d machines, want %d", len(pagedAdminMachines), len(fullAdminMachines))
	}

	creatorPage, err := store.Execution().ListVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListVisibleMachinesForPrincipalInput{OrgID: testOrgID, Principal: userPrincipal(creator.ID), Limit: 10},
	)
	if err != nil {
		t.Fatalf("list creator visible machines: %v", err)
	}
	creatorMachines := creatorPage.Machines
	if len(creatorMachines) != 1 || creatorMachines[0].Machine.ID != creatorMachine.ID || creatorMachines[0].CanManage {
		t.Fatalf("unexpected creator machines: %+v", creatorMachines)
	}
	if len(creatorMachines[0].Sources) != 1 ||
		creatorMachines[0].Sources[0].Kind != "project_machine_grant" ||
		creatorMachines[0].Sources[0].ProjectID != creatorOverlapProject.ID ||
		creatorMachines[0].Sources[0].GrantID != creatorOverlapGrant.ID {
		t.Fatalf("unexpected creator machine sources: %+v", creatorMachines[0].Sources)
	}
	assertVisibleMachineManageMatchesAuthorize(t, ctx, store, creator.ID, creatorMachines)
	removedPage, err := store.Execution().ListVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListVisibleMachinesForPrincipalInput{OrgID: testOrgID, Principal: userPrincipal(removedCreator.ID), Limit: 10},
	)
	if err != nil {
		t.Fatalf("list removed creator visible machines: %v", err)
	}
	removedMachines := removedPage.Machines
	if len(removedMachines) != 0 {
		t.Fatalf("removed creator should not see old machine, got %+v", removedMachines)
	}
	projectPage, err := store.Execution().ListProjectVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListProjectVisibleMachinesForPrincipalInput{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(viewer.ID), Limit: 10},
	)
	if err != nil {
		t.Fatalf("list project visible machines: %v", err)
	}
	projectMachines := projectPage.Machines
	if len(projectMachines) != 2 || projectMachines[0].CanManage || projectMachines[1].CanManage {
		t.Fatalf("unexpected project visible machines: %+v", projectMachines)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE machines
		SET lifecycle_state = 'provisioning', next_reconcile_after = $1, updated_at = $1
		WHERE id = $2
	`, now.Add(time.Minute), poolMachineID); err != nil {
		t.Fatalf("mark project-granted machine non-active: %v", err)
	}
	nonActiveProjectPage, err := store.Execution().ListProjectVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListProjectVisibleMachinesForPrincipalInput{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(viewer.ID), Limit: 10},
	)
	if err != nil {
		t.Fatalf("list project visible machines with non-active grant: %v", err)
	}
	if len(nonActiveProjectPage.Machines) != 1 || nonActiveProjectPage.Machines[0].Machine.ID != grantedMachine.ID {
		t.Fatalf("non-active granted machine should not be project-visible, got %+v", nonActiveProjectPage.Machines)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE machines
		SET lifecycle_state = 'active', next_reconcile_after = NULL, updated_at = $1
		WHERE id = $2
	`, now.Add(2*time.Minute), poolMachineID); err != nil {
		t.Fatalf("restore project-granted machine active: %v", err)
	}
	projectBYOPage, err := store.Execution().ListProjectVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListProjectVisibleMachinesForPrincipalInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			Principal: userPrincipal(viewer.ID),
			Filters:   executionstore.MachineListFilters{SourceKinds: []executionstore.MachineSourceKind{executionstore.MachineSourceKindBYO}},
			Limit:     10,
		},
	)
	if err != nil {
		t.Fatalf("list project visible BYO machines: %v", err)
	}
	projectBYOMachines := projectBYOPage.Machines
	if len(projectBYOMachines) != 1 || projectBYOMachines[0].Machine.ID != grantedMachine.ID {
		t.Fatalf("unexpected project visible BYO machines: %+v", projectBYOMachines)
	}
	projectPoolPage, err := store.Execution().ListProjectVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListProjectVisibleMachinesForPrincipalInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			Principal: userPrincipal(viewer.ID),
			Filters:   executionstore.MachineListFilters{SourceKinds: []executionstore.MachineSourceKind{executionstore.MachineSourceKindPool}},
			Limit:     10,
		},
	)
	if err != nil {
		t.Fatalf("list project visible pool machines: %v", err)
	}
	projectPoolMachines := projectPoolPage.Machines
	if len(projectPoolMachines) != 1 || projectPoolMachines[0].Machine.ID != poolMachineID {
		t.Fatalf("unexpected project visible pool machines: %+v", projectPoolMachines)
	}
	if _, err := store.Execution().ListProjectVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListProjectVisibleMachinesForPrincipalInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			Principal: userPrincipal(viewer.ID),
			Filters:   executionstore.MachineListFilters{SourceKinds: []executionstore.MachineSourceKind{"bad"}},
			Limit:     10,
		},
	); err == nil {
		t.Fatal("list project visible machines with invalid source kind succeeded")
	}
	noProjectPage, err := store.Execution().ListProjectVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListProjectVisibleMachinesForPrincipalInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			Principal: userPrincipal(noProjectAccess.ID),
			Limit:     10,
		},
	)
	if err != nil {
		t.Fatalf("list project visible machines without project access: %v", err)
	}
	noProjectMachines := noProjectPage.Machines
	if len(noProjectMachines) != 0 {
		t.Fatalf("member without project access should not see project machines, got %+v", noProjectMachines)
	}
	adminProjectPage, err := store.Execution().ListProjectVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListProjectVisibleMachinesForPrincipalInput{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID), Limit: 10},
	)
	if err != nil {
		t.Fatalf("list admin project visible machines: %v", err)
	}
	adminProjectMachines := adminProjectPage.Machines
	if len(adminProjectMachines) != 2 || !adminProjectMachines[0].CanManage || !adminProjectMachines[1].CanManage {
		t.Fatalf("admin project visible machine should report manage access: %+v", adminProjectMachines)
	}
	assertVisibleMachineManageMatchesAuthorize(t, ctx, store, admin.ID, adminMachines)

	if _, err := store.Execution().DeleteMachine(
		ctx,
		executionstore.DeleteMachineInput{
			OrgID:     testOrgID,
			MachineID: grantedMachine.ID,
		},
	); err != nil {
		t.Fatalf("delete granted machine: %v", err)
	}
	viewerPage, err = store.Execution().ListVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListVisibleMachinesForPrincipalInput{OrgID: testOrgID, Principal: userPrincipal(viewer.ID), Limit: 10},
	)
	if err != nil {
		t.Fatalf("list viewer visible machines after delete: %v", err)
	}
	viewerMachines = viewerPage.Machines
	if len(viewerMachines) != 1 || viewerMachines[0].Machine.ID != poolMachineID {
		t.Fatalf("deleted granted machine should not remain visible through project grant, got %+v", viewerMachines)
	}
	projectPage, err = store.Execution().ListProjectVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListProjectVisibleMachinesForPrincipalInput{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(viewer.ID), Limit: 10},
	)
	if err != nil {
		t.Fatalf("list project visible machines after delete: %v", err)
	}
	projectMachines = projectPage.Machines
	if len(projectMachines) != 1 || projectMachines[0].Machine.ID != poolMachineID {
		t.Fatalf("deleted granted machine should not remain project-visible, got %+v", projectMachines)
	}
	adminPage, err = store.Execution().ListVisibleMachinesForPrincipal(
		ctx,
		executionstore.ListVisibleMachinesForPrincipalInput{OrgID: testOrgID, Principal: userPrincipal(admin.ID), Limit: 10},
	)
	if err != nil {
		t.Fatalf("list admin visible machines after delete: %v", err)
	}
	adminMachines = adminPage.Machines
	if len(adminMachines) != 3 {
		t.Fatalf("admin should not see deleted machines in default visible machine list, got %+v", adminMachines)
	}
}

func assertVisibleMachineManageMatchesAuthorize(
	t *testing.T,
	ctx context.Context,
	store *Store,
	userID ID,
	records []executionstore.VisibleMachineRecord,
) {
	t.Helper()
	for _, record := range records {
		allowed, err := store.Execution().AuthorizeMachine(
			ctx,
			executionstore.AuthorizeMachineInput{
				Principal: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: userID},
				OrgID:     testOrgID,
				MachineID: record.Machine.ID,
				Action:    executionstore.MachineActionManage,
			},
		)
		if err != nil {
			t.Fatalf("authorize visible machine manage: %v", err)
		}
		if allowed != record.CanManage {
			t.Fatalf("visible machine %s can_manage=%v but authorize manage=%v", record.Machine.ID, record.CanManage, allowed)
		}
	}
}

func TestCreateOrgForUserReplayBypassesCurrentOwnerLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	user := mustCreateIdentityUser(t, ctx, store, "limit-replay@example.com", "Limit Replay")
	first, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{UserID: user.ID, Name: "Replay Org", IdempotencyKey: "replay-org"},
	)
	if err != nil {
		t.Fatalf("create first org: %v", err)
	}
	for i := 1; i < identitystore.MaxOwnedOrgsPerUser; i++ {
		if _, err := store.Organizations().CreateOrgForUser(
			ctx,
			orglifecycle.CreateOrgForUserInput{
				UserID:         user.ID,
				Name:           fmt.Sprintf("Limit Org %03d", i),
				IdempotencyKey: fmt.Sprintf("limit-org-%03d", i),
			},
		); err != nil {
			t.Fatalf("create org %d up to limit: %v", i, err)
		}
	}
	if _, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{
			UserID:         user.ID,
			Name:           "Over Limit Org",
			IdempotencyKey: "over-limit-org",
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("expected new org over limit to fail, got %v", err)
	}
	replayed, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{UserID: user.ID, Name: "Replay Org", IdempotencyKey: "replay-org"},
	)
	if err != nil {
		t.Fatalf("replay first org at limit: %v", err)
	}
	if replayed.Created || replayed.Org.ID != first.Org.ID {
		t.Fatalf("unexpected replay at limit: %+v", replayed)
	}
}

func TestCreateProjectForPrincipalIdempotencyAndRestrictedCreatorGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	creator := mustCreateIdentityUser(t, ctx, store, "project-creator@example.com", "Project Creator")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: creator.ID, Role: "member"},
	); err != nil {
		t.Fatalf("add creator org membership: %v", err)
	}

	project, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID:          testOrgID,
			Creator:        userPrincipal(creator.ID),
			Name:           "Restricted",
			IdempotencyKey: "restricted",
		},
	)
	if err != nil {
		t.Fatalf("create restricted project: %v", err)
	}
	if !project.Created {
		t.Fatalf("unexpected created restricted project: %+v", project)
	}
	allowed, err := store.Identity().AuthorizeProject(
		ctx,
		identitystore.AuthorizeProjectInput{
			Principal: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: creator.ID},
			OrgID:     testOrgID,
			ProjectID: project.ID,
			Action:    identitystore.ProjectActionAccessManage,
		},
	)
	if err != nil {
		t.Fatalf("authorize creator for restricted project: %v", err)
	}
	if !allowed {
		t.Fatal("expected restricted project creator to receive project admin permission")
	}

	replayed, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID:          testOrgID,
			Creator:        userPrincipal(creator.ID),
			Name:           "Restricted",
			IdempotencyKey: "restricted",
		},
	)
	if err != nil {
		t.Fatalf("replay restricted project: %v", err)
	}
	if replayed.Created || replayed.ID != project.ID {
		t.Fatalf("unexpected replayed project: %+v", replayed)
	}

	if _, err := pool.Exec(ctx, `
		DELETE FROM project_memberships pm
		USING org_memberships om
		WHERE om.org_id = pm.org_id
		  AND om.id = pm.org_membership_id
		  AND pm.org_id = $1
		  AND pm.project_id = $2
		  AND om.user_id = $3
	`, testOrgID, project.ID, creator.ID); err != nil {
		t.Fatalf("remove creator project membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM org_memberships
		WHERE org_id = $1
		  AND user_id = $2
	`, testOrgID, creator.ID); err != nil {
		t.Fatalf("remove creator org membership: %v", err)
	}
	if _, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID:          testOrgID,
			Creator:        userPrincipal(creator.ID),
			Name:           "Restricted",
			IdempotencyKey: "restricted",
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("expected replay by removed creator to fail, got %v", err)
	}
}

func TestDeleteOrganizationDeletesInvitationsAndSecrets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedDefaultProject(t, ctx, newIntegrationStore(pool))
	keyWrapper, err := secrets.NewLocalKeyWrapper(
		"test-key",
		map[string][]byte{"test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("create key wrapper: %v", err)
	}
	store := newIntegrationStore(pool, WithSecretKeyWrapper(keyWrapper))
	owner := mustCreateIdentityUser(t, ctx, store, "org-delete-owner@example.com", "Org Delete Owner")
	invitee := mustCreateIdentityUser(t, ctx, store, "org-delete-invitee@example.com", "Org Delete Invitee")
	created, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{UserID: owner.ID, Name: "Doomed Org", IdempotencyKey: "doomed-org"},
	)
	if err != nil {
		t.Fatalf("create org for user: %v", err)
	}
	invite, err := store.Identity().CreateOrgInvitation(
		ctx,
		identitystore.CreateOrgInvitationInput{
			OrgID: created.Org.ID, Email: "org-delete-invitee@example.com", Role: "admin",
		},
	)
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     created.Org.ID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "doomed-org-secret",
		Material:  secrets.GenericMaterial{Value: "secret"},
		Actor:     userPrincipal(owner.ID),
	})
	if err != nil {
		t.Fatalf("create org secret: %v", err)
	}

	if _, err := store.Organizations().DeleteOrganization(ctx, created.Org.ID, userPrincipal(owner.ID)); err != nil {
		t.Fatalf("delete organization: %v", err)
	}

	var inviteRows int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM org_invitations WHERE id = $1`,
		invite.ID,
	).Scan(&inviteRows); err != nil {
		t.Fatalf("count invitation after org deletion: %v", err)
	}
	if inviteRows != 0 {
		t.Fatalf("invitation after org deletion: %d rows, want 0", inviteRows)
	}
	if _, err := store.Identity().AcceptOrgInvitation(
		ctx,
		identitystore.AcceptOrgInvitationInput{ID: invite.ID, UserID: invitee.ID},
	); !storeerr.IsNotFound(err) {
		t.Fatalf("accept invitation to deleted org error = %v, want not found", err)
	}
	var liveSecrets, versionRows int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FILTER (WHERE deleted_at IS NULL), (SELECT count(*) FROM secret_versions WHERE secret_id = $2) FROM secrets WHERE org_id = $1`,
		created.Org.ID,
		secret.ID,
	).Scan(&liveSecrets, &versionRows); err != nil {
		t.Fatalf("count secrets after org deletion: %v", err)
	}
	if liveSecrets != 0 || versionRows != 0 {
		t.Fatalf("org secrets after deletion: live=%d versions=%d, want soft-deleted rows with destroyed ciphertext", liveSecrets, versionRows)
	}
}

func TestInvitationAcceptRequiresLiveOrganization(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	now := time.Date(2026, 4, 29, 16, 10, 0, 0, time.UTC)
	invitee := mustCreateIdentityUser(t, ctx, store, "race-invitee@example.com", "Race Invitee")
	invite, err := store.Identity().CreateOrgInvitation(
		ctx,
		identitystore.CreateOrgInvitationInput{OrgID: testOrgID, Email: "race-invitee@example.com", Role: "member"},
	)
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	// Soft delete the org directly, bypassing the deletion flow's invitation
	// revocation, to exercise the accept path's own liveness guard.
	if _, err := pool.Exec(
		ctx,
		`UPDATE orgs SET deleted_at = $2, updated_at = $2 WHERE id = $1`,
		testOrgID,
		now.Add(time.Minute),
	); err != nil {
		t.Fatalf("soft delete org: %v", err)
	}
	if _, err := store.Identity().AcceptOrgInvitation(
		ctx,
		identitystore.AcceptOrgInvitationInput{ID: invite.ID, UserID: invitee.ID},
	); !storeerr.IsNotFound(err) {
		t.Fatalf("accept invitation to soft-deleted org error = %v, want not found", err)
	}
	var membershipCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM org_memberships WHERE org_id = $1 AND user_id = $2`,
		testOrgID,
		invitee.ID,
	).Scan(&membershipCount); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	if membershipCount != 0 {
		t.Fatal("accepting an invitation to a deleted org must not create a membership")
	}
}

func TestPoolTeardownCompletionDestroysDeletedOrgSecretVersions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedDefaultProject(t, ctx, newIntegrationStore(pool))
	keyWrapper, err := secrets.NewLocalKeyWrapper(
		"test-key",
		map[string][]byte{"test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("create key wrapper: %v", err)
	}
	store := newIntegrationStore(pool, WithSecretKeyWrapper(keyWrapper))
	now := time.Date(2026, 4, 29, 16, 20, 0, 0, time.UTC)
	admin := createSecretTestUser(t, ctx, store, "Teardown Admin", "admin")
	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "teardown-pool-credential",
		Material:  secrets.GenericMaterial{Value: "secret"},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create pool credential secret: %v", err)
	}
	// A deleted pool still holding its credential, with one machine whose
	// provider teardown is in flight: the state organization deletion leaves
	// behind when it cannot destroy the credential ciphertext yet.
	poolID := testID("teardown-destroy-pool")
	machineID := testID("teardown-destroy-machine")
	if _, err := pool.Exec(ctx, `
		INSERT INTO machine_pools(
			id, org_id, name, management_kind, provider, default_machine_memory_mb,
			max_total_machines, max_total_memory_mb, max_machine_memory_mb,
			provider_auth_secret_id, deleted_at, created_at, updated_at
		)
		VALUES ($1, $2, 'teardown-destroy-pool', 'tenant', 'test', 1024, 1, 1024, 1024, $3, $4, $4, $4)
	`, poolID, testOrgID, credential.ID, now); err != nil {
		t.Fatalf("insert deleted machine pool: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO machines(
			id, org_id, machine_pool_id, source_kind, display_name, provider, lifecycle_state,
			lifecycle_changed_at,
			memory_mb, provider_options, next_reconcile_after, delete_attempts,
			created_at, updated_at
		)
		VALUES ($1, $2, $3, 'pool', 'Teardown Machine', 'test', 'deleting', $4, 1024, '{}'::jsonb, $4, 1, $4, $4)
	`, machineID, testOrgID, poolID, now); err != nil {
		t.Fatalf("insert deleting pool machine: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE orgs SET deleted_at = $2, updated_at = $2 WHERE id = $1`,
		testOrgID,
		now.Add(time.Minute),
	); err != nil {
		t.Fatalf("soft delete org: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE secrets SET deleted_at = $2, current_version_id = NULL, updated_at = $2 WHERE id = $1`,
		credential.ID,
		now.Add(time.Minute),
	); err != nil {
		t.Fatalf("soft delete secret: %v", err)
	}

	if err := store.Execution().CompletePoolMachineDeletion(ctx, testOrgID, machineID, 1); err != nil {
		t.Fatalf("complete pool machine deletion: %v", err)
	}

	var credentialCleared bool
	var versionRows int
	if err := pool.QueryRow(ctx, `
		SELECT provider_auth_secret_id IS NULL,
		       (SELECT count(*) FROM secret_versions WHERE secret_id = $2)
		FROM machine_pools WHERE id = $1
	`, poolID, credential.ID).Scan(&credentialCleared, &versionRows); err != nil {
		t.Fatalf("load pool credential state: %v", err)
	}
	if !credentialCleared || versionRows != 0 {
		t.Fatalf(
			"teardown completion should release the credential and destroy its ciphertext: cleared=%v versions=%d",
			credentialCleared, versionRows,
		)
	}
}
func TestIdempotencyKeysStayConsumedAfterDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	creator := mustCreateIdentityUser(t, ctx, store, "consumed-key@example.com", "Consumed Key")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: creator.ID, Role: "admin"},
	); err != nil {
		t.Fatalf("add creator org membership: %v", err)
	}

	project, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID:          testOrgID,
			Creator:        userPrincipal(creator.ID),
			Name:           "Consumed",
			IdempotencyKey: "consumed",
		},
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := store.Organizations().DeleteProject(ctx, testOrgID, project.ID, userPrincipal(creator.ID)); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if _, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID:          testOrgID,
			Creator:        userPrincipal(creator.ID),
			Name:           "Consumed",
			IdempotencyKey: "consumed",
		},
	); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("recreate project with deleted key error = %v, want idempotency conflict", err)
	}

	created, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{UserID: creator.ID, Name: "Consumed Org", IdempotencyKey: "consumed-org"},
	)
	if err != nil {
		t.Fatalf("create org for user: %v", err)
	}
	if _, err := store.Organizations().DeleteOrganization(ctx, created.Org.ID, userPrincipal(creator.ID)); err != nil {
		t.Fatalf("delete organization: %v", err)
	}
	if _, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{
			UserID:         creator.ID,
			Name:           "Consumed Org",
			IdempotencyKey: "consumed-org",
		},
	); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("recreate org with deleted key error = %v, want idempotency conflict", err)
	}
}

func TestDeleteUserAccountReleasesEmailForReregistration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	first := mustCreateIdentityUser(t, ctx, store, "rejoin@example.com", "Rejoin")

	if err := store.Identity().DeleteUserAccount(ctx, first.ID); err != nil {
		t.Fatalf("delete user account: %v", err)
	}
	var emailCount, identityCount, credentialCount int
	if err := pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM user_emails WHERE user_id = $1),
       (SELECT count(*) FROM user_auth_identities WHERE user_id = $1),
       (SELECT count(*) FROM user_credentials WHERE user_id = $1)
`, first.ID).Scan(&emailCount, &identityCount, &credentialCount); err != nil {
		t.Fatalf("count identity rows: %v", err)
	}
	if emailCount != 0 || identityCount != 0 || credentialCount != 0 {
		t.Fatalf(
			"identity rows should be released on account deletion: emails=%d identities=%d credentials=%d",
			emailCount, identityCount, credentialCount,
		)
	}

	second, err := store.CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{Email: "rejoin@example.com", DisplayName: "Rejoin Again"},
	)
	if err != nil {
		t.Fatalf("re-register with released email: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("re-registration should create a fresh account, not revive the deleted one")
	}
}

func TestDeleteUserAccountAllowsCoOwnerBlocksLastOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	owner := mustCreateIdentityUser(t, ctx, store, "solo-owner@example.com", "Solo Owner")
	created, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{UserID: owner.ID, Name: "Solo Org", IdempotencyKey: "solo-org"},
	)
	if err != nil {
		t.Fatalf("create org for user: %v", err)
	}

	if err := store.Identity().DeleteUserAccount(ctx, owner.ID); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("delete last-owner account error = %v, want conflict", err)
	}

	coOwner := mustCreateIdentityUser(t, ctx, store, "co-owner@example.com", "Co Owner")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: created.Org.ID, UserID: coOwner.ID, Role: "owner"},
	); err != nil {
		t.Fatalf("add co-owner: %v", err)
	}
	if err := store.Identity().DeleteUserAccount(ctx, owner.ID); err != nil {
		t.Fatalf("delete co-owner account: %v", err)
	}

	var remainingOwners int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM org_memberships WHERE org_id = $1 AND role = 'owner'`,
		created.Org.ID,
	).Scan(&remainingOwners); err != nil {
		t.Fatalf("count remaining owners: %v", err)
	}
	if remainingOwners != 1 {
		t.Fatalf("remaining owners = %d, want 1", remainingOwners)
	}
	if err := store.Identity().DeleteUserAccount(ctx, coOwner.ID); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("delete remaining-owner account error = %v, want conflict", err)
	}
}

func TestDeleteUserAccountDeletesOwnedSkillsAndSecrets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	keyWrapper, err := secrets.NewLocalKeyWrapper(
		"test-key",
		map[string][]byte{"test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("create key wrapper: %v", err)
	}
	store := newIntegrationStore(pool, WithBlobStore(integrationblob.MustOpen(t, ctx)), WithSecretKeyWrapper(keyWrapper))
	member := createSecretTestUser(t, ctx, store, "Account Deleter", "member")

	skill := createIntegrationSkill(t, ctx, store, skillstore.CreateSkillInput{
		OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerUser, OwnerUserID: member.ID,
		Name: "account-skill", Actor: userPrincipal(member.ID),
	})
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:       testOrgID,
		OwnerKind:   secretstore.SecretOwnerUser,
		OwnerUserID: member.ID,
		Name:        "account-secret",
		Material:    secrets.GenericMaterial{Value: "secret"},
		Actor:       userPrincipal(member.ID),
	})
	if err != nil {
		t.Fatalf("create owned secret: %v", err)
	}
	if err := store.Identity().DeleteUserAccount(ctx, member.ID); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	var skillDeleted bool
	if err := pool.QueryRow(
		ctx,
		`SELECT deleted_at IS NOT NULL FROM skills WHERE org_id = $1 AND id = $2`,
		testOrgID,
		skill.ID,
	).Scan(&skillDeleted); err != nil {
		t.Fatalf("load owned skill after account deletion: %v", err)
	}
	if !skillDeleted {
		t.Fatal("user-owned skill should be deleted with the account")
	}
	var secretSoftDeleted bool
	var secretVersions int
	if err := pool.QueryRow(
		ctx,
		`SELECT deleted_at IS NOT NULL, (SELECT count(*) FROM secret_versions WHERE secret_id = secrets.id) FROM secrets WHERE org_id = $1 AND id = $2`,
		testOrgID,
		secret.ID,
	).Scan(&secretSoftDeleted, &secretVersions); err != nil {
		t.Fatalf("load owned secret after account deletion: %v", err)
	}
	if !secretSoftDeleted || secretVersions != 0 {
		t.Fatalf("user-owned secret after account deletion: softDeleted=%v versions=%d, want soft-deleted row with destroyed ciphertext", secretSoftDeleted, secretVersions)
	}
}

func TestDeleteProjectArchivesActiveAgents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool)
	now := time.Date(2026, 4, 29, 15, 40, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "delete-project-agent@example.com", "Delete Project")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "delete-project-agent", "Delete Project Agent", `
instruction: Deletable project agent.
model:
  provider_config: openai-prod
  name: delete-project-agent
`, now)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-delete-project-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}

	if _, err := store.Organizations().DeleteProject(ctx, testOrgID, testProjectID, userPrincipal(user.ID)); err != nil {
		t.Fatalf("delete project with active agent: %v", err)
	}

	var agentState string
	var agentArchivedAt bool
	if err := pool.QueryRow(
		ctx,
		`SELECT state, archived_at IS NOT NULL FROM agents WHERE project_id = $1 AND id = $2`,
		testProjectID,
		launch.Agent.ID,
	).Scan(&agentState, &agentArchivedAt); err != nil {
		t.Fatalf("load agent after project deletion: %v", err)
	}
	if agentState != "archived" || !agentArchivedAt {
		t.Fatalf("agent after project deletion: state=%s archived=%v, want archived in the same commit", agentState, agentArchivedAt)
	}
	if _, err := store.Identity().GetProject(ctx, testOrgID, testProjectID); !storeerr.IsNotFound(err) {
		t.Fatalf("deleted project lookup error = %v, want not found", err)
	}
	if _, err := store.Organizations().DeleteProject(ctx, testOrgID, testProjectID, userPrincipal(user.ID)); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("second delete project error = %v, want not found", err)
	}
}

func TestDeleteProjectDestroysOutstandingOAuthRefreshLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"delete-project-oauth-lease@example.com",
		"Delete Project OAuth Lease",
	)
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "project-oauth-lease",
		Material: oauthSecretMaterialForTest(
			"access-token",
			"refresh-token",
			secrets.FixedOAuthAccessTokenLifetime(time.Hour),
		),
		Actor: userPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create project oauth secret: %v", err)
	}
	if _, acquired, err := store.Secrets().AcquireProjectOAuthRefreshLease(
		ctx,
		secretstore.AcquireProjectOAuthRefreshLeaseInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			SecretID:  secret.ID,
			TTL:       time.Minute,
		},
	); err != nil {
		t.Fatalf("acquire project oauth refresh lease: %v", err)
	} else if !acquired {
		t.Fatal("project oauth refresh lease was not acquired")
	}

	if _, err := store.Organizations().DeleteProject(ctx, testOrgID, testProjectID, userPrincipal(user.ID)); err != nil {
		t.Fatalf("delete project with outstanding oauth refresh lease: %v", err)
	}
	var leases int
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM secret_oauth_refresh_leases WHERE secret_id = $1",
		secret.ID,
	).Scan(&leases); err != nil {
		t.Fatalf("count project oauth refresh leases after deletion: %v", err)
	}
	if leases != 0 {
		t.Fatalf("project oauth refresh leases after deletion = %d, want 0", leases)
	}
}

func TestBrowserSessionIdleLifetime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 4, 29, 13, 45, 0, 0, time.UTC)
	user := mustCreateIdentityUser(t, ctx, store, "browser-idle@example.com", "Browser Idle")
	if _, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    user.ID,
			Token:     "idle-session",
			CSRFToken: "idle-csrf",
			TTL:       time.Hour,
		},
	); err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE browser_sessions SET created_at = $1, last_seen_at = $1 WHERE user_id = $2`,
		now.Add(-8*24*time.Hour),
		user.ID,
	); err != nil {
		t.Fatalf("age browser session: %v", err)
	}
	if _, _, err := store.Identity().AuthenticateBrowserSession(ctx, "idle-session"); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("expected idle browser session to be unauthorized, got %v", err)
	}
}

func TestBrowserSessionAuthenticationDoesNotWaitForTouchLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	user := mustCreateIdentityUser(t, ctx, store, "browser-touch-lock@example.com", "Browser Touch Lock")
	session, err := store.Identity().CreateBrowserSession(ctx, identitystore.CreateBrowserSessionInput{
		UserID: user.ID, Token: "touch-lock-session", CSRFToken: "touch-lock-csrf", TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	var staleLastSeen time.Time
	if err := pool.QueryRow(ctx, `
		UPDATE browser_sessions
		SET created_at = statement_timestamp() - interval '10 minutes',
		    last_seen_at = statement_timestamp() - interval '6 minutes'
		WHERE id = $1
		RETURNING last_seen_at
	`, session.ID).Scan(&staleLastSeen); err != nil {
		t.Fatalf("age browser session: %v", err)
	}

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin browser session blocker: %v", err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	var lockedID ID
	if err := blocker.QueryRow(
		ctx,
		`SELECT id FROM browser_sessions WHERE id = $1 FOR UPDATE`,
		session.ID,
	).Scan(&lockedID); err != nil {
		t.Fatalf("lock browser session: %v", err)
	}

	authenticated := make(chan error, 1)
	go func() {
		_, _, authErr := store.Identity().AuthenticateBrowserSession(ctx, "touch-lock-session")
		authenticated <- authErr
	}()
	select {
	case err := <-authenticated:
		if err != nil {
			t.Fatalf("authenticate browser session: %v", err)
		}
	case <-time.After(2 * time.Second):
		_ = blocker.Rollback(ctx)
		<-authenticated
		t.Fatal("browser session authentication waited for the best-effort touch lock")
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatalf("release browser session blocker: %v", err)
	}

	var lastSeen time.Time
	if err := pool.QueryRow(ctx, `SELECT last_seen_at FROM browser_sessions WHERE id = $1`, session.ID).
		Scan(&lastSeen); err != nil {
		t.Fatalf("load browser session after skipped touch: %v", err)
	}
	if !lastSeen.Equal(staleLastSeen) {
		t.Fatalf("last_seen_at = %s, want skipped touch to preserve %s", lastSeen, staleLastSeen)
	}
	if _, _, err := store.Identity().AuthenticateBrowserSession(ctx, "touch-lock-session"); err != nil {
		t.Fatalf("authenticate browser session after releasing lock: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT last_seen_at FROM browser_sessions WHERE id = $1`, session.ID).
		Scan(&lastSeen); err != nil {
		t.Fatalf("load browser session after touch: %v", err)
	}
	if !lastSeen.After(staleLastSeen) {
		t.Fatalf("last_seen_at = %s, want after %s", lastSeen, staleLastSeen)
	}
}

func TestAuthUsageTimestampTouchesAreThrottled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	user := mustCreateIdentityUser(t, ctx, store, "auth-touch@example.com", "Auth Touch")

	session, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    user.ID,
			Token:     "touch-session",
			CSRFToken: "touch-csrf",
			TTL:       time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	if _, _, err := store.Identity().AuthenticateBrowserSession(ctx, "touch-session"); err != nil {
		t.Fatalf("authenticate fresh browser session: %v", err)
	}
	var browserLastSeen time.Time
	if err := pool.QueryRow(ctx, `SELECT last_seen_at FROM browser_sessions WHERE id = $1`, session.ID).
		Scan(&browserLastSeen); err != nil {
		t.Fatalf("load fresh browser session last_seen_at: %v", err)
	}
	if !browserLastSeen.Equal(session.LastSeenAt) {
		t.Fatalf("fresh browser session last_seen_at = %s, want unchanged %s", browserLastSeen, session.LastSeenAt)
	}
	var staleBrowserLastSeen time.Time
	if err := pool.QueryRow(ctx, `
		UPDATE browser_sessions
		SET created_at = transaction_timestamp() - interval '10 minutes',
		    last_seen_at = transaction_timestamp() - interval '6 minutes'
		WHERE id = $1
		RETURNING last_seen_at
	`, session.ID).Scan(&staleBrowserLastSeen); err != nil {
		t.Fatalf("age browser session: %v", err)
	}
	if _, _, err := store.Identity().AuthenticateBrowserSession(ctx, "touch-session"); err != nil {
		t.Fatalf("authenticate stale browser session: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT last_seen_at FROM browser_sessions WHERE id = $1`, session.ID).
		Scan(&browserLastSeen); err != nil {
		t.Fatalf("load stale browser session last_seen_at: %v", err)
	}
	if !browserLastSeen.After(staleBrowserLastSeen) {
		t.Fatalf("stale browser session last_seen_at = %s, want after %s", browserLastSeen, staleBrowserLastSeen)
	}

	pat, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: user.ID,
			Name:   "Touch PAT",
		},
	)
	if err != nil {
		t.Fatalf("create personal access token: %v", err)
	}
	if _, err := store.Identity().AuthenticatePersonalAccessToken(ctx, pat.Token); err != nil {
		t.Fatalf("authenticate unused PAT: %v", err)
	}
	var patLastUsed *time.Time
	if err := pool.QueryRow(ctx, `SELECT last_used_at FROM personal_access_tokens WHERE id = $1`, pat.Record.ID).
		Scan(&patLastUsed); err != nil {
		t.Fatalf("load PAT last_used_at: %v", err)
	}
	if patLastUsed == nil {
		t.Fatal("first PAT authentication did not set last_used_at")
	}
	firstPATAuthAt := *patLastUsed
	if _, err := store.Identity().AuthenticatePersonalAccessToken(
		ctx,
		pat.Token,
	); err != nil {
		t.Fatalf("authenticate fresh PAT: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT last_used_at FROM personal_access_tokens WHERE id = $1`, pat.Record.ID).
		Scan(&patLastUsed); err != nil {
		t.Fatalf("load fresh PAT last_used_at: %v", err)
	}
	if patLastUsed == nil || !patLastUsed.Equal(firstPATAuthAt) {
		t.Fatalf("fresh PAT last_used_at = %v, want %s", patLastUsed, firstPATAuthAt)
	}
	var stalePATLastUsed time.Time
	if err := pool.QueryRow(ctx, `
		UPDATE personal_access_tokens
		SET last_used_at = transaction_timestamp() - interval '61 minutes'
		WHERE id = $1
		RETURNING last_used_at
	`, pat.Record.ID).Scan(&stalePATLastUsed); err != nil {
		t.Fatalf("age PAT last_used_at: %v", err)
	}
	if _, err := store.Identity().AuthenticatePersonalAccessToken(ctx, pat.Token); err != nil {
		t.Fatalf("authenticate stale PAT: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT last_used_at FROM personal_access_tokens WHERE id = $1`, pat.Record.ID).
		Scan(&patLastUsed); err != nil {
		t.Fatalf("load stale PAT last_used_at: %v", err)
	}
	if patLastUsed == nil || !patLastUsed.After(stalePATLastUsed) {
		t.Fatalf("stale PAT last_used_at = %v, want after %s", patLastUsed, stalePATLastUsed)
	}

	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			executionstore.CreateMachinePoolInput{OrgID: testOrgID, Name: "Touch Pool", Provider: "test.provider", MaxTotalMachines: 1},
		))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	var machineID ID
	if err := pool.QueryRow(ctx, `
			INSERT INTO machines(org_id, machine_pool_id, source_kind, display_name, provider, lifecycle_state, lifecycle_changed_at, cpu, memory_mb, cwd, env, secret_env, provider_options, metadata, next_reconcile_after, provision_attempts, created_at, updated_at)
			VALUES ($1, $2, 'pool', 'Touch Machine', $3, 'provisioning', $4, 1, 1024, '', '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, $4, 1, $4, $4)
			RETURNING id
		`, testOrgID, machinePool.ID, machinePool.Provider, now).Scan(&machineID); err != nil {
		t.Fatalf("insert machine: %v", err)
	}
	providerProvisioning, err := store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            testOrgID,
			MachineID:        machineID,
			ProvisionAttempt: 1,
			TokenName:        "touch daemon",
		},
	)
	if err != nil {
		t.Fatalf("create daemon token: %v", err)
	}
	recordPoolMachineProvisioningResourceForTest(
		t,
		ctx,
		store,
		machineID,
		1,
		"touch-machine-resource",
	)
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET lifecycle_state = 'active', next_reconcile_after = NULL, updated_at = $1 WHERE id = $2`,
		now.Add(time.Second),
		machineID,
	); err != nil {
		t.Fatalf("mark machine active: %v", err)
	}
	if _, err := store.Execution().AuthenticateMachineDaemonToken(
		ctx,
		providerProvisioning.DaemonToken.Token,
	); err != nil {
		t.Fatalf("authenticate unused daemon token: %v", err)
	}
	var daemonLastUsed *time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT last_used_at FROM machine_daemon_tokens WHERE id = $1`,
		providerProvisioning.DaemonToken.Record.ID,
	).
		Scan(&daemonLastUsed); err != nil {
		t.Fatalf("load daemon token last_used_at: %v", err)
	}
	if daemonLastUsed == nil {
		t.Fatal("first daemon token authentication did not set last_used_at")
	}
	firstDaemonAuthAt := *daemonLastUsed
	if _, err := store.Execution().AuthenticateMachineDaemonToken(
		ctx,
		providerProvisioning.DaemonToken.Token,
	); err != nil {
		t.Fatalf("authenticate fresh daemon token: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT last_used_at FROM machine_daemon_tokens WHERE id = $1`,
		providerProvisioning.DaemonToken.Record.ID,
	).
		Scan(&daemonLastUsed); err != nil {
		t.Fatalf("load fresh daemon token last_used_at: %v", err)
	}
	if daemonLastUsed == nil || !daemonLastUsed.Equal(firstDaemonAuthAt) {
		t.Fatalf("fresh daemon token last_used_at = %v, want %s", daemonLastUsed, firstDaemonAuthAt)
	}
	var staleDaemonLastUsed time.Time
	if err := pool.QueryRow(ctx, `
		UPDATE machine_daemon_tokens
		SET last_used_at = transaction_timestamp() - interval '61 minutes'
		WHERE id = $1
		RETURNING last_used_at
	`, providerProvisioning.DaemonToken.Record.ID).Scan(&staleDaemonLastUsed); err != nil {
		t.Fatalf("age daemon token last_used_at: %v", err)
	}
	if _, err := store.Execution().AuthenticateMachineDaemonToken(
		ctx,
		providerProvisioning.DaemonToken.Token,
	); err != nil {
		t.Fatalf("authenticate stale daemon token: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT last_used_at FROM machine_daemon_tokens WHERE id = $1`,
		providerProvisioning.DaemonToken.Record.ID,
	).
		Scan(&daemonLastUsed); err != nil {
		t.Fatalf("load stale daemon token last_used_at: %v", err)
	}
	if daemonLastUsed == nil || !daemonLastUsed.After(staleDaemonLastUsed) {
		t.Fatalf("stale daemon token last_used_at = %v, want after %s", daemonLastUsed, staleDaemonLastUsed)
	}
}

func TestOrgInvitationAcceptDoesNotChangeExistingMembership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	now := time.Date(2026, 4, 29, 14, 0, 0, 0, time.UTC)
	user := mustCreateIdentityUser(t, ctx, store, "invitee@example.com", "Invitee")

	invite, err := store.Identity().CreateOrgInvitation(
		ctx,
		identitystore.CreateOrgInvitationInput{OrgID: testOrgID, Email: "Invitee@Example.com ", Role: "admin"},
	)
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	replayed, err := store.Identity().CreateOrgInvitation(
		ctx,
		identitystore.CreateOrgInvitationInput{OrgID: testOrgID, Email: "invitee@example.com", Role: "admin"},
	)
	if err != nil {
		t.Fatalf("replay invitation: %v", err)
	}
	if replayed.ID != invite.ID {
		t.Fatalf("expected same pending invite, got %+v", replayed)
	}
	if _, err := store.Identity().CreateOrgInvitation(
		ctx,
		identitystore.CreateOrgInvitationInput{OrgID: testOrgID, Email: "invitee@example.com", Role: "member"},
	); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("expected pending invitation role conflict, got %v", err)
	}
	if _, err := store.Identity().CreateOrgInvitation(
		ctx,
		identitystore.CreateOrgInvitationInput{OrgID: testOrgID, Email: "owner-invite@example.com", Role: "owner"},
	); err == nil ||
		err.Error() != "role must be admin or member" {
		t.Fatalf("expected owner invitation to be rejected, got %v", err)
	}

	secondOrgID := testID("invite_page_org_2")
	thirdOrgID := testID("invite_page_org_3")
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, created_at, updated_at)
		 VALUES ($1, 'Invite Page Org 2', $2, $2), ($3, 'Invite Page Org 3', $4, $4)`,
		secondOrgID,
		now.Add(time.Minute),
		thirdOrgID,
		now.Add(2*time.Minute),
	); err != nil {
		t.Fatalf("seed pagination invitation orgs: %v", err)
	}
	secondInvite, err := store.Identity().CreateOrgInvitation(
		ctx,
		identitystore.CreateOrgInvitationInput{OrgID: secondOrgID, Email: "invitee@example.com", Role: "member"},
	)
	if err != nil {
		t.Fatalf("create second invitation: %v", err)
	}
	thirdInvite, err := store.Identity().CreateOrgInvitation(
		ctx,
		identitystore.CreateOrgInvitationInput{
			OrgID: thirdOrgID,
			Email: "invitee@example.com",
			Role:  "member",
		},
	)
	if err != nil {
		t.Fatalf("create third invitation: %v", err)
	}
	wantPendingPages := []ID{invite.ID, secondInvite.ID, thirdInvite.ID}
	var gotPendingPages []ID
	var afterInvitation listing.KeysetCursor
	for {
		page, err := store.Identity().ListPendingOrgInvitationsForUser(
			ctx,
			identitystore.ListPendingOrgInvitationsForUserInput{UserID: user.ID, Limit: 1, After: afterInvitation},
		)
		if err != nil {
			t.Fatalf("list paged pending invitations: %v", err)
		}
		if len(page.Invitations) != 1 {
			t.Fatalf("paged pending invitations returned %d rows, want 1", len(page.Invitations))
		}
		invitation := page.Invitations[0]
		gotPendingPages = append(gotPendingPages, invitation.ID)
		if !page.HasMore {
			break
		}
		afterInvitation = listing.KeysetCursor{Set: true, CreatedAt: invitation.CreatedAt, ID: invitation.ID}
	}
	if !slices.Equal(gotPendingPages, wantPendingPages) {
		t.Fatalf("paged pending invitations = %v, want %v", gotPendingPages, wantPendingPages)
	}

	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: user.ID, Role: "member"},
	); err != nil {
		t.Fatalf("add existing membership: %v", err)
	}
	accepted, err := store.Identity().AcceptOrgInvitation(
		ctx,
		identitystore.AcceptOrgInvitationInput{ID: invite.ID, UserID: user.ID},
	)
	if err != nil {
		t.Fatalf("accept invitation: %v", err)
	}
	if accepted.ID != invite.ID {
		t.Fatalf("expected consumed invite %s, got %+v", invite.ID, accepted)
	}
	var inviteRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM org_invitations WHERE id = $1`, invite.ID).Scan(&inviteRows); err != nil {
		t.Fatalf("count invitation after accept: %v", err)
	}
	if inviteRows != 0 {
		t.Fatalf("accepted invitation should be consumed, %d rows remain", inviteRows)
	}
	role, err := testQueries(store).GetOrgAuthorizationRole(
		ctx,
		dbsqlc.GetOrgAuthorizationRoleParams{OrgID: testOrgID, UserID: user.ID},
	)
	if err != nil {
		t.Fatalf("load role: %v", err)
	}
	if role != "member" {
		t.Fatalf("accepting invite should not change existing role, got %q", role)
	}
}

func TestOrgInvitationCanonicalizesInternationalizedDomain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)

	user := mustCreateIdentityUser(t, ctx, store, "Invitee@BÜCHER.example", "Invitee")
	invite, err := store.Identity().CreateOrgInvitation(
		ctx,
		identitystore.CreateOrgInvitationInput{
			OrgID: testOrgID,
			Email: "invitee@xn--bcher-kva.example",
			Role:  "member",
		},
	)
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	if invite.NormalizedEmail != "invitee@xn--bcher-kva.example" {
		t.Fatalf("invitation normalized email = %q, want canonical key", invite.NormalizedEmail)
	}
	accepted, err := store.Identity().AcceptOrgInvitation(
		ctx,
		identitystore.AcceptOrgInvitationInput{ID: invite.ID, UserID: user.ID},
	)
	if err != nil || accepted.ID != invite.ID {
		t.Fatalf("accept invitation with unicode spelling: record=%+v err=%v", accepted, err)
	}
}

func TestListOrgInvitationsPaginatesConsumedInvitesAway(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

	older, err := store.Identity().CreateOrgInvitation(ctx, identitystore.CreateOrgInvitationInput{
		OrgID: testOrgID,
		Email: "older-pending@example.com",
		Role:  "member",
	})
	if err != nil {
		t.Fatalf("create older pending invitation: %v", err)
	}
	consumed, err := store.Identity().CreateOrgInvitation(ctx, identitystore.CreateOrgInvitationInput{
		OrgID: testOrgID,
		Email: "consumed@example.com",
		Role:  "member",
	})
	if err != nil {
		t.Fatalf("create consumed invitation: %v", err)
	}
	if _, err := store.Identity().DeleteOrgInvitation(ctx, testOrgID, consumed.ID); err != nil {
		t.Fatalf("delete consumed invitation: %v", err)
	}
	newer, err := store.Identity().CreateOrgInvitation(ctx, identitystore.CreateOrgInvitationInput{
		OrgID: testOrgID,
		Email: "newer-pending@example.com",
		Role:  "admin",
	})
	if err != nil {
		t.Fatalf("create newer pending invitation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE org_invitations
		SET created_at = CASE id
			WHEN $1 THEN $4::timestamptz
			WHEN $2 THEN $4::timestamptz + interval '1 minute'
			WHEN $3 THEN $4::timestamptz + interval '2 minutes'
		END
		WHERE id IN ($1, $2, $3)
	`, older.ID, consumed.ID, newer.ID, now); err != nil {
		t.Fatalf("set invitation fixture order: %v", err)
	}

	first, err := store.Identity().ListOrgInvitations(ctx, identitystore.ListOrgInvitationsInput{
		OrgID: testOrgID,
		Limit: 1,
	})
	if err != nil {
		t.Fatalf("list first pending invitation page: %v", err)
	}
	if len(first.Invitations) != 1 || first.Invitations[0].ID != newer.ID || !first.HasMore {
		t.Fatalf("first pending invitation page = %+v, want newer invitation with more", first)
	}
	second, err := store.Identity().ListOrgInvitations(ctx, identitystore.ListOrgInvitationsInput{
		OrgID: testOrgID,
		Limit: 1,
		After: listing.KeysetCursor{
			Set:       true,
			CreatedAt: first.Invitations[0].CreatedAt,
			ID:        first.Invitations[0].ID,
		},
	})
	if err != nil {
		t.Fatalf("list second pending invitation page: %v", err)
	}
	if len(second.Invitations) != 1 || second.Invitations[0].ID != older.ID || second.HasMore {
		t.Fatalf("second pending invitation page = %+v, want only older invitation", second)
	}
}

func TestOrgInvitationRejectsExistingMemberAndPreservesMembershipLimits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	now := time.Date(2026, 4, 29, 14, 30, 0, 0, time.UTC)
	member := mustCreateIdentityUser(t, ctx, store, "member@example.com", "Member")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: member.ID, Role: "member"},
	); err != nil {
		t.Fatalf("add existing member: %v", err)
	}
	if _, err := store.Identity().CreateOrgInvitation(
		ctx,
		identitystore.CreateOrgInvitationInput{OrgID: testOrgID, Email: "member@example.com", Role: "admin"},
	); !errors.Is(
		err,
		storeerr.ErrConflict,
	) {
		t.Fatalf("expected existing member invite conflict, got %v", err)
	}

	fullUser := mustCreateIdentityUser(t, ctx, store, "full@example.com", "Full")
	for i := 0; i < identitystore.MaxOrgMembershipsPerUser; i++ {
		orgID := testID(fmt.Sprintf("limit-org-%d", i))
		if _, err := pool.Exec(
			ctx,
			`INSERT INTO orgs(id, name, created_at, updated_at) VALUES ($1, $2, $3, $3)`,
			orgID,
			"Limit Org",
			now,
		); err != nil {
			t.Fatalf("seed limit org %d: %v", i, err)
		}
		if _, err := store.Identity().AddOrgMembership(
			ctx,
			identitystore.AddOrgMembershipInput{OrgID: orgID, UserID: fullUser.ID, Role: "member"},
		); err != nil {
			t.Fatalf("seed limit membership %d: %v", i, err)
		}
	}
	invite, err := store.Identity().CreateOrgInvitation(
		ctx,
		identitystore.CreateOrgInvitationInput{OrgID: testOrgID, Email: "full@example.com", Role: "member"},
	)
	if err != nil {
		t.Fatalf("create full-user invitation: %v", err)
	}
	if _, err := store.Identity().AcceptOrgInvitation(
		ctx,
		identitystore.AcceptOrgInvitationInput{ID: invite.ID, UserID: fullUser.ID},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("expected membership limit on invite accept, got %v", err)
	}
}

func TestAddOrgMembershipPreservesLastOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	owner := mustCreateIdentityUser(t, ctx, store, "owner-only@example.com", "Owner")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: owner.ID, Role: "owner"},
	); err != nil {
		t.Fatalf("add owner: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: owner.ID, Role: "admin"},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("expected last owner demotion to fail, got %v", err)
	}
	otherOwner := mustCreateIdentityUser(t, ctx, store, "owner-other@example.com", "Other Owner")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: otherOwner.ID, Role: "owner"},
	); err != nil {
		t.Fatalf("add other owner: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: owner.ID, Role: "admin"},
	); err != nil {
		t.Fatalf("demote owner with another owner: %v", err)
	}
}

func TestUpdateOrgMemberRole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	member := mustCreateIdentityUser(t, ctx, store, "role-update-member@example.com", "Member")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: member.ID, Role: "member"},
	); err != nil {
		t.Fatalf("add org membership: %v", err)
	}

	updated, err := store.Identity().UpdateOrgMemberRole(ctx, identitystore.UpdateOrgMemberRoleInput{
		OrgID: testOrgID, UserID: member.ID, Role: "admin",
	})
	if err != nil {
		t.Fatalf("update org member role: %v", err)
	}
	if updated.Role != "admin" || updated.UserID != member.ID {
		t.Fatalf("updated membership = %+v", updated)
	}

	if _, err := store.Identity().UpdateOrgMemberRole(ctx, identitystore.UpdateOrgMemberRoleInput{
		OrgID: testOrgID, UserID: member.ID, Role: "owner",
	}); err == nil {
		t.Fatal("expected owner role to be rejected")
	}

	stranger := mustCreateIdentityUser(t, ctx, store, "role-update-stranger@example.com", "Stranger")
	if _, err := store.Identity().UpdateOrgMemberRole(ctx, identitystore.UpdateOrgMemberRoleInput{
		OrgID: testOrgID, UserID: stranger.ID, Role: "admin",
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("expected not found for non-member, got %v", err)
	}

	owner := mustCreateIdentityUser(t, ctx, store, "role-update-owner@example.com", "Owner")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: owner.ID, Role: "owner"},
	); err != nil {
		t.Fatalf("add owner: %v", err)
	}
	if _, err := store.Identity().UpdateOrgMemberRole(ctx, identitystore.UpdateOrgMemberRoleInput{
		OrgID: testOrgID, UserID: owner.ID, Role: "admin",
	}); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("expected sole owner demotion to fail, got %v", err)
	}

	otherOwner := mustCreateIdentityUser(t, ctx, store, "role-update-other-owner@example.com", "Other Owner")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: otherOwner.ID, Role: "owner"},
	); err != nil {
		t.Fatalf("add other owner: %v", err)
	}
	if _, err := store.Identity().UpdateOrgMemberRole(ctx, identitystore.UpdateOrgMemberRoleInput{
		OrgID: testOrgID, UserID: owner.ID, Role: "admin",
	}); err != nil {
		t.Fatalf("demote owner with another owner present: %v", err)
	}
}

func TestRemoveOrgMemberCascadesAccessGrants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newSecretIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	member := mustCreateIdentityUser(t, ctx, store, "remove-member@example.com", "Member")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: member.ID, Role: "member"},
	); err != nil {
		t.Fatalf("add org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: testOrgID, ProjectID: testProjectID, UserID: member.ID, Role: "developer",
	}); err != nil {
		t.Fatalf("add project membership: %v", err)
	}
	userSecret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:       testOrgID,
		OwnerKind:   secretstore.SecretOwnerUser,
		OwnerUserID: member.ID,
		Name:        "removed-member-secret",
		Material:    secrets.GenericMaterial{Value: "secret"},
		Actor:       userPrincipal(member.ID),
	})
	if err != nil {
		t.Fatalf("create member-owned secret: %v", err)
	}

	if err := store.Identity().RemoveOrgMember(ctx, identitystore.RemoveOrgMemberInput{OrgID: testOrgID, UserID: member.ID}); err != nil {
		t.Fatalf("remove org member: %v", err)
	}

	if _, err := store.Identity().UpdateOrgMemberRole(ctx, identitystore.UpdateOrgMemberRoleInput{
		OrgID: testOrgID, UserID: member.ID, Role: "admin",
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("expected removed member to be gone, got %v", err)
	}
	grants, err := store.Identity().ListProjectMembershipGrantsForUser(ctx, testOrgID, member.ID)
	if err != nil {
		t.Fatalf("list project membership grants: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("expected no project grants after removal, got %+v", grants)
	}
	var memberSecretSoftDeleted bool
	var memberSecretVersions int
	if err := pool.QueryRow(
		ctx,
		`SELECT deleted_at IS NOT NULL, (SELECT count(*) FROM secret_versions WHERE secret_id = secrets.id) FROM secrets WHERE org_id = $1 AND id = $2`,
		testOrgID,
		userSecret.ID,
	).Scan(&memberSecretSoftDeleted, &memberSecretVersions); err != nil {
		t.Fatalf("load member-owned secret: %v", err)
	}
	if !memberSecretSoftDeleted || memberSecretVersions != 0 {
		t.Fatalf("member-owned secret should leave with the member: softDeleted=%v versions=%d", memberSecretSoftDeleted, memberSecretVersions)
	}

	if err := store.Identity().RemoveOrgMember(
		ctx,
		identitystore.RemoveOrgMemberInput{OrgID: testOrgID, UserID: member.ID},
	); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("expected not found removing already-removed member, got %v", err)
	}

	owner := mustCreateIdentityUser(t, ctx, store, "remove-sole-owner@example.com", "Owner")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: owner.ID, Role: "owner"},
	); err != nil {
		t.Fatalf("add owner: %v", err)
	}
	if err := store.Identity().RemoveOrgMember(
		ctx,
		identitystore.RemoveOrgMemberInput{OrgID: testOrgID, UserID: owner.ID},
	); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("expected sole owner removal to fail, got %v", err)
	}

	otherOwner := mustCreateIdentityUser(t, ctx, store, "remove-other-owner@example.com", "Other Owner")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: otherOwner.ID, Role: "owner"},
	); err != nil {
		t.Fatalf("add other owner: %v", err)
	}
	if err := store.Identity().RemoveOrgMember(
		ctx,
		identitystore.RemoveOrgMemberInput{OrgID: testOrgID, UserID: owner.ID},
	); err != nil {
		t.Fatalf("remove non-sole owner: %v", err)
	}
}

func TestProjectMembershipGrantLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	member := mustCreateIdentityUser(t, ctx, store, "grant-member@example.com", "Member")
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: testOrgID, UserID: member.ID, Role: "member"},
	); err != nil {
		t.Fatalf("add org membership: %v", err)
	}

	grants, err := store.Identity().ListProjectMembershipGrantsForUser(ctx, testOrgID, member.ID)
	if err != nil {
		t.Fatalf("list project membership grants: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("expected no grants before any are set, got %+v", grants)
	}

	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: testOrgID, ProjectID: testProjectID, UserID: member.ID, Role: "viewer",
	}); err != nil {
		t.Fatalf("add project membership: %v", err)
	}
	grants, err = store.Identity().ListProjectMembershipGrantsForUser(ctx, testOrgID, member.ID)
	if err != nil {
		t.Fatalf("list project membership grants: %v", err)
	}
	if len(grants) != 1 || grants[0].ProjectID != testProjectID || grants[0].Role != "viewer" {
		t.Fatalf("grants = %+v", grants)
	}

	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: testOrgID, ProjectID: testProjectID, UserID: member.ID, Role: "developer",
	}); err != nil {
		t.Fatalf("update project membership: %v", err)
	}
	grants, err = store.Identity().ListProjectMembershipGrantsForUser(ctx, testOrgID, member.ID)
	if err != nil {
		t.Fatalf("list project membership grants: %v", err)
	}
	if len(grants) != 1 || grants[0].Role != "developer" {
		t.Fatalf("grants after re-grant = %+v", grants)
	}

	if err := store.Identity().RemoveProjectMembership(ctx, identitystore.RemoveProjectMembershipInput{
		OrgID: testOrgID, ProjectID: testProjectID, UserID: member.ID,
	}); err != nil {
		t.Fatalf("remove project membership: %v", err)
	}
	grants, err = store.Identity().ListProjectMembershipGrantsForUser(ctx, testOrgID, member.ID)
	if err != nil {
		t.Fatalf("list project membership grants: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("expected no grants after removal, got %+v", grants)
	}

	if err := store.Identity().RemoveProjectMembership(ctx, identitystore.RemoveProjectMembershipInput{
		OrgID: testOrgID, ProjectID: testProjectID, UserID: member.ID,
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("expected not found removing already-removed grant, got %v", err)
	}

	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: testOrgID, ProjectID: testProjectID, UserID: member.ID, Role: "not-a-real-role",
	}); err == nil {
		t.Fatal("expected invalid project role to be rejected")
	}
}

func TestProjectMembershipSchemaEnforcesOrganizationIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	now := time.Date(2026, 5, 1, 12, 30, 0, 0, time.UTC)

	otherOrgID := testID("project_membership_schema_other_org")
	otherProjectID := testID("project_membership_schema_other_project")
	if _, err := pool.Exec(ctx, `
		INSERT INTO orgs(id, name, created_at, updated_at)
		VALUES ($1, 'Project Membership Schema Other Org', $2, $2)
	`, otherOrgID, now); err != nil {
		t.Fatalf("create other org: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects(id, org_id, name, created_at, updated_at)
		VALUES ($1, $2, 'Other Project', $3, $3)
	`, otherProjectID, otherOrgID, now); err != nil {
		t.Fatalf("create other project: %v", err)
	}

	member := mustCreateIdentityUser(t, ctx, store, "project-membership-schema-member@example.com", "Member")
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID: testOrgID, UserID: member.ID, Role: "member",
	}); err != nil {
		t.Fatalf("add member to default org: %v", err)
	}
	var memberOrgMembershipID ID
	if err := pool.QueryRow(
		ctx,
		`SELECT id FROM org_memberships WHERE org_id = $1 AND user_id = $2`,
		testOrgID,
		member.ID,
	).Scan(&memberOrgMembershipID); err != nil {
		t.Fatalf("load member org membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_memberships(org_id, project_id, org_membership_id, role, created_at)
		VALUES ($1, $2, $3, 'viewer', $4)
	`, testOrgID, otherProjectID, memberOrgMembershipID, now); !isForeignKeyViolation(err) {
		t.Fatalf("cross-org project membership error = %v, want foreign key violation", err)
	}

	otherOrgMember := mustCreateIdentityUser(
		t,
		ctx,
		store,
		"project-membership-schema-other-org@example.com",
		"Other Org Member",
	)
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID: otherOrgID, UserID: otherOrgMember.ID, Role: "member",
	}); err != nil {
		t.Fatalf("add member to other org: %v", err)
	}
	var otherOrgMembershipID ID
	if err := pool.QueryRow(
		ctx,
		`SELECT id FROM org_memberships WHERE org_id = $1 AND user_id = $2`,
		otherOrgID,
		otherOrgMember.ID,
	).Scan(&otherOrgMembershipID); err != nil {
		t.Fatalf("load other-org member org membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_memberships(org_id, project_id, org_membership_id, role, created_at)
		VALUES ($1, $2, $3, 'viewer', $4)
	`, testOrgID, testProjectID, otherOrgMembershipID, now); !isForeignKeyViolation(err) {
		t.Fatalf("non-member grant error = %v, want foreign key violation", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO project_memberships(org_id, project_id, org_membership_id, role, created_at)
		VALUES ($1, $2, $3, 'billing-admin', $4)
	`, testOrgID, testProjectID, memberOrgMembershipID, now); !isCheckViolation(err) {
		t.Fatalf("invalid project membership role error = %v, want check violation", err)
	}
}

func TestResolveTrustedAuthIdentityLinksExistingVerifiedEmail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newSecretIntegrationStore(pool)
	const (
		unicodeEmail   = "same-email@bücher.example"
		canonicalEmail = "same-email@xn--bcher-kva.example"
	)
	existing := mustCreateIdentityUser(t, ctx, store, unicodeEmail, "Existing")
	connector, err := store.Identity().UpsertAuthConnector(
		ctx,
		identitystore.CreateAuthConnectorInput{
			Slug:             "oidc-test",
			Kind:             identitystore.AuthConnectorKindOIDC,
			DisplayName:      "OIDC Test",
			Issuer:           "https://idp.example.com",
			ClientID:         "client-id",
			ClientSecret:     "client-secret",
			EmailTrustPolicy: identitystore.AuthConnectorEmailTrustPolicyVerifiedEmail,
			Enabled:          true,
		},
	)
	if err != nil {
		t.Fatalf("create auth connector: %v", err)
	}

	resolved, err := resolveAuthIdentitySessionForTest(
		ctx,
		store,
		identitystore.ResolveAuthIdentityInput{
			AuthConnectorID: connector.ID,
			Issuer:          "https://idp.example.com",
			Subject:         "subject-1",
			Email:           canonicalEmail,
			EmailVerified:   true,
			DisplayName:     "OIDC User",
		},
	)
	if err != nil {
		t.Fatalf("resolve auth identity user: %v", err)
	}
	if resolved.ID != existing.ID {
		t.Fatalf("resolved user=%s want existing=%s", resolved.ID, existing.ID)
	}
	emails, err := testQueries(store).ListVerifiedUserEmailsByUser(
		ctx,
		dbsqlc.ListVerifiedUserEmailsByUserParams{UserID: resolved.ID},
	)
	if err != nil {
		t.Fatalf("list resolved user emails: %v", err)
	}
	if len(emails) != 1 || emails[0].Email != unicodeEmail {
		t.Fatalf("resolved user emails = %+v, want existing verified email", emails)
	}
	replayed, err := resolveAuthIdentitySessionForTest(
		ctx,
		store,
		identitystore.ResolveAuthIdentityInput{
			AuthConnectorID: connector.ID,
			Issuer:          "https://idp.example.com",
			Subject:         "subject-1",
		},
	)
	if err != nil {
		t.Fatalf("replay auth identity without email: %v", err)
	}
	if replayed.ID != resolved.ID {
		t.Fatalf(
			"expected same issuer/subject replay to return linked user, got first=%s replay=%s",
			resolved.ID,
			replayed.ID,
		)
	}
	if _, err := resolveAuthIdentitySessionForTest(
		ctx,
		store,
		identitystore.ResolveAuthIdentityInput{
			AuthConnectorID: connector.ID,
			Issuer:          "https://other-idp.example.com",
			Subject:         "subject-1",
			Email:           unicodeEmail,
			EmailVerified:   true,
			DisplayName:     "OIDC User",
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("issuer mismatch replay error=%v, want unauthorized", err)
	}
	if _, err := resolveAuthIdentitySessionForTest(
		ctx,
		store,
		identitystore.ResolveAuthIdentityInput{
			AuthConnectorID: connector.ID,
			Issuer:          "https://idp.example.com",
			Subject:         "subject-2",
			Email:           canonicalEmail,
			EmailVerified:   true,
			DisplayName:     "Second OIDC User",
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("same connector second subject error=%v, want unauthorized", err)
	}
}

func TestResolveAuthIdentityRequiresTrustedVerifiedEmailForFirstLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newSecretIntegrationStore(pool)
	existing := mustCreateIdentityUser(t, ctx, store, "claimed@example.com", "Existing")
	untrusted, err := store.Identity().UpsertAuthConnector(
		ctx,
		identitystore.CreateAuthConnectorInput{
			Slug:         "untrusted-sso",
			Kind:         identitystore.AuthConnectorKindOIDC,
			DisplayName:  "Untrusted SSO",
			Issuer:       "https://untrusted.example.com",
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			Enabled:      true,
		},
	)
	if err != nil {
		t.Fatalf("create untrusted connector: %v", err)
	}
	trusted, err := store.Identity().UpsertAuthConnector(
		ctx,
		identitystore.CreateAuthConnectorInput{
			Slug:             "trusted-sso",
			Kind:             identitystore.AuthConnectorKindOIDC,
			DisplayName:      "Trusted SSO",
			Issuer:           "https://trusted.example.com",
			ClientID:         "client-id",
			ClientSecret:     "client-secret",
			EmailTrustPolicy: identitystore.AuthConnectorEmailTrustPolicyVerifiedEmail,
			Enabled:          true,
		},
	)
	if err != nil {
		t.Fatalf("create trusted connector: %v", err)
	}
	cases := []identitystore.ResolveAuthIdentityInput{
		{
			AuthConnectorID: untrusted.ID,
			Issuer:          untrusted.Issuer,
			Subject:         "claimed-subject",
			Email:           "claimed@example.com",
			EmailVerified:   true,
		},
		{
			AuthConnectorID: untrusted.ID,
			Issuer:          untrusted.Issuer,
			Subject:         "unclaimed-subject",
			Email:           "unclaimed@example.com",
			EmailVerified:   true,
		},
		{
			AuthConnectorID: trusted.ID,
			Issuer:          trusted.Issuer,
			Subject:         "missing-email-subject",
			EmailVerified:   true,
		},
		{
			AuthConnectorID: trusted.ID,
			Issuer:          trusted.Issuer,
			Subject:         "unverified-subject",
			Email:           "unverified@example.com",
			EmailVerified:   false,
		},
	}
	for _, tc := range cases {
		if _, err := resolveAuthIdentitySessionForTest(ctx, store, tc); !errors.Is(err, storeerr.ErrUnauthorized) {
			t.Fatalf("resolve %+v error=%v, want unauthorized", tc, err)
		}
	}
	var userCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&userCount); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("user count = %d, want only existing user %s", userCount, existing.ID)
	}
}

func TestResolveTrustedAuthIdentityCreatesVerifiedEmailOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newSecretIntegrationStore(pool)
	connector, err := store.Identity().UpsertAuthConnector(
		ctx,
		identitystore.CreateAuthConnectorInput{
			Slug:             "trusted-create",
			Kind:             identitystore.AuthConnectorKindOIDC,
			DisplayName:      "Trusted Create",
			Issuer:           "https://trusted-create.example.com",
			ClientID:         "client-id",
			ClientSecret:     "client-secret",
			EmailTrustPolicy: identitystore.AuthConnectorEmailTrustPolicyVerifiedEmail,
			Enabled:          true,
		},
	)
	if err != nil {
		t.Fatalf("create trusted connector: %v", err)
	}
	resolved, err := resolveAuthIdentitySessionForTest(
		ctx,
		store,
		identitystore.ResolveAuthIdentityInput{
			AuthConnectorID: connector.ID,
			Issuer:          connector.Issuer,
			Subject:         "subject-1",
			Email:           "new-user@example.com",
			EmailVerified:   true,
			DisplayName:     "New User",
		},
	)
	if err != nil {
		t.Fatalf("resolve trusted auth identity user: %v", err)
	}
	emails, err := testQueries(store).ListVerifiedUserEmailsByUser(
		ctx,
		dbsqlc.ListVerifiedUserEmailsByUserParams{UserID: resolved.ID},
	)
	if err != nil {
		t.Fatalf("list resolved emails: %v", err)
	}
	if len(emails) != 1 || emails[0].Email != "new-user@example.com" {
		t.Fatalf("resolved emails = %+v, want new verified email", emails)
	}
	signup, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: "new-user@example.com"},
	)
	if err != nil {
		t.Fatalf("start password signup for social-owned email: %v", err)
	}
	if !signup.EmailAlreadyVerified || signup.User.ID != NilID {
		t.Fatalf("password signup for social-owned email = %+v, want already verified without new user", signup)
	}
}

func TestResolveTrustedAuthIdentityConcurrentFirstLinkSerializesEmailOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newSecretIntegrationStore(pool)
	firstConnector, err := store.Identity().UpsertAuthConnector(
		ctx,
		identitystore.CreateAuthConnectorInput{
			Slug:             "trusted-race-a",
			Kind:             identitystore.AuthConnectorKindOIDC,
			DisplayName:      "Trusted Race A",
			Issuer:           "https://trusted-race-a.example.com",
			ClientID:         "client-id",
			ClientSecret:     "client-secret",
			EmailTrustPolicy: identitystore.AuthConnectorEmailTrustPolicyVerifiedEmail,
			Enabled:          true,
		},
	)
	if err != nil {
		t.Fatalf("create first connector: %v", err)
	}
	secondConnector, err := store.Identity().UpsertAuthConnector(
		ctx,
		identitystore.CreateAuthConnectorInput{
			Slug:             "trusted-race-b",
			Kind:             identitystore.AuthConnectorKindOIDC,
			DisplayName:      "Trusted Race B",
			Issuer:           "https://trusted-race-b.example.com",
			ClientID:         "client-id",
			ClientSecret:     "client-secret",
			EmailTrustPolicy: identitystore.AuthConnectorEmailTrustPolicyVerifiedEmail,
			Enabled:          true,
		},
	)
	if err != nil {
		t.Fatalf("create second connector: %v", err)
	}
	type result struct {
		user identitystore.UserRecord
		err  error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, input := range []identitystore.ResolveAuthIdentityInput{
		{AuthConnectorID: firstConnector.ID, Issuer: firstConnector.Issuer, Subject: "race-a", Email: "race@bücher.example", EmailVerified: true, DisplayName: "Race A"},
		{AuthConnectorID: secondConnector.ID, Issuer: secondConnector.Issuer, Subject: "race-b", Email: "race@xn--bcher-kva.example", EmailVerified: true, DisplayName: "Race B"},
	} {
		input := input
		go func() {
			<-start
			user, err := resolveAuthIdentitySessionForTest(ctx, store, input)
			results <- result{user: user, err: err}
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent resolve errors: first=%v second=%v", first.err, second.err)
	}
	if first.user.ID != second.user.ID {
		t.Fatalf("concurrent resolve users differ: first=%s second=%s", first.user.ID, second.user.ID)
	}
	var userCount, emailCount, identityCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&userCount); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_emails WHERE normalized_email = 'race@xn--bcher-kva.example' AND verified_at IS NOT NULL`).
		Scan(&emailCount); err != nil {
		t.Fatalf("count verified race emails: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_auth_identities WHERE user_id = $1`, first.user.ID).
		Scan(&identityCount); err != nil {
		t.Fatalf("count linked identities: %v", err)
	}
	if userCount != 1 || emailCount != 1 || identityCount != 2 {
		t.Fatalf("counts users=%d emails=%d identities=%d, want 1/1/2", userCount, emailCount, identityCount)
	}
}

func TestResolveTrustedAuthIdentityConcurrentSameSubjectIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newSecretIntegrationStore(pool)
	connector, err := store.Identity().UpsertAuthConnector(
		ctx,
		identitystore.CreateAuthConnectorInput{
			Slug:             "trusted-same-subject-race",
			Kind:             identitystore.AuthConnectorKindOIDC,
			DisplayName:      "Trusted Same Subject Race",
			Issuer:           "https://trusted-same-subject.example.com",
			ClientID:         "client-id",
			ClientSecret:     "client-secret",
			EmailTrustPolicy: identitystore.AuthConnectorEmailTrustPolicyVerifiedEmail,
			Enabled:          true,
		},
	)
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}
	type result struct {
		user identitystore.UserRecord
		err  error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			user, err := resolveAuthIdentitySessionForTest(
				ctx,
				store,
				identitystore.ResolveAuthIdentityInput{
					AuthConnectorID: connector.ID,
					Issuer:          connector.Issuer,
					Subject:         "same-subject",
					Email:           "same-subject@example.com",
					EmailVerified:   true,
					DisplayName:     "Same Subject",
				},
			)
			results <- result{user: user, err: err}
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent same-subject errors: first=%v second=%v", first.err, second.err)
	}
	if first.user.ID != second.user.ID {
		t.Fatalf("concurrent same-subject users differ: first=%s second=%s", first.user.ID, second.user.ID)
	}
	var userCount, emailCount, identityCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&userCount); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_emails WHERE normalized_email = 'same-subject@example.com' AND verified_at IS NOT NULL`).
		Scan(&emailCount); err != nil {
		t.Fatalf("count same-subject emails: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_auth_identities WHERE auth_connector_id = $1 AND subject = 'same-subject'`, connector.ID).
		Scan(&identityCount); err != nil {
		t.Fatalf("count same-subject identities: %v", err)
	}
	if userCount != 1 || emailCount != 1 || identityCount != 1 {
		t.Fatalf("counts users=%d emails=%d identities=%d, want 1/1/1", userCount, emailCount, identityCount)
	}
}

func TestAuthConnectorDisableUnlistedTreatsConfigAsSourceOfTruth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newSecretIntegrationStore(pool)
	if _, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:             "manual-oidc",
		Kind:             identitystore.AuthConnectorKindOIDC,
		DisplayName:      "Manual OIDC",
		Issuer:           "https://manual.example.com",
		AuthorizationURL: "https://manual.example.com/auth",
		ClientID:         "manual-client",
		ClientSecret:     "manual-secret",
		Enabled:          true,
	}); err == nil || !strings.Contains(err.Error(), "issuer discovery") {
		t.Fatalf("manual oidc endpoint error=%v, want issuer discovery validation", err)
	}
	if _, err := store.Identity().UpsertAuthConnector(
		ctx,
		identitystore.CreateAuthConnectorInput{
			Slug:         "keep-sso",
			Kind:         identitystore.AuthConnectorKindOIDC,
			DisplayName:  "Keep SSO",
			Issuer:       "https://keep.example.com",
			ClientID:     "keep-client",
			ClientSecret: "keep-secret",
			Enabled:      true,
		},
	); err != nil {
		t.Fatalf("create kept connector: %v", err)
	}
	if _, err := store.Identity().UpsertAuthConnector(
		ctx,
		identitystore.CreateAuthConnectorInput{
			Slug:         "stale-sso",
			Kind:         identitystore.AuthConnectorKindOIDC,
			DisplayName:  "Stale SSO",
			Issuer:       "https://stale.example.com",
			ClientID:     "stale-client",
			ClientSecret: "stale-secret",
			Enabled:      true,
		},
	); err != nil {
		t.Fatalf("create stale connector: %v", err)
	}
	disabled, err := store.Identity().DisableUnlistedAuthConnectors(ctx, []string{"keep-sso"})
	if err != nil {
		t.Fatalf("disable unlisted connectors: %v", err)
	}
	if disabled != 1 {
		t.Fatalf("disabled connectors = %d, want 1", disabled)
	}
	summaries, err := store.Identity().ListEnabledAuthConnectorSummaries(ctx)
	if err != nil {
		t.Fatalf("list enabled connectors: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Slug != "keep-sso" {
		t.Fatalf("enabled connector summaries = %+v", summaries)
	}
	var staleEnabled bool
	if err := pool.QueryRow(ctx, `SELECT enabled FROM auth_connectors WHERE slug = 'stale-sso'`).
		Scan(&staleEnabled); err != nil {
		t.Fatalf("load stale connector enabled flag: %v", err)
	}
	if staleEnabled {
		t.Fatal("stale connector remained enabled")
	}
}

func TestUpsertAuthConnectorUpdatesConfigButKeepsIdentityNamespaceImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newSecretIntegrationStore(pool)
	first, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:         "corp-sso",
		Kind:         identitystore.AuthConnectorKindOIDC,
		DisplayName:  "Corp SSO",
		Issuer:       "https://idp.example.com",
		ClientID:     "client-id",
		ClientSecret: "first-secret",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("first upsert connector: %v", err)
	}
	updated, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:         "corp-sso",
		Kind:         identitystore.AuthConnectorKindOIDC,
		DisplayName:  "Corp SSO Updated",
		Issuer:       "https://idp.example.com",
		ClientID:     "updated-client-id",
		ClientSecret: "updated-secret",
		Scopes:       []string{"openid", "profile"},
		Enabled:      false,
	})
	if err != nil {
		t.Fatalf("update connector: %v", err)
	}
	if updated.ID != first.ID {
		t.Fatalf("upsert changed connector id: first=%s updated=%s", first.ID, updated.ID)
	}
	if updated.DisplayName != "Corp SSO Updated" || updated.ClientID != "updated-client-id" ||
		updated.ClientSecret != "updated-secret" ||
		updated.Enabled {
		t.Fatalf("updated connector = %+v", updated)
	}
	if !slices.Equal(updated.Scopes, []string{"openid", "profile"}) {
		t.Fatalf("updated scopes = %v", updated.Scopes)
	}
	if _, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:         "corp-sso",
		Kind:         identitystore.AuthConnectorKindOIDC,
		DisplayName:  "Corp SSO Moved",
		Issuer:       "https://other-idp.example.com",
		ClientID:     "client-id",
		ClientSecret: "secret",
		Enabled:      true,
	}); !errors.Is(err, storeerr.ErrAuthConnectorImmutable) {
		t.Fatalf("issuer mutation error = %v, want immutable connector error", err)
	}
	if _, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:             "corp-sso-copy",
		Kind:             identitystore.AuthConnectorKindGitHub,
		DisplayName:      "Corp SSO Copy",
		Issuer:           "https://idp.example.com",
		AuthorizationURL: "https://github.example.com/login/oauth/authorize",
		TokenURL:         "https://github.example.com/login/oauth/access_token",
		UserinfoURL:      "https://github.example.com/api/user",
		ClientID:         "copy-client-id",
		ClientSecret:     "copy-secret",
		Enabled:          true,
	}); !errors.Is(err, storeerr.ErrAuthConnectorIdentityConflict) {
		t.Fatalf("duplicate connector issuer error = %v, want identity conflict", err)
	}
	if _, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:             "github-missing-issuer",
		Kind:             identitystore.AuthConnectorKindGitHub,
		DisplayName:      "GitHub Missing Issuer",
		AuthorizationURL: "https://github.com/login/oauth/authorize",
		TokenURL:         "https://github.com/login/oauth/access_token",
		UserinfoURL:      "https://api.github.com/user",
		ClientID:         "client-id",
		ClientSecret:     "secret",
		Enabled:          true,
	}); err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("missing issuer error = %v, want issuer validation error", err)
	}
	if _, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
		Slug:             "corp-sso",
		Kind:             identitystore.AuthConnectorKindGitHub,
		DisplayName:      "Corp GitHub",
		Issuer:           "https://idp.example.com",
		AuthorizationURL: "https://github.com/login/oauth/authorize",
		TokenURL:         "https://github.com/login/oauth/access_token",
		UserinfoURL:      "https://api.github.com/user",
		ClientID:         "client-id",
		ClientSecret:     "secret",
		Enabled:          true,
	}); !errors.Is(err, storeerr.ErrAuthConnectorImmutable) {
		t.Fatalf("kind mutation error = %v, want immutable connector error", err)
	}
}

func TestConcurrentAuthConnectorIssuerConflictIsClassified(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newSecretIntegrationStore(pool)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, slug := range []string{"concurrent-sso-a", "concurrent-sso-b"} {
		slug := slug
		go func() {
			<-start
			_, err := store.Identity().UpsertAuthConnector(ctx, identitystore.CreateAuthConnectorInput{
				Slug:         slug,
				Kind:         identitystore.AuthConnectorKindOIDC,
				DisplayName:  slug,
				Issuer:       "https://concurrent-idp.example.com",
				ClientID:     slug,
				ClientSecret: "secret",
				Enabled:      true,
			})
			results <- err
		}()
	}
	close(start)

	var succeeded, conflicted int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, storeerr.ErrAuthConnectorIdentityConflict):
			conflicted++
		default:
			t.Fatalf("concurrent auth connector error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent auth connector results = %d succeeded, %d conflicted; want 1/1", succeeded, conflicted)
	}
}

func TestAuthConnectorSchemaEnforcesIdentityNamespace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newSecretIntegrationStore(pool)
	now := time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC)
	connector, err := store.Identity().UpsertAuthConnector(
		ctx,
		identitystore.CreateAuthConnectorInput{
			Slug:         "schema-oidc",
			Kind:         identitystore.AuthConnectorKindOIDC,
			DisplayName:  "Schema OIDC",
			Issuer:       "https://schema-idp.example.com",
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			Enabled:      true,
		},
	)
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}
	for _, test := range []struct {
		name  string
		query string
		value any
	}{
		{name: "id", query: `UPDATE auth_connectors SET id = $1 WHERE id = $2`, value: testID("changed-auth-connector-id")},
		{name: "slug", query: `UPDATE auth_connectors SET slug = $1 WHERE id = $2`, value: "changed-schema-oidc"},
		{name: "kind", query: `UPDATE auth_connectors SET kind = $1 WHERE id = $2`, value: "github"},
		{name: "issuer", query: `UPDATE auth_connectors SET issuer = $1 WHERE id = $2`, value: "https://changed-idp.example.com"},
	} {
		if _, err := pool.Exec(ctx, test.query, test.value, connector.ID); !isPgCode(err, "25006") {
			t.Fatalf("update auth connector %s error = %v, want SQLSTATE 25006", test.name, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_connectors(slug, kind, display_name, issuer, authorization_url, token_url, userinfo_url, client_id, encrypted_client_secret, created_at, updated_at)
		VALUES ('invalid-oidc', 'oidc', 'Invalid OIDC', 'https://invalid-oidc.example.com', 'https://invalid-oidc.example.com/auth', '', '', 'client', '{}'::jsonb, $1, $1)
	`, now); !isCheckViolation(err) {
		t.Fatalf("invalid oidc connector error = %v, want check violation", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_connectors(slug, kind, display_name, issuer, authorization_url, token_url, userinfo_url, client_id, encrypted_client_secret, created_at, updated_at)
		VALUES ('invalid-github', 'github', 'Invalid GitHub', 'https://github.com', 'https://github.com/login/oauth/authorize', '', 'https://api.github.com/user', 'client', '{}'::jsonb, $1, $1)
	`, now); !isCheckViolation(err) {
		t.Fatalf("invalid github connector error = %v, want check violation", err)
	}
	user := mustCreateIdentityUser(t, ctx, store, "schema-identity@example.com", "Schema Identity")
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_auth_identities(user_id, auth_connector_id, issuer, subject, created_at)
		VALUES ($1, $2, 'https://other-idp.example.com', 'subject', $3)
	`, user.ID, connector.ID, now); !isForeignKeyViolation(err) {
		t.Fatalf("mismatched identity issuer error = %v, want foreign key violation", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO personal_access_tokens(user_id, name, token_id, token_hash, created_at)
		VALUES ($1, '', 'empty-name-token-id', 'empty-name-token-hash', $2)
	`, user.ID, now); !isCheckViolation(err) {
		t.Fatalf("empty personal access token name error = %v, want check violation", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO browser_sessions(user_id, token_hash, csrf_token_hash, created_at, last_seen_at, expires_at)
		VALUES ($1, 'invalid-session-token', 'invalid-session-csrf', $2, $2, $2)
	`, user.ID, now); !isCheckViolation(err) {
		t.Fatalf("invalid browser session expiry error = %v, want check violation", err)
	}
}

func TestDeviceAuthFlowSchemaEnforcesApprovalIntegrity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 6, 25, 9, 30, 0, 0, time.UTC)
	user := mustCreateIdentityUser(t, ctx, store, "device-schema@example.com", "Device Schema")
	otherUser := mustCreateIdentityUser(t, ctx, store, "device-schema-other@example.com", "Device Schema Other")
	otherSession, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    otherUser.ID,
			Token:     "device-schema-other-session",
			CSRFToken: "device-schema-other-csrf",
			TTL:       time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}
	session, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    user.ID,
			Token:     "device-schema-session",
			CSRFToken: "device-schema-csrf",
			TTL:       time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_device_flows(device_code_hash, user_code_hash, client_id, client_name, token_name, created_at, expires_at, approved_by_user_id, approved_browser_session_id, approved_at)
		VALUES ('device-schema-wrong-session', 'device-schema-wrong-user', 'test-client', 'client', 'token', $1, $2, $3, $4, $1)
	`, now, now.Add(time.Hour), user.ID, otherSession.ID); !isForeignKeyViolation(err) {
		t.Fatalf("cross-user approved session error = %v, want foreign key violation", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_device_flows(device_code_hash, user_code_hash, client_id, client_name, token_name, created_at, expires_at, consumed_at)
		VALUES ('device-schema-consumed-unapproved', 'device-schema-consumed-user', 'test-client', 'client', 'token', $1, $2, $1)
	`, now, now.Add(time.Hour)); !isCheckViolation(err) {
		t.Fatalf("consumed unapproved flow error = %v, want check violation", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_device_flows(device_code_hash, user_code_hash, client_id, client_name, token_name, created_at, expires_at, approved_by_user_id, approved_browser_session_id, approved_at, denied_at, consumed_at)
		VALUES ('device-schema-denied-consumed', 'device-schema-denied-consumed-user', 'test-client', 'client', 'token', $1, $2, $3, $4, $1, $1, $1)
	`, now, now.Add(time.Hour), user.ID, session.ID); !isCheckViolation(err) {
		t.Fatalf("denied consumed flow error = %v, want check violation", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_device_flows(device_code_hash, user_code_hash, client_id, client_name, token_name, created_at, expires_at)
		VALUES ('device-schema-long-client', 'device-schema-long-client-user', 'test-client', $1, 'token', $2, $3)
	`, strings.Repeat("a", resourcename.MaxCodePoints+1), now, now.Add(time.Hour)); !isCheckViolation(err) {
		t.Fatalf("long client name error = %v, want check violation", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_device_flows(device_code_hash, user_code_hash, client_id, client_name, token_name, created_at, expires_at)
		VALUES ('device-schema-long-token', 'device-schema-long-token-user', 'test-client', 'client', $1, $2, $3)
	`, strings.Repeat("a", resourcename.MaxCodePoints+1), now, now.Add(time.Hour)); !isCheckViolation(err) {
		t.Fatalf("long token name error = %v, want check violation", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_device_flows(device_code_hash, user_code_hash, client_id, client_name, token_name, created_at, expires_at)
		VALUES ('device-schema-control-client', 'device-schema-control-client-user', 'test-client', $1, 'token', $2, $3)
	`, "bad\nclient", now, now.Add(time.Hour)); !isCheckViolation(err) {
		t.Fatalf("control character client name error = %v, want check violation", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_device_flows(device_code_hash, user_code_hash, client_id, client_name, token_name, created_at, expires_at)
		VALUES ('device-schema-control-client-id', 'device-schema-control-client-id-user', $1, 'client', 'token', $2, $3)
	`, "bad\nclient", now, now.Add(time.Hour)); !isCheckViolation(err) {
		t.Fatalf("control character client id error = %v, want check violation", err)
	}
}

func TestDeviceAuthClientIDValidationMatchesSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	clientID := strings.Repeat("é", 129)
	if _, err := store.Identity().StartDeviceAuthFlow(
		ctx,
		identitystore.StartDeviceAuthFlowInput{
			ClientID: clientID, ClientName: "CLI", TokenName: "CLI token",
		},
	); !errors.Is(err, storeerr.ErrInvalidDeviceAuthFlow) {
		t.Fatalf("long device client id error = %v, want ErrInvalidDeviceAuthFlow", err)
	}
	now := time.Date(2026, 6, 25, 9, 30, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_device_flows(device_code_hash, user_code_hash, client_id, client_name, token_name, created_at, expires_at)
		VALUES ('device-schema-long-client-id', 'device-schema-long-client-id-user', $1, 'client', 'token', $2, $3)
	`, clientID, now, now.Add(time.Hour)); !isCheckViolation(err) {
		t.Fatalf("long client id error = %v, want check violation", err)
	}
}

func TestDeviceAuthFlowMintsSingleUsePersonalAccessToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	user := mustCreateIdentityUser(t, ctx, store, "device@example.com", "Device User")
	session, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    user.ID,
			Token:     "device-session",
			CSRFToken: "device-csrf",
			TTL:       time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	if _, err := store.Identity().StartDeviceAuthFlow(
		ctx,
		identitystore.StartDeviceAuthFlowInput{
			ClientID:   testDeviceOAuthClientID,
			ClientName: strings.Repeat("a", resourcename.MaxCodePoints+1),
			TokenName:  "CLI token",
		},
	); !errors.Is(
		err,
		storeerr.ErrInvalidDeviceAuthFlow,
	) {
		t.Fatalf("long device client name error = %v, want ErrInvalidDeviceAuthFlow", err)
	}
	if _, err := store.Identity().StartDeviceAuthFlow(
		ctx,
		identitystore.StartDeviceAuthFlowInput{
			ClientID: testDeviceOAuthClientID, ClientName: "CLI", TokenName: "bad\ntoken",
		},
	); !errors.Is(
		err,
		storeerr.ErrInvalidDeviceAuthFlow,
	) {
		t.Fatalf("control character token name error = %v, want ErrInvalidDeviceAuthFlow", err)
	}
	flow, err := store.Identity().StartDeviceAuthFlow(
		ctx,
		identitystore.StartDeviceAuthFlowInput{
			ClientID: testDeviceOAuthClientID, ClientName: "CLI", TokenName: "CLI token",
		},
	)
	if err != nil {
		t.Fatalf("start device auth flow: %v", err)
	}
	mismatchedClient, err := store.Identity().PollDeviceAuthFlow(
		ctx,
		identitystore.DeviceAuthFlowPollInput{DeviceCode: flow.DeviceCode, ClientID: "other-client"},
	)
	if err != nil {
		t.Fatalf("poll device auth flow with mismatched client: %v", err)
	}
	if mismatchedClient.Status != identitystore.DeviceAuthFlowStatusInvalid || mismatchedClient.Token != "" {
		t.Fatalf("mismatched client poll = %+v, want invalid grant without token", mismatchedClient)
	}
	pendingFlow, err := store.Identity().PendingDeviceAuthFlow(
		ctx,
		identitystore.DeviceAuthFlowPendingInput{UserCode: flow.UserCode},
	)
	if err != nil {
		t.Fatalf("load pending device auth flow: %v", err)
	}
	if pendingFlow.ClientName != "CLI" || pendingFlow.TokenName != "CLI token" ||
		pendingFlow.ExpiresAt.Sub(pendingFlow.CreatedAt) != identitystore.DeviceAuthFlowTTL {
		t.Fatalf("pending device auth flow = %+v", pendingFlow)
	}
	pending, err := store.Identity().PollDeviceAuthFlow(
		ctx,
		identitystore.DeviceAuthFlowPollInput{DeviceCode: flow.DeviceCode, ClientID: testDeviceOAuthClientID},
	)
	if err != nil {
		t.Fatalf("poll pending device auth flow: %v", err)
	}
	if pending.Status != identitystore.DeviceAuthFlowStatusPending || pending.Token != "" {
		t.Fatalf("pending poll = %+v, want pending without token", pending)
	}
	var lastPolledAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT last_polled_at FROM auth_device_flows WHERE device_code_hash = $1`, identitystore.HashBearerToken(flow.DeviceCode)).
		Scan(&lastPolledAt); err != nil {
		t.Fatalf("load pending device poll timestamp: %v", err)
	}
	if lastPolledAt == nil {
		t.Fatal("pending device poll did not set last_polled_at")
	}
	firstPollAt := *lastPolledAt
	slow, err := store.Identity().PollDeviceAuthFlow(
		ctx,
		identitystore.DeviceAuthFlowPollInput{DeviceCode: flow.DeviceCode, ClientID: testDeviceOAuthClientID},
	)
	if err != nil {
		t.Fatalf("poll slow device auth flow: %v", err)
	}
	if slow.Status != identitystore.DeviceAuthFlowStatusSlowDown {
		t.Fatalf("second poll status = %s, want slow_down", slow.Status)
	}
	if err := pool.QueryRow(ctx, `SELECT last_polled_at FROM auth_device_flows WHERE device_code_hash = $1`, identitystore.HashBearerToken(flow.DeviceCode)).
		Scan(&lastPolledAt); err != nil {
		t.Fatalf("load slow device poll timestamp: %v", err)
	}
	if lastPolledAt == nil || !lastPolledAt.Equal(firstPollAt) {
		t.Fatalf("slow device poll last_polled_at = %v, want unchanged %s", lastPolledAt, firstPollAt)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE auth_device_flows
		SET last_polled_at = transaction_timestamp() - ($2::bigint * interval '1 second')
		WHERE device_code_hash = $1
	`, identitystore.HashBearerToken(flow.DeviceCode), int64(identitystore.DeviceAuthPollInterval/time.Second)+1); err != nil {
		t.Fatalf("age device poll timestamp: %v", err)
	}
	boundaryPending, err := store.Identity().PollDeviceAuthFlow(
		ctx,
		identitystore.DeviceAuthFlowPollInput{DeviceCode: flow.DeviceCode, ClientID: testDeviceOAuthClientID},
	)
	if err != nil {
		t.Fatalf("poll boundary device auth flow: %v", err)
	}
	if boundaryPending.Status != identitystore.DeviceAuthFlowStatusPending || boundaryPending.Token != "" {
		t.Fatalf("boundary poll = %+v, want pending without token", boundaryPending)
	}
	if err := pool.QueryRow(ctx, `SELECT last_polled_at FROM auth_device_flows WHERE device_code_hash = $1`, identitystore.HashBearerToken(flow.DeviceCode)).
		Scan(&lastPolledAt); err != nil {
		t.Fatalf("load boundary device poll timestamp: %v", err)
	}
	if lastPolledAt == nil || !lastPolledAt.After(firstPollAt) {
		t.Fatalf("boundary device poll last_polled_at = %v, want after %s", lastPolledAt, firstPollAt)
	}
	boundaryPollAt := *lastPolledAt
	nextPending, err := store.Identity().PollDeviceAuthFlow(
		ctx,
		identitystore.DeviceAuthFlowPollInput{DeviceCode: flow.DeviceCode, ClientID: testDeviceOAuthClientID},
	)
	if err != nil {
		t.Fatalf("poll next device auth flow: %v", err)
	}
	if nextPending.Status != identitystore.DeviceAuthFlowStatusSlowDown || nextPending.Token != "" {
		t.Fatalf("next poll = %+v, want slow_down without token", nextPending)
	}
	if err := pool.QueryRow(ctx, `SELECT last_polled_at FROM auth_device_flows WHERE device_code_hash = $1`, identitystore.HashBearerToken(flow.DeviceCode)).
		Scan(&lastPolledAt); err != nil {
		t.Fatalf("load next device poll timestamp: %v", err)
	}
	if lastPolledAt == nil || !lastPolledAt.Equal(boundaryPollAt) {
		t.Fatalf("next device poll last_polled_at = %v, want unchanged %s", lastPolledAt, boundaryPollAt)
	}
	revokedSession, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    user.ID,
			Token:     "device-revoked-session",
			CSRFToken: "device-revoked-csrf",
			TTL:       time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("create revoked browser session: %v", err)
	}
	if err := store.Identity().RevokeBrowserSession(ctx, "device-revoked-session"); err != nil {
		t.Fatalf("revoke browser session: %v", err)
	}
	revokedFlow, err := store.Identity().StartDeviceAuthFlow(ctx, identitystore.StartDeviceAuthFlowInput{
		ClientID: testDeviceOAuthClientID, ClientName: "CLI revoked",
	})
	if err != nil {
		t.Fatalf("start revoked-session device auth flow: %v", err)
	}
	if err := store.Identity().ApproveDeviceAuthFlow(
		ctx,
		identitystore.ApproveDeviceAuthFlowInput{
			UserCode:                 revokedFlow.UserCode,
			UserID:                   user.ID,
			ApprovedBrowserSessionID: revokedSession.ID,
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("approve with revoked session error = %v, want ErrUnauthorized", err)
	}
	approvalRevokedSession, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    user.ID,
			Token:     "device-approval-revoked-session",
			CSRFToken: "device-approval-revoked-csrf",
			TTL:       time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("create approval-revoked browser session: %v", err)
	}
	approvalRevokedFlow, err := store.Identity().StartDeviceAuthFlow(
		ctx,
		identitystore.StartDeviceAuthFlowInput{
			ClientID: testDeviceOAuthClientID, ClientName: "CLI approval revoked",
		},
	)
	if err != nil {
		t.Fatalf("start approval-revoked device auth flow: %v", err)
	}
	if err := store.Identity().ApproveDeviceAuthFlow(
		ctx,
		identitystore.ApproveDeviceAuthFlowInput{
			UserCode:                 approvalRevokedFlow.UserCode,
			UserID:                   user.ID,
			ApprovedBrowserSessionID: approvalRevokedSession.ID,
		},
	); err != nil {
		t.Fatalf("approve approval-revoked device auth flow: %v", err)
	}
	if err := store.Identity().RevokeBrowserSession(ctx, "device-approval-revoked-session"); err != nil {
		t.Fatalf("revoke approval browser session: %v", err)
	}
	invalidated, err := store.Identity().PollDeviceAuthFlow(
		ctx,
		identitystore.DeviceAuthFlowPollInput{
			DeviceCode: approvalRevokedFlow.DeviceCode, ClientID: testDeviceOAuthClientID,
		},
	)
	if err != nil {
		t.Fatalf("poll invalidated device auth flow: %v", err)
	}
	if invalidated.Status != identitystore.DeviceAuthFlowStatusDenied || invalidated.Token != "" {
		t.Fatalf("invalidated poll = %+v, want denied without token", invalidated)
	}
	if err := store.Identity().ApproveDeviceAuthFlow(
		ctx,
		identitystore.ApproveDeviceAuthFlowInput{
			UserCode:                 flow.UserCode,
			UserID:                   user.ID,
			ApprovedBrowserSessionID: session.ID,
		},
	); err != nil {
		t.Fatalf("approve device auth flow: %v", err)
	}
	if _, err := store.Identity().PendingDeviceAuthFlow(
		ctx,
		identitystore.DeviceAuthFlowPendingInput{UserCode: flow.UserCode},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("pending approved device flow error = %v, want unauthorized", err)
	}
	approved, err := store.Identity().PollDeviceAuthFlow(
		ctx,
		identitystore.DeviceAuthFlowPollInput{DeviceCode: flow.DeviceCode, ClientID: testDeviceOAuthClientID},
	)
	if err != nil {
		t.Fatalf("poll approved device auth flow: %v", err)
	}
	if approved.Status != identitystore.DeviceAuthFlowStatusApproved || approved.Token == "" {
		t.Fatalf("approved poll = %+v, want approved token", approved)
	}
	if err := bearertoken.Validate(approved.Token, bearertoken.KindPersonalAccess); err != nil {
		t.Fatalf("validate device-flow PAT: %v", err)
	}
	principal, err := store.Identity().AuthenticatePersonalAccessToken(ctx, approved.Token)
	if err != nil {
		t.Fatalf("authenticate minted device token: %v", err)
	}
	if principal.Type != identitystore.PrincipalTypeUser || principal.ID != user.ID || principal.PersonalAccessTokenID == NilID {
		t.Fatalf("minted token principal = %+v, want user PAT principal", principal)
	}
	var deviceTokenHash string
	if err := pool.QueryRow(
		ctx,
		`SELECT token_hash FROM personal_access_tokens WHERE id = $1`,
		principal.PersonalAccessTokenID,
	).Scan(&deviceTokenHash); err != nil {
		t.Fatalf("load device-flow PAT hash: %v", err)
	}
	if deviceTokenHash != identitystore.HashBearerToken(approved.Token) {
		t.Fatalf("device-flow PAT hash = %q", deviceTokenHash)
	}
	replay, err := store.Identity().PollDeviceAuthFlow(
		ctx,
		identitystore.DeviceAuthFlowPollInput{DeviceCode: flow.DeviceCode, ClientID: testDeviceOAuthClientID},
	)
	if err != nil {
		t.Fatalf("poll consumed device auth flow: %v", err)
	}
	if replay.Status != identitystore.DeviceAuthFlowStatusExpired || replay.Token != "" {
		t.Fatalf("consumed poll = %+v, want expired without token", replay)
	}
}

func TestDeviceAuthFlowPollSerializesWithCompromiseRevocation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateIdentityUser(t, ctx, store, "device-compromise@example.com", "Device Compromise")
	session, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    user.ID,
			Token:     "device-compromise-session",
			CSRFToken: "device-compromise-csrf",
			TTL:       time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	flow, err := store.Identity().StartDeviceAuthFlow(ctx, identitystore.StartDeviceAuthFlowInput{
		ClientID: testDeviceOAuthClientID, ClientName: "CLI compromise",
	})
	if err != nil {
		t.Fatalf("start device auth flow: %v", err)
	}
	if err := store.Identity().ApproveDeviceAuthFlow(
		ctx,
		identitystore.ApproveDeviceAuthFlowInput{
			UserCode:                 flow.UserCode,
			UserID:                   user.ID,
			ApprovedBrowserSessionID: session.ID,
		},
	); err != nil {
		t.Fatalf("approve device auth flow: %v", err)
	}

	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin user lock: %v", err)
	}
	if _, err := lockTx.Exec(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, user.ID); err != nil {
		_ = lockTx.Rollback(ctx)
		t.Fatalf("lock user: %v", err)
	}
	var blockingPID int32
	if err := lockTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		_ = lockTx.Rollback(ctx)
		t.Fatalf("get user lock backend: %v", err)
	}
	revokeCh := make(chan error, 1)
	go func() {
		revokeCh <- store.AccountSecurity().RevokeUserTokensForCompromiseWithPasswordIfPresent(ctx, user.ID, "")
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "FROM users", blockingPID)

	type pollResult struct {
		record identitystore.DeviceAuthFlowPollRecord
		err    error
	}
	pollCh := make(chan pollResult, 1)
	go func() {
		record, err := store.Identity().PollDeviceAuthFlow(ctx, identitystore.DeviceAuthFlowPollInput{
			DeviceCode: flow.DeviceCode, ClientID: testDeviceOAuthClientID,
		})
		pollCh <- pollResult{record: record, err: err}
	}()
	integrationdb.WaitForLockWaiters(t, ctx, pool, "FROM users", 2)
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release user lock: %v", err)
	}
	if err := <-revokeCh; err != nil {
		t.Fatalf("compromise revocation: %v", err)
	}
	result := <-pollCh
	if result.err != nil {
		t.Fatalf("poll approved device auth flow: %v", result.err)
	}
	if result.record.Status != identitystore.DeviceAuthFlowStatusDenied || result.record.Token != "" {
		t.Fatalf("poll after compromise = %+v, want denied without token", result.record)
	}
}

func TestDeviceAuthFlowDenial(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	flow, err := store.Identity().StartDeviceAuthFlow(ctx, identitystore.StartDeviceAuthFlowInput{
		ClientID: testDeviceOAuthClientID, ClientName: "CLI",
	})
	if err != nil {
		t.Fatalf("start device auth flow: %v", err)
	}
	if err := store.Identity().DenyDeviceAuthFlow(
		ctx,
		identitystore.DenyDeviceAuthFlowInput{UserCode: flow.UserCode},
	); err != nil {
		t.Fatalf("deny device auth flow: %v", err)
	}
	denied, err := store.Identity().PollDeviceAuthFlow(
		ctx,
		identitystore.DeviceAuthFlowPollInput{DeviceCode: flow.DeviceCode, ClientID: testDeviceOAuthClientID},
	)
	if err != nil {
		t.Fatalf("poll denied device auth flow: %v", err)
	}
	if denied.Status != identitystore.DeviceAuthFlowStatusDenied || denied.Token != "" {
		t.Fatalf("denied poll = %+v, want denied without token", denied)
	}
}

func TestDeviceAuthFlowApprovalRejectsExpiryAfterLockWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	user := mustCreateIdentityUser(
		t,
		ctx,
		store,
		"device-lock-expiry@example.com",
		"Device Lock Expiry")

	session, err := store.Identity().CreateBrowserSession(ctx, identitystore.CreateBrowserSessionInput{
		UserID:    user.ID,
		Token:     "device-lock-expiry-session",
		CSRFToken: "device-lock-expiry-csrf",
		TTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	flow, err := store.Identity().StartDeviceAuthFlow(ctx, identitystore.StartDeviceAuthFlowInput{
		ClientID: testDeviceOAuthClientID, ClientName: "CLI lock expiry",
	})
	if err != nil {
		t.Fatalf("start device auth flow: %v", err)
	}
	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin device flow lock: %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	var blockingPID int32
	if err := lockTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get device flow lock backend: %v", err)
	}
	if _, err := lockTx.Exec(
		ctx,
		`SELECT id FROM auth_device_flows WHERE user_code_hash = $1 FOR UPDATE`,
		identitystore.HashBearerToken(identitystore.NormalizeDeviceUserCode(flow.UserCode)),
	); err != nil {
		t.Fatalf("lock device auth flow: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- store.Identity().ApproveDeviceAuthFlow(context.Background(), identitystore.ApproveDeviceAuthFlowInput{
			UserCode:                 flow.UserCode,
			UserID:                   user.ID,
			ApprovedBrowserSessionID: session.ID,
		})
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "user_code_hash", blockingPID)
	if _, err := lockTx.Exec(
		ctx,
		`UPDATE auth_device_flows SET expires_at = statement_timestamp() - interval '1 millisecond' WHERE user_code_hash = $1`,
		identitystore.HashBearerToken(identitystore.NormalizeDeviceUserCode(flow.UserCode)),
	); err != nil {
		t.Fatalf("expire locked device auth flow: %v", err)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release device auth flow lock: %v", err)
	}
	if err := <-done; !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("device approval after lock-wait expiry error = %v, want ErrUnauthorized", err)
	}
	var approvedAt *time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT approved_at FROM auth_device_flows WHERE user_code_hash = $1`,
		identitystore.HashBearerToken(identitystore.NormalizeDeviceUserCode(flow.UserCode)),
	).Scan(&approvedAt); err != nil {
		t.Fatalf("load expired device auth flow: %v", err)
	}
	if approvedAt != nil {
		t.Fatalf("expired device auth flow approved_at = %v, want nil", approvedAt)
	}
}

func TestPasswordSignupAllowsDuplicateUnverifiedAndFirstVerificationWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)

	first, err := store.Identity().StartPasswordSignup(ctx, identitystore.PasswordSignupStartInput{Email: "Signup@Example.com"})
	if err != nil {
		t.Fatalf("start first signup: %v", err)
	}
	second, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: "signup@example.com"},
	)
	if err != nil {
		t.Fatalf("start second signup: %v", err)
	}
	if first.User.ID == second.User.ID || first.Email.ID == second.Email.ID {
		t.Fatalf("duplicate unverified signups should create separate rows: first=%+v second=%+v", first, second)
	}
	if _, err := authenticatePasswordForTest(
		t,
		ctx,
		store,
		"signup@example.com",
		"correct horse battery staple",
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("unverified signup authenticated with err=%v, want unauthorized", err)
	}

	firstHash, err := authn.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash first password: %v", err)
	}
	completed, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{
			Token:        first.Token,
			PasswordHash: firstHash,
			DisplayName:  "First",
		},
	)
	if err != nil {
		t.Fatalf("complete first signup: %v", err)
	}
	if !completed.Verified || completed.User.ID != first.User.ID {
		t.Fatalf("unexpected first completion: %+v", completed)
	}
	if _, err := store.CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{
			Email:       "signup@example.com",
			DisplayName: "Duplicate",
		},
	); !errors.Is(
		err,
		storeerr.ErrIdempotencyConflict,
	) {
		t.Fatalf("duplicate verified email error=%v, want idempotency conflict", err)
	}

	secondHash, err := authn.HashPassword("another correct horse staple")
	if err != nil {
		t.Fatalf("hash second password: %v", err)
	}
	losing, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{
			Token:        second.Token,
			PasswordHash: secondHash,
			DisplayName:  "Second",
		},
	)
	if err != nil {
		t.Fatalf("complete losing signup: %v", err)
	}
	if losing.Verified || losing.User.ID != NilID {
		t.Fatalf("losing signup should not verify or create a credential: %+v", losing)
	}
	user, err := authenticatePasswordForTest(
		t,
		ctx,
		store,
		"signup@example.com",
		"correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("authenticate winning password: %v", err)
	}
	if user.ID != first.User.ID {
		t.Fatalf("authenticated user=%s want first=%s", user.ID, first.User.ID)
	}
	if _, err := authenticatePasswordForTest(
		t,
		ctx,
		store,
		"signup@example.com",
		"another correct horse staple",
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("losing password auth error=%v, want unauthorized", err)
	}
}

func TestPasswordSignupRejectsExpiryAfterTokenLockWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	start, err := store.Identity().StartPasswordSignup(ctx, identitystore.PasswordSignupStartInput{Email: "signup-lock-expiry@example.com"})
	if err != nil {
		t.Fatalf("start signup: %v", err)
	}
	passwordHash, err := authn.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin signup token lock: %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	var blockingPID int32
	if err := lockTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get signup token lock backend: %v", err)
	}
	if _, err := lockTx.Exec(
		ctx,
		`SELECT id FROM user_auth_tokens WHERE token_hash = $1 FOR UPDATE`,
		identitystore.HashBearerToken(start.Token),
	); err != nil {
		t.Fatalf("lock signup token: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := store.Identity().CompletePasswordSignup(context.Background(), identitystore.CompletePasswordSignupInput{
			Token:        start.Token,
			PasswordHash: passwordHash,
		})
		done <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "FOR UPDATE OF token", blockingPID)
	if _, err := lockTx.Exec(
		ctx,
		`UPDATE user_auth_tokens SET expires_at = statement_timestamp() - interval '1 millisecond' WHERE token_hash = $1`,
		identitystore.HashBearerToken(start.Token),
	); err != nil {
		t.Fatalf("expire locked signup token: %v", err)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release signup token lock: %v", err)
	}
	if err := <-done; !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("signup after lock-wait expiry error = %v, want ErrUnauthorized", err)
	}
	var verifiedEmails, credentials int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*)::int FROM user_emails WHERE user_id = $1 AND verified_at IS NOT NULL`,
		start.User.ID,
	).Scan(&verifiedEmails); err != nil {
		t.Fatalf("count verified signup emails: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM user_credentials WHERE user_id = $1`, start.User.ID).
		Scan(&credentials); err != nil {
		t.Fatalf("count signup credentials: %v", err)
	}
	if verifiedEmails != 0 || credentials != 0 {
		t.Fatalf("expired signup side effects: verified emails=%d credentials=%d, want zero", verifiedEmails, credentials)
	}
}

func TestUserAuthTokenSchemaEnforcesEmailOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	owner := mustCreateIdentityUser(t, ctx, store, "token-owner@example.com", "Token Owner")
	other := mustCreateIdentityUser(t, ctx, store, "token-other@example.com", "Token Other")
	emails, err := testQueries(store).ListVerifiedUserEmailsByUser(ctx, dbsqlc.ListVerifiedUserEmailsByUserParams{UserID: owner.ID})
	if err != nil {
		t.Fatalf("list owner email: %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("owner emails = %+v, want one", emails)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_auth_tokens(user_id, user_email_id, purpose, token_hash, created_at, expires_at)
		VALUES ($1, $2, 'email_verification', 'mismatched-email-token', $3, $4)
	`, other.ID, emails[0].ID, now, now.Add(time.Hour)); !isForeignKeyViolation(err) {
		t.Fatalf("mismatched token email owner error = %v, want foreign key violation", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_auth_tokens(user_id, user_email_id, purpose, token_hash, created_at, expires_at)
		VALUES ($1, $2, 'email_verification', 'owned-email-token', $3, $4)
	`, owner.ID, emails[0].ID, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("insert owned email token: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM user_emails WHERE id = $1`, emails[0].ID); err != nil {
		t.Fatalf("delete owner email: %v", err)
	}
	var tokenCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_auth_tokens WHERE token_hash = 'owned-email-token'`).
		Scan(&tokenCount); err != nil {
		t.Fatalf("count owned email token: %v", err)
	}
	if tokenCount != 0 {
		t.Fatalf("owned email token count = %d, want 0", tokenCount)
	}
}

func TestPasswordSignupCanonicalizesInternationalizedDomain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)

	const (
		unicodeEmail   = "User@BÜCHER.example"
		canonicalEmail = "user@xn--bcher-kva.example"
		password       = "correct horse battery staple"
	)
	start, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: unicodeEmail},
	)
	if err != nil {
		t.Fatalf("start signup: %v", err)
	}
	if start.Email.NormalizedEmail != canonicalEmail {
		t.Fatalf("normalized email = %q, want %q", start.Email.NormalizedEmail, canonicalEmail)
	}
	passwordHash, err := authn.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	completed, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{Token: start.Token, PasswordHash: passwordHash},
	)
	if err != nil || !completed.Verified {
		t.Fatalf("complete signup: record=%+v err=%v", completed, err)
	}
	user, err := authenticatePasswordForTest(t, ctx, store, "user@bu\u0308cher.example", password)
	if err != nil {
		t.Fatalf("authenticate with decomposed spelling: %v", err)
	}
	if user.ID != completed.User.ID {
		t.Fatalf("authenticated user = %s, want %s", user.ID, completed.User.ID)
	}
	replay, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: canonicalEmail},
	)
	if err != nil {
		t.Fatalf("repeat signup with punycode spelling: %v", err)
	}
	if !replay.EmailAlreadyVerified {
		t.Fatalf("repeat signup = %+v, want existing verified identity", replay)
	}
}

func TestPasswordSignupConcurrentVerificationFirstCommitWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)

	first, err := store.Identity().StartPasswordSignup(ctx, identitystore.PasswordSignupStartInput{Email: "race-signup@bu\u0308cher.example"})
	if err != nil {
		t.Fatalf("start first signup: %v", err)
	}
	second, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: "race-signup@xn--bcher-kva.example"},
	)
	if err != nil {
		t.Fatalf("start second signup: %v", err)
	}
	firstHash, err := authn.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash first password: %v", err)
	}
	secondHash, err := authn.HashPassword("another correct horse staple")
	if err != nil {
		t.Fatalf("hash second password: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin token lock tx: %v", err)
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT id FROM user_auth_tokens WHERE token_hash = ANY($1) FOR UPDATE`,
		[]string{identitystore.HashBearerToken(first.Token), identitystore.HashBearerToken(second.Token)},
	); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("lock auth tokens: %v", err)
	}
	type signupResult struct {
		name   string
		record identitystore.CompletePasswordSignupRecord
		err    error
	}
	results := make(chan signupResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		record, err := store.Identity().CompletePasswordSignup(
			ctx,
			identitystore.CompletePasswordSignupInput{
				Token:        first.Token,
				PasswordHash: firstHash,
				DisplayName:  "First",
			},
		)
		results <- signupResult{name: "first", record: record, err: err}
	}()
	go func() {
		defer wg.Done()
		record, err := store.Identity().CompletePasswordSignup(
			ctx,
			identitystore.CompletePasswordSignupInput{
				Token:        second.Token,
				PasswordHash: secondHash,
				DisplayName:  "Second",
			},
		)
		results <- signupResult{name: "second", record: record, err: err}
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "GetActiveUserAuthTokenByHashForUpdate", 2)
	select {
	case result := <-results:
		t.Fatalf("signup completion finished while auth tokens were locked: %+v", result)
	default:
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit token lock tx: %v", err)
	}
	wg.Wait()
	close(results)

	var verified, losing int
	var winningPassword string
	for result := range results {
		if result.err != nil {
			t.Fatalf("%s completion error: %v", result.name, result.err)
		}
		if result.record.Verified {
			verified++
			if result.name == "first" {
				winningPassword = "correct horse battery staple"
			} else {
				winningPassword = "another correct horse staple"
			}
			continue
		}
		losing++
		if result.record.User.ID != NilID {
			t.Fatalf("%s losing completion returned user: %+v", result.name, result.record)
		}
	}
	if verified != 1 || losing != 1 {
		t.Fatalf("verified=%d losing=%d, want one of each", verified, losing)
	}
	if _, err := authenticatePasswordForTest(
		t,
		ctx,
		store,
		"race-signup@bücher.example",
		winningPassword,
	); err != nil {
		t.Fatalf("authenticate winning password: %v", err)
	}
}

func TestPasswordResetConsumesTokenAndRevokesSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	start, err := store.Identity().StartPasswordSignup(ctx, identitystore.PasswordSignupStartInput{Email: "reset@example.com"})
	if err != nil {
		t.Fatalf("start signup: %v", err)
	}
	hash, err := authn.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	completed, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{Token: start.Token, PasswordHash: hash, DisplayName: "Reset"},
	)
	if err != nil || !completed.Verified {
		t.Fatalf("complete signup: record=%+v err=%v", completed, err)
	}
	if _, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    completed.User.ID,
			Token:     "reset-session",
			CSRFToken: "reset-csrf",
			TTL:       time.Hour,
		},
	); err != nil {
		t.Fatalf("create browser session: %v", err)
	}

	reset, err := store.Identity().StartPasswordReset(
		ctx,
		identitystore.PasswordResetStartInput{Email: "reset@example.com"},
	)
	if err != nil {
		t.Fatalf("start reset: %v", err)
	}
	if !reset.Found || reset.Token == "" {
		t.Fatalf("expected reset token: %+v", reset)
	}
	newHash, err := authn.HashPassword("new correct horse staple")
	if err != nil {
		t.Fatalf("hash new password: %v", err)
	}
	user, err := store.Identity().CompletePasswordReset(ctx, identitystore.CompletePasswordResetInput{
		Token:            reset.Token,
		PasswordHash:     newHash,
		SessionToken:     "reset-new-session",
		SessionCSRFToken: "reset-new-csrf",
		SessionTTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("complete reset: %v", err)
	}
	if user.ID != completed.User.ID {
		t.Fatalf("reset user=%s want %s", user.ID, completed.User.ID)
	}
	if _, err := store.Identity().CompletePasswordReset(
		ctx,
		identitystore.CompletePasswordResetInput{Token: reset.Token, PasswordHash: newHash},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("reused reset token error=%v, want unauthorized", err)
	}
	if _, _, err := store.Identity().AuthenticateBrowserSession(
		ctx,
		"reset-session",
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("old reset session error=%v, want unauthorized", err)
	}
	principal, _, err := store.Identity().AuthenticateBrowserSession(ctx, "reset-new-session")
	if err != nil {
		t.Fatalf("new reset session: %v", err)
	}
	if principal.ID != completed.User.ID || principal.Type != identitystore.PrincipalTypeUser || isNilID(principal.BrowserSessionID) {
		t.Fatalf("new reset principal=%+v", principal)
	}
	if _, err := authenticatePasswordForTest(
		t,
		ctx,
		store,
		"reset@example.com",
		"new correct horse staple",
	); err != nil {
		t.Fatalf("authenticate reset password: %v", err)
	}
}

func TestConcurrentPasswordResetRequestsLeaveOneUsableToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	user := createPasswordResetUserForTest(
		t,
		ctx,
		store,
		"concurrent-reset-requests@example.com",
		"correct horse battery staple",
	)

	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin password reset request lock: %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	if _, err := dbsqlc.New(lockTx).LockUserForUpdate(
		ctx,
		dbsqlc.LockUserForUpdateParams{ID: user.ID},
	); err != nil {
		t.Fatalf("lock password reset request user: %v", err)
	}
	const applicationName = "concurrent-password-reset-requests"
	writerConfig := pool.Config()
	writerConfig.MaxConns = 2
	writerConfig.ConnConfig.RuntimeParams["application_name"] = applicationName
	writerPool, err := pgxpool.NewWithConfig(ctx, writerConfig)
	if err != nil {
		t.Fatalf("open password reset request writers: %v", err)
	}
	t.Cleanup(writerPool.Close)
	writerStore := newIntegrationStore(writerPool)
	type resetResult struct {
		record identitystore.PasswordResetStartRecord
		err    error
	}
	results := make(chan resetResult, 2)
	for range 2 {
		go func() {
			record, resetErr := writerStore.Identity().StartPasswordReset(
				context.Background(),
				identitystore.PasswordResetStartInput{Email: "concurrent-reset-requests@example.com"},
			)
			results <- resetResult{record: record, err: resetErr}
		}()
	}
	integrationdb.WaitForApplicationNamedLockWaiters(t, ctx, pool, applicationName, "LockUserForUpdate", 2)
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release password reset request user: %v", err)
	}

	records := make([]identitystore.PasswordResetStartRecord, 0, 2)
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent password reset request: %v", result.err)
		}
		if !result.record.Found || result.record.Token == "" {
			t.Fatalf("concurrent password reset record = %+v", result.record)
		}
		records = append(records, result.record)
	}
	var activeCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM user_auth_tokens
WHERE user_id = $1 AND purpose = 'password_reset' AND consumed_at IS NULL`,
		user.ID,
	).Scan(&activeCount); err != nil {
		t.Fatalf("count active password reset tokens: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active password reset token count = %d, want 1", activeCount)
	}
	usable := 0
	for _, record := range records {
		if _, err := store.Identity().ActiveAuthTokenEmail(
			ctx,
			record.Token,
			identitystore.UserAuthTokenPurposePasswordReset,
		); err == nil {
			usable++
		} else if !errors.Is(err, storeerr.ErrUnauthorized) {
			t.Fatalf("validate concurrent password reset token: %v", err)
		}
	}
	if usable != 1 {
		t.Fatalf("usable concurrent password reset tokens = %d, want 1", usable)
	}
}

func TestPasswordResetRequestAndCompletionSerialize(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	const (
		email       = "reset-request-completion-race@example.com"
		oldPassword = "correct horse battery staple"
		newPassword = "new correct horse battery staple"
	)
	user := createPasswordResetUserForTest(t, ctx, store, email, oldPassword)
	initialReset, err := store.Identity().StartPasswordReset(ctx, identitystore.PasswordResetStartInput{Email: email})
	if err != nil || !initialReset.Found {
		t.Fatalf("start initial password reset: record=%+v err=%v", initialReset, err)
	}
	newHash, err := authn.HashPassword(newPassword)
	if err != nil {
		t.Fatalf("hash reset password: %v", err)
	}

	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin password reset race lock: %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	if _, err := dbsqlc.New(lockTx).LockUserForUpdate(
		ctx,
		dbsqlc.LockUserForUpdateParams{ID: user.ID},
	); err != nil {
		t.Fatalf("lock password reset race user: %v", err)
	}
	const applicationName = "password-reset-request-completion-race"
	writerConfig := pool.Config()
	writerConfig.MaxConns = 2
	writerConfig.ConnConfig.RuntimeParams["application_name"] = applicationName
	writerPool, err := pgxpool.NewWithConfig(ctx, writerConfig)
	if err != nil {
		t.Fatalf("open password reset race writers: %v", err)
	}
	t.Cleanup(writerPool.Close)
	writerStore := newIntegrationStore(writerPool)
	completionDone := make(chan error, 1)
	go func() {
		_, completeErr := writerStore.Identity().CompletePasswordReset(
			context.Background(),
			identitystore.CompletePasswordResetInput{
				Token:            initialReset.Token,
				PasswordHash:     newHash,
				SessionToken:     "reset-race-session",
				SessionCSRFToken: "reset-race-csrf",
				SessionTTL:       time.Hour,
			},
		)
		completionDone <- completeErr
	}()
	type requestResult struct {
		record identitystore.PasswordResetStartRecord
		err    error
	}
	requestDone := make(chan requestResult, 1)
	go func() {
		record, requestErr := writerStore.Identity().StartPasswordReset(
			context.Background(),
			identitystore.PasswordResetStartInput{Email: email},
		)
		requestDone <- requestResult{record: record, err: requestErr}
	}()
	integrationdb.WaitForApplicationNamedLockWaiters(t, ctx, pool, applicationName, "LockUserForUpdate", 2)
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release password reset race user: %v", err)
	}

	completionErr := <-completionDone
	if completionErr != nil && !errors.Is(completionErr, storeerr.ErrUnauthorized) {
		t.Fatalf("complete password reset during request race: %v", completionErr)
	}
	request := <-requestDone
	if request.err != nil {
		t.Fatalf("request password reset during completion race: %v", request.err)
	}
	if !request.record.Found || request.record.Token == "" {
		t.Fatalf("password reset race request = %+v", request.record)
	}
	if _, err := store.Identity().ActiveAuthTokenEmail(
		ctx,
		request.record.Token,
		identitystore.UserAuthTokenPurposePasswordReset,
	); err != nil {
		t.Fatalf("new password reset token after request/completion race: %v", err)
	}
	if completionErr == nil {
		if _, err := authenticatePasswordForTest(t, ctx, store, email, newPassword); err != nil {
			t.Fatalf("authenticate completed reset password: %v", err)
		}
	} else if _, err := authenticatePasswordForTest(t, ctx, store, email, oldPassword); err != nil {
		t.Fatalf("authenticate unchanged password after losing reset completion: %v", err)
	}
}

func TestPasswordResetRejectsExpiryAfterTokenLockWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	start, err := store.Identity().StartPasswordSignup(ctx, identitystore.PasswordSignupStartInput{Email: "reset-lock-expiry@example.com"})
	if err != nil {
		t.Fatalf("start signup: %v", err)
	}
	oldHash, err := authn.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash old password: %v", err)
	}
	if _, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{Token: start.Token, PasswordHash: oldHash},
	); err != nil {
		t.Fatalf("complete signup: %v", err)
	}
	reset, err := store.Identity().StartPasswordReset(ctx, identitystore.PasswordResetStartInput{Email: "reset-lock-expiry@example.com"})
	if err != nil || !reset.Found {
		t.Fatalf("start password reset: record=%+v err=%v", reset, err)
	}
	newHash, err := authn.HashPassword("new correct horse battery staple")
	if err != nil {
		t.Fatalf("hash new password: %v", err)
	}
	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin reset token lock: %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	var blockingPID int32
	if err := lockTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get reset token lock backend: %v", err)
	}
	if _, err := lockTx.Exec(
		ctx,
		`SELECT id FROM user_auth_tokens WHERE token_hash = $1 FOR UPDATE`,
		identitystore.HashBearerToken(reset.Token),
	); err != nil {
		t.Fatalf("lock reset token: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := store.Identity().CompletePasswordReset(context.Background(), identitystore.CompletePasswordResetInput{
			Token:            reset.Token,
			PasswordHash:     newHash,
			SessionToken:     "reset-lock-expiry-session",
			SessionCSRFToken: "reset-lock-expiry-csrf",
			SessionTTL:       time.Hour,
		})
		done <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "FOR UPDATE OF token", blockingPID)
	if _, err := lockTx.Exec(
		ctx,
		`UPDATE user_auth_tokens SET expires_at = statement_timestamp() - interval '1 millisecond' WHERE token_hash = $1`,
		identitystore.HashBearerToken(reset.Token),
	); err != nil {
		t.Fatalf("expire locked reset token: %v", err)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release reset token lock: %v", err)
	}
	if err := <-done; !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("reset after lock-wait expiry error = %v, want ErrUnauthorized", err)
	}
	if _, err := authenticatePasswordForTest(
		t,
		ctx,
		store,
		"reset-lock-expiry@example.com",
		"correct horse battery staple",
	); err != nil {
		t.Fatalf("authenticate unchanged password: %v", err)
	}
	if _, err := authenticatePasswordForTest(
		t,
		ctx,
		store,
		"reset-lock-expiry@example.com",
		"new correct horse battery staple",
	); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("authenticate rolled-back password error = %v, want ErrUnauthorized", err)
	}
	if _, _, err := store.Identity().AuthenticateBrowserSession(
		ctx,
		"reset-lock-expiry-session",
	); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("rolled-back reset session error = %v, want ErrUnauthorized", err)
	}
}

func TestPasswordLoginAndResetSerializeSessionCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)

	start, err := store.Identity().StartPasswordSignup(ctx, identitystore.PasswordSignupStartInput{Email: "login-reset-race@example.com"})
	if err != nil {
		t.Fatalf("start signup: %v", err)
	}
	oldHash, err := authn.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash old password: %v", err)
	}
	completed, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{Token: start.Token, PasswordHash: oldHash},
	)
	if err != nil {
		t.Fatalf("complete signup: %v", err)
	}
	reset, err := store.Identity().StartPasswordReset(
		ctx,
		identitystore.PasswordResetStartInput{Email: "login-reset-race@example.com"},
	)
	if err != nil {
		t.Fatalf("start reset: %v", err)
	}
	newHash, err := authn.HashPassword("new correct horse staple")
	if err != nil {
		t.Fatalf("hash new password: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, completed.User.ID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("lock user: %v", err)
	}
	loginCh := make(chan error, 1)
	go func() {
		_, err := store.Identity().AuthenticatePasswordAndCreateSession(ctx, identitystore.PasswordLoginSessionInput{
			Email:            "login-reset-race@example.com",
			Password:         "correct horse battery staple",
			SessionToken:     "race-login-session",
			SessionCSRFToken: "race-login-csrf",
			SessionTTL:       time.Hour,
		})
		loginCh <- err
	}()
	resetCh := make(chan error, 1)
	go func() {
		_, err := store.Identity().CompletePasswordReset(ctx, identitystore.CompletePasswordResetInput{
			Token:            reset.Token,
			PasswordHash:     newHash,
			SessionToken:     "race-reset-session",
			SessionCSRFToken: "race-reset-csrf",
			SessionTTL:       time.Hour,
		})
		resetCh <- err
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockUserForUpdate", 2)
	assertErrorResultBlocked(t, "password login", loginCh)
	assertErrorResultBlocked(t, "password reset", resetCh)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit lock tx: %v", err)
	}
	loginErr := <-loginCh
	if loginErr != nil && !errors.Is(loginErr, storeerr.ErrUnauthorized) {
		t.Fatalf("password login error=%v", loginErr)
	}
	if err := <-resetCh; err != nil {
		t.Fatalf("password reset error=%v", err)
	}
	if _, _, err := store.Identity().AuthenticateBrowserSession(
		ctx,
		"race-login-session",
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("stale login session error=%v, want unauthorized", err)
	}
	if _, _, err := store.Identity().AuthenticateBrowserSession(ctx, "race-reset-session"); err != nil {
		t.Fatalf("reset session auth: %v", err)
	}
	if _, err := authenticatePasswordForTest(
		t,
		ctx,
		store,
		"login-reset-race@example.com",
		"correct horse battery staple",
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("old password auth error=%v, want unauthorized", err)
	}
	if _, err := authenticatePasswordForTest(
		t,
		ctx,
		store,
		"login-reset-race@example.com",
		"new correct horse staple",
	); err != nil {
		t.Fatalf("new password auth: %v", err)
	}
}

func TestPasswordChangeAndResetSerializePasswordMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)

	start, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: "change-reset-race@example.com"},
	)
	if err != nil {
		t.Fatalf("start signup: %v", err)
	}
	oldHash, err := authn.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash old password: %v", err)
	}
	completed, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{Token: start.Token, PasswordHash: oldHash},
	)
	if err != nil {
		t.Fatalf("complete signup: %v", err)
	}
	reset, err := store.Identity().StartPasswordReset(
		ctx,
		identitystore.PasswordResetStartInput{Email: "change-reset-race@example.com"},
	)
	if err != nil {
		t.Fatalf("start reset: %v", err)
	}
	changeHash, err := authn.HashPassword("changed correct horse staple")
	if err != nil {
		t.Fatalf("hash changed password: %v", err)
	}
	resetHash, err := authn.HashPassword("reset correct horse staple")
	if err != nil {
		t.Fatalf("hash reset password: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, completed.User.ID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("lock user: %v", err)
	}
	changeCh := make(chan error, 1)
	go func() {
		_, err := store.Identity().ChangePassword(ctx, identitystore.ChangePasswordInput{
			UserID:           completed.User.ID,
			CurrentPassword:  "correct horse battery staple",
			PasswordHash:     changeHash,
			SessionToken:     "race-change-session",
			SessionCSRFToken: "race-change-csrf",
			SessionTTL:       time.Hour,
		})
		changeCh <- err
	}()
	resetCh := make(chan error, 1)
	go func() {
		_, err := store.Identity().CompletePasswordReset(ctx, identitystore.CompletePasswordResetInput{
			Token:            reset.Token,
			PasswordHash:     resetHash,
			SessionToken:     "race-change-reset-session",
			SessionCSRFToken: "race-change-reset-csrf",
			SessionTTL:       time.Hour,
		})
		resetCh <- err
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockUserForUpdate", 2)
	assertErrorResultBlocked(t, "password change", changeCh)
	assertErrorResultBlocked(t, "password reset", resetCh)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit lock tx: %v", err)
	}
	changeErr := <-changeCh
	if changeErr != nil && !errors.Is(changeErr, storeerr.ErrUnauthorized) {
		t.Fatalf("password change error=%v", changeErr)
	}
	if err := <-resetCh; err != nil {
		t.Fatalf("password reset error=%v", err)
	}
	if _, _, err := store.Identity().AuthenticateBrowserSession(
		ctx,
		"race-change-session",
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("stale change session error=%v, want unauthorized", err)
	}
	if _, _, err := store.Identity().AuthenticateBrowserSession(
		ctx,
		"race-change-reset-session",
	); err != nil {
		t.Fatalf("reset session auth: %v", err)
	}
	if _, err := authenticatePasswordForTest(
		t,
		ctx,
		store,
		"change-reset-race@example.com",
		"changed correct horse staple",
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("changed password auth error=%v, want unauthorized", err)
	}
	if _, err := authenticatePasswordForTest(
		t,
		ctx,
		store,
		"change-reset-race@example.com",
		"reset correct horse staple",
	); err != nil {
		t.Fatalf("reset password auth: %v", err)
	}
}

func TestPasswordAuthTokenLifecycleRejectsExpiredReusedAndWrongPurpose(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	hash, err := authn.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	expiredSignup, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: "expired-verify@example.com"},
	)
	if err != nil {
		t.Fatalf("start expired signup: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE user_auth_tokens
		SET created_at = transaction_timestamp() - interval '2 minutes',
		    expires_at = transaction_timestamp() - interval '1 minute'
		WHERE token_hash = $1`, identitystore.HashBearerToken(expiredSignup.Token)); err != nil {
		t.Fatalf("expire signup token: %v", err)
	}
	if _, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{Token: expiredSignup.Token, PasswordHash: hash},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("expired verification token error=%v, want unauthorized", err)
	}

	signup, err := store.Identity().StartPasswordSignup(ctx, identitystore.PasswordSignupStartInput{Email: "reuse-verify@example.com"})
	if err != nil {
		t.Fatalf("start signup: %v", err)
	}
	if _, err := store.Identity().CompletePasswordReset(
		ctx,
		identitystore.CompletePasswordResetInput{Token: signup.Token, PasswordHash: hash},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("verification token used for reset error=%v, want unauthorized", err)
	}
	completed, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{Token: signup.Token, PasswordHash: hash},
	)
	if err != nil || !completed.Verified {
		t.Fatalf("complete signup: record=%+v err=%v", completed, err)
	}
	if _, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{Token: signup.Token, PasswordHash: hash},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("reused verification token error=%v, want unauthorized", err)
	}

	expiredReset, err := store.Identity().StartPasswordReset(
		ctx,
		identitystore.PasswordResetStartInput{Email: "reuse-verify@example.com"},
	)
	if err != nil {
		t.Fatalf("start expired reset: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE user_auth_tokens
		SET created_at = transaction_timestamp() - interval '2 minutes',
		    expires_at = transaction_timestamp() - interval '1 minute'
		WHERE token_hash = $1`, identitystore.HashBearerToken(expiredReset.Token)); err != nil {
		t.Fatalf("expire reset token: %v", err)
	}
	if !expiredReset.Found {
		t.Fatal("expected reset token for verified password user")
	}
	if _, err := store.Identity().CompletePasswordReset(
		ctx,
		identitystore.CompletePasswordResetInput{Token: expiredReset.Token, PasswordHash: hash},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("expired reset token error=%v, want unauthorized", err)
	}
	reset, err := store.Identity().StartPasswordReset(
		ctx,
		identitystore.PasswordResetStartInput{Email: "reuse-verify@example.com"},
	)
	if err != nil {
		t.Fatalf("start reset: %v", err)
	}
	if _, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{Token: reset.Token, PasswordHash: hash},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("reset token used for verification error=%v, want unauthorized", err)
	}
}

func TestCleanupInactiveAuthStatePurgesOnlyAbandonedSignupState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	hash, err := authn.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	abandoned, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: "abandoned-cleanup@example.com"},
	)
	if err != nil {
		t.Fatalf("start abandoned signup: %v", err)
	}
	active, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: "active-cleanup@example.com"},
	)
	if err != nil {
		t.Fatalf("start active signup: %v", err)
	}
	consumed, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: "consumed-cleanup@example.com"},
	)
	if err != nil {
		t.Fatalf("start consumed signup: %v", err)
	}
	if _, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{Token: consumed.Token, PasswordHash: hash},
	); err != nil {
		t.Fatalf("complete consumed signup: %v", err)
	}
	connector, err := store.Identity().UpsertAuthConnector(
		ctx,
		identitystore.CreateAuthConnectorInput{
			Slug:         "cleanup-oidc",
			Kind:         identitystore.AuthConnectorKindOIDC,
			DisplayName:  "Cleanup OIDC",
			Issuer:       "https://idp.example.com",
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			Enabled:      true,
		},
	)
	if err != nil {
		t.Fatalf("create auth connector: %v", err)
	}
	patProtected, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: "pat-protected-cleanup@example.com"},
	)
	if err != nil {
		t.Fatalf("start pat protected signup: %v", err)
	}
	if _, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: patProtected.User.ID,
			Name:   "cleanup protected",
		},
	); err != nil {
		t.Fatalf("create pat cleanup guard: %v", err)
	}
	identityProtected, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: "identity-protected-cleanup@example.com"},
	)
	if err != nil {
		t.Fatalf("start identity protected signup: %v", err)
	}
	if _, err := store.Identity().CreateUserAuthIdentity(
		ctx,
		identitystore.CreateUserAuthIdentityInput{
			UserID:          identityProtected.User.ID,
			AuthConnectorID: connector.ID,
			Issuer:          "https://idp.example.com",
			Subject:         "cleanup-protected",
			EmailAtLink:     "identity-protected-cleanup@example.com",
			EmailVerified:   true,
		},
	); err != nil {
		t.Fatalf("create auth identity cleanup guard: %v", err)
	}
	sessionProtected, err := store.Identity().StartPasswordSignup(
		ctx,
		identitystore.PasswordSignupStartInput{Email: "session-protected-cleanup@example.com"},
	)
	if err != nil {
		t.Fatalf("start session protected signup: %v", err)
	}
	if _, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    sessionProtected.User.ID,
			Token:     "session-protected-cleanup",
			CSRFToken: "session-protected-cleanup-csrf",
			TTL:       time.Hour,
		},
	); err != nil {
		t.Fatalf("create browser session cleanup guard: %v", err)
	}
	expiredSessionUser := mustCreateIdentityUser(
		t,
		ctx,
		store,
		"expired-session-cleanup@example.com",
		"Expired Session")

	if _, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    expiredSessionUser.ID,
			Token:     "expired-session-cleanup",
			CSRFToken: "expired-session-cleanup-csrf",
			TTL:       time.Hour,
		},
	); err != nil {
		t.Fatalf("create expired browser session: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE browser_sessions
		SET created_at = transaction_timestamp() - interval '2 hours',
		    last_seen_at = transaction_timestamp() - interval '2 hours',
		    expires_at = transaction_timestamp() - interval '1 hour'
		WHERE token_hash = $1`, identitystore.HashBearerToken("expired-session-cleanup")); err != nil {
		t.Fatalf("expire browser session: %v", err)
	}
	revokedSessionUser := mustCreateIdentityUser(
		t,
		ctx,
		store,
		"revoked-session-cleanup@example.com",
		"Revoked Session")

	if _, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    revokedSessionUser.ID,
			Token:     "revoked-session-cleanup",
			CSRFToken: "revoked-session-cleanup-csrf",
			TTL:       time.Hour,
		},
	); err != nil {
		t.Fatalf("create revoked browser session: %v", err)
	}
	if err := store.Identity().RevokeBrowserSession(ctx, "revoked-session-cleanup"); err != nil {
		t.Fatalf("revoke browser session: %v", err)
	}
	referencedSessionUser := mustCreateIdentityUser(
		t,
		ctx,
		store,
		"referenced-session-cleanup@example.com",
		"Referenced Session")

	referencedSession, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    referencedSessionUser.ID,
			Token:     "referenced-session-cleanup",
			CSRFToken: "referenced-session-cleanup-csrf",
			TTL:       time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("create referenced browser session: %v", err)
	}
	referencedDeviceFlow, err := store.Identity().StartDeviceAuthFlow(
		ctx,
		identitystore.StartDeviceAuthFlowInput{
			ClientID:   testDeviceOAuthClientID,
			ClientName: "Referenced",
			TokenName:  "Referenced",
		},
	)
	if err != nil {
		t.Fatalf("start referenced device flow: %v", err)
	}
	if err := store.Identity().ApproveDeviceAuthFlow(
		ctx,
		identitystore.ApproveDeviceAuthFlowInput{
			UserCode:                 referencedDeviceFlow.UserCode,
			UserID:                   referencedSessionUser.ID,
			ApprovedBrowserSessionID: referencedSession.ID,
		},
	); err != nil {
		t.Fatalf("approve referenced device flow: %v", err)
	}
	if err := store.Identity().RevokeBrowserSession(ctx, "referenced-session-cleanup"); err != nil {
		t.Fatalf("revoke referenced browser session: %v", err)
	}
	expiredDeviceFlow, err := store.Identity().StartDeviceAuthFlow(
		ctx,
		identitystore.StartDeviceAuthFlowInput{
			ClientID:   testDeviceOAuthClientID,
			ClientName: "Expired",
			TokenName:  "Expired",
		},
	)
	if err != nil {
		t.Fatalf("start expired device flow: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE auth_device_flows
		SET created_at = transaction_timestamp() - interval '2 minutes',
		    expires_at = transaction_timestamp() - interval '1 minute'
		WHERE device_code_hash = $1`, identitystore.HashBearerToken(expiredDeviceFlow.DeviceCode)); err != nil {
		t.Fatalf("expire device flow: %v", err)
	}
	activeDeviceFlow, err := store.Identity().StartDeviceAuthFlow(
		ctx,
		identitystore.StartDeviceAuthFlowInput{
			ClientID:   testDeviceOAuthClientID,
			ClientName: "Active",
			TokenName:  "Active",
		},
	)
	if err != nil {
		t.Fatalf("start active device flow: %v", err)
	}
	deniedDeviceFlow, err := store.Identity().StartDeviceAuthFlow(
		ctx,
		identitystore.StartDeviceAuthFlowInput{
			ClientID:   testDeviceOAuthClientID,
			ClientName: "Denied",
			TokenName:  "Denied",
		},
	)
	if err != nil {
		t.Fatalf("start denied device flow: %v", err)
	}
	if err := store.Identity().DenyDeviceAuthFlow(
		ctx,
		identitystore.DenyDeviceAuthFlowInput{UserCode: deniedDeviceFlow.UserCode},
	); err != nil {
		t.Fatalf("deny device flow: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE users
		SET created_at = transaction_timestamp() - interval '8 days'
		WHERE id IN ($1, $2, $3, $4)`,
		abandoned.User.ID,
		patProtected.User.ID,
		identityProtected.User.ID,
		sessionProtected.User.ID,
	); err != nil {
		t.Fatalf("age inactive auth users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE user_auth_tokens
		SET created_at = transaction_timestamp() - interval '2 days',
		    expires_at = transaction_timestamp() - interval '1 day'
		WHERE user_id IN ($1, $2, $3, $4)
		  AND consumed_at IS NULL`,
		abandoned.User.ID,
		patProtected.User.ID,
		identityProtected.User.ID,
		sessionProtected.User.ID,
	); err != nil {
		t.Fatalf("age inactive auth state: %v", err)
	}

	result, err := store.Identity().CleanupInactiveAuthState(ctx)
	if err != nil {
		t.Fatalf("cleanup inactive auth state: %v", err)
	}
	if result.DeletedInactiveTokens != 5 || result.DeletedBrowserSessions != 2 || result.DeletedAbandonedUsers != 1 ||
		result.DeletedDeviceFlows != 1 {
		t.Fatalf(
			"cleanup result = %+v, want 5 inactive tokens, 2 browser sessions, 1 abandoned user, and 1 device flow",
			result,
		)
	}

	assertUserRowCount(t, ctx, pool, abandoned.User.ID, 0)
	assertUserRowCount(t, ctx, pool, active.User.ID, 1)
	assertUserRowCount(t, ctx, pool, consumed.User.ID, 1)
	assertUserRowCount(t, ctx, pool, patProtected.User.ID, 1)
	assertUserRowCount(t, ctx, pool, identityProtected.User.ID, 1)
	assertUserRowCount(t, ctx, pool, sessionProtected.User.ID, 1)
	assertAuthTokenRowCount(t, ctx, pool, abandoned.Token, 0)
	assertAuthTokenRowCount(t, ctx, pool, active.Token, 1)
	assertAuthTokenRowCount(t, ctx, pool, consumed.Token, 0)
	assertBrowserSessionRowCountByToken(t, ctx, pool, "session-protected-cleanup", 1)
	assertBrowserSessionRowCountByToken(t, ctx, pool, "expired-session-cleanup", 0)
	assertBrowserSessionRowCountByToken(t, ctx, pool, "revoked-session-cleanup", 0)
	assertBrowserSessionRowCountByToken(t, ctx, pool, "referenced-session-cleanup", 1)
	assertAuthDeviceFlowRowCountByDeviceCode(t, ctx, pool, expiredDeviceFlow.DeviceCode, 0)
	assertAuthDeviceFlowRowCountByDeviceCode(t, ctx, pool, activeDeviceFlow.DeviceCode, 1)
	assertAuthDeviceFlowRowCountByDeviceCode(t, ctx, pool, deniedDeviceFlow.DeviceCode, 1)
	assertAuthDeviceFlowRowCountByDeviceCode(t, ctx, pool, referencedDeviceFlow.DeviceCode, 1)
}

func TestCompromiseRevocationBlocksStalePersonalAccessTokenCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)

	user := mustCreateProjectOperatorUser(t, ctx, store, "compromise@example.com", "Compromise")
	machine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Compromise Machine",
			IdempotencyKey: "idem-compromise-machine",
		},
	)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	session, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    user.ID,
			Token:     "compromise-session",
			CSRFToken: "compromise-csrf",
			TTL:       time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	idleSession, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    user.ID,
			Token:     "compromise-idle-session",
			CSRFToken: "compromise-idle-csrf",
			TTL:       time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("create idle browser session: %v", err)
	}
	pat, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: user.ID, Name: "compromise"},
	)
	if err != nil {
		t.Fatalf("create pat: %v", err)
	}
	sessionPrincipal := identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: user.ID, BrowserSessionID: session.ID}
	idleSessionPrincipal := identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: user.ID, BrowserSessionID: idleSession.ID}
	patPrincipal := identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: user.ID, PersonalAccessTokenID: pat.Record.ID}
	unboundUserPrincipal := identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: user.ID}
	nonUserPrincipal := identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeMachineDaemon, ID: machine.ID}
	if _, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID:         user.ID,
			ActorPrincipal: sessionPrincipal,
			Name:           "active-session-created",
		},
	); err != nil {
		t.Fatalf("create pat with active session: %v", err)
	}
	createdDaemonToken, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     testOrgID,
			MachineID: machine.ID,
			Name:      "active",
		},
	)
	if err != nil {
		t.Fatalf("create daemon token with active session: %v", err)
	}
	if _, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID:         user.ID,
			ActorPrincipal: patPrincipal,
			Name:           "pat-created-pat",
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("active PAT principal PAT create error=%v, want unauthorized", err)
	}
	if _, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID:         user.ID,
			ActorPrincipal: unboundUserPrincipal,
			Name:           "unbound-user-pat",
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("unbound user principal PAT create error=%v, want unauthorized", err)
	}
	if _, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID:         user.ID,
			ActorPrincipal: nonUserPrincipal,
			Name:           "non-user-pat",
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("non-user principal PAT create error=%v, want unauthorized", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE browser_sessions
		SET created_at = transaction_timestamp() - interval '8 days',
		    last_seen_at = transaction_timestamp() - interval '8 days'
		WHERE id = $1
	`, idleSession.ID); err != nil {
		t.Fatalf("age idle browser session: %v", err)
	}
	if _, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID:         user.ID,
			ActorPrincipal: idleSessionPrincipal,
			Name:           "idle-session-pat",
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("idle browser session PAT create error=%v, want unauthorized", err)
	}

	if err := store.AccountSecurity().RevokeUserTokensForCompromiseWithPasswordIfPresent(
		ctx,
		user.ID,
		"",
	); err != nil {
		t.Fatalf("revoke compromised user tokens: %v", err)
	}
	if _, err := store.Execution().AuthenticateMachineDaemonToken(
		ctx,
		createdDaemonToken.Token,
	); err != nil {
		t.Fatalf("daemon token after compromise: %v", err)
	}
	if _, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID:         user.ID,
			ActorPrincipal: sessionPrincipal,
			Name:           "stale-session-pat",
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("stale browser session PAT create error=%v, want unauthorized", err)
	}
	if _, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID:         user.ID,
			ActorPrincipal: patPrincipal,
			Name:           "stale-pat-pat",
		},
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("stale PAT create error=%v, want unauthorized", err)
	}
}

func TestMachineDaemonTokenRequiresEligibleMachineLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 6, 24, 12, 30, 0, 0, time.UTC)

	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			machinePoolInputWithDefaultMachineForTest(
				executionstore.CreateMachinePoolInput{
					OrgID:            testOrgID,
					Name:             "Lifecycle Pool",
					Provider:         "test.provider",
					MaxTotalMachines: 1,
				},
				defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`)},
			),
		))

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	var machineID ID
	if err := pool.QueryRow(ctx, `
			INSERT INTO machines(org_id, machine_pool_id, source_kind, display_name, provider, lifecycle_state, lifecycle_changed_at, cpu, memory_mb, cwd, env, secret_env, provider_options, metadata, next_reconcile_after, provision_attempts, created_at, updated_at)
			VALUES ($1, $2, 'pool', 'Lifecycle Machine', $3, 'provisioning', $4, 1, 1024, '', '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, $4, 1, $4, $4)
			RETURNING id
		`, testOrgID, machinePool.ID, machinePool.Provider, now).Scan(&machineID); err != nil {
		t.Fatalf("insert pool machine: %v", err)
	}
	providerProvisioning, err := store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            testOrgID,
			MachineID:        machineID,
			ProvisionAttempt: 1,
			TokenName:        "daemon",
		},
	)
	if err != nil {
		t.Fatalf("create daemon token: %v", err)
	}
	recordPoolMachineProvisioningResourceForTest(
		t,
		ctx,
		store,
		machineID,
		1,
		"lifecycle-machine-resource",
	)
	if _, err := store.Execution().AuthenticateMachineDaemonToken(
		ctx,
		providerProvisioning.DaemonToken.Token,
	); err != nil {
		t.Fatalf("authenticate provisioning machine daemon token: %v", err)
	}
	installationID, err := store.Identity().GetInstallationID(ctx)
	if err != nil {
		t.Fatalf("get installation id: %v", err)
	}
	bootstrap, err := store.Execution().BootstrapMachineDaemon(ctx, executionstore.MachineDaemonBootstrapInput{
		OrgID:         testOrgID,
		MachineID:     machineID,
		DaemonTokenID: providerProvisioning.DaemonToken.Record.ID,
	})
	if err != nil {
		t.Fatalf("bootstrap provisioning machine daemon: %v", err)
	}
	if installationID != bootstrap.InstallationID {
		t.Fatalf("bootstrap installation id = %s, want %s", bootstrap.InstallationID, installationID)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET lifecycle_state = 'provision_failed', next_reconcile_after = $1, updated_at = $1 WHERE id = $2`,
		now.Add(2*time.Second),
		machineID,
	); err != nil {
		t.Fatalf("mark machine provision failed: %v", err)
	}
	if _, err := store.Execution().AuthenticateMachineDaemonToken(
		ctx,
		providerProvisioning.DaemonToken.Token,
	); err != nil {
		t.Fatalf("authenticate provision-failed machine daemon token: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET lifecycle_state = 'active', next_reconcile_after = NULL, updated_at = $1 WHERE id = $2`,
		now.Add(4*time.Second),
		machineID,
	); err != nil {
		t.Fatalf("mark machine active: %v", err)
	}
	if _, err := store.Execution().AuthenticateMachineDaemonToken(
		ctx,
		providerProvisioning.DaemonToken.Token,
	); err != nil {
		t.Fatalf("authenticate active daemon token: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET lifecycle_state = 'deleting', next_reconcile_after = $1, updated_at = $1 WHERE id = $2`,
		now.Add(6*time.Second),
		machineID,
	); err != nil {
		t.Fatalf("mark machine deleting: %v", err)
	}
	if _, err := store.Execution().AuthenticateMachineDaemonToken(
		ctx,
		providerProvisioning.DaemonToken.Token,
	); !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		t.Fatalf("inactive machine daemon auth error=%v, want unauthorized", err)
	}
}

type tokenCreationRaceResult struct {
	token string
	err   error
}

func TestCompromiseRevocationSerializesConcurrentTokenCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)

	user := mustCreateProjectOperatorUser(t, ctx, store, "compromise-race@example.com", "Compromise Race")
	session, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    user.ID,
			Token:     "compromise-race-session",
			CSRFToken: "compromise-race-csrf",
			TTL:       time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	principal := identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: user.ID, BrowserSessionID: session.ID}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, user.ID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("lock user: %v", err)
	}
	patCh := make(chan tokenCreationRaceResult, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		created, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
			ctx,
			identitystore.CreatePersonalAccessTokenInput{
				UserID:         user.ID,
				ActorPrincipal: principal,
				Name:           "race pat",
			},
		)
		if err != nil {
			patCh <- tokenCreationRaceResult{err: err}
			return
		}
		patCh <- tokenCreationRaceResult{token: created.Token}
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockUserForUpdate", 1)
	assertRaceResultBlocked(t, "PAT creation", patCh)
	revokeCh := make(chan error, 1)
	go func() {
		revokeCh <- store.AccountSecurity().RevokeUserTokensForCompromiseWithPasswordIfPresent(ctx, user.ID, "")
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockUserForUpdate", 2)
	assertRaceResultBlocked(t, "PAT creation", patCh)
	assertErrorResultBlocked(t, "compromise revocation", revokeCh)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit lock tx: %v", err)
	}
	wg.Wait()
	patResult := <-patCh
	if patResult.err != nil && !errors.Is(patResult.err, storeerr.ErrUnauthorized) {
		t.Fatalf("concurrent PAT creation: %v", patResult.err)
	}
	if err := <-revokeCh; err != nil {
		t.Fatalf("concurrent revoke: %v", err)
	}
	if patResult.err == nil {
		if _, err := store.Identity().AuthenticatePersonalAccessToken(
			ctx,
			patResult.token,
		); !errors.Is(
			err,
			storeerr.ErrUnauthorized,
		) {
			t.Fatalf("concurrent PAT after revoke error=%v, want unauthorized", err)
		}
	}
}

func mustCreateIdentityUser(
	t *testing.T,
	ctx context.Context,
	store *Store,
	email, displayName string,
) identitystore.UserRecord {
	t.Helper()
	user, err := store.CreateVerifiedUser(ctx, CreateVerifiedUserInput{Email: email, DisplayName: displayName})
	if err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
	return user
}

func resolveAuthIdentitySessionForTest(
	ctx context.Context,
	store *Store,
	input identitystore.ResolveAuthIdentityInput,
) (identitystore.UserRecord, error) {
	sessionToken, err := randomTokenPart(16)
	if err != nil {
		return identitystore.UserRecord{}, err
	}
	csrfToken, err := randomTokenPart(16)
	if err != nil {
		return identitystore.UserRecord{}, err
	}
	return store.Identity().ResolveAuthIdentityUserAndCreateSession(ctx, identitystore.ResolveAuthIdentitySessionInput{
		ResolveAuthIdentityInput: input,
		SessionToken:             "auth-session-" + sessionToken,
		SessionCSRFToken:         "auth-csrf-" + csrfToken,
		SessionTTL:               time.Hour,
	})
}

func createPasswordResetUserForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	email, password string,
) identitystore.UserRecord {
	t.Helper()
	start, err := store.Identity().StartPasswordSignup(ctx, identitystore.PasswordSignupStartInput{Email: email})
	if err != nil {
		t.Fatalf("start password user signup: %v", err)
	}
	passwordHash, err := authn.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password user password: %v", err)
	}
	completed, err := store.Identity().CompletePasswordSignup(
		ctx,
		identitystore.CompletePasswordSignupInput{Token: start.Token, PasswordHash: passwordHash},
	)
	if err != nil || !completed.Verified {
		t.Fatalf("complete password user signup: record=%+v err=%v", completed, err)
	}
	return completed.User
}

func authenticatePasswordForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	email, password string,
) (identitystore.UserRecord, error) {
	t.Helper()
	sessionToken, err := randomTokenPart(16)
	if err != nil {
		t.Fatalf("generate test session token: %v", err)
	}
	csrfToken, err := randomTokenPart(16)
	if err != nil {
		t.Fatalf("generate test csrf token: %v", err)
	}
	return store.Identity().AuthenticatePasswordAndCreateSession(ctx, identitystore.PasswordLoginSessionInput{
		Email:            email,
		Password:         password,
		SessionToken:     sessionToken,
		SessionCSRFToken: csrfToken,
		SessionTTL:       time.Hour,
	})
}

func assertUserRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID ID, want int64) {
	t.Helper()
	var got int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = $1`, userID).Scan(&got); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if got != want {
		t.Fatalf("user row count for %s = %d, want %d", userID, got, want)
	}
}

func assertAuthTokenRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, token string, want int64) {
	t.Helper()
	var got int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_auth_tokens WHERE token_hash = $1`, identitystore.HashBearerToken(token)).
		Scan(&got); err != nil {
		t.Fatalf("count auth tokens: %v", err)
	}
	if got != want {
		t.Fatalf("auth token row count = %d, want %d", got, want)
	}
}

func assertBrowserSessionRowCountByToken(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	token string,
	want int64,
) {
	t.Helper()
	var got int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM browser_sessions WHERE token_hash = $1`, identitystore.HashBearerToken(token)).
		Scan(&got); err != nil {
		t.Fatalf("count browser sessions: %v", err)
	}
	if got != want {
		t.Fatalf("browser session row count = %d, want %d", got, want)
	}
}

func assertAuthDeviceFlowRowCountByDeviceCode(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	deviceCode string,
	want int64,
) {
	t.Helper()
	var got int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_device_flows WHERE device_code_hash = $1`, identitystore.HashBearerToken(deviceCode)).
		Scan(&got); err != nil {
		t.Fatalf("count device flows: %v", err)
	}
	if got != want {
		t.Fatalf("auth device flow row count = %d, want %d", got, want)
	}
}

func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

func isPgCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

func assertRaceResultBlocked(t *testing.T, name string, ch <-chan tokenCreationRaceResult) {
	t.Helper()
	select {
	case result := <-ch:
		t.Fatalf("%s completed while user row lock was held: %+v", name, result)
	default:
	}
}

func assertErrorResultBlocked(t *testing.T, name string, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("%s completed while user row lock was held: %v", name, err)
	default:
	}
}
