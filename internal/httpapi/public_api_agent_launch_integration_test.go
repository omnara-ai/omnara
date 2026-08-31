//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestPublicAgentLaunchFlow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := newIntegrationStore(pool)
	project := bootstrapPublicHTTPProject(t, handler, "agent-launch")

	machine := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/machines",
		`{"display_name":"launch machine"}`,
		"idem-agent-launch-machine",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	machineName := machine["display_name"].(string)
	ungrantedSource := "instruction: Use the machine when helpful.\n" +
		"model:\n" +
		"  provider_config: openai-prod\n" +
		"  name: gpt-test\n" +
		"machine_sources:\n" +
		"  - machine_name: " + machineName + "\n" +
		"tools:\n" +
		"  run_command: {}\n"
	createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"agent-launch-ungranted-machine",
		"yaml",
		ungrantedSource,
		project.AdminToken,
		http.StatusBadRequest,
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/machine-grants",
		`{"machine_id":"`+machine["id"].(string)+`"}`,
		"idem-agent-launch-grant",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	sourceYAML := "instruction: Use the machine when helpful.\n" +
		"model:\n" +
		"  provider_config: openai-prod\n" +
		"  name: gpt-test\n" +
		"machine_sources:\n" +
		"  - machine_name: " + machineName + "\n" +
		"    cwd: /workspace\n" +
		"tools:\n" +
		"  run_command: {}\n"
	config := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"agent-launch",
		"yaml",
		sourceYAML,
		project.AdminToken,
		http.StatusCreated,
	)
	warnings := config["warnings"].([]any)
	if len(warnings) != 1 {
		t.Fatalf("config warnings = %v, want missing machine tools warning", warnings)
	}
	warning := warnings[0].(map[string]any)
	if warning["code"] != "missing_recommended_machine_tools" ||
		!strings.Contains(warning["message"].(string), "write_process") ||
		!strings.Contains(warning["message"].(string), "upload_artifact") ||
		!strings.Contains(warning["message"].(string), "download_artifact") {
		t.Fatalf("config warnings = %v, want missing machine tools warning", warnings)
	}
	profile := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		"agent-launch",
		"Agent Launch",
		config["id"].(string),
		project.AdminToken,
		http.StatusCreated,
	)
	profileWarnings := profile["current_config"].(map[string]any)["warnings"]
	if !reflect.DeepEqual(profileWarnings, warnings) {
		t.Fatalf("profile config warnings = %v, want %v", profileWarnings, warnings)
	}
	profileID := profile["id"].(string)
	configID := profile["current_config"].(map[string]any)["id"].(string)
	retargetYAML := "instruction: Updated default.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\nmachine_sources:\n  - machine_name: " + machineName + "\n    cwd: /workspace\ntools:\n  run_command: {}\n"
	retargetConfig := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"agent-launch-retarget",
		"yaml",
		retargetYAML,
		project.AdminToken,
		http.StatusCreated,
	)
	retargetConfigID := retargetConfig["id"].(string)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profileID+"/config",
		`{"config":"`+retargetConfigID+`"}`,
		"idem-agent-launch-retarget-missing-expected",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profileID+"/config",
		`{"config":"`+retargetConfigID+`","expected_current_config_id":"not-a-config-id"}`,
		"idem-agent-launch-retarget-invalid-expected",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	retargeted := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profileID+"/config",
		`{"config":"`+retargetConfigID+`","expected_current_config_id":"`+configID+`"}`,
		"idem-agent-launch-retarget",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	retargetedConfigID := retargeted["current_config"].(map[string]any)["id"].(string)
	if retargetedConfigID == configID {
		t.Fatalf("retarget should create a new current config: %+v", retargeted)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profileID+"/config",
		`{"config":"`+configID+`","expected_current_config_id":"`+configID+`"}`,
		"idem-agent-launch-retarget-stale",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	invalidMessage := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"config":"`+configID+`","message":"before\u0000after"}`,
		"idem-agent-launch-invalid-message",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if invalidMessage["code"] != "invalid_request" {
		t.Fatalf("invalid launch message response = %+v, want invalid_request", invalidMessage)
	}

	explicitConfig := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"profile":"`+profileID+`","config":"`+configID+`","message":"start first"}`,
		"idem-agent-launch-first",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	firstAgent := explicitConfig["agent"].(map[string]any)
	if firstAgent["current_config_id"] != configID {
		t.Fatalf(
			"explicit launch did not pin requested config: %+v",
			explicitConfig,
		)
	}
	if firstAgent["state"] != "active" {
		t.Fatalf("new public agent state = %s, want active", firstAgent["state"])
	}
	if explicitConfig["message"] != nil ||
		explicitConfig["agent_input"] == nil {
		t.Fatalf(
			"expected launch initial agent input without message projection: %+v",
			explicitConfig,
		)
	}
	replayedFirst := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"profile":"`+profileID+`","config":"`+retargetedConfigID+`","message":"ignored\u0000retry body"}`,
		"idem-agent-launch-first",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	replayedFirstAgent := replayedFirst["agent"].(map[string]any)
	if len(replayedFirst) != 1 ||
		replayedFirstAgent["id"] != firstAgent["id"] ||
		replayedFirstAgent["current_config_id"] != configID {
		t.Fatalf(
			"changed replay body did not return the current original agent: original=%+v replay=%+v",
			explicitConfig,
			replayedFirst,
		)
	}

	configOnly := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"config":"`+retargetedConfigID+`"}`,
		"idem-agent-launch-config-only",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	if configOnly["agent"].(map[string]any)["current_config_id"] != retargetedConfigID {
		t.Fatalf(
			"config-only launch did not use requested config: %+v",
			configOnly,
		)
	}

	retargetedLaunch := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"profile":"`+profileID+`","config":"`+retargetedConfigID+`"}`,
		"idem-agent-launch-retargeted",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	bindings := retargetedLaunch["machine_bindings"].([]any)
	if retargetedLaunch["agent"].(map[string]any)["current_config_id"] != retargetedConfigID ||
		len(bindings) != 1 {
		t.Fatalf(
			"retargeted launch did not use machine-backed config: %+v",
			retargetedLaunch,
		)
	}
	binding := bindings[0].(map[string]any)
	if _, ok := binding["metadata"]; ok {
		t.Fatalf(
			"machine binding public response must not expose raw storage metadata: %+v",
			binding,
		)
	}
	if binding["machine_ref"] == "" {
		t.Fatalf("machine binding response missing machine_ref: %+v", binding)
	}
	if retargetedLaunch["agent_config"].(map[string]any)["model"] == nil ||
		retargetedLaunch["agent_config"].(map[string]any)["instruction_hash"] == "" {
		t.Fatalf("launch config projection missing model/instruction evidence: %+v", retargetedLaunch["agent_config"])
	}
	launchWarnings := retargetedLaunch["agent_config"].(map[string]any)["warnings"]
	if !reflect.DeepEqual(launchWarnings, warnings) {
		t.Fatalf("launch config warnings = %v, want %v", launchWarnings, warnings)
	}
	retargetedAgentID := retargetedLaunch["agent"].(map[string]any)["id"].(string)
	archivedResponse := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+retargetedAgentID+"/archive",
		``,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if archivedResponse["agent"].(map[string]any)["state"] != "archived" {
		t.Fatalf("archive agent response = %+v, want archived", archivedResponse)
	}
	archivedRead := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+retargetedAgentID,
		``,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if archivedRead["agent"].(map[string]any)["state"] != "archived" {
		t.Fatalf("archived agent read = %+v, want archived", archivedRead)
	}
	archivedReplay := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"profile":"`+profileID+`","config":"`+configID+`"}`,
		"idem-agent-launch-retargeted",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	archivedReplayAgent := archivedReplay["agent"].(map[string]any)
	if len(archivedReplay) != 1 ||
		archivedReplayAgent["id"] != retargetedAgentID ||
		archivedReplayAgent["state"] != "archived" {
		t.Fatalf("archived agent launch replay = %+v, want current archived agent", archivedReplay)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"profile":"`+profileID+`"}`,
		"idem-agent-launch-missing-config",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)

	other, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID: project.ProjectUUID,
			ProfileID: mustPublicHTTPID(
				t,
				publicid.KindAgentProfile,
				profileID,
			),
			AgentConfigID: mustPublicHTTPID(
				t,
				publicid.KindAgentConfig,
				configID,
			),
			LaunchedBy:     httpUserPrincipal(project.AdminUserUUID),
			IdempotencyKey: "idem-agent-launch-storage-same-config",
		},
	)
	if err != nil {
		t.Fatalf("second storage launch from same config: %v", err)
	}
	if other.AgentConfig.ID != mustPublicHTTPID(
		t,
		publicid.KindAgentConfig,
		configID,
	) {
		t.Fatalf(
			"one config should back many agents without changing config pin: %+v",
			other,
		)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		project.ProjectPath+"/agent-profiles/"+profileID,
		"",
		"idem-agent-profile-delete",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		project.ProjectPath+"/agent-profiles/"+profileID,
		"",
		"",
		http.StatusNoContent,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agent-profiles/"+profileID,
		``,
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	otherAgentID, err := publicid.Encode(publicid.KindAgent, other.Agent.ID)
	if err != nil {
		t.Fatalf("encode other agent id: %v", err)
	}
	launchedAfterDelete := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+otherAgentID,
		``,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	launchedAfterDeleteAgent := launchedAfterDelete["agent"].(map[string]any)
	if launchedAfterDeleteAgent["id"] != otherAgentID || launchedAfterDeleteAgent["state"] != "active" {
		t.Fatalf(
			"profile deletion should not delete launched agents: %+v",
			launchedAfterDelete,
		)
	}
}

func TestPublicAgentConfigChangeAcceptsLiveMCPDiff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "agent-config-live-policy")
	sourceYAML := "instruction: Original instruction.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n"
	config := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"config-policy",
		"yaml",
		sourceYAML,
		project.AdminToken,
		http.StatusCreated,
	)
	profile := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		"config-policy",
		"Config Policy",
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
		"idem-config-policy-agent",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	agentID := launched["agent"].(map[string]any)["id"].(string)
	changedYAML := `instruction: Add MCP.
model:
  provider_config: openai-prod
  name: gpt-test
mcp:
  docs:
    url: https://mcp.example.com
`
	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/config",
		`{"source_format":"yaml","source":`+quotedJSONString(changedYAML)+`}`,
		"idem-config-policy-mcp-change",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if response["agent_config"] == nil || response["agent_input"] == nil {
		t.Fatalf("unexpected config change response: %+v", response)
	}
}

func TestPublicAgentProfilesRejectSourceAuthoring(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "agent-profile-bookmark-only")
	sourceYAML := "instruction: Profiles point to configs.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n"
	config := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"agent-profile-bookmark-only",
		"yaml",
		sourceYAML,
		project.AdminToken,
		http.StatusCreated,
	)
	profile := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		"agent-profile-bookmark-only",
		"Bookmark Only",
		config["id"].(string),
		project.AdminToken,
		http.StatusCreated,
	)

	createResponse := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles",
		`{"name":"Old Shape","source_format":"yaml","source":`+quotedJSONString(
			sourceYAML,
		)+`}`,
		"idem-agent-profile-old-create",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if errText, _ := createResponse["error"].(string); !strings.Contains(errText, "unsupported") {
		t.Fatalf(
			"profile create should reject source fields as unknown: %+v",
			createResponse,
		)
	}
	updateResponse := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profile["id"].(string)+"/config",
		`{"source_format":"yaml","source":`+quotedJSONString(
			sourceYAML,
		)+`,"expected_current_config_id":"`+config["id"].(string)+`"}`,
		"idem-agent-profile-old-retarget",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	if errText, _ := updateResponse["error"].(string); !strings.Contains(errText, "unsupported") {
		t.Fatalf(
			"profile retarget should reject source fields as unknown: %+v",
			updateResponse,
		)
	}

	missingConfigID := testPublicID(
		t,
		publicid.KindAgentConfig,
		httpTestID("missing-agent-profile-config"),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles",
		`{"name":"Missing Config","config":"`+missingConfigID+`"}`,
		"idem-agent-profile-missing-config",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles/"+profile["id"].(string)+"/config",
		`{"config":"`+missingConfigID+`","expected_current_config_id":"`+config["id"].(string)+`"}`,
		"idem-agent-profile-retarget-missing-config",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
}

func TestPublicAgentConfigValidatesMCPAuthSecretReferences(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "agent-config-mcp-auth")

	genericSecret := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"project","project_id":"`+project.ProjectID+`"},"name":"github-bearer","material":{"kind":"generic","value":"gh-token"}}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	oauthSecret := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"project","project_id":"`+project.ProjectID+`"},"name":"github-oauth","material":{"kind":"oauth_token_set","access_token":"gh-access"}}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	orgSecret := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"org"},"name":"ungranted-bearer","material":{"kind":"generic","value":"org-token"}}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)

	bearerSource := "instruction: Use authenticated MCP.\n" +
		"model:\n" +
		"  provider_config: openai-prod\n" +
		"  name: gpt-test\n" +
		"mcp:\n" +
		"  github:\n" +
		"    url: https://api.githubcopilot.com/mcp\n" +
		"    auth:\n" +
		"      type: bearer\n" +
		"      secret_id: " + genericSecret["id"].(string) + "\n"
	createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"mcp-auth-bearer",
		"yaml",
		bearerSource,
		project.AdminToken,
		http.StatusCreated,
	)

	oauthSource := "instruction: Use authenticated MCP.\n" +
		"model:\n" +
		"  provider_config: openai-prod\n" +
		"  name: gpt-test\n" +
		"mcp:\n" +
		"  github:\n" +
		"    url: https://api.githubcopilot.com/mcp\n" +
		"    auth:\n" +
		"      type: oauth\n" +
		"      secret_id: " + oauthSecret["id"].(string) + "\n"
	createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"mcp-auth-oauth",
		"yaml",
		oauthSource,
		project.AdminToken,
		http.StatusCreated,
	)

	wrongKindSource := "instruction: Use authenticated MCP.\n" +
		"model:\n" +
		"  provider_config: openai-prod\n" +
		"  name: gpt-test\n" +
		"mcp:\n" +
		"  github:\n" +
		"    url: https://api.githubcopilot.com/mcp\n" +
		"    auth:\n" +
		"      type: oauth\n" +
		"      secret_id: " + genericSecret["id"].(string) + "\n"
	createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"mcp-auth-wrong-kind",
		"yaml",
		wrongKindSource,
		project.AdminToken,
		http.StatusBadRequest,
	)

	ungrantedSource := "instruction: Use authenticated MCP.\n" +
		"model:\n" +
		"  provider_config: openai-prod\n" +
		"  name: gpt-test\n" +
		"mcp:\n" +
		"  github:\n" +
		"    url: https://api.githubcopilot.com/mcp\n" +
		"    auth:\n" +
		"      type: bearer\n" +
		"      secret_id: " + orgSecret["id"].(string) + "\n"
	createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"mcp-auth-ungranted",
		"yaml",
		ungrantedSource,
		project.AdminToken,
		http.StatusBadRequest,
	)
}

func TestPublicAgentConfigAcceptsJSONSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "agent-config-json-source")
	sourceJSON := `{"instruction":"Configured from JSON.","model":{"provider_config":"openai-prod","name":"gpt-test"}}`

	config := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"agent-config-json-source",
		"json",
		sourceJSON,
		project.AdminToken,
		http.StatusCreated,
	)
	if config["source_format"] != "json" || config["source"] != sourceJSON {
		t.Fatalf("JSON-authored config source was not preserved: %+v", config)
	}
	model := config["model"].(map[string]any)
	if model["provider_config"] != "openai-prod" || model["name"] != "gpt-test" || config["instruction_hash"] == "" {
		t.Fatalf("JSON-authored config did not compile into runtime projection: %+v", config)
	}
}
