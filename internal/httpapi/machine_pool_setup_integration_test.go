//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestPublicMachinePoolSetupLaunchFlow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool, WithPublicURL("https://app.omnara.test"))
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "machine-pool-setup")
	otherProject := bootstrapPublicHTTPProject(t, handler, "machine-pool-setup-other")

	providerAuthSecretID := createPublicHTTPMachinePoolProviderAuthSecret(
		t,
		handler,
		project,
		"machine-pool-provider-auth",
		"test-token",
	)
	providerAuthSecretField := `,"provider_auth_secret_id":"` + providerAuthSecretID + `"`
	poolBody := `{"name":"default","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{"SECRET_THING":"pool-value","machine_id":"pool-machine"},"default_machine_secret_env":{"API_TOKEN":"` + providerAuthSecretID + `","storage_key":"` + providerAuthSecretID + `"},"default_machine_provider_options":{"image":"test","metro":"sfo","startup_script":"echo setup"},"default_cwd":"/pool","provider_config":{"api_base_url":"https://api.custom.example"}` + providerAuthSecretField + `,"max_total_machines":2,"max_total_cpu":4,"max_total_memory_mb":8192,"min_machine_cpu":1,"min_machine_memory_mb":1024,"max_machine_cpu":2,"max_machine_memory_mb":4096}`
	requiredPoolCaps := `,"max_total_cpu":2,"max_total_memory_mb":4096,"max_machine_cpu":2,"max_machine_memory_mb":4096`
	machinePool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		poolBody,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	poolID := machinePool["id"].(string)
	poolName := machinePool["name"].(string)
	if machinePool["management_kind"] != string(management.Tenant) {
		t.Fatalf("machine pool management_kind = %v, want tenant", machinePool["management_kind"])
	}
	if machinePool["provider"] != "unikraft" || machinePool["max_total_machines"].(float64) != 2 ||
		machinePool["min_machine_cpu"].(float64) != 1 ||
		machinePool["min_machine_memory_mb"].(float64) != 1024 ||
		machinePool["max_machine_cpu"].(float64) != 2 ||
		machinePool["max_machine_memory_mb"].(float64) != 4096 {
		t.Fatalf("unexpected pool response: %+v", machinePool)
	}
	if machinePool["runtime_protection_enabled"] != false {
		t.Fatalf("default runtime_protection_enabled = %v, want false", machinePool["runtime_protection_enabled"])
	}
	defaultProviderOptions := machinePool["default_machine_provider_options"].(map[string]any)
	if machinePool["default_cwd"] != "/pool" || defaultProviderOptions["image"] != "test" {
		t.Fatalf("unexpected pool default_machine fields: %+v", machinePool)
	}
	if defaultProviderOptions["startup_script"] != "echo setup" {
		t.Fatalf("unexpected pool default_machine_provider_options startup_script: %+v", machinePool)
	}
	defaultMachineEnv := machinePool["default_machine_env"].(map[string]any)
	if defaultMachineEnv["SECRET_THING"] != "pool-value" || defaultMachineEnv["machine_id"] != "pool-machine" {
		t.Fatalf("unexpected pool default_machine_env: %+v", machinePool)
	}
	defaultMachineSecretEnv := machinePool["default_machine_secret_env"].(map[string]any)
	if defaultMachineSecretEnv["API_TOKEN"] != providerAuthSecretID || defaultMachineSecretEnv["storage_key"] != providerAuthSecretID {
		t.Fatalf("unexpected pool default_machine_secret_env: %+v", machinePool)
	}
	providerConfig := machinePool["provider_config"].(map[string]any)
	if providerConfig["api_base_url"] != "https://api.custom.example" {
		t.Fatalf("unexpected provider config response: %+v", machinePool)
	}
	if _, ok := providerConfig["allowed_images"]; ok {
		t.Fatalf("provider config materialized allowed_images: %+v", providerConfig)
	}
	if _, ok := providerConfig["allowed_metros"]; ok {
		t.Fatalf("provider config materialized allowed_metros: %+v", providerConfig)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"bad-provider-url","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_provider_options":{"image":"test","metro":"sfo"},"default_cwd":"/workspace","provider_config":{"api_base_url":"not-a-url"}`+providerAuthSecretField+`,"max_total_machines":1`+requiredPoolCaps+`}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"missing-default-machine-image","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_provider_options":{"metro":"sfo"},"default_cwd":"/workspace","provider_config":{}`+providerAuthSecretField+`,"max_total_machines":1`+requiredPoolCaps+`}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"bad-default-machine-provider-options","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_provider_options":{"image":"test","metro":"sfo","unknown":"bad"},"default_cwd":"/workspace","provider_config":{}`+providerAuthSecretField+`,"max_total_machines":1`+requiredPoolCaps+`}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"bad-default-machine-args","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_provider_options":{"image":"test","metro":"sfo","args":["echo","user"]},"default_cwd":"/workspace","provider_config":{}`+providerAuthSecretField+`,"max_total_machines":1`+requiredPoolCaps+`}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"bad-startup-script","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_provider_options":{"image":"test","metro":"sfo","startup_script":["echo","bad"]},"default_cwd":"/workspace","provider_config":{}`+providerAuthSecretField+`,"max_total_machines":1`+requiredPoolCaps+`}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	oversizedStartupScriptBody, err := json.Marshal(
		map[string]any{
			"name":                      "oversized-startup-script",
			"provider":                  "unikraft",
			"default_machine_memory_mb": 1024,
			"default_machine_cpu":       1,
			"default_machine_env":       map[string]string{},
			"default_machine_provider_options": map[string]any{
				"image":          "test",
				"metro":          "sfo",
				"startup_script": strings.Repeat("x", 64*1024+1),
			},
			"default_cwd":             "/workspace",
			"provider_config":         map[string]any{},
			"provider_auth_secret_id": providerAuthSecretID,
			"max_total_machines":      1,
			"max_total_cpu":           2,
			"max_total_memory_mb":     4096,
			"max_machine_cpu":         2,
			"max_machine_memory_mb":   4096,
		},
	)
	if err != nil {
		t.Fatalf("marshal oversized startup script body: %v", err)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		string(oversizedStartupScriptBody),
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"reserved-env","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{"OMNARA_MACHINE_TOKEN":"bad"},"default_machine_provider_options":{"image":"test","metro":"sfo"},"default_cwd":"/workspace","provider_config":{}`+providerAuthSecretField+`,"max_total_machines":1`+requiredPoolCaps+`}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"reserved-env-namespace","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{"OMNARA_FUTURE_SETTING":"bad"},"default_machine_provider_options":{"image":"test","metro":"sfo"},"default_cwd":"/workspace","provider_config":{}`+providerAuthSecretField+`,"max_total_machines":1`+requiredPoolCaps+`}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"reserved-startup-env","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{"OMNARA_STARTUP_SCRIPT_PAYLOAD":"bad"},"default_machine_provider_options":{"image":"test","metro":"sfo"},"default_cwd":"/workspace","provider_config":{}`+providerAuthSecretField+`,"max_total_machines":1`+requiredPoolCaps+`}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"bad-provider-config","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_provider_options":{"image":"test","metro":"sfo"},"default_cwd":"/workspace","provider_config":{"unknown":"bad"}`+providerAuthSecretField+`,"max_total_machines":1`+requiredPoolCaps+`}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	emptyImagesPool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"empty-allowed-images","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_provider_options":{"image":"test","metro":"sfo"},"default_cwd":"/workspace","provider_config":{"allowed_images":[]}`+providerAuthSecretField+`,"max_total_machines":1`+requiredPoolCaps+`}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if !strings.Contains(emptyImagesPool["error"].(string), "allowed_images must not be empty") {
		t.Fatalf("empty allowed_images response = %+v", emptyImagesPool)
	}
	emptyMetrosPool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"empty-allowed-metros","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_provider_options":{"image":"test","metro":"sfo"},"default_cwd":"/workspace","provider_config":{"allowed_metros":[]}`+providerAuthSecretField+`,"max_total_machines":1`+requiredPoolCaps+`}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if !strings.Contains(emptyMetrosPool["error"].(string), "allowed_metros must not be empty") {
		t.Fatalf("empty allowed_metros response = %+v", emptyMetrosPool)
	}
	replayedPool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		poolBody,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if replayedPool["id"] != poolID {
		t.Fatalf("pool replay changed id: original=%+v replay=%+v", machinePool, replayedPool)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"default","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_provider_options":{"image":"test","metro":"sfo"},"default_cwd":"/workspace","provider_config":{}`+providerAuthSecretField+`,"max_total_machines":3,"max_total_cpu":4,"max_total_memory_mb":8192,"max_machine_cpu":2,"max_machine_memory_mb":4096}`,
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+poolID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	pools := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if got := len(pools["data"].([]any)); got != 1 {
		t.Fatalf("listed pools = %d, want 1: %+v", got, pools)
	}
	providerAuthSecretUUID := mustPublicHTTPID(t, publicid.KindSecret, providerAuthSecretID)
	if _, err := store.Secrets().CreateSecretGrant(ctx, secretstore.CreateSecretGrantInput{OrgID: project.OrgUUID, SecretID: providerAuthSecretUUID, TargetProjectID: project.ProjectUUID, Actor: httpUserPrincipal(project.AdminUserUUID)}); err != nil {
		t.Fatalf("grant pool secret env to project: %v", err)
	}
	updatePoolBody := `{"name":"update-target","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_provider_options":{"image":"update-initial","metro":"sfo"},"default_cwd":"/workspace"` + providerAuthSecretField + `,"max_total_machines":2,"max_total_cpu":4,"max_total_memory_mb":8192,"max_machine_cpu":2,"max_machine_memory_mb":4096,"runtime_protection_enabled":false}`
	updatePool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		updatePoolBody,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	updatePoolID := updatePool["id"].(string)
	if updatePool["runtime_protection_enabled"] != false || updatePool["min_machine_cpu"] != nil ||
		updatePool["min_machine_memory_mb"] != nil {
		t.Fatalf("unexpected created pool defaults: %+v", updatePool)
	}
	patchedUnprotectedPool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+updatePoolID,
		`{"description":"still unprotected"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if patchedUnprotectedPool["runtime_protection_enabled"] != false {
		t.Fatalf(
			"omitted update changed runtime_protection_enabled = %v, want false",
			patchedUnprotectedPool["runtime_protection_enabled"],
		)
	}
	rotatedProviderAuthSecretID := createPublicHTTPMachinePoolProviderAuthSecret(
		t,
		handler,
		project,
		"machine-pool-provider-auth-rotated",
		"rotated-token",
	)
	poolUpdateBody := `{"name":"updated-target","description":"updated pool","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{"SECRET_THING":"updated-pool-value"},"default_machine_provider_options":{"image":"updated","metro":"sfo","startup_script":"echo updated"},"default_cwd":"/pool-updated","provider_auth_secret_id":"` + rotatedProviderAuthSecretID + `","max_total_machines":2,"max_total_cpu":4,"max_total_memory_mb":8192,"min_machine_cpu":1,"min_machine_memory_mb":1024,"max_machine_cpu":2,"max_machine_memory_mb":4096,"runtime_protection_enabled":true,"metadata":{"team":"infra"}}`
	updatedPool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+updatePoolID,
		poolUpdateBody,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if updatedPool["id"] != updatePoolID || updatedPool["provider"] != "unikraft" ||
		updatedPool["name"] != "updated-target" ||
		updatedPool["description"] != "updated pool" || updatedPool["default_cwd"] != "/pool-updated" ||
		updatedPool["provider_auth_secret_id"] != rotatedProviderAuthSecretID ||
		updatedPool["max_total_cpu"].(float64) != 4 ||
		updatedPool["min_machine_cpu"].(float64) != 1 ||
		updatedPool["min_machine_memory_mb"].(float64) != 1024 ||
		updatedPool["runtime_protection_enabled"] != true {
		t.Fatalf("unexpected updated pool response: %+v", updatedPool)
	}
	patchedPool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+updatePoolID,
		`{"description":"patched pool"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if patchedPool["id"] != updatePoolID || patchedPool["description"] != "patched pool" ||
		patchedPool["default_cwd"] != "/pool-updated" || patchedPool["max_total_cpu"].(float64) != 4 ||
		patchedPool["min_machine_cpu"].(float64) != 1 ||
		patchedPool["min_machine_memory_mb"].(float64) != 1024 ||
		patchedPool["runtime_protection_enabled"] != true {
		t.Fatalf("unexpected patched pool response: %+v", patchedPool)
	}
	zeroMinimumPool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+updatePoolID,
		`{"min_machine_cpu":0,"min_machine_memory_mb":0}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if zeroMinimumPool["min_machine_cpu"].(float64) != 0 ||
		zeroMinimumPool["min_machine_memory_mb"].(float64) != 0 {
		t.Fatalf("explicit zero minimums were not preserved: %+v", zeroMinimumPool)
	}
	clearedMinimumPool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+updatePoolID,
		`{"min_machine_cpu":null,"min_machine_memory_mb":null}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if clearedMinimumPool["min_machine_cpu"] != nil || clearedMinimumPool["min_machine_memory_mb"] != nil {
		t.Fatalf("null minimums were not cleared: %+v", clearedMinimumPool)
	}
	updatedEnv := updatedPool["default_machine_env"].(map[string]any)
	updatedProviderOptions := updatedPool["default_machine_provider_options"].(map[string]any)
	if updatedEnv["SECRET_THING"] != "updated-pool-value" ||
		updatedProviderOptions["image"] != "updated" ||
		updatedProviderOptions["startup_script"] != "echo updated" {
		t.Fatalf("unexpected updated pool default_machine fields: %+v", updatedPool)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+updatePoolID,
		`{"provider":"unikraft"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+updatePoolID,
		`{"provider_config":{"unknown":"bad"}}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+updatePoolID,
		`{"provider_auth_secret_id":"sec_invalid"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+updatePoolID,
		"",
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+updatePoolID,
		`{"name":null}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+updatePoolID,
		`{"max_total_cpu":null}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	archivedPoolBody := `{"name":"archived","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_provider_options":{"image":"archived","metro":"sfo"},"default_cwd":"/workspace","provider_config":{}` + providerAuthSecretField + `,"max_total_machines":1,"max_total_cpu":2,"max_total_memory_mb":4096,"max_machine_cpu":2,"max_machine_memory_mb":4096}`
	archivedPool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		archivedPoolBody,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+updatePoolID,
		`{"name":"archived"}`,
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+archivedPool["id"].(string),
		"",
		"",
		http.StatusNoContent,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+archivedPool["id"].(string),
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+archivedPool["id"].(string),
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	recreatedPool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		archivedPoolBody,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	if recreatedPool["id"] == archivedPool["id"] {
		t.Fatalf(
			"archived pool name reuse returned archived pool: archived=%+v recreated=%+v",
			archivedPool,
			recreatedPool,
		)
	}

	ungrantedPoolSource := "instruction: Use the machine when useful.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\nmachine_sources:\n  - machine_pool_name: " + poolName + "\ntools:\n  run_command: {}\n"
	createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"machine-pool-setup-ungranted-agent",
		"yaml",
		ungrantedPoolSource,
		project.AdminToken,
		http.StatusBadRequest,
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+poolID+`","max_total_cpu":-1}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+poolID+`","max_machine_cpu":3}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+poolID+`","min_machine_cpu":2}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+poolID+`","default_machine_provider_options_overlay":{"unknown":"bad"}}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+poolID+`","default_machine_env_overlay":[]}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+poolID+`","metadata":null}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	badProjectImageGrant := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+poolID+`","default_machine_provider_options_overlay":{"image":"other"},"max_total_cpu":4,"max_total_memory_mb":4096,"max_machine_cpu":2,"max_machine_memory_mb":4096}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if !strings.Contains(badProjectImageGrant["error"].(string), "provider_config.allowed_images") {
		t.Fatalf("bad project image grant error = %+v, want allowed_images context", badProjectImageGrant)
	}

	poolGrantBody := `{"machine_pool_id":"` + poolID + `","description":"default project pool","default_machine_memory_mb":2048,"default_machine_env_overlay":{"SECRET_THING":"project-value","PROJECT_ONLY":"yes","uri":"project-uri"},"default_machine_secret_env_overlay":{"process_id":"` + providerAuthSecretID + `"},"default_cwd":"/project","max_total_machines":2,"max_total_cpu":4,"max_total_memory_mb":4096,"min_machine_cpu":1,"min_machine_memory_mb":2048,"max_machine_cpu":2,"max_machine_memory_mb":4096}`
	poolGrant := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		poolGrantBody,
		"idem-machine-pool-setup-grant",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	poolGrantID := poolGrant["id"].(string)
	if poolGrant["machine_pool_id"] != poolID {
		t.Fatalf("unexpected pool grant response: %+v", poolGrant)
	}
	if poolGrant["default_cwd"] != "/project" || poolGrant["default_machine_memory_mb"].(float64) != 2048 ||
		poolGrant["max_total_cpu"].(float64) != 4 ||
		poolGrant["max_total_memory_mb"].(float64) != 4096 ||
		poolGrant["min_machine_cpu"].(float64) != 1 ||
		poolGrant["min_machine_memory_mb"].(float64) != 2048 ||
		poolGrant["max_machine_cpu"].(float64) != 2 ||
		poolGrant["max_machine_memory_mb"].(float64) != 4096 {
		t.Fatalf("unexpected pool grant default_machine fields/caps: %+v", poolGrant)
	}
	poolGrantEnv := poolGrant["default_machine_env_overlay"].(map[string]any)
	if poolGrantEnv["SECRET_THING"] != "project-value" || poolGrantEnv["PROJECT_ONLY"] != "yes" ||
		poolGrantEnv["uri"] != "project-uri" {
		t.Fatalf("unexpected pool grant default_machine_env_overlay: %+v", poolGrant)
	}
	poolGrantSecretEnv := poolGrant["default_machine_secret_env_overlay"].(map[string]any)
	if poolGrantSecretEnv["process_id"] != providerAuthSecretID {
		t.Fatalf("unexpected pool grant default_machine_secret_env_overlay: %+v", poolGrant)
	}
	projectAdmin, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email:       "machine-pool-project-admin@example.com",
		DisplayName: "Project Admin",
	},
	)
	if err != nil {
		t.Fatalf("create project admin: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: project.OrgUUID, UserID: projectAdmin.ID, Role: "member"},
	); err != nil {
		t.Fatalf("add project admin org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     project.OrgUUID,
			ProjectID: project.ProjectUUID,
			UserID:    projectAdmin.ID,
			Role:      "admin",
		},
	); err != nil {
		t.Fatalf("add project admin membership: %v", err)
	}
	projectAdminPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: projectAdmin.ID,
			Name:   "project admin",
		},
	)
	if err != nil {
		t.Fatalf("create project admin token: %v", err)
	}
	projectAdminToken := projectAdminPAT.Token
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+poolID+`","description":"duplicate active project pool"}`,
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+poolID+`","description":"project admin cannot grant org pool"}`,
		"",
		http.StatusForbidden,
		authHeaders(projectAdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+testPublicID(t, publicid.KindMachinePool, httpTestID("missing-pool"))+`"}`,
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		otherProject.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+poolID+`"}`,
		"",
		http.StatusNotFound,
		authHeaders(otherProject.AdminToken),
	)
	replayedPoolGrant := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		poolGrantBody,
		"idem-machine-pool-setup-grant",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if replayedPoolGrant["id"] != poolGrantID {
		t.Fatalf("pool grant replay changed grant: original=%+v replay=%+v", poolGrant, replayedPoolGrant)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+poolID+`","description":"different"}`,
		"idem-machine-pool-setup-grant",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	projectAdminPoolGrant := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/machine-pool-grants/"+poolGrantID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/machine-pool-grants/"+poolGrantID,
		"",
		"",
		http.StatusOK,
		authHeaders(projectAdminToken),
	)
	projectAdminPoolGrantEnv := projectAdminPoolGrant["default_machine_env_overlay"].(map[string]any)
	if projectAdminPoolGrantEnv["SECRET_THING"] != "project-value" ||
		projectAdminPoolGrantEnv["PROJECT_ONLY"] != "yes" ||
		projectAdminPoolGrantEnv["uri"] != "project-uri" {
		t.Fatalf("project admin pool grant env = %+v, want plaintext env", projectAdminPoolGrant)
	}
	projectAdminPoolGrantSecretEnv := projectAdminPoolGrant["default_machine_secret_env_overlay"].(map[string]any)
	if projectAdminPoolGrantSecretEnv["process_id"] != providerAuthSecretID {
		t.Fatalf("project admin pool grant secret_env = %+v, want secret_env", projectAdminPoolGrant)
	}
	poolGrants := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/machine-pool-grants",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/machine-pool-grants",
		"",
		"",
		http.StatusOK,
		authHeaders(projectAdminToken),
	)
	if got := len(poolGrants["data"].([]any)); got != 1 {
		t.Fatalf("listed pool grants = %d, want 1: %+v", got, poolGrants)
	}

	patchedPoolGrant := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		project.ProjectPath+"/machine-pool-grants/"+poolGrantID,
		`{"description":"patched project pool","default_machine_memory_mb":1024,"min_machine_cpu":null,"min_machine_memory_mb":1024,"max_total_cpu":null,"default_machine_env_overlay":{"PATCHED_ONLY":"yes"}}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if patchedPoolGrant["id"] != poolGrantID ||
		patchedPoolGrant["description"] != "patched project pool" ||
		patchedPoolGrant["default_machine_memory_mb"].(float64) != 1024 ||
		patchedPoolGrant["min_machine_cpu"] != nil ||
		patchedPoolGrant["min_machine_memory_mb"].(float64) != 1024 ||
		patchedPoolGrant["max_total_cpu"] != nil ||
		patchedPoolGrant["max_machine_cpu"].(float64) != 2 ||
		patchedPoolGrant["default_cwd"] != "/project" {
		t.Fatalf("patched pool grant mismatch: %+v", patchedPoolGrant)
	}
	patchedPoolGrantEnv := patchedPoolGrant["default_machine_env_overlay"].(map[string]any)
	if patchedPoolGrantEnv["PATCHED_ONLY"] != "yes" || len(patchedPoolGrantEnv) != 1 {
		t.Fatalf("patched pool grant env overlay should be replaced whole: %+v", patchedPoolGrant)
	}
	patchedPoolGrantSecretEnv := patchedPoolGrant["default_machine_secret_env_overlay"].(map[string]any)
	if patchedPoolGrantSecretEnv["process_id"] != providerAuthSecretID {
		t.Fatalf("patched pool grant secret env overlay should be kept: %+v", patchedPoolGrant)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		project.ProjectPath+"/machine-pool-grants/"+poolGrantID,
		`{}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		project.ProjectPath+"/machine-pool-grants/"+poolGrantID,
		`{"max_machine_cpu":3}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		project.ProjectPath+"/machine-pool-grants/"+poolGrantID,
		`{"description":"project admin cannot edit org pool grant"}`,
		"",
		http.StatusForbidden,
		authHeaders(projectAdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		project.ProjectPath+"/machine-pool-grants/"+testPublicID(t, publicid.KindProjectMachinePoolGrant, httpTestID("missing-pool-grant")),
		`{"description":"missing"}`,
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	restoredPoolGrant := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		project.ProjectPath+"/machine-pool-grants/"+poolGrantID,
		`{"description":"default project pool","default_machine_memory_mb":2048,"max_total_cpu":4,"default_machine_env_overlay":{"SECRET_THING":"project-value","PROJECT_ONLY":"yes","uri":"project-uri"}}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if restoredPoolGrant["description"] != "default project pool" ||
		restoredPoolGrant["default_machine_memory_mb"].(float64) != 2048 ||
		restoredPoolGrant["max_total_cpu"].(float64) != 4 {
		t.Fatalf("restored pool grant mismatch: %+v", restoredPoolGrant)
	}

	overCountSourceYAML := "instruction: Use too many machines.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\nmachine_sources:\n  - machine_pool_name: " + poolName + "\n    max_machines: 3\n    initial_num_machines: 3\ntools:\n  run_command: {}\n"
	createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"machine-pool-setup-over-count-agent",
		"yaml",
		overCountSourceYAML,
		project.AdminToken,
		http.StatusCreated,
	)

	archivedGrantPoolBody := `{"name":"archived-grant","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_provider_options":{"image":"archived-grant","metro":"sfo"},"default_cwd":"/workspace","provider_config":{}` + providerAuthSecretField + `,"max_total_machines":1,"max_total_cpu":2,"max_total_memory_mb":4096,"max_machine_cpu":2,"max_machine_memory_mb":4096}`
	archivedGrantPool := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		archivedGrantPoolBody,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	archivedGrantPoolID := archivedGrantPool["id"].(string)
	archivedGrantBody := `{"machine_pool_id":"` + archivedGrantPoolID + `","description":"archived pool replay"}`
	archivedGrant := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		archivedGrantBody,
		"idem-archived-pool-grant-replay",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+archivedGrantPoolID,
		"",
		"",
		http.StatusNoContent,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		archivedGrantBody,
		"idem-archived-pool-grant-replay",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		archivedGrantBody,
		"idem-new-archived-pool-grant",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	deletedPoolGrantID := mustPublicHTTPID(t, publicid.KindProjectMachinePoolGrant, archivedGrant["id"].(string))
	if _, err := store.Execution().GetProjectMachinePoolGrant(
		ctx,
		project.OrgUUID,
		project.ProjectUUID,
		deletedPoolGrantID,
	); !storeerr.IsNotFound(err) {
		t.Fatalf("deleted pool grant lookup error = %v, want not found", err)
	}

	badSourceYAML := "instruction: Use the machine when useful.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\nmachine_sources:\n  - machine_pool_name: " + poolName + "\n    machine_provider_options_overlay:\n      unknown: bad\ntools:\n  run_command: {}\n"
	createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"machine-pool-setup-bad-agent-machine-config",
		"yaml",
		badSourceYAML,
		project.AdminToken,
		http.StatusBadRequest,
	)
	badMetroSourceYAML := "instruction: Use the machine when useful.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\nmachine_sources:\n  - machine_pool_name: " + poolName + "\n    machine_provider_options_overlay:\n      metro: iad\ntools:\n  run_command: {}\n"
	badMetroConfig := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"machine-pool-setup-bad-metro",
		"yaml",
		badMetroSourceYAML,
		project.AdminToken,
		http.StatusBadRequest,
	)
	if !strings.Contains(badMetroConfig["error"].(string), "provider_config.allowed_metros") {
		t.Fatalf("bad metro config response = %+v, want allowed_metros error", badMetroConfig)
	}

	sourceYAML := "instruction: Use the machine when useful.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\nmachine_sources:\n  - machine_pool_name: " + poolName + "\n    max_machines: 2\n    initial_num_machines: 2\n    cwd: /workspace\n    machine_cpu: 2\n    env_overlay:\n      SECRET_THING: agent-value\n      AGENT_ONLY: \"yes\"\ntools:\n  run_command: {}\n"
	config := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"machine-pool-setup",
		"yaml",
		sourceYAML,
		project.AdminToken,
		http.StatusCreated,
	)
	profile := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		"machine-pool-setup",
		"Machine Pool Setup Agent",
		config["id"].(string),
		project.AdminToken,
		http.StatusCreated,
	)
	profileID := profile["id"].(string)
	configID := profile["current_config"].(map[string]any)["id"].(string)
	launched := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"profile":"`+profileID+`","config":"`+configID+`"}`,
		"idem-machine-pool-setup-agent",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	bindings := launched["machine_bindings"].([]any)
	if len(bindings) != 2 {
		t.Fatalf("expected two launch bindings: %+v", launched)
	}
	binding := bindings[0].(map[string]any)
	if binding["state"] != "attached" || binding["cwd"] != "/workspace" {
		t.Fatalf("unexpected launch binding: %+v", binding)
	}
	bindingEnv := binding["env_overlay"].(map[string]any)
	if bindingEnv["SECRET_THING"] != "agent-value" ||
		bindingEnv["AGENT_ONLY"] != "yes" ||
		bindingEnv["PROJECT_ONLY"] != nil {
		t.Fatalf("launch binding did not preserve agent env overlay: %+v", binding)
	}
	secondBinding := bindings[1].(map[string]any)
	if secondBinding["state"] != "attached" || secondBinding["cwd"] != "/workspace" ||
		secondBinding["id"] == binding["id"] ||
		secondBinding["machine_ref"] == binding["machine_ref"] {
		t.Fatalf("unexpected second launch binding: first=%+v second=%+v", binding, secondBinding)
	}
	machineID := mustPublicHTTPID(t, publicid.KindMachine, binding["machine_id"].(string))
	machine, err := store.Execution().GetMachine(ctx, project.OrgUUID, machineID)
	if err != nil {
		t.Fatalf("get launched pool machine: %v", err)
	}
	poolUUID := mustPublicHTTPID(t, publicid.KindMachinePool, poolID)
	if machine.SourceKind != "pool" || machine.MachinePoolID != poolUUID || machine.LifecycleState != "provisioning" ||
		machine.Provider != "unikraft" {
		t.Fatalf("unexpected launched machine: %+v", machine)
	}
	storedProvisioning, err := executionstore.MachineProvisioningFromRecord(machine)
	if err != nil {
		t.Fatalf("build launched machine provisioning: %v", err)
	}
	var startupScript string
	if err := json.Unmarshal(storedProvisioning.ProviderOptions["startup_script"], &startupScript); err != nil {
		t.Fatalf("unmarshal launched machine startup_script: %v", err)
	}
	if startupScript != "echo setup" {
		t.Fatalf("launched machine startup_script = %q, want echo setup", startupScript)
	}
	if storedProvisioning.CPU == nil || *storedProvisioning.CPU != 2 ||
		storedProvisioning.MemoryMB == nil || *storedProvisioning.MemoryMB != 2048 {
		t.Fatalf("launched machine did not snapshot resolved cpu/memory: %+v", storedProvisioning)
	}
	var storedEnv map[string]string
	if err := json.Unmarshal(machine.Env, &storedEnv); err != nil {
		t.Fatalf("decode launched machine env: %v", err)
	}
	if storedEnv["SECRET_THING"] != "project-value" ||
		storedEnv["PROJECT_ONLY"] != "yes" ||
		storedEnv["machine_id"] != "pool-machine" ||
		storedEnv["uri"] != "project-uri" ||
		storedEnv["AGENT_ONLY"] != "" {
		t.Fatalf("launched machine did not snapshot pool and grant env: %+v", storedEnv)
	}
	var storedSecretEnv map[string]string
	if err := json.Unmarshal(machine.SecretEnv, &storedSecretEnv); err != nil {
		t.Fatalf("decode launched machine secret env: %v", err)
	}
	if storedSecretEnv["API_TOKEN"] != providerAuthSecretID ||
		storedSecretEnv["storage_key"] != providerAuthSecretID ||
		storedSecretEnv["process_id"] != providerAuthSecretID {
		t.Fatalf("launched machine did not snapshot pool and grant secret env: %+v", storedSecretEnv)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		project.ProjectPath+"/machine-pool-grants/"+poolGrantID,
		"",
		"",
		http.StatusForbidden,
		authHeaders(projectAdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		project.ProjectPath+"/machine-pool-grants/"+poolGrantID,
		"",
		"",
		http.StatusNoContent,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		project.ProjectPath+"/machine-pool-grants/"+poolGrantID,
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"profile":"`+profileID+`","config":"`+configID+`"}`,
		"idem-machine-pool-setup-after-revoke",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
}

func TestPublicBlaxelMachinePoolOmitsCPU(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool, WithPublicURL("https://app.omnara.test"))
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "blaxel-machine-pool")
	providerAuthSecretID := createPublicHTTPMachinePoolProviderAuthSecret(
		t,
		handler,
		project,
		"blaxel-provider-auth",
		"test-token",
	)
	poolResponse := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"blaxel","provider":"blaxel","default_machine_memory_mb":1024,"default_machine_env":{},"default_machine_provider_options":{"image":"blaxel/base-image:latest","region":"us-pdx-1","startup_script":"echo ready"},"provider_config":{"workspace":"omnara"},"provider_auth_secret_id":"`+providerAuthSecretID+`","max_total_machines":2,"max_total_memory_mb":2048,"max_machine_memory_mb":1024}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	for _, field := range []string{"default_machine_cpu", "max_total_cpu", "min_machine_cpu", "max_machine_cpu"} {
		value, found := poolResponse[field]
		if !found || value != nil {
			t.Fatalf("blaxel pool %s = %#v, want null", field, value)
		}
	}
	poolID := poolResponse["id"].(string)
	poolUUID := mustPublicHTTPID(t, publicid.KindMachinePool, poolID)
	stored, err := store.Execution().GetMachinePool(ctx, project.OrgUUID, poolUUID)
	if err != nil {
		t.Fatalf("get blaxel machine pool: %v", err)
	}
	if stored.DefaultMachineCPU != nil || stored.MaxTotalCPU != nil || stored.MinMachineCPU != nil ||
		stored.MaxMachineCPU != nil {
		t.Fatalf(
			"stored blaxel cpu fields = default %v total %v minimum %v maximum %v",
			stored.DefaultMachineCPU,
			stored.MaxTotalCPU,
			stored.MinMachineCPU,
			stored.MaxMachineCPU,
		)
	}
	unsupportedCap := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+poolID+`","max_total_cpu":1}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if !strings.Contains(unsupportedCap["error"].(string), "max_total_cpu is not supported") {
		t.Fatalf("blaxel cpu cap response = %+v", unsupportedCap)
	}
	unsupportedOverride := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+poolID+`","default_machine_cpu":1}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if !strings.Contains(unsupportedOverride["error"].(string), "does not support cpu") {
		t.Fatalf("blaxel cpu override response = %+v", unsupportedOverride)
	}
	grant := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-pool-grants",
		`{"machine_pool_id":"`+poolID+`","max_total_memory_mb":2048,"max_machine_memory_mb":1024}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	if grant["default_machine_cpu"] != nil || grant["max_total_cpu"] != nil ||
		grant["min_machine_cpu"] != nil || grant["max_machine_cpu"] != nil {
		t.Fatalf("blaxel grant cpu fields = %+v", grant)
	}
}

func TestPublicMachinePoolAcceptsZeroTotalCaps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool, WithPublicURL("https://app.omnara.test"))
	project := bootstrapPublicHTTPProject(t, handler, "zero-cap-machine-pool")
	providerAuthSecretID := createPublicHTTPMachinePoolProviderAuthSecret(
		t,
		handler,
		project,
		"zero-cap-provider-auth",
		"test-token",
	)
	poolResponse := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"zero-cap","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_provider_options":{"image":"test","metro":"sfo"},"default_cwd":"/workspace","provider_config":{},"provider_auth_secret_id":"`+providerAuthSecretID+`","max_total_machines":0,"max_total_cpu":0,"max_total_memory_mb":0,"max_machine_cpu":2,"max_machine_memory_mb":4096}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	if poolResponse["max_total_machines"].(float64) != 0 ||
		poolResponse["max_total_cpu"].(float64) != 0 ||
		poolResponse["max_total_memory_mb"].(float64) != 0 {
		t.Fatalf("created pool total caps = %+v, want zero", poolResponse)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"negative-cap","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_provider_options":{"image":"test","metro":"sfo"},"default_cwd":"/workspace","provider_config":{},"provider_auth_secret_id":"`+providerAuthSecretID+`","max_total_machines":-1,"max_total_cpu":2,"max_total_memory_mb":4096,"max_machine_cpu":2,"max_machine_memory_mb":4096}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
}

func TestPublicDaytonaMachinePoolAcceptsOptionalDefaultsWithoutProviderResolution(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	daytona := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("machine pool configuration must not call Daytona")
	}))
	defer daytona.Close()

	handler := newIntegrationServer(pool, WithPublicURL("https://app.omnara.test"))
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "daytona-machine-pool")
	providerAuthSecretID := createPublicHTTPMachinePoolProviderAuthSecret(
		t,
		handler,
		project,
		"daytona-provider-auth",
		"daytona-token",
	)
	poolResponse := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		`{"name":"daytona","provider":"daytona","default_machine_cpu":2,"default_machine_memory_mb":4096,"default_machine_env":{},"default_machine_provider_options":{"snapshot":"team-snapshot","target":"us"},"provider_config":{"api_base_url":"`+daytona.URL+`","allowed_snapshots":["*"],"allowed_targets":["us"]},"provider_auth_secret_id":"`+providerAuthSecretID+`","max_total_machines":2,"max_total_cpu":4,"max_total_memory_mb":8192,"max_machine_cpu":2,"max_machine_memory_mb":4096}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	if poolResponse["default_machine_cpu"] != float64(2) ||
		poolResponse["default_machine_memory_mb"] != float64(4096) {
		t.Fatalf("daytona configured resources = %+v", poolResponse)
	}
	options := poolResponse["default_machine_provider_options"].(map[string]any)
	if options["snapshot"] != "team-snapshot" || options["target"] != "us" {
		t.Fatalf("daytona configured options = %+v", options)
	}
	poolID := poolResponse["id"].(string)
	stored, err := store.Execution().GetMachinePool(
		ctx,
		project.OrgUUID,
		mustPublicHTTPID(t, publicid.KindMachinePool, poolID),
	)
	if err != nil {
		t.Fatalf("get daytona machine pool: %v", err)
	}
	if stored.DefaultMachineCPU == nil || *stored.DefaultMachineCPU != 2 ||
		stored.DefaultMachineMemoryMB == nil || *stored.DefaultMachineMemoryMB != 4096 {
		t.Fatalf("stored daytona resources = cpu %v memory %v", stored.DefaultMachineCPU, stored.DefaultMachineMemoryMB)
	}
	updated := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+poolID,
		`{"default_machine_provider_options":{"snapshot":"team-large","target":"us"},"max_total_cpu":8,"max_total_memory_mb":16384,"max_machine_cpu":4,"max_machine_memory_mb":8192}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if updated["default_machine_cpu"] != float64(2) ||
		updated["default_machine_memory_mb"] != float64(4096) ||
		updated["default_machine_provider_options"].(map[string]any)["snapshot"] != "team-large" {
		t.Fatalf("updated daytona resources = %+v", updated)
	}
}

func TestPublicDefaultMachinePoolAgentConfigValidationDoesNotRequireProviderAuth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	keyWrapper := integrationKeyWrapper()
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(keyWrapper),
		storage.WithMachinePoolProviders(machinepool.DefaultCatalog()),
	)
	defaultPool := executionstore.DefaultMachinePoolTemplate{
		Name:                          "api-default-pool",
		Provider:                      "unikraft",
		ProviderAuthEnvVar:            "TEST_DEFAULT_POOL_TOKEN",
		DefaultMachineCPU:             intPtrForHTTPMachinePoolTest(1),
		DefaultMachineMemoryMB:        intPtrForHTTPMachinePoolTest(1024),
		DefaultMachineEnv:             json.RawMessage(`{}`),
		DefaultMachineProviderOptions: json.RawMessage(`{"image":"registry.example/daemon:latest","metro":"sfo"}`),
		ProviderConfig:                json.RawMessage(`{}`),
		MaxTotalMachines:              1,
		MaxTotalCPU:                   intPtrForHTTPMachinePoolTest(1),
		MaxTotalMemoryMB:              intPtrForHTTPMachinePoolTest(1024),
		MaxMachineCPU:                 intPtrForHTTPMachinePoolTest(1),
		MaxMachineMemoryMB:            intPtrForHTTPMachinePoolTest(1024),
	}
	manager := &machinepool.Manager{
		Execution: store.Execution(),
		Identity:  store.Identity(),
		Catalog:   machinepool.DefaultCatalog(),
		PublicURL: "https://app.omnara.test",
	}
	handler := newIntegrationHTTPHandler(withDefaultRequestOrigin(
		mustNewServer(
			t,
			store,
			WithSecretKeyWrapper(keyWrapper),
			WithPublicURL("https://app.omnara.test"),
			WithDefaultMachinePools([]executionstore.DefaultMachinePoolTemplate{defaultPool}),
			WithMachinePoolManager(manager),
		).Handler(),
		"https://app.omnara.test",
	), pool, store)
	project := bootstrapPublicHTTPProject(t, handler, "default-pool-agent-config")
	pools := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	poolData := pools["data"].([]any)
	if len(poolData) != 1 {
		t.Fatalf("default pool list response = %+v, want one pool", pools)
	}
	defaultPoolResponse := poolData[0].(map[string]any)
	defaultPoolID := defaultPoolResponse["id"].(string)
	if defaultPoolResponse["management_kind"] != string(management.Cluster) {
		t.Fatalf("default pool management_kind = %v, want cluster", defaultPoolResponse["management_kind"])
	}
	providerConfig := defaultPoolResponse["provider_config"].(map[string]any)
	if len(providerConfig) != 0 {
		t.Fatalf("default pool provider_config = %+v, want empty config", providerConfig)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+defaultPoolID,
		`{"description":"not allowed"}`,
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/machine-pools/"+defaultPoolID,
		"",
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)

	sourceYAML := "instruction: Use the default pool when useful.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\nmachine_sources:\n  - machine_pool_name: " + defaultPool.Name + "\n    max_machines: 1\n    initial_num_machines: 0\n    machine_provider_options_overlay:\n      startup_script: echo ready\ntools:\n  create_machine: {}\n"
	config := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"default-pool-agent-config",
		"yaml",
		sourceYAML,
		project.AdminToken,
		http.StatusCreated,
	)

	badImageSourceYAML := "instruction: Use the default pool when useful.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\nmachine_sources:\n  - machine_pool_name: " + defaultPool.Name + "\n    machine_provider_options_overlay:\n      image: registry.example/other:latest\ntools:\n  create_machine: {}\n"
	response := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"bad-default-pool-image-agent-config",
		"yaml",
		badImageSourceYAML,
		project.AdminToken,
		http.StatusBadRequest,
	)
	if !strings.Contains(response["error"].(string), "provider_config.allowed_images") {
		t.Fatalf("bad default pool image overlay error = %+v, want allowed_images", response)
	}

	badSourceYAML := "instruction: Use the default pool when useful.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\nmachine_sources:\n  - machine_pool_name: " + defaultPool.Name + "\n    machine_provider_options_overlay:\n      startup_script:\n        - echo bad\ntools:\n  create_machine: {}\n"
	response = createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"bad-default-pool-agent-config",
		"yaml",
		badSourceYAML,
		project.AdminToken,
		http.StatusBadRequest,
	)
	if !strings.Contains(response["error"].(string), "startup_script") {
		t.Fatalf("bad default pool provider validation error = %+v, want startup_script", response)
	}

	profile := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		"default-pool-agent-config",
		"Cluster Pool Agent",
		config["id"].(string),
		project.AdminToken,
		http.StatusCreated,
	)
	profileID := profile["id"].(string)
	configID := profile["current_config"].(map[string]any)["id"].(string)
	launched := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"profile":"`+profileID+`","config":"`+configID+`"}`,
		"idem-default-pool-agent-config-launch",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	agentID := launched["agent"].(map[string]any)["id"].(string)

	updateSourceYAML := "instruction: Updated instruction, same machine source.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\nmachine_sources:\n  - machine_pool_name: " + defaultPool.Name + "\n    max_machines: 1\n    initial_num_machines: 0\n    machine_provider_options_overlay:\n      startup_script: echo ready\ntools:\n  create_machine: {}\n"
	updated := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/config",
		`{"source_format":"yaml","source":`+quotedJSONString(updateSourceYAML)+`}`,
		"idem-default-pool-agent-config-update",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if updated["agent_config"] == nil || updated["agent_input"] == nil {
		t.Fatalf("default pool agent config update response missing config/input: %+v", updated)
	}

	response = requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/config",
		`{"source_format":"yaml","source":`+quotedJSONString(badSourceYAML)+`}`,
		"idem-default-pool-agent-config-bad-update",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if !strings.Contains(response["error"].(string), "startup_script") {
		t.Fatalf("bad default pool update provider validation error = %+v, want startup_script", response)
	}
}

func intPtrForHTTPMachinePoolTest(value int) *int {
	return &value
}

func createPublicHTTPMachinePoolProviderAuthSecret(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
	name, value string,
) string {
	t.Helper()
	secret := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"org"},"name":"`+name+`","material":{"kind":"generic","value":"`+value+`"}}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	return secret["id"].(string)
}
