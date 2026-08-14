//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestPublicProjectMachineGrantLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := storage.NewStore(pool)
	project := bootstrapPublicHTTPProject(t, handler, "machine-grant-lifecycle")

	// A project viewer can read the project but cannot manage access.
	viewer, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "machine-grant-viewer@example.com", DisplayName: "Grant Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: project.OrgUUID, UserID: viewer.ID, Role: "member"},
	); err != nil {
		t.Fatalf("add viewer org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     project.OrgUUID,
			ProjectID: project.ProjectUUID,
			UserID:    viewer.ID,
			Role:      "viewer",
		},
	); err != nil {
		t.Fatalf("add viewer project membership: %v", err)
	}
	viewerPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: viewer.ID, Name: "viewer"},
	)
	if err != nil {
		t.Fatalf("create viewer token: %v", err)
	}
	viewerToken := viewerPAT.Token

	// An org member without project access cannot even see the project.
	member, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "machine-grant-member@example.com", DisplayName: "Grant Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: project.OrgUUID, UserID: member.ID, Role: "member"},
	); err != nil {
		t.Fatalf("add member org membership: %v", err)
	}
	memberPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: member.ID, Name: "member"},
	)
	if err != nil {
		t.Fatalf("create member token: %v", err)
	}
	memberToken := memberPAT.Token

	// An outsider belongs to no org.
	outsider, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: "machine-grant-outsider@example.com", DisplayName: "Grant Outsider"})
	if err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	outsiderPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: outsider.ID,
			Name:   "outsider",
		},
	)
	if err != nil {
		t.Fatalf("create outsider token: %v", err)
	}
	outsiderToken := outsiderPAT.Token

	grantsPath := project.ProjectPath + "/machine-grants"

	machine := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		`{"display_name":"grantable machine"}`,
		"idem-machine-grant-machine",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	machineID := machine["id"].(string)

	// Strict request validation rejects unknown fields, malformed ids, and non-object metadata.
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		grantsPath,
		`{"machine_id":"`+machineID+`","bogus":true}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		grantsPath,
		`{"machine_id":"not-a-machine"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		grantsPath,
		`{"machine_id":"`+machineID+`","metadata":null}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		grantsPath,
		`{"machine_id":"`+machineID+`","metadata":[]}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)

	// Only access-managers can create grants; everyone else is forbidden or cannot see the project.
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		grantsPath,
		`{"machine_id":"`+machineID+`"}`,
		"",
		http.StatusForbidden,
		authHeaders(viewerToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		grantsPath,
		`{"machine_id":"`+machineID+`"}`,
		"",
		http.StatusNotFound,
		authHeaders(memberToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		grantsPath,
		`{"machine_id":"`+machineID+`"}`,
		"",
		http.StatusNotFound,
		authHeaders(outsiderToken),
	)

	// A valid-looking but unknown machine is not found.
	missingMachineID := testPublicID(t, publicid.KindMachine, httpTestID("machine-grant-missing-machine"))
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		grantsPath,
		`{"machine_id":"`+missingMachineID+`"}`,
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)

	grantBody := `{"machine_id":"` + machineID + `","description":"primary access","metadata":{"team":"infra"}}`
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		grantsPath,
		grantBody,
		"idem-machine-grant",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	grant := created["grant"].(map[string]any)
	grantID := grant["id"].(string)
	if grant["machine_id"] != machineID || grant["source_kind"] != "explicit" ||
		grant["description"] != "primary access" {
		t.Fatalf("unexpected created grant: %+v", grant)
	}
	if meta, ok := grant["metadata"].(map[string]any); !ok || meta["team"] != "infra" {
		t.Fatalf("unexpected created grant metadata: %+v", grant["metadata"])
	}
	if created["machine"].(map[string]any)["id"] != machineID {
		t.Fatalf("unexpected created grant machine echo: %+v", created["machine"])
	}

	// Idempotent replay returns the same grant; a conflicting body or duplicate active grant is a conflict.
	replayed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		grantsPath,
		grantBody,
		"idem-machine-grant",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if replayed["grant"].(map[string]any)["id"] != grantID {
		t.Fatalf("machine grant replay changed grant: original=%+v replay=%+v", grant, replayed["grant"])
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		grantsPath,
		`{"machine_id":"`+machineID+`","description":"different"}`,
		"idem-machine-grant",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		grantsPath,
		`{"machine_id":"`+machineID+`"}`,
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)

	// A pool-derived machine grant is managed through its parent pool grant and
	// must not appear in the directly revocable individual-grant list.
	var machinePoolID storage.ID
	if err := pool.QueryRow(ctx, `
		INSERT INTO machine_pools(
			org_id, name, management_kind, provider, default_machine_memory_mb,
			provider_auth_env_var, max_total_machines, max_total_memory_mb,
			max_machine_memory_mb, created_at, updated_at
		)
		VALUES ($1, 'machine-grant-list-pool', 'cluster', 'test', 1024,
			'TEST_PROVIDER_TOKEN', 1, 1024, 1024, transaction_timestamp(), transaction_timestamp())
		RETURNING id
	`, project.OrgUUID).Scan(&machinePoolID); err != nil {
		t.Fatalf("insert machine pool fixture: %v", err)
	}
	var poolGrantID storage.ID
	if err := pool.QueryRow(ctx, `
		INSERT INTO project_machine_pool_grants(
			org_id, project_id, machine_pool_id, created_at, updated_at
		)
		VALUES ($1, $2, $3, transaction_timestamp(), transaction_timestamp())
		RETURNING id
	`, project.OrgUUID, project.ProjectUUID, machinePoolID).Scan(&poolGrantID); err != nil {
		t.Fatalf("insert machine pool grant fixture: %v", err)
	}
	var poolMachineID storage.ID
	if err := pool.QueryRow(ctx, `
		INSERT INTO machines(
			org_id, machine_pool_id, source_kind, display_name, provider, lifecycle_state,
			lifecycle_changed_at,
			provider_resource_id, provider_provision_attempted_at,
			memory_mb, cwd, env, secret_env, provider_options, metadata, created_at, updated_at
		)
		VALUES ($1, $2, 'pool', 'pool-derived machine', 'test', 'active',
			statement_timestamp(),
			'pool-derived-resource', statement_timestamp(),
			1024, '', '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb,
			statement_timestamp(), statement_timestamp())
		RETURNING id
	`, project.OrgUUID, machinePoolID).Scan(&poolMachineID); err != nil {
		t.Fatalf("insert pool machine fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_machine_grants(
			org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id,
			metadata, created_at, updated_at
		)
		VALUES ($1, $2, $3, 'pool', $4, '{}'::jsonb,
			transaction_timestamp(), transaction_timestamp())
	`, project.OrgUUID, project.ProjectUUID, poolMachineID, poolGrantID); err != nil {
		t.Fatalf("insert pool-derived machine grant fixture: %v", err)
	}

	// Listing requires access-manage and returns only the explicit grant.
	requestJSONWithHeaders(t, handler, http.MethodGet, grantsPath, "", "", http.StatusForbidden, authHeaders(viewerToken))
	requestJSONWithHeaders(t, handler, http.MethodGet, grantsPath, "", "", http.StatusNotFound, authHeaders(memberToken))
	listed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		grantsPath,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	listedData := listed["data"].([]any)
	if len(listedData) != 1 ||
		listedData[0].(map[string]any)["grant"].(map[string]any)["id"] != grantID {
		t.Fatalf("unexpected machine grant list: %+v", listed)
	}

	// Delete requires access-manage, is a not-found for missing grants, and
	// removes the grant from the public surface entirely.
	deletePath := grantsPath + "/" + grantID
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		deletePath,
		"",
		"",
		http.StatusForbidden,
		authHeaders(viewerToken),
	)
	missingGrantID := testPublicID(t, publicid.KindProjectMachineGrant, httpTestID("machine-grant-missing-grant"))
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		grantsPath+"/"+missingGrantID,
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		deletePath,
		"",
		"",
		http.StatusNoContent,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		deletePath,
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)

	// Deleted grants leave listings.
	afterList := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		grantsPath,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	afterData := afterList["data"].([]any)
	if len(afterData) != 0 {
		t.Fatalf("deleted grant should leave listings: %+v", afterList)
	}
}
