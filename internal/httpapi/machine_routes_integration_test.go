//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestMachineDaemonTokenCannotAdminMachineTokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "machine-admin-auth")

	machine := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		`{"display_name":"Daemon Auth Machine"}`,
		"idem-machine-admin-auth-machine",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	machineID := machine["id"].(string)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+machineID+"/daemon-tokens",
		`{"name":"pat minted daemon token"}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	adminKey := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/api-keys",
		`{"name":"daemon minting key","org_role":"admin"}`,
		"",
		http.StatusCreated,
		project.adminBrowserAuthHeaders(),
	)
	adminKeyToken := adminKey["token"].(string)
	orgKeyMint := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+machineID+"/daemon-tokens",
		`{"name":"org key minted daemon token"}`,
		"",
		http.StatusCreated,
		authHeaders(adminKeyToken),
	)
	if orgKeyMint["token"].(string) == "" {
		t.Fatal("org key minted daemon token missing plaintext token")
	}
	tokenResponse := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+machineID+"/daemon-tokens",
		`{"name":"daemon"}`,
		"",
		http.StatusCreated,
		project.adminBrowserAuthHeaders(),
	)
	token := tokenResponse["token"].(string)
	tokenID := tokenResponse["token_record"].(map[string]any)["id"].(string)

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+machineID+"/daemon-tokens/"+tokenID+"/revoke",
		"",
		"",
		http.StatusForbidden,
		authHeaders(token),
	)
}

func TestConnectBYOMachineCreatesAtomicConnection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "connect-byo-machine")
	path := "/api/v1/orgs/" + project.OrgID + "/machines/connect"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		`{"display_name":"PAT Connection","project_ids":[]}`,
		"",
		http.StatusForbidden,
		authHeaders(project.AdminToken),
	)
	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		`{"display_name":"Connected Through API","project_ids":["`+project.ProjectID+`"]}`,
		"",
		http.StatusCreated,
		project.adminBrowserAuthHeaders(),
	)
	machine := response["machine"].(map[string]any)
	machineID := machine["id"].(string)
	if machine["display_name"] != "Connected Through API" || machine["source_kind"] != "byo" {
		t.Fatalf("unexpected connected machine: %+v", machine)
	}
	token := response["token"].(string)
	if !strings.HasPrefix(token, executionstore.MachineDaemonTokenPlaintextPrefix) {
		t.Fatalf("unexpected daemon token prefix: %q", token)
	}
	tokenRecord := response["token_record"].(map[string]any)
	if tokenRecord["machine_id"] != machineID || tokenRecord["name"] != "web-console" {
		t.Fatalf("unexpected token record: %+v", tokenRecord)
	}
	grants := response["project_grants"].([]any)
	if len(grants) != 1 {
		t.Fatalf("project grants = %d, want 1", len(grants))
	}
	grant := grants[0].(map[string]any)
	if grant["project_id"] != project.ProjectID || grant["machine_id"] != machineID {
		t.Fatalf("unexpected project grant: %+v", grant)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		`{"display_name":"Connected Through API","project_ids":[]}`,
		"",
		http.StatusConflict,
		project.adminBrowserAuthHeaders(),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		`{"display_name":"   ","project_ids":[]}`,
		"",
		http.StatusBadRequest,
		project.adminBrowserAuthHeaders(),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		`{"display_name":"Duplicate Project Connection","project_ids":["`+project.ProjectID+`","`+project.ProjectID+`"]}`,
		"",
		http.StatusBadRequest,
		project.adminBrowserAuthHeaders(),
	)
	missingProjectID, err := publicid.Encode(publicid.KindProject, httpTestID("missing-connect-project"))
	if err != nil {
		t.Fatalf("encode missing project ID: %v", err)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		`{"display_name":"Missing Project Connection","project_ids":["`+missingProjectID+`"]}`,
		"",
		http.StatusNotFound,
		project.adminBrowserAuthHeaders(),
	)
	var missingMachineCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM machines WHERE org_id = $1 AND display_name = 'Missing Project Connection'
`, project.OrgUUID).Scan(&missingMachineCount); err != nil {
		t.Fatalf("count missing-project machines: %v", err)
	}
	if missingMachineCount != 0 {
		t.Fatalf("missing-project machine count = %d, want 0", missingMachineCount)
	}
}

func TestConnectBYOMachineAuthorizesEveryProjectGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "connect-byo-project-authorization")
	projectDeveloper, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email:       "connect-byo-project-developer@example.com",
		DisplayName: "Project Developer",
	})
	if err != nil {
		t.Fatalf("create project developer: %v", err)
	}
	if _, err := project.Store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID:  project.OrgUUID,
		UserID: projectDeveloper.ID,
		Role:   authz.OrgRoleMember,
	}); err != nil {
		t.Fatalf("add project developer org membership: %v", err)
	}
	if _, err := project.Store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID:     project.OrgUUID,
		ProjectID: project.ProjectUUID,
		UserID:    projectDeveloper.ID,
		Role:      authz.ProjectRoleDeveloper,
	}); err != nil {
		t.Fatalf("add project developer membership: %v", err)
	}
	org, err := project.Store.Identity().GetOrg(ctx, project.OrgUUID)
	if err != nil {
		t.Fatalf("get organization: %v", err)
	}
	requestCtx := context.WithValue(ctx, principalContextKey{}, identitystore.PrincipalRecord{
		Type:             identitystore.PrincipalTypeUser,
		ID:               projectDeveloper.ID,
		BrowserSessionID: httpTestID("connect-byo-project-developer-session"),
	})
	requestCtx = withOrgScope(requestCtx, org)
	response, err := (strictOpenAPIServer{server: mustNewServer(t, project.Store)}).ConnectBYOMachine(
		requestCtx,
		openapi.ConnectBYOMachineRequestObject{
			OrgID: project.OrgID,
			Body: &openapi.ConnectBYOMachineRequest{
				DisplayName: "Unauthorized Project Connection",
				ProjectIds:  []openapi.ProjectID{openapi.ProjectID(project.ProjectID)},
			},
		},
	)
	if response != nil {
		t.Fatalf("response = %#v, want nil", response)
	}
	var responseErr apierror.ResponseError
	if !errors.As(err, &responseErr) || responseErr.Status != http.StatusForbidden {
		t.Fatalf("error = %T %v, want forbidden", err, err)
	}
	var machineCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM machines WHERE org_id = $1 AND display_name = 'Unauthorized Project Connection'
`, project.OrgUUID).Scan(&machineCount); err != nil {
		t.Fatalf("count unauthorized machines: %v", err)
	}
	if machineCount != 0 {
		t.Fatalf("unauthorized machine count = %d, want 0", machineCount)
	}
}

func TestCreateMachineRejectsNonObjectMetadata(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "machine-metadata-object")

	cases := []struct {
		name string
		body string
	}{
		{name: "array", body: `{"display_name":"Bad Array Metadata","metadata":[]}`},
		{name: "null", body: `{"display_name":"Bad Null Metadata","metadata":null}`},
	}
	for _, tc := range cases {
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			"/api/v1/orgs/"+project.OrgID+"/machines",
			tc.body,
			"idem-machine-metadata-"+tc.name,
			http.StatusBadRequest,
			authHeaders(project.AdminToken),
		)
	}

	var badRows int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM machines WHERE org_id = $1 AND display_name LIKE 'Bad % Metadata'`,
		project.OrgUUID,
	).
		Scan(&badRows); err != nil {
		t.Fatalf("count invalid machine rows: %v", err)
	}
	if badRows != 0 {
		t.Fatalf("invalid metadata persisted %d machine rows", badRows)
	}

	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		`{"display_name":"Object Metadata","metadata":{"team":"infra","nested":{"ok":true}}}`,
		"idem-machine-metadata-object",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	metadata, ok := created["metadata"].(map[string]any)
	if !ok || metadata["team"] != "infra" {
		t.Fatalf("created machine metadata = %+v", created["metadata"])
	}
}

func TestMachineExecutionDefaultsAPI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "machine-execution-defaults")
	store := integrationStoreForHandler(t, handler)
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     project.OrgUUID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "machine-execution-default",
		Material:  secrets.GenericMaterial{Value: "secret-plaintext"},
		Actor:     httpUserPrincipal(project.AdminUserUUID),
	})
	if err != nil {
		t.Fatalf("create machine execution secret: %v", err)
	}
	secretID := testPublicID(t, publicid.KindSecret, secret.ID)
	createBody := `{"display_name":"Execution Defaults Machine","cwd":"/workspace","env":{"APP":"one","DROP":"old"},"secret_env":{"TOKEN":` + quote(secretID) + `}}`
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		createBody,
		"idem-machine-execution-defaults",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	machineID := created["id"].(string)
	if created["cwd"] != "/workspace" || created["env"].(map[string]any)["APP"] != "one" ||
		created["secret_env"].(map[string]any)["TOKEN"] != secretID {
		t.Fatalf("created machine execution defaults = %+v", created)
	}
	createdJSON, err := json.Marshal(created)
	if err != nil {
		t.Fatalf("marshal created machine response: %v", err)
	}
	if strings.Contains(string(createdJSON), "secret-plaintext") {
		t.Fatalf("machine response exposed secret plaintext: %s", createdJSON)
	}
	if _, err := store.Secrets().DeleteSecret(ctx, secretstore.DeleteSecretInput{
		OrgID:    project.OrgUUID,
		SecretID: secret.ID,
		Actor:    httpUserPrincipal(project.AdminUserUUID),
	}); err != nil {
		t.Fatalf("delete machine execution secret: %v", err)
	}
	cwdOnly := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+machineID,
		`{"cwd":"/changed"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if cwdOnly["cwd"] != "/changed" || cwdOnly["env"].(map[string]any)["DROP"] != "old" ||
		cwdOnly["secret_env"].(map[string]any)["TOKEN"] != secretID {
		t.Fatalf("cwd-only update changed omitted fields: %+v", cwdOnly)
	}
	replaced := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+machineID,
		`{"env":{"APP":"two"}}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if replaced["env"].(map[string]any)["APP"] != "two" {
		t.Fatalf("replaced machine env = %+v", replaced["env"])
	}
	if _, ok := replaced["env"].(map[string]any)["DROP"]; ok {
		t.Fatalf("replace-whole env retained removed key: %+v", replaced["env"])
	}
	replayed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		createBody,
		"idem-machine-execution-defaults",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if replayed["id"] != machineID || replayed["cwd"] != "/changed" ||
		replayed["env"].(map[string]any)["APP"] != "two" {
		t.Fatalf("replayed machine after execution-default update = %+v", replayed)
	}
	orgAdmin, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email:       "machine-defaults-admin@example.com",
		DisplayName: "Machine Defaults Admin",
	})
	if err != nil {
		t.Fatalf("create second org admin: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID:  project.OrgUUID,
		UserID: orgAdmin.ID,
		Role:   "admin",
	}); err != nil {
		t.Fatalf("add second org admin: %v", err)
	}
	orgAdminPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(ctx, identitystore.CreatePersonalAccessTokenInput{
		UserID:  orgAdmin.ID,
		Name:    "machine-defaults-admin",
		TokenID: "machine-defaults-admin",
	})
	if err != nil {
		t.Fatalf("create second org admin token: %v", err)
	}
	replayedByOtherAdmin := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		`{"display_name":"Different Machine","description":"different","cwd":"/different","env":{"APP":"different"},"metadata":{"changed":true}}`,
		"idem-machine-execution-defaults",
		http.StatusOK,
		authHeaders(orgAdminPAT.Token),
	)
	if replayedByOtherAdmin["id"] != machineID ||
		replayedByOtherAdmin["display_name"] != "Execution Defaults Machine" ||
		replayedByOtherAdmin["description"] != "" ||
		replayedByOtherAdmin["cwd"] != "/changed" ||
		replayedByOtherAdmin["env"].(map[string]any)["APP"] != "two" ||
		len(replayedByOtherAdmin["metadata"].(map[string]any)) != 0 {
		t.Fatalf("machine idempotency replay = %+v", replayedByOtherAdmin)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+machineID,
		`{"secret_env":{}}`,
		"",
		http.StatusOK,
		authHeaders(orgAdminPAT.Token),
	)
	member, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email:       "machine-defaults-member@example.com",
		DisplayName: "Machine Defaults Member",
	})
	if err != nil {
		t.Fatalf("create org member: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID:  project.OrgUUID,
		UserID: member.ID,
		Role:   "member",
	}); err != nil {
		t.Fatalf("add org member: %v", err)
	}
	memberPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(ctx, identitystore.CreatePersonalAccessTokenInput{
		UserID:  member.ID,
		Name:    "machine-defaults-member",
		TokenID: "machine-defaults-member",
	})
	if err != nil {
		t.Fatalf("create org member token: %v", err)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+machineID,
		`{"cwd":"/unauthorized"}`,
		"",
		http.StatusForbidden,
		authHeaders(memberPAT.Token),
	)
}

func TestMachineInventoryIncludesPoolMachinesAndRestrictsBYOOnlyOperations(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "machine-inventory-pool")
	store := integrationStoreForHandler(t, handler)
	now := time.Date(2026, 5, 21, 11, 0, 0, 0, time.UTC)
	viewer, err := storagetest.CreateVerifiedUser(
		ctx,
		pool,
		storagetest.CreateVerifiedUserInput{
			Email:       "machine-inventory-viewer@example.com",
			DisplayName: "Machine Inventory Viewer",
		},
	)
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	viewerPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID:  viewer.ID,
			Name:    "viewer",
			TokenID: "machine-inventory-viewer",
		},
	)
	if err != nil {
		t.Fatalf("create viewer token: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID:  project.OrgUUID,
		UserID: viewer.ID,
		Role:   "member",
	}); err != nil {
		t.Fatalf("add viewer org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID:     project.OrgUUID,
		ProjectID: project.ProjectUUID,
		UserID:    viewer.ID,
		Role:      "viewer",
	}); err != nil {
		t.Fatalf("add viewer project membership: %v", err)
	}

	byo := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		`{"display_name":"BYO Inventory Machine"}`,
		"idem-inventory-byo",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	byoID := byo["id"].(string)
	providerAuthSecret, _, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:     project.OrgUUID,
			OwnerKind: secretstore.SecretOwnerOrg,
			Name:      "inventory-pool-provider-auth",
			Material:  secrets.GenericMaterial{Value: "test-token"},
			Actor:     httpUserPrincipal(project.AdminUserUUID),
		},
	)
	if err != nil {
		t.Fatalf("create provider auth secret: %v", err)
	}
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		executionstore.CreateMachinePoolInput{
			OrgID:                         project.OrgUUID,
			Name:                          "Inventory Pool",
			Provider:                      "unikraft",
			DefaultMachineCPU:             intPtrForHTTPMachinePoolTest(1),
			DefaultMachineMemoryMB:        intPtrForHTTPMachinePoolTest(1024),
			DefaultMachineEnv:             json.RawMessage(`{}`),
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"test","metro":"sfo"}`),
			ProviderAuthSecretID:          providerAuthSecret.ID,
			MaxTotalMachines:              1,
			MaxTotalCPU:                   intPtrForHTTPMachinePoolTest(32),
			MaxTotalMemoryMB:              intPtrForHTTPMachinePoolTest(65536),
			MaxMachineCPU:                 intPtrForHTTPMachinePoolTest(32),
			MaxMachineMemoryMB:            intPtrForHTTPMachinePoolTest(65536),
		})

	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          project.OrgUUID,
			ProjectID:      project.ProjectUUID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "idem-inventory-pool-grant",
		})

	if err != nil {
		t.Fatalf("create machine pool grant: %v", err)
	}
	var poolMachineUUID storage.ID
	if err := pool.QueryRow(ctx, `
			INSERT INTO machines(
				org_id, machine_pool_id, source_kind, display_name, provider, lifecycle_state,
				lifecycle_changed_at,
				provider_resource_id, provider_provision_attempted_at,
				cpu, memory_mb, cwd, env, secret_env, provider_options, metadata, created_at, updated_at
			)
			VALUES ($1, $2, 'pool', 'Pool Inventory Machine', $3, 'active',
				$4,
				'pool-inventory-resource', $4,
				1, 1024, '', '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, $4, $4)
			RETURNING id
		`, project.OrgUUID, machinePool.ID, machinePool.Provider, now).Scan(&poolMachineUUID); err != nil {
		t.Fatalf("insert pool machine: %v", err)
	}
	var poolMachineGrantID storage.ID
	if err := pool.QueryRow(ctx, `
		INSERT INTO project_machine_grants(
			org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id,
			metadata, created_at, updated_at
		)
		VALUES ($1, $2, $3, 'pool', $4, '{}'::jsonb, $5, $5)
		RETURNING id
	`,
		project.OrgUUID,
		project.ProjectUUID,
		poolMachineUUID,
		poolGrant.ID,
		now,
	).Scan(&poolMachineGrantID); err != nil {
		t.Fatalf("insert pool machine grant: %v", err)
	}
	poolMachineID := testPublicID(t, publicid.KindMachine, poolMachineUUID)
	poolMachineGrantPublicID := testPublicID(
		t,
		publicid.KindProjectMachineGrant,
		poolMachineGrantID,
	)

	all := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if ids := machineIDsFromListResponse(t, all); !stringSetHas(ids, byoID) || !stringSetHas(ids, poolMachineID) {
		t.Fatalf("default inventory should include BYO and pool machines, got %+v", ids)
	}
	byoOnly := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machines?source_kind=byo",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if ids := machineIDsFromListResponse(t, byoOnly); !stringSetHas(ids, byoID) || stringSetHas(ids, poolMachineID) {
		t.Fatalf("BYO filter should only include BYO machine, got %+v", ids)
	}
	poolOnly := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machines?source_kind=pool",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if ids := machineIDsFromListResponse(t, poolOnly); stringSetHas(ids, byoID) || !stringSetHas(ids, poolMachineID) {
		t.Fatalf("pool filter should only include pool machine, got %+v", ids)
	}
	detail := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+poolMachineID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if detail["source_kind"] != "pool" {
		t.Fatalf(
			"pool machine detail source_kind = %+v, want pool",
			detail["source_kind"],
		)
	}
	viewerDetail := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+poolMachineID,
		"",
		"",
		http.StatusOK,
		authHeaders(viewerPAT.Token),
	)
	if viewerDetail["id"] != poolMachineID || viewerDetail["source_kind"] != "pool" {
		t.Fatalf("project-visible viewer pool machine detail = %+v", viewerDetail)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machines?source_kind=bad",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	projectAll := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/machines",
		"",
		"",
		http.StatusOK,
		authHeaders(viewerPAT.Token),
	)
	if ids := machineIDsFromListResponse(t, projectAll); !stringSetHas(ids, poolMachineID) {
		t.Fatalf("project inventory should include pool machine, got %+v", ids)
	}
	projectPool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/machines?source_kind=pool",
		"",
		"",
		http.StatusOK,
		authHeaders(viewerPAT.Token),
	)
	if ids := machineIDsFromListResponse(t, projectPool); !stringSetHas(
		ids,
		poolMachineID,
	) ||
		stringSetHas(ids, byoID) {
		t.Fatalf(
			"project pool filter should only include project-visible pool machine, got %+v",
			ids,
		)
	}
	projectBYO := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/machines?source_kind=byo",
		"",
		"",
		http.StatusOK,
		authHeaders(viewerPAT.Token),
	)
	if ids := machineIDsFromListResponse(t, projectBYO); stringSetHas(
		ids,
		poolMachineID,
	) {
		t.Fatalf("project BYO filter should exclude pool machine, got %+v", ids)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/machines?source_kind=bad",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(viewerPAT.Token),
	)
	poolTokenID := testPublicID(t, publicid.KindMachineDaemonToken, httpTestID("pool-machine-token-route"))
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+poolMachineID+"/daemon-tokens",
		`{"name":"daemon"}`,
		"",
		http.StatusNotFound,
		project.adminBrowserAuthHeaders(),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+poolMachineID+"/daemon-tokens",
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+poolMachineID+"/daemon-tokens/"+poolTokenID+"/revoke",
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	updatedPool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+poolMachineID,
		`{"cwd":"/changed"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if updatedPool["cwd"] != "/changed" {
		t.Fatalf("updated pool machine cwd = %+v, want /changed", updatedPool["cwd"])
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/machines/"+poolMachineID,
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-grants",
		`{"machine_id":`+quote(poolMachineID)+`}`,
		"idem-explicit-pool-machine-grant",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		project.ProjectPath+"/machine-grants/"+poolMachineGrantPublicID,
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
}

func TestMachineDaemonReportConflictingReplayIsCleanupOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-report-replay")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	process := createDaemonAcceptedProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-report-replay",
	)
	endedAt := now.Add(2 * time.Second)
	startedAt := now.Add(time.Second)
	exitCode := 0

	applyDaemonReportForTest(
		t,
		ctx,
		store,
		project,
		process,
		daemonReportedEvent{
			Type:      "process_finished",
			ProcessID: process.ProcessID,
			State:     "exited",
			ExitCode:  &exitCode,
			StartedAt: startedAt,
			EndedAt:   endedAt,
			Result: json.RawMessage(
				`{"output":"first","cursor":0,"next_cursor":5,"truncated":false}`,
			),
		},
	)
	cleanupOnly, err := applyDaemonReportDispositionForTest(
		t,
		ctx,
		store,
		project,
		process,
		daemonReportedEvent{
			Type:      "process_finished",
			ProcessID: process.ProcessID,
			State:     "exited",
			ExitCode:  &exitCode,
			StartedAt: startedAt,
			EndedAt:   endedAt,
			Result: json.RawMessage(
				`{"output":"changed","cursor":0,"next_cursor":7,"truncated":false}`,
			),
		},
	)
	if err != nil || !cleanupOnly {
		t.Fatalf("conflicting terminal replay cleanup_only=%v err=%v", cleanupOnly, err)
	}
}

func TestMachineDaemonStartedReportUsesTransactionalCancellationState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-started-after-cancel")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	fixture := createDaemonAcceptedProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-started-after-cancel",
	)
	staleStartingProcess, err := store.Execution().GetProcess(
		ctx,
		project.ProjectUUID,
		fixture.AgentUUID,
		fixture.ProcessUUID,
	)
	if err != nil {
		t.Fatalf("get accepted process: %v", err)
	}
	if staleStartingProcess.State != executionstore.ProcessStateStarting {
		t.Fatalf("accepted process = %+v, want starting", staleStartingProcess)
	}
	if _, err := store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: project.ProjectUUID,
			AgentID:   fixture.AgentUUID,
			Actor: httpOmnaraActorParams(
				t,
				project.OrgUUID,
				project.AdminUserUUID,
			),
		},
	); err != nil {
		t.Fatalf("cancel agent: %v", err)
	}

	cleanupOnly, err := mustNewServer(t, store).applyDaemonReportedEventForProcess(
		ctx,
		fixture.authority(),
		staleStartingProcess,
		daemonReportedEvent{
			Type:       "process_started",
			ProcessID:  fixture.ProcessID,
			StartedAt:  now.Add(2 * time.Second),
			ObservedAt: now.Add(3 * time.Second),
		},
		errors.New("missing process"),
	)
	if err != nil || !cleanupOnly {
		t.Fatalf(
			"started report after cancellation cleanup_only=%v err=%v",
			cleanupOnly,
			err,
		)
	}
	current, err := store.Execution().GetProcess(
		ctx,
		project.ProjectUUID,
		fixture.AgentUUID,
		fixture.ProcessUUID,
	)
	if err != nil {
		t.Fatalf("get canceled process: %v", err)
	}
	if current.State != executionstore.ProcessStateUnknown ||
		current.StateReasonCode != "agent_canceled_after_grant" {
		t.Fatalf("late readiness changed canceled process: %+v", current)
	}
}

func machineIDsFromListResponse(
	t *testing.T,
	response map[string]any,
) map[string]bool {
	t.Helper()
	data := response["data"].([]any)
	out := make(map[string]bool, len(data))
	for _, item := range data {
		machine := item.(map[string]any)
		out[machine["id"].(string)] = true
	}
	return out
}

func stringSetHas(values map[string]bool, value string) bool {
	return values[value]
}

func TestMachineDaemonReportProcessStartedCompletesStartToolCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-started-report")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 21, 12, 10, 0, 0, time.UTC)
	process := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-started-report",
		"run_command",
	)
	if _, found, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		process.authority(),
		process.ProcessUUID,
	); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process offer")
	}
	startedAt := now.Add(1500 * time.Millisecond)
	observedAt := now.Add(2 * time.Second)

	beforeReport := time.Now().UTC()
	applyDaemonReportForTest(
		t,
		ctx,
		store,
		project,
		process,
		daemonReportedEvent{
			Type:       "process_started",
			ProcessID:  process.ProcessID,
			StartedAt:  startedAt,
			ObservedAt: observedAt,
		},
	)
	afterReport := time.Now().UTC()
	reportedProcess, err := store.Execution().GetProcess(
		ctx,
		project.ProjectUUID,
		process.AgentUUID,
		process.ProcessUUID,
	)
	if err != nil {
		t.Fatalf("get reported process: %v", err)
	}
	if reportedProcess.State != executionstore.ProcessStateRunning {
		t.Fatalf("reported process state = %+v, want running", reportedProcess)
	}
	if reportedProcess.SourceStartedAt == nil ||
		!reportedProcess.SourceStartedAt.Equal(startedAt) {
		t.Fatalf(
			"reported process source_started_at = %v, want %s",
			reportedProcess.SourceStartedAt,
			startedAt,
		)
	}
	toolCall, err := store.Execution().GetToolCall(
		ctx,
		project.ProjectUUID,
		process.AgentUUID,
		process.ToolCallUUID,
	)
	if err != nil {
		t.Fatalf("get start tool call: %v", err)
	}
	if toolCall.State != "completed" || toolCall.CompletedAt == nil ||
		toolCall.CompletedAt.Before(beforeReport) ||
		toolCall.CompletedAt.After(afterReport) ||
		!strings.Contains(
			string(toolCall.ResultContentParts),
			process.ProcessID,
		) {
		t.Fatalf("start tool call after process_started = %+v", toolCall)
	}
}

func TestMachineDaemonRuntimeRegistrationClosesQueuedPreparation(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-registration-unaccepted")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 21, 12, 30, 0, 0, time.UTC)
	process := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-registration-unaccepted",
		"run_command",
	)
	body := `{"daemon_instance_id":` + quote(
		httpTestID("daemon-registration-unaccepted-replacement").String(),
	) + `,"daemon_version":"1.0.0",` +
		`"processes":[{"process_id":` + quote(
		process.ProcessID,
	) + `,"supervisor_instance_id":"prepared-test","phase":"prepared",` +
		`"supervisor_live":true,"execution_committed":false,` +
		`"action_admission_closed":false,"resolved_action_seq":0,"actions":[]}]}`
	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/daemon/runtimes",
		body,
		"",
		http.StatusCreated,
		authHeaders(process.Token),
	)
	reconciliation := response["reconciliation"].(map[string]any)
	processes := reconciliation["processes"].([]any)
	if len(processes) != 1 ||
		processes[0].(map[string]any)["process_id"] != process.ProcessID ||
		processes[0].(map[string]any)["disposition"] != "close_preparation" {
		t.Fatalf("queued preparation reconciliation = %+v", reconciliation)
	}
	updated, err := store.Execution().GetProcess(
		ctx,
		project.ProjectUUID,
		process.AgentUUID,
		process.ProcessUUID,
	)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	if updated.State != executionstore.ProcessStateQueued ||
		updated.ExecutionGrantedAt != nil {
		t.Fatalf("unaccepted process after HTTP registration = %+v", updated)
	}
}

func TestMachineDaemonRuntimeRegistrationClosesCanceledPreparation(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(
		t,
		handler,
		"daemon-registration-canceled-preparation",
	)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 21, 12, 32, 0, 0, time.UTC)
	process := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-registration-canceled-preparation",
		"run_command",
	)
	if _, err := store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: project.ProjectUUID,
			AgentID:   process.AgentUUID,
			Actor: httpOmnaraActorParams(
				t,
				project.OrgUUID,
				project.AdminUserUUID,
			),
		},
	); err != nil {
		t.Fatalf("cancel agent before process accept: %v", err)
	}

	body := `{"daemon_instance_id":` + quote(
		httpTestID(
			"daemon-registration-canceled-preparation-replacement",
		).String(),
	) + `,"daemon_version":"1.0.0",` +
		`"processes":[{"process_id":` + quote(
		process.ProcessID,
	) + `,"supervisor_instance_id":"prepared-canceled-test","phase":"prepared",` +
		`"supervisor_live":true,"execution_committed":false,` +
		`"action_admission_closed":false,"resolved_action_seq":0,"actions":[]}]}`
	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/daemon/runtimes",
		body,
		"",
		http.StatusCreated,
		authHeaders(process.Token),
	)
	reconciliation := response["reconciliation"].(map[string]any)
	processes := reconciliation["processes"].([]any)
	if len(processes) != 1 ||
		processes[0].(map[string]any)["process_id"] != process.ProcessID ||
		processes[0].(map[string]any)["disposition"] !=
			"close_preparation" {
		t.Fatalf(
			"canceled preparation reconciliation = %+v",
			reconciliation,
		)
	}
}

func TestMachineDaemonRuntimeRegistrationReturnsUnavailableWhileProvisioning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-registration-provisioning")
	store := newIntegrationStore(pool)
	now := time.Now().UTC()

	var machinePoolID storage.ID
	if err := pool.QueryRow(ctx, `
		INSERT INTO machine_pools(
			org_id, name, management_kind, provider, default_machine_memory_mb,
			provider_auth_env_var, max_total_machines, max_total_memory_mb,
			max_machine_memory_mb, created_at, updated_at
		)
		VALUES ($1, 'daemon-registration-provisioning', 'cluster', 'test', 1024,
			'TEST_PROVIDER_TOKEN', 1, 1024, 1024, $2, $2)
		RETURNING id
	`, project.OrgUUID, now).Scan(&machinePoolID); err != nil {
		t.Fatalf("insert machine pool fixture: %v", err)
	}
	var machineID storage.ID
	if err := pool.QueryRow(ctx, `
		INSERT INTO machines(
			org_id, machine_pool_id, source_kind, display_name, provider, lifecycle_state,
			lifecycle_changed_at,
			memory_mb, cwd, env, secret_env, provider_options, metadata,
			next_reconcile_after, provision_attempts, created_at, updated_at
		)
		VALUES ($1, $2, 'pool', 'provisioning machine', 'test', 'provisioning',
			$3,
			1024, '', '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb,
			$3, 1, $3, $3)
		RETURNING id
	`, project.OrgUUID, machinePoolID, now).Scan(&machineID); err != nil {
		t.Fatalf("insert provisioning machine fixture: %v", err)
	}
	providerProvisioning, err := store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            project.OrgUUID,
			MachineID:        machineID,
			ProvisionAttempt: 1,
			TokenName:        "daemon",
		},
	)
	if err != nil {
		t.Fatalf("create machine daemon token: %v", err)
	}

	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/daemon/runtimes",
		`{"daemon_instance_id":`+quote(httpTestID("daemon-registration-provisioning").String())+`,"daemon_version":"1.0.0","processes":[]}`,
		"",
		http.StatusServiceUnavailable,
		authHeaders(providerProvisioning.DaemonToken.Token),
	)
	if response["code"] != string(openapi.ErrorCodeServiceUnavailable) {
		t.Fatalf("registration error code = %q, want %q", response["code"], openapi.ErrorCodeServiceUnavailable)
	}
	if response["error"] != "service unavailable: daemon registration unavailable" {
		t.Fatalf("registration error = %q, want service unavailable detail", response["error"])
	}
}

func TestMachineDaemonRuntimeRegistrationRejectsSupersededInstance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-registration-superseded")
	store := newIntegrationStore(pool)
	fixture := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		time.Date(2026, 5, 21, 12, 35, 0, 0, time.UTC),
		"daemon-registration-superseded",
		"run_command",
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/daemon/runtimes",
		`{"daemon_instance_id":`+quote(
			httpTestID("daemon-registration-superseded-replacement").String(),
		)+`,"daemon_version":"1.0.0","processes":[]}`,
		"",
		http.StatusCreated,
		authHeaders(fixture.Token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/daemon/runtimes",
		`{"daemon_instance_id":`+quote(httpTestID("daemon-http-machine-routes").String())+`,"daemon_version":"1.0.0","processes":[]}`,
		"",
		http.StatusGone,
		authHeaders(fixture.Token),
	)
}

func TestMachineDaemonRuntimeRegistrationValidatesDaemonVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-registration-version")
	store := newIntegrationStore(pool)
	fixture := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		time.Date(2026, 5, 21, 12, 37, 0, 0, time.UTC),
		"daemon-registration-version",
		"run_command",
	)
	for _, body := range []string{
		`{"daemon_instance_id":` + quote(httpTestID("daemon-registration-version-missing").String()) + `,"processes":[]}`,
		`{"daemon_instance_id":` + quote(httpTestID("daemon-registration-version-malformed").String()) + `,"daemon_version":"1.2","processes":[]}`,
	} {
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			"/api/v1/daemon/runtimes",
			body,
			"",
			http.StatusBadRequest,
			authHeaders(fixture.Token),
		)
	}
}

func TestMachineDaemonRuntimeRegistrationReportsTerminalAndUnknownProcesses(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-registration-reconcile")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 21, 12, 40, 0, 0, time.UTC)
	terminal := createDaemonAcceptedProcessFixtureWithToolCalls(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-registration-terminal",
		[]model.ToolCall{
			{
				ID:    "call_registration_unknown",
				Name:  "run_command",
				Input: json.RawMessage(`{}`),
			},
		},
	)
	unknownToolCall := terminal.toolCall(t, "call_registration_unknown")
	unknownProcess, err := storagetest.StartProcessForToolCall(
		ctx,
		store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       terminal.AgentUUID,
			ToolCallID:    unknownToolCall.ID,
			RuntimeLockID: terminal.RuntimeLock.ID,
		},
		executionstore.CreateProcessInput{
			AgentMachineBindingID: terminal.BindingUUID,
			Command:               "sleep 60",
			ShellSelector:         "sh",
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatalf("start unknown process: %v", err)
	}
	if _, found, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		terminal.authority(),
		unknownProcess.ID,
	); err != nil {
		t.Fatalf("accept unknown process: %v", err)
	} else if !found {
		t.Fatal("expected unknown process offer")
	}
	endedAt := now.Add(10 * time.Second)
	exitCode := 0
	applyDaemonReportForTest(
		t,
		ctx,
		store,
		project,
		terminal,
		daemonReportedEvent{
			Type:      "process_finished",
			ProcessID: terminal.ProcessID,
			State:     "exited",
			ExitCode:  &exitCode,
			StartedAt: now.Add(9 * time.Second),
			EndedAt:   endedAt,
			Result: json.RawMessage(
				`{"output":"terminal-output","cursor":0,"next_cursor":15,"truncated":false}`,
			),
		},
	)

	body := `{"daemon_instance_id":` + quote(
		httpTestID("daemon-registration-reconcile-replacement").String(),
	) + `,"daemon_version":"1.0.0","processes":[{"process_id":` + quote(
		terminal.ProcessID,
	) + `,"supervisor_instance_id":"terminal-test","phase":"terminal",` +
		`"supervisor_live":false,"execution_committed":true,` +
		`"action_admission_closed":true,"resolved_action_seq":0,"actions":[]}]}`
	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/daemon/runtimes",
		body,
		"",
		http.StatusCreated,
		authHeaders(terminal.Token),
	)
	reconciliation := response["reconciliation"].(map[string]any)
	processes := reconciliation["processes"].([]any)
	if len(processes) != 1 ||
		processes[0].(map[string]any)["process_id"] != terminal.ProcessID ||
		processes[0].(map[string]any)["disposition"] != "release" {
		t.Fatalf("terminal reconciliation = %+v, want release", reconciliation)
	}
	terminalProcess, err := store.Execution().GetProcess(
		ctx,
		project.ProjectUUID,
		terminal.AgentUUID,
		terminal.ProcessUUID,
	)
	if err != nil {
		t.Fatalf("get terminal process: %v", err)
	}
	if terminalProcess.State != executionstore.ProcessStateExited {
		t.Fatalf("terminal process = %+v, want exited", terminalProcess)
	}
	unknownUpdated, err := store.Execution().GetProcess(
		ctx,
		project.ProjectUUID,
		terminal.AgentUUID,
		unknownProcess.ID,
	)
	if err != nil {
		t.Fatalf("get unknown process: %v", err)
	}
	if unknownUpdated.State != executionstore.ProcessStateUnknown ||
		unknownUpdated.StateReasonCode != "local_process_missing_after_daemon_reconnect" {
		t.Fatalf("unknown process = %+v", unknownUpdated)
	}
	toolCall, err := store.Execution().GetToolCall(
		ctx,
		project.ProjectUUID,
		terminal.AgentUUID,
		terminal.ToolCallUUID,
	)
	if err != nil {
		t.Fatalf("get terminal tool call: %v", err)
	}
	if toolCall.State != "completed" ||
		!strings.Contains(
			string(toolCall.ResultContentParts),
			"terminal-output",
		) {
		t.Fatalf("terminal tool call = %+v", toolCall)
	}
}

func TestMachineDaemonRuntimeRegistrationRetainsLiveProcess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-registration-reconcile")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 21, 12, 45, 0, 0, time.UTC)
	process := createDaemonAcceptedProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-registration-reconcile",
	)
	if _, err := store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		Authority:       process.authority(),
		ProjectID:       project.ProjectUUID,
		AgentID:         process.AgentUUID,
		ID:              process.ProcessUUID,
		SourceStartedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("mark process started: %v", err)
	}

	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/daemon/runtimes",
		`{"daemon_instance_id":`+quote(
			httpTestID("daemon-registration-reconcile-replacement").String(),
		)+`,"daemon_version":"1.0.0","processes":[{"process_id":`+quote(
			process.ProcessID,
		)+`,"supervisor_instance_id":"live-test","phase":"accepted",`+
			`"supervisor_live":true,"execution_committed":true,`+
			`"action_admission_closed":false,"resolved_action_seq":0,"actions":[]}]}`,
		"",
		http.StatusCreated,
		authHeaders(process.Token),
	)
	reconciliation := response["reconciliation"].(map[string]any)
	processes := reconciliation["processes"].([]any)
	if len(processes) != 1 ||
		processes[0].(map[string]any)["process_id"] != process.ProcessID ||
		processes[0].(map[string]any)["disposition"] != "retain" {
		t.Fatalf("live process reconciliation = %+v, want retain", reconciliation)
	}
	updated, err := store.Execution().GetProcess(
		ctx,
		project.ProjectUUID,
		process.AgentUUID,
		process.ProcessUUID,
	)
	if err != nil {
		t.Fatalf("get reconciled process: %v", err)
	}
	if updated.State != executionstore.ProcessStateRunning ||
		updated.ExecutionGrantedAt == nil {
		t.Fatalf("reconciled process = %+v, want granted running process", updated)
	}
	replacementRuntimeID, err := publicid.Decode(
		publicid.KindDaemonRuntime,
		response["runtime"].(map[string]any)["id"].(string),
	)
	if err != nil {
		t.Fatalf("decode replacement runtime: %v", err)
	}
	replacement := process
	replacement.RuntimeUUID = replacementRuntimeID
	cleanupOnly, err := applyDaemonReportDispositionForTest(
		t,
		ctx,
		store,
		project,
		replacement,
		daemonReportedEvent{
			Type:       "process_started",
			ProcessID:  process.ProcessID,
			StartedAt:  now.Add(1500 * time.Millisecond),
			ObservedAt: now.Add(2 * time.Second),
		},
	)
	if err != nil || cleanupOnly {
		t.Fatalf(
			"replacement runtime reporting old grant: cleanup_only=%t err=%v",
			cleanupOnly,
			err,
		)
	}
	afterReport, err := store.Execution().GetProcess(
		ctx,
		project.ProjectUUID,
		process.AgentUUID,
		process.ProcessUUID,
	)
	if err != nil {
		t.Fatalf("get process after replacement report: %v", err)
	}
	if afterReport.State != executionstore.ProcessStateRunning ||
		afterReport.ExecutionGrantedAt == nil ||
		!afterReport.ExecutionGrantedAt.Equal(*updated.ExecutionGrantedAt) {
		t.Fatalf("process after replacement report = %+v, want unchanged grant", afterReport)
	}
}

func TestMachineDaemonRuntimeRegistrationReleasesCommittedRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(
		t,
		handler,
		"daemon-registration-read-release",
	)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 21, 12, 46, 0, 0, time.UTC)
	process := createDaemonAcceptedProcessFixtureWithToolCalls(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-registration-read-release",
		[]model.ToolCall{
			{
				ID:    "call_registration_read_release",
				Name:  "read_process",
				Input: json.RawMessage(`{}`),
			},
		},
	)
	if _, err := store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			Authority:       process.authority(),
			ProjectID:       project.ProjectUUID,
			AgentID:         process.AgentUUID,
			ID:              process.ProcessUUID,
			SourceStartedAt: now.Add(time.Second),
		},
	); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	toolCall := process.toolCall(t, "call_registration_read_release")
	action, err := storagetest.CreateProcessActionForToolCall(
		ctx,
		store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       process.AgentUUID,
			ToolCallID:    toolCall.ID,
			RuntimeLockID: process.RuntimeLock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ProcessUUID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{"cursor":0}`),
		},
	)
	if err != nil {
		t.Fatalf("create process read: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionOfferForTest(
		ctx,
		store,
		process.authority(),
		process.ProcessUUID,
		action.ID,
	); err != nil {
		t.Fatalf("accept process read: %v", err)
	} else if !found {
		t.Fatal("expected process read offer")
	}
	actionID := testPublicID(t, publicid.KindProcessAction, action.ID)
	applyDaemonReportForTest(
		t,
		ctx,
		store,
		project,
		process,
		daemonReportedEvent{
			Type:            "process_action_applied",
			ProcessID:       process.ProcessID,
			ProcessActionID: actionID,
			Result: json.RawMessage(
				`{"output":"ready","cursor":0,"next_cursor":5,"truncated":false}`,
			),
		},
	)

	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/daemon/runtimes",
		`{"daemon_instance_id":`+quote(
			httpTestID("daemon-registration-read-release-replacement").String(),
		)+`,"daemon_version":"1.0.0","processes":[{"process_id":`+quote(
			process.ProcessID,
		)+`,"supervisor_instance_id":"read-release-test","phase":"accepted",`+
			`"supervisor_live":true,"execution_committed":true,`+
			`"action_admission_closed":false,"resolved_action_seq":0,"actions":[]}]}`,
		"",
		http.StatusCreated,
		authHeaders(process.Token),
	)
	reconciliation := response["reconciliation"].(map[string]any)
	processes := reconciliation["processes"].([]any)
	if len(processes) != 1 {
		t.Fatalf("read reconciliation = %+v, want one process", reconciliation)
	}
	disposition := processes[0].(map[string]any)
	actions := disposition["actions"].([]any)
	if disposition["disposition"] != "retain" || len(actions) != 1 {
		t.Fatalf("read reconciliation disposition = %+v", disposition)
	}
	readDisposition := actions[0].(map[string]any)
	if readDisposition["process_action_id"] != actionID ||
		readDisposition["seq"] != float64(action.Seq) ||
		readDisposition["action_kind"] != "read" ||
		readDisposition["disposition"] != "release" {
		t.Fatalf(
			"committed read reconciliation = %+v, want read release",
			readDisposition,
		)
	}
}

func TestReplacementDaemonRuntimeCompletesGrantedProcessAndReplaysEvidence(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-replacement-report")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 21, 12, 47, 0, 0, time.UTC)
	process := createDaemonAcceptedProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-replacement-report",
	)
	granted, err := store.Execution().GetProcess(
		ctx,
		project.ProjectUUID,
		process.AgentUUID,
		process.ProcessUUID,
	)
	if err != nil {
		t.Fatalf("get granted process: %v", err)
	}
	if granted.State != executionstore.ProcessStateStarting ||
		granted.ExecutionGrantedAt == nil {
		t.Fatalf("granted process = %+v, want starting with execution grant", granted)
	}

	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/daemon/runtimes",
		`{"daemon_instance_id":`+quote(
			httpTestID("daemon-replacement-report-current").String(),
		)+`,"daemon_version":"1.0.0","processes":[{"process_id":`+quote(
			process.ProcessID,
		)+`,"supervisor_instance_id":"replacement-report-test","phase":"accepted",`+
			`"supervisor_live":true,"execution_committed":true,`+
			`"action_admission_closed":false,"resolved_action_seq":0,"actions":[]}]}`,
		"",
		http.StatusCreated,
		authHeaders(process.Token),
	)
	replacementRuntimeID, err := publicid.Decode(
		publicid.KindDaemonRuntime,
		response["runtime"].(map[string]any)["id"].(string),
	)
	if err != nil {
		t.Fatalf("decode replacement runtime: %v", err)
	}
	exitCode := 0
	report := daemonReportedEvent{
		Type:      "process_finished",
		ProcessID: process.ProcessID,
		State:     "exited",
		ExitCode:  &exitCode,
		Result: json.RawMessage(
			`{"output":"durable output","cursor":0,"next_cursor":14,"truncated":false}`,
		),
		StartedAt: now.Add(1500 * time.Millisecond),
		EndedAt:   now.Add(2 * time.Second),
	}
	if _, err := applyDaemonReportDispositionForTest(
		t,
		ctx,
		store,
		project,
		process,
		report,
	); !errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
		t.Fatalf(
			"superseded runtime report error = %v, want ErrDaemonRuntimeUnregistered",
			err,
		)
	}

	replacement := process
	replacement.RuntimeUUID = replacementRuntimeID
	cleanupOnly, err := applyDaemonReportDispositionForTest(
		t,
		ctx,
		store,
		project,
		replacement,
		report,
	)
	if err != nil || cleanupOnly {
		t.Fatalf(
			"replacement terminal report cleanup_only=%t err=%v, want committed",
			cleanupOnly,
			err,
		)
	}
	cleanupOnly, err = applyDaemonReportDispositionForTest(
		t,
		ctx,
		store,
		project,
		replacement,
		report,
	)
	if err != nil || cleanupOnly {
		t.Fatalf(
			"identical terminal replay cleanup_only=%t err=%v, want committed",
			cleanupOnly,
			err,
		)
	}
	report.Result = json.RawMessage(`{"output":"conflicting output"}`)
	cleanupOnly, err = applyDaemonReportDispositionForTest(
		t,
		ctx,
		store,
		project,
		replacement,
		report,
	)
	if err != nil || !cleanupOnly {
		t.Fatalf(
			"conflicting terminal replay cleanup_only=%t err=%v, want cleanup-only",
			cleanupOnly,
			err,
		)
	}

	completed, err := store.Execution().GetProcess(
		ctx,
		project.ProjectUUID,
		process.AgentUUID,
		process.ProcessUUID,
	)
	if err != nil {
		t.Fatalf("get completed process: %v", err)
	}
	if completed.State != executionstore.ProcessStateExited ||
		completed.ExecutionGrantedAt == nil ||
		!completed.ExecutionGrantedAt.Equal(*granted.ExecutionGrantedAt) {
		t.Fatalf("completed process = %+v, want exited with unchanged grant", completed)
	}
}

func TestMachineDaemonOffersExpiredRuntimeReturnsNoOffers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-offers-expired-runtime")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 21, 12, 50, 0, 0, time.UTC)
	process := createDaemonProcessFixture(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-offers-expired-runtime",
		"run_command",
	)
	if _, err := pool.Exec(ctx, `
		UPDATE daemon_runtimes
		SET last_seen_at = statement_timestamp() - INTERVAL '2 seconds',
		    lease_expires_at = statement_timestamp() - INTERVAL '1 second',
		    updated_at = statement_timestamp()
		WHERE org_id = $1 AND machine_id = $2 AND id = $3
	`, project.OrgUUID, process.MachineUUID, process.RuntimeUUID); err != nil {
		t.Fatalf("expire daemon runtime lease: %v", err)
	}

	offers, err := store.Execution().ListDaemonProcessOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: process.authority(),
			Limit:     8,
		},
	)
	if err != nil {
		t.Fatalf("list offers for expired runtime: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf(
			"expired runtime offers = %+v, want none until heartbeat restores lease",
			offers,
		)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE daemon_runtimes
		SET last_seen_at = statement_timestamp(),
		    lease_expires_at = statement_timestamp() + INTERVAL '1 hour',
		    updated_at = statement_timestamp()
		WHERE org_id = $1 AND machine_id = $2 AND id = $3
	`, project.OrgUUID, process.MachineUUID, process.RuntimeUUID); err != nil {
		t.Fatalf("restore daemon runtime lease: %v", err)
	}
	offers, err = store.Execution().ListDaemonProcessOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: process.authority(),
			Limit:     8,
		},
	)
	if err != nil {
		t.Fatalf("list offers after lease restore: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf(
			"restored lease offers = %+v, want the pending process offer",
			offers,
		)
	}
}

func TestMachineDaemonReportProcessActionReplayConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-action-replay")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 21, 12, 50, 0, 0, time.UTC)
	process := createDaemonProcessFixtureWithToolCalls(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-action-replay",
		"run_command",
		[]model.ToolCall{
			{
				ID:    "call_action_replay",
				Name:  "read_process",
				Input: json.RawMessage(`{}`),
			},
			{
				ID:    "call_action_failed",
				Name:  "read_process",
				Input: json.RawMessage(`{}`),
			},
			{
				ID:    "call_action_unknown",
				Name:  "read_process",
				Input: json.RawMessage(`{}`),
			},
		},
	)
	if _, found, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		process.authority(),
		process.ProcessUUID,
	); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process offer")
	}
	if _, err := store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		Authority:       process.authority(),
		ProjectID:       project.ProjectUUID,
		AgentID:         process.AgentUUID,
		ID:              process.ProcessUUID,
		SourceStartedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	actionToolCall := process.toolCall(t, "call_action_replay")
	action, err := storagetest.CreateProcessActionForToolCall(
		ctx,
		store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       process.AgentUUID,
			ToolCallID:    actionToolCall.ID,
			RuntimeLockID: process.RuntimeLock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ProcessUUID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{"cursor":0}`),
		},
	)
	if err != nil {
		t.Fatalf("create process action: %v", err)
	}
	actionID := testPublicID(t, publicid.KindProcessAction, action.ID)
	if _, found, err := acceptDaemonProcessActionOfferForTest(
		ctx,
		store,
		process.authority(),
		process.ProcessUUID,
		action.ID,
	); err != nil {
		t.Fatalf("accept action: %v", err)
	} else if !found {
		t.Fatal("expected action offer")
	}
	applyDaemonReportForTest(
		t,
		ctx,
		store,
		project,
		process,
		daemonReportedEvent{
			Type:            "process_action_applied",
			ProcessID:       process.ProcessID,
			ProcessActionID: actionID,
			Result: json.RawMessage(
				`{"process_id":"` + process.ProcessID +
					`","output":"first","cursor":0,"next_cursor":5,"truncated":false}`,
			),
		},
	)
	applyDaemonReportForTest(
		t,
		ctx,
		store,
		project,
		process,
		daemonReportedEvent{
			Type:            "process_action_applied",
			ProcessID:       process.ProcessID,
			ProcessActionID: actionID,
			Result: json.RawMessage(
				`{"process_id":"` + process.ProcessID +
					`","output":"first","cursor":0,"next_cursor":5,"truncated":false}`,
			),
		},
	)
	cleanupOnly, err := applyDaemonReportDispositionForTest(
		t,
		ctx,
		store,
		project,
		process,
		daemonReportedEvent{
			Type:            "process_action_applied",
			ProcessID:       process.ProcessID,
			ProcessActionID: actionID,
			Result: json.RawMessage(
				`{"process_id":"` + process.ProcessID +
					`","output":"changed","cursor":0,"next_cursor":7,"truncated":false}`,
			),
		},
	)
	if err != nil || !cleanupOnly {
		t.Fatalf("conflicting action replay cleanup_only=%v err=%v", cleanupOnly, err)
	}
	updated, found, err := store.Execution().GetProcessActionByToolCall(
		ctx,
		project.ProjectUUID,
		process.AgentUUID,
		actionToolCall.ID,
	)
	if err != nil {
		t.Fatalf("get action: %v", err)
	}
	if !found || updated.ID != action.ID ||
		updated.State != executionstore.ProcessActionStateApplied {
		t.Fatalf("action after report = found %v %+v", found, updated)
	}
	reportTerminalAction := func(
		providerCallID, toolName string,
		ordinal int,
		actionKind executionstore.ProcessActionKind,
		eventType daemonprotocol.ReportedEventType,
		reasonCode string,
		wantState executionstore.ProcessActionState,
	) {
		t.Helper()
		toolCall := process.toolCall(t, providerCallID)
		if toolCall.Name != toolName {
			t.Fatalf("tool call %s name=%s want=%s", providerCallID, toolCall.Name, toolName)
		}
		action, err := storagetest.CreateProcessActionForToolCall(
			ctx,
			store,
			executionstore.ExecuteToolCallInput{
				ProjectID:     project.ProjectUUID,
				AgentID:       process.AgentUUID,
				ToolCallID:    toolCall.ID,
				RuntimeLockID: process.RuntimeLock.ID,
			},
			executionstore.CreateProcessActionInput{
				ProcessID:  process.ProcessUUID,
				ActionKind: actionKind,
				Payload:    json.RawMessage(`{"cursor":0}`),
			},
		)
		if err != nil {
			t.Fatalf("create %s action: %v", eventType, err)
		}
		actionID := testPublicID(t, publicid.KindProcessAction, action.ID)
		if _, found, err := acceptDaemonProcessActionOfferForTest(
			ctx,
			store,
			process.authority(),
			process.ProcessUUID,
			action.ID,
		); err != nil {
			t.Fatalf("accept %s action: %v", eventType, err)
		} else if !found {
			t.Fatalf("expected %s action offer", eventType)
		}
		applyDaemonReportForTest(
			t,
			ctx,
			store,
			project,
			process,
			daemonReportedEvent{
				Type:               eventType,
				ProcessID:          process.ProcessID,
				ProcessActionID:    actionID,
				StateReasonCode:    reasonCode,
				StateReasonMessage: "test action terminal",
				Result: json.RawMessage(
					`{"error":"test action terminal"}`,
				),
			},
		)
		updated, found, err := store.Execution().GetProcessActionByToolCall(
			ctx,
			project.ProjectUUID,
			process.AgentUUID,
			toolCall.ID,
		)
		if err != nil {
			t.Fatalf("get %s action: %v", eventType, err)
		}
		if !found || updated.ID != action.ID || updated.State != wantState ||
			updated.StateReasonCode != reasonCode {
			t.Fatalf(
				"%s action after report = found %v %+v",
				eventType,
				found,
				updated,
			)
		}
	}
	reportTerminalAction(
		"call_action_failed",
		"read_process",
		2,
		executionstore.ProcessActionKindRead,
		daemonprotocol.EventProcessActionFailed,
		"runner_failed",
		executionstore.ProcessActionStateFailed,
	)
	reportTerminalAction(
		"call_action_unknown",
		"read_process",
		3,
		executionstore.ProcessActionKindRead,
		daemonprotocol.EventProcessActionUnknown,
		"read_interrupted",
		executionstore.ProcessActionStateFailed,
	)
}

func TestMachineDaemonReportProcessFinishedPreservesOutstandingReads(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "daemon-terminal-actions")
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 21, 13, 0, 0, 0, time.UTC)
	process := createDaemonProcessFixtureWithToolCalls(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		"daemon-terminal-actions",
		"run_command",
		[]model.ToolCall{
			{
				ID:    "call_terminal_accepted",
				Name:  "read_process",
				Input: json.RawMessage(`{}`),
			},
			{
				ID:    "call_terminal_queued",
				Name:  "read_process",
				Input: json.RawMessage(`{}`),
			},
		},
	)
	if _, found, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		process.authority(),
		process.ProcessUUID,
	); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process offer")
	}
	if _, err := store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		Authority:       process.authority(),
		ProjectID:       project.ProjectUUID,
		AgentID:         process.AgentUUID,
		ID:              process.ProcessUUID,
		SourceStartedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	acceptedToolCall := process.toolCall(t, "call_terminal_accepted")
	queuedToolCall := process.toolCall(t, "call_terminal_queued")
	acceptedAction, err := storagetest.CreateProcessActionForToolCall(
		ctx,
		store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       process.AgentUUID,
			ToolCallID:    acceptedToolCall.ID,
			RuntimeLockID: process.RuntimeLock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ProcessUUID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{"cursor":0,"wait_ms":1000}`),
		},
	)
	if err != nil {
		t.Fatalf("create accepted action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionOfferForTest(
		ctx,
		store,
		process.authority(),
		process.ProcessUUID,
		acceptedAction.ID,
	); err != nil {
		t.Fatalf("accept action: %v", err)
	} else if !found {
		t.Fatal("expected action offer")
	}
	queuedAction, err := storagetest.CreateProcessActionForToolCall(
		ctx,
		store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       process.AgentUUID,
			ToolCallID:    queuedToolCall.ID,
			RuntimeLockID: process.RuntimeLock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ProcessUUID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{"cursor":0}`),
		},
	)
	if err != nil {
		t.Fatalf("create queued action: %v", err)
	}
	endedAt := now.Add(8 * time.Second)
	exitCode := 0
	applyDaemonReportForTest(
		t,
		ctx,
		store,
		project,
		process,
		daemonReportedEvent{
			Type:      "process_finished",
			ProcessID: process.ProcessID,
			State:     "exited",
			ExitCode:  &exitCode,
			EndedAt:   endedAt,
			Result: json.RawMessage(
				`{"output":"done","cursor":0,"next_cursor":4,"truncated":false}`,
			),
		},
	)

	queuedUpdated, found, err := store.Execution().GetProcessActionByToolCall(
		ctx,
		project.ProjectUUID,
		process.AgentUUID,
		queuedToolCall.ID,
	)
	if err != nil {
		t.Fatalf("get queued action: %v", err)
	}
	if !found || queuedUpdated.ID != queuedAction.ID ||
		queuedUpdated.State != executionstore.ProcessActionStateQueued ||
		queuedUpdated.StateReasonCode != "" {
		t.Fatalf(
			"queued action after terminal = found %v %+v",
			found,
			queuedUpdated,
		)
	}
	acceptedUpdated, found, err := store.Execution().GetProcessActionByToolCall(
		ctx,
		project.ProjectUUID,
		process.AgentUUID,
		acceptedToolCall.ID,
	)
	if err != nil {
		t.Fatalf("get accepted action: %v", err)
	}
	if !found || acceptedUpdated.ID != acceptedAction.ID ||
		acceptedUpdated.State != executionstore.ProcessActionStateAccepted ||
		acceptedUpdated.StateReasonCode != "" {
		t.Fatalf(
			"accepted action after terminal = found %v %+v",
			found,
			acceptedUpdated,
		)
	}
}

type daemonProcessFixture struct {
	OrgUUID      storage.ID
	AgentUUID    storage.ID
	ProcessUUID  storage.ID
	MachineUUID  storage.ID
	BindingUUID  storage.ID
	RuntimeUUID  storage.ID
	TokenUUID    storage.ID
	RuntimeLock  executionstore.AgentRuntimeLockRecord
	ToolCallUUID storage.ID
	ToolCall     executionstore.ToolCallRecord
	ToolCalls    map[string]executionstore.ToolCallRecord
	Token        string
	RuntimeID    string
	ProcessID    string
}

func (f daemonProcessFixture) authority() executionstore.DaemonRuntimeAuthority {
	return executionstore.DaemonRuntimeAuthority{
		OrgID:           f.OrgUUID,
		MachineID:       f.MachineUUID,
		DaemonRuntimeID: f.RuntimeUUID,
		DaemonTokenID:   f.TokenUUID,
	}
}

func (f daemonProcessFixture) toolCall(
	t testing.TB,
	providerCallID string,
) executionstore.ToolCallRecord {
	t.Helper()
	toolCall, ok := f.ToolCalls[providerCallID]
	if !ok {
		t.Fatalf("tool call %s missing from accepted proposal batch", providerCallID)
	}
	return toolCall
}

func containsString(values []any, want string) bool {
	for _, value := range values {
		if got, ok := value.(string); ok && got == want {
			return true
		}
	}
	return false
}

func createDaemonAcceptedProcessFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store *storage.Store,
	project publicHTTPProject,
	now time.Time,
	name string,
) daemonProcessFixture {
	t.Helper()
	return createDaemonAcceptedProcessFixtureWithToolCalls(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		name,
		nil,
	)
}

func createDaemonAcceptedProcessFixtureWithToolCalls(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store *storage.Store,
	project publicHTTPProject,
	now time.Time,
	name string,
	additionalToolCalls []model.ToolCall,
) daemonProcessFixture {
	t.Helper()
	fixture := createDaemonProcessFixtureWithToolCalls(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		name,
		"run_command",
		additionalToolCalls,
	)
	if _, found, err := acceptDaemonProcessOfferForTest(
		ctx,
		store,
		fixture.authority(),
		fixture.ProcessUUID,
	); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process offer")
	}
	return fixture
}

func acceptDaemonProcessOfferForTest(
	ctx context.Context,
	store *storage.Store,
	authority executionstore.DaemonRuntimeAuthority,
	processID storage.ID,
) (executionstore.DaemonProcessOffer, bool, error) {
	offers, err := store.Execution().ListDaemonProcessOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: authority,
			Limit:     32,
		},
	)
	if err != nil {
		return executionstore.DaemonProcessOffer{}, false, err
	}
	for _, offer := range offers {
		if processID != storage.NilID && offer.Process.ID != processID {
			continue
		}
		return store.Execution().AcceptDaemonProcess(
			ctx,
			executionstore.AcceptDaemonProcessInput{
				Authority: authority,
				ProcessID: offer.Process.ID,
			},
		)
	}
	return executionstore.DaemonProcessOffer{}, false, nil
}

func acceptDaemonProcessActionOfferForTest(
	ctx context.Context,
	store *storage.Store,
	authority executionstore.DaemonRuntimeAuthority,
	processID, actionID storage.ID,
) (executionstore.DaemonProcessActionGrant, bool, error) {
	offers, err := store.Execution().ListDaemonProcessActionOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: authority,
			Limit:     32,
		},
	)
	if err != nil {
		return executionstore.DaemonProcessActionGrant{}, false, err
	}
	for _, offer := range offers {
		if processID != storage.NilID && offer.ProcessID != processID {
			continue
		}
		if actionID != storage.NilID && offer.ID != actionID {
			continue
		}
		return store.Execution().AcceptDaemonProcessAction(
			ctx,
			executionstore.AcceptDaemonProcessActionInput{
				Authority: authority,
				ProcessID: offer.ProcessID,
				ID:        offer.ID,
			},
		)
	}
	return executionstore.DaemonProcessActionGrant{}, false, nil
}

func applyDaemonReportForTest(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	project publicHTTPProject,
	fixture daemonProcessFixture,
	event daemonReportedEvent,
) {
	t.Helper()
	_, err := applyDaemonReportDispositionForTest(
		t,
		ctx,
		store,
		project,
		fixture,
		event,
	)
	if err != nil {
		t.Fatalf("apply daemon report: %v", err)
	}
}

func applyDaemonReportDispositionForTest(
	t testing.TB,
	ctx context.Context,
	store *storage.Store,
	project publicHTTPProject,
	fixture daemonProcessFixture,
	event daemonReportedEvent,
) (bool, error) {
	server := mustNewServer(t, store)
	return server.applyDaemonReportedEventForMachineWithContext(
		ctx,
		fixture.authority(),
		event,
		errors.New("daemon process does not belong to this machine"),
	)
}

func createDaemonProcessFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store *storage.Store,
	project publicHTTPProject,
	now time.Time,
	name string,
	toolName string,
) daemonProcessFixture {
	t.Helper()
	return createDaemonProcessFixtureWithToolCalls(
		t,
		ctx,
		pool,
		store,
		project,
		now,
		name,
		toolName,
		nil,
	)
}

func createDaemonProcessFixtureWithToolCalls(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store *storage.Store,
	project publicHTTPProject,
	now time.Time,
	name string,
	toolName string,
	additionalToolCalls []model.ToolCall,
) daemonProcessFixture {
	t.Helper()
	_, err := storagetest.CreateVerifiedUser(
		ctx,
		pool,
		storagetest.CreateVerifiedUserInput{Email: name + "@example.com", DisplayName: name},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          project.OrgUUID,
			DisplayName:    name,
			IdempotencyKey: "idem-machine-" + name,
		},
	)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	_, machine, err = store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          project.OrgUUID,
			ProjectID:      project.ProjectUUID,
			MachineID:      machine.ID,
			IdempotencyKey: "idem-grant-" + name,
		},
	)
	if err != nil {
		t.Fatalf("create grant: %v", err)
	}
	token := executionstore.MachineDaemonTokenPlaintextPrefix + name
	tokenRecord, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     project.OrgUUID,
			MachineID: machine.ID,
			Name:      "daemon",
			Token:     token,
		},
	)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	registration, err := store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            project.OrgUUID,
			MachineID:        machine.ID,
			DaemonTokenID:    tokenRecord.ID,
			DaemonInstanceID: httpTestID("daemon-http-machine-routes"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("register runtime: %v", err)
	}
	runtime := registration.Runtime
	agent, toolCall, toolCalls, lock, binding := createHTTPProcessToolCallBatch(
		t,
		ctx,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		project.AdminUserUUID,
		now,
		name,
		toolName,
		machine.DisplayName,
		additionalToolCalls,
	)
	process, err := storagetest.StartProcessForToolCall(
		ctx,
		store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       agent.ID,
			ToolCallID:    toolCall.ID,
			RuntimeLockID: lock.ID,
		},
		executionstore.CreateProcessInput{
			AgentMachineBindingID: binding.ID,
			Command:               "echo ok",
			ShellSelector:         "sh",
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	return daemonProcessFixture{
		OrgUUID:      project.OrgUUID,
		AgentUUID:    agent.ID,
		ProcessUUID:  process.ID,
		MachineUUID:  machine.ID,
		BindingUUID:  binding.ID,
		RuntimeUUID:  runtime.ID,
		TokenUUID:    tokenRecord.ID,
		RuntimeLock:  lock,
		ToolCallUUID: toolCall.ID,
		ToolCall:     toolCall,
		ToolCalls:    toolCalls,
		Token:        token,
		RuntimeID:    testPublicID(t, publicid.KindDaemonRuntime, runtime.ID),
		ProcessID:    testPublicID(t, publicid.KindProcess, process.ID),
	}
}

func createHTTPProcessToolCall(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	orgID, projectID, producerID storage.ID,
	now time.Time,
	name string,
	toolName string,
	machineName string,
) (executionstore.AgentRecord, executionstore.ToolCallRecord, executionstore.AgentRuntimeLockRecord, executionstore.AgentMachineBindingRecord) {
	t.Helper()
	agent, toolCall, _, lock, binding := createHTTPProcessToolCallBatch(
		t,
		ctx,
		store,
		orgID,
		projectID,
		producerID,
		now,
		name,
		toolName,
		machineName,
		nil,
	)
	return agent, toolCall, lock, binding
}

func createHTTPProcessToolCallBatch(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	orgID, projectID, producerID storage.ID,
	now time.Time,
	name string,
	toolName string,
	machineName string,
	additionalToolCalls []model.ToolCall,
) (
	executionstore.AgentRecord,
	executionstore.ToolCallRecord,
	map[string]executionstore.ToolCallRecord,
	executionstore.AgentRuntimeLockRecord,
	executionstore.AgentMachineBindingRecord,
) {
	t.Helper()
	launch := createHTTPRuntimeAgentWithMachineSource(
		t,
		ctx,
		store,
		orgID,
		projectID,
		producerID,
		"process-"+name,
		machineName,
	)
	if len(launch.MachineBindings) != 1 {
		t.Fatalf("launch machine bindings = %d, want 1", len(launch.MachineBindings))
	}
	agent := launch.Agent
	binding := launch.MachineBindings[0]
	input, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      projectID,
			AgentID:        agent.ID,
			Actor:          httpOmnaraActorParams(t, orgID, producerID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"run"}]`),
			IdempotencyKey: "msg-" + name,
		},
	)
	if err != nil {
		t.Fatalf("create message: %v", err)
	}
	claim, found, err := store.Execution().ClaimNextAgentWork(
		ctx,
		httpTestClaimInput(),
	)
	if err != nil {
		t.Fatalf("claim input work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || len(claim.Model.AdmittedInputTurn.Inputs) != 1 ||
		claim.Model.AdmittedInputTurn.Inputs[0].ID != input.ID {
		t.Fatalf("claim input found=%v executable=%v", found, claim.Kind == executionstore.AgentWorkModel)
	}
	lock := claim.RuntimeLock
	admitted := claim.Model.AdmittedInputTurn
	modelCall := claimNormalModelCallForHTTPTest(
		t,
		ctx,
		store,
		projectID,
		agent.ID,
		lock,
		[]storage.ID{input.ID},
		launch.AgentConfig.ID,
		admitted.Events[0].Sequence,
	)
	modelContext := modelCall.Context
	providerResponseID := "resp_" + name
	primaryToolCall := model.ToolCall{ID: "call_" + name, Name: toolName, Input: json.RawMessage(`{}`)}
	responseToolCalls := append([]model.ToolCall{primaryToolCall}, additionalToolCalls...)
	providerResponse, err := model.NewResponseEnvelopeForStorage(
		"http-test",
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		model.Response{
			ID:         providerResponseID,
			StopReason: model.StopReasonToolUse,
			Content:    modeltest.ResponsePartsForToolCalls(responseToolCalls),
		},
	)
	if err != nil {
		t.Fatalf("provider response: %v", err)
	}
	bindings := make([]executionstore.ToolCallBindingInput, 0, len(responseToolCalls))
	for _, call := range responseToolCalls {
		bindings = append(bindings, executionstore.ToolCallBindingInput{
			ProviderCallID: call.ID,
			Type:           toolcatalog.ToolTypeBuiltIn,
		})
	}
	_, calls, err := store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          projectID,
			AgentID:            agent.ID,
			RuntimeLockID:      lock.ID,
			ModelCallContextID: modelContext.ID,
			ProviderResponse:   providerResponse,
			ToolCallBindings:   bindings,
		},
	)
	if err != nil || len(calls) != len(responseToolCalls) {
		t.Fatalf("record tool calls err=%v calls=%d want=%d", err, len(calls), len(responseToolCalls))
	}
	callsByProviderID := make(map[string]executionstore.ToolCallRecord, len(calls))
	for _, call := range calls {
		allowed, markErr := store.Execution().MarkToolCallReady(
			ctx,
			executionstore.MarkToolCallReadyInput{
				ProjectID:     projectID,
				AgentID:       agent.ID,
				ID:            call.ID,
				RuntimeLockID: lock.ID,
			},
		)
		if markErr != nil {
			t.Fatalf("mark tool call %s permission allowed: %v", call.ProviderCallID, markErr)
		}
		callsByProviderID[allowed.ProviderCallID] = allowed
	}
	primaryRecord, ok := callsByProviderID[primaryToolCall.ID]
	if !ok {
		t.Fatalf("primary tool call %s missing from accepted proposal batch", primaryToolCall.ID)
	}
	return agent, primaryRecord, callsByProviderID, lock, binding
}

func quote(value string) string {
	body, _ := json.Marshal(value)
	return string(body)
}
