//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestEditAgentConfigs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "agent-config-edit")
	headers := authHeaders(project.AdminToken)
	model := "model:\n  provider_config: openai-prod\n  name: gpt-test\n"
	config := createPublicHTTPAgentConfig(t, handler, project, "agent-config-edit", "yaml",
		"instruction: |\n  Be direct.\n  Ask before deleting.\n"+model, project.AdminToken, http.StatusCreated)
	configID := testutil.RequireType[string](t, config["id"])
	profile := createPublicHTTPAgentProfile(t, handler, project, "agent-config-edit", "Edit Fleet", configID,
		project.AdminToken, http.StatusCreated)
	profileID := testutil.RequireType[string](t, profile["id"])
	launch := func(seed, body string) string {
		launched := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agents",
			body, "idem-agent-config-edit-"+seed, http.StatusCreated, headers)
		return testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launched["agent"])["id"])
	}
	secondProfile := createPublicHTTPAgentProfile(t, handler, project, "agent-config-edit-second", "Second Fleet",
		configID, project.AdminToken, http.StatusCreated)
	secondProfileID := testutil.RequireType[string](t, secondProfile["id"])
	shared := launch("shared", `{"profile":"`+profileID+`","config":"`+configID+`"}`)
	personal := launch("personal", `{"profile":"`+profileID+`","config_source":{"source_format":"json","source":`+
		`{"instruction":"Hi Maya. Be direct. Ask before deleting.","model":{"provider_config":"openai-prod","name":"gpt-test"}}}}`)
	secondEarly := launch("second-early", `{"profile":"`+secondProfileID+`","config":"`+configID+`"}`)
	other := launch("other", `{"profile":"`+profileID+`","config_source":{"source_format":"yaml","source":`+
		quotedJSONString("instruction: Be calm. Ask before deleting.\n"+model)+`}}`)
	secondLate := launch("second-late", `{"profile":"`+secondProfileID+`","config":"`+configID+`"}`)

	edit := func(body, key string, want int) []map[string]any {
		t.Helper()
		response := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agent-configs/edit",
			body, key, want, headers)
		var out []map[string]any
		if want == http.StatusOK {
			for _, item := range testutil.RequireType[[]any](t, response["data"]) {
				out = append(out, testutil.RequireType[map[string]any](t, item))
			}
		}
		return out
	}
	source := func(configID string) string {
		t.Helper()
		config := requestJSONWithHeaders(t, handler, http.MethodGet, project.ProjectPath+"/agent-configs/"+configID,
			"", "", http.StatusOK, headers)
		return testutil.RequireType[string](t, config["source"])
	}
	currentSource := func(agentID string) string {
		t.Helper()
		agent := testutil.RequireType[map[string]any](t, requestJSONWithHeaders(t, handler, http.MethodGet,
			project.ProjectPath+"/agents/"+agentID, "", "", http.StatusOK, headers)["agent"])
		return source(testutil.RequireType[string](t, agent["current_config_id"]))
	}
	agentTargets := func(ids ...string) string {
		var targets []string
		for _, id := range ids {
			targets = append(targets, `{"type":"agent","agent_id":"`+id+`"}`)
		}
		return `"targets":[` + strings.Join(targets, ",") + `]`
	}
	targetIDs := func(results []map[string]any) []any {
		var ids []any
		for _, result := range results {
			target := testutil.RequireType[map[string]any](t, result["target"])
			if id, ok := target["agent_id"]; ok {
				ids = append(ids, id)
			} else {
				ids = append(ids, target["agent_profile_id"])
			}
		}
		return ids
	}

	edits := `"edits":[{"type":"replace_text","path":"/instruction","find":"Be direct.","replace":"Be direct and kind."},` +
		`{"type":"set","path":"/model/cache_retention","value":"long"}]`
	fleet := `{"type":"profile","agent_profile_id":"` + profileID + `","apply_to_instances":true}`
	body := `{"targets":[` + fleet + `,{"type":"agent","agent_id":"` + shared + `"}],` + edits + `}`
	data := edit(body, "", http.StatusOK)
	require.Equal(t, []any{profileID, shared, personal, other}, targetIDs(data))
	require.Equal(t, []any{map[string]any{"type": "profile", "agent_profile_id": profileID}, "updated", float64(2)},
		[]any{data[0]["target"], data[0]["status"], data[0]["current_generation"]})
	require.Equal(t, configID, data[0]["previous_config_id"])
	require.Equal(t, map[string]any{"type": "agent", "agent_id": shared}, data[1]["target"])
	require.Equal(t, []any{"updated", []any{"applied", "applied"}}, []any{data[1]["status"], data[1]["edits"]})
	require.Equal(t, "updated", data[2]["status"])
	require.Contains(t, source(testutil.RequireType[string](t, data[2]["current_config_id"])),
		"Hi Maya. Be direct and kind.")
	require.Equal(t, []any{"failed", "edit_conflict", []any{"text_not_found", "not_applied"},
		"edit conflict: edit 0 didn't match this config, so nothing was saved; " +
			"set on_failed_edit to ignore to save the other edits"},
		[]any{data[3]["status"], data[3]["code"], data[3]["edits"], data[3]["error"]})
	missed := edit(`{`+agentTargets(other)+`,"edits":[`+
		`{"type":"replace_text","path":"/instruction","find":"a","replace":"b"},`+
		`{"type":"replace_text","path":"/instruction","find":"Be direct.","replace":"x"},`+
		`{"type":"remove","path":"/tools/gone/permission"}]}`, "", http.StatusOK)
	require.Equal(t, "edit conflict: edits 1 and 2 didn't match this config, so nothing was saved; "+
		"set on_failed_edit to ignore to save the other edits", missed[0]["error"])
	partial := edit(`{`+agentTargets(other)+`,"on_failed_edit":"ignore",`+edits+`}`, "", http.StatusOK)
	require.Equal(t, []any{"updated", []any{"text_not_found", "applied"}},
		[]any{partial[0]["status"], partial[0]["edits"]})

	for _, result := range edit(body, "", http.StatusOK) {
		require.Equal(t, "unchanged", result["status"])
	}
	instancesOnly := func(id string) string {
		return `{"type":"profile","agent_profile_id":"` + id + `","apply_to_profile":false,"apply_to_instances":true}`
	}
	setLong := `"edits":[{"type":"set","path":"/model/cache_retention","value":"long"}]`
	require.Equal(t, []any{shared, personal, other, secondEarly, secondLate}, targetIDs(edit(`{"targets":[`+
		instancesOnly(profileID)+`,`+instancesOnly(secondProfileID)+`],`+setLong+`}`, "", http.StatusOK)))
	edit(`{"targets":[{"type":"profile","agent_profile_id":"`+profileID+`","apply_to_profile":false}],`+setLong+`}`,
		"", http.StatusBadRequest)

	insert := `{"targets":[` + fleet + `],"edits":[{"type":"replace_text","path":"/instruction",` +
		`"find":"Ask before deleting.","replace":"Ask before deleting. Log every command."}]}`
	inserted := edit(insert, "idem-agent-config-edit-insert", http.StatusOK)
	require.Equal(t, []any{profileID, shared, personal, other}, targetIDs(inserted))
	for _, result := range inserted {
		require.Equal(t, "updated", result["status"])
		require.Equal(t, 1, strings.Count(source(testutil.RequireType[string](t, result["current_config_id"])),
			"Log every command."))
	}
	intervening := edit(`{"targets":[`+fleet+`],"edits":[{"type":"replace_text",`+
		`"path":"/instruction","find":"Log every command.","replace":"Log every command!"}]}`, "", http.StatusOK)
	countConfigs := func() int {
		var count int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM agent_configs WHERE project_id = $1`,
			project.ProjectUUID).Scan(&count))
		return count
	}
	configsBefore := countConfigs()
	for i, replayed := range edit(insert, "idem-agent-config-edit-insert", http.StatusOK) {
		require.Equal(t, []any{"updated", inserted[i]["current_config_id"]},
			[]any{replayed["status"], replayed["current_config_id"]})
		require.NotContains(t, replayed, "edits")
		require.Contains(t, source(testutil.RequireType[string](t, intervening[i]["current_config_id"])),
			"Log every command!")
	}
	require.Equal(t, configsBefore, countConfigs())

	nulled := edit(`{`+agentTargets(shared)+`,"edits":[{"type":"set","path":"/mcp","value":null}]}`, "", http.StatusOK)
	require.Equal(t, "updated", nulled[0]["status"])
	nullItem := `"edits":[{"type":"add_item","path":"/skills","value":null}]`
	failedItem := edit(`{`+agentTargets(shared)+`,`+nullItem+`}`, "", http.StatusOK)
	require.Equal(t, []any{"failed", "invalid_request", []any{"not_applied"}},
		[]any{failedItem[0]["status"], failedItem[0]["code"], failedItem[0]["edits"]})
	statuses := func(results []map[string]any) []any {
		var out []any
		for _, result := range results {
			out = append(out, result["status"])
		}
		return out
	}
	kinder := `"edits":[{"type":"replace_text","path":"/instruction","find":"Be direct and kind.","replace":"Be kind."},` +
		`{"type":"set","path":"/model/cache_retention","value":"short"}]`
	stopped := edit(`{`+agentTargets(personal, other, shared)+`,"on_failed_edit":"stop",`+kinder+`}`, "", http.StatusOK)
	require.Equal(t, []any{"updated", "failed", "skipped"}, statuses(stopped))
	require.NotContains(t, stopped[2], "edits")
	require.Contains(t, currentSource(personal), "Be kind.")
	require.Contains(t, currentSource(shared), "Be direct and kind.")
	continued := edit(`{`+agentTargets(personal, other, shared)+`,`+kinder+`}`, "", http.StatusOK)
	require.Equal(t, []any{"unchanged", "failed", "updated"}, statuses(continued))
	require.Contains(t, currentSource(shared), "Be kind.")
	require.Contains(t, currentSource(other), "cache_retention: long")

	missing := testPublicID(t, publicid.KindAgent, httpTestID("agent-config-edit-missing"))
	setShort := `"edits":[{"type":"set","path":"/model/cache_retention","value":"short"}]`
	edit(`{`+agentTargets(missing)+`,`+setShort+`}`, "", http.StatusNotFound)
	profileOnly := edit(`{"targets":[{"type":"profile","agent_profile_id":"`+profileID+`"}],`+setShort+`}`,
		"", http.StatusOK)
	require.Equal(t, []any{[]any{profileID}, "updated"}, []any{targetIDs(profileOnly), profileOnly[0]["status"]})

	setOther := `{` + agentTargets(other) + `,` + setShort + `}`
	beforeArchive := edit(setOther, "idem-agent-config-edit-before-archive", http.StatusOK)
	require.Equal(t, "updated", beforeArchive[0]["status"])
	requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agents/"+other+"/archive", "",
		"idem-agent-config-edit-archive", http.StatusOK, headers)
	afterArchive := edit(setOther, "idem-agent-config-edit-before-archive", http.StatusOK)
	require.Equal(t, []any{"updated", beforeArchive[0]["current_config_id"]},
		[]any{afterArchive[0]["status"], afterArchive[0]["current_config_id"]})
	archived := edit(setOther, "", http.StatusOK)
	require.Equal(t, []any{"failed", "state_transition_conflict"}, []any{archived[0]["status"], archived[0]["code"]})
}

func TestEditAgentConfigsRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "agent-config-edit-retry")
	headers := authHeaders(project.AdminToken)
	config := func(seed, instruction string) string {
		created := createPublicHTTPAgentConfig(t, handler, project, "agent-config-edit-retry-"+seed, "yaml",
			"instruction: "+instruction+"\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
			project.AdminToken, http.StatusCreated)
		return testutil.RequireType[string](t, created["id"])
	}
	created := createPublicHTTPAgentProfile(t, handler, project, "agent-config-edit-retry", "Retry",
		config("ask", "Ask before deleting."), project.AdminToken, http.StatusCreated)
	profileID := testutil.RequireType[string](t, created["id"])
	launch := func(seed string) string {
		launched := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agents",
			`{"profile":"`+profileID+`"}`, "idem-agent-config-edit-retry-"+seed, http.StatusCreated, headers)
		return testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launched["agent"])["id"])
	}
	inserted := func(agentID string) int {
		agent := testutil.RequireType[map[string]any](t, requestJSONWithHeaders(t, handler, http.MethodGet,
			project.ProjectPath+"/agents/"+agentID, "", "", http.StatusOK, headers)["agent"])
		config := requestJSONWithHeaders(t, handler, http.MethodGet, project.ProjectPath+"/agent-configs/"+
			testutil.RequireType[string](t, agent["current_config_id"]), "", "", http.StatusOK, headers)
		return strings.Count(testutil.RequireType[string](t, config["source"]), "Log every command.")
	}
	edit := func(body, key string) []any {
		t.Helper()
		response := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agent-configs/edit",
			body, key, http.StatusOK, headers)
		var ids []any
		for _, item := range testutil.RequireType[[]any](t, response["data"]) {
			target := testutil.RequireType[map[string]any](t, testutil.RequireType[map[string]any](t, item)["target"])
			if id, ok := target["agent_id"]; ok {
				ids = append(ids, id)
			} else {
				ids = append(ids, target["agent_profile_id"])
			}
		}
		return ids
	}
	first, second := launch("first"), launch("second")
	insert := `"edits":[{"type":"replace_text","path":"/instruction",` +
		`"find":"Ask before deleting.","replace":"Ask before deleting. Log every command."}]}`
	fleet := `{"targets":[{"type":"profile","agent_profile_id":"` + profileID + `","apply_to_instances":true}],` + insert
	require.Equal(t, []any{profileID, first, second}, edit(fleet, "idem-agent-config-edit-retry-fleet"))
	late := launch("late")
	requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agents/"+first+"/archive", "",
		"idem-agent-config-edit-retry-archive", http.StatusOK, headers)
	require.Equal(t, []any{profileID, second}, edit(fleet, "idem-agent-config-edit-retry-fleet"))
	for _, agentID := range []string{first, second, late} {
		require.Equal(t, 1, inserted(agentID), agentID)
	}

	stale := launch("stale")
	setLong := `{"targets":[{"type":"profile","agent_profile_id":"` + profileID + `","apply_to_profile":false,` +
		`"apply_to_instances":true}],"edits":[{"type":"set","path":"/model/cache_retention","value":"long"}]}`
	require.Equal(t, []any{second, late, stale}, edit(setLong, "idem-agent-config-edit-retry-agents"))
	lost := launch("lost")
	require.Equal(t, []any{second, late, stale, lost}, edit(setLong, "idem-agent-config-edit-retry-agents"))

	requestJSONWithHeaders(t, handler, http.MethodDelete, project.ProjectPath+"/agent-profiles/"+profileID, "",
		"", http.StatusNoContent, headers)
	requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agent-configs/edit",
		fleet, "idem-agent-config-edit-retry-fleet", http.StatusNotFound, headers)
}

func TestEditAgentConfigsTargetLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "agent-config-edit-limit")
	headers := authHeaders(project.AdminToken)
	config := createPublicHTTPAgentConfig(t, handler, project, "agent-config-edit-limit", "yaml",
		"instruction: Be direct.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
		project.AdminToken, http.StatusCreated)
	profile := createPublicHTTPAgentProfile(t, handler, project, "agent-config-edit-limit", "Limit",
		testutil.RequireType[string](t, config["id"]), project.AdminToken, http.StatusCreated)
	profileID := testutil.RequireType[string](t, profile["id"])
	for i := range maxAgentConfigEditTargets {
		requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agents", `{"profile":"`+profileID+`"}`,
			"idem-agent-config-edit-limit-"+strconv.Itoa(i), http.StatusCreated, headers)
	}
	response := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agent-configs/edit",
		`{"targets":[{"type":"profile","agent_profile_id":"`+profileID+`","apply_to_instances":true}],`+
			`"edits":[{"type":"set","path":"/model/cache_retention","value":"long"}]}`, "", http.StatusBadRequest, headers)
	require.Equal(t, "invalid request: targets expand to more than 500 profiles and agents", response["error"])
}

func TestEditAgentConfigsReportsReplayErrorsPerTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "agent-config-edit-replay-error")
	headers := authHeaders(project.AdminToken)
	config := createPublicHTTPAgentConfig(t, handler, project, "agent-config-edit-replay-error", "yaml",
		"instruction: Be direct.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
		project.AdminToken, http.StatusCreated)
	var profiles []string
	for _, seed := range []string{"first", "second", "third"} {
		profile := createPublicHTTPAgentProfile(t, handler, project, "agent-config-edit-replay-error-"+seed, seed,
			testutil.RequireType[string](t, config["id"]), project.AdminToken, http.StatusCreated)
		profiles = append(profiles, testutil.RequireType[string](t, profile["id"]))
	}
	traced := newIntegrationServer(poolWithQueryTracer(t, ctx, pool,
		failProfileReplayTracer{profileID: mustPublicHTTPID(t, publicid.KindAgentProfile, profiles[1])}))
	response := requestJSONWithHeaders(t, traced, http.MethodPost, project.ProjectPath+"/agent-configs/edit",
		`{"targets":[{"type":"profile","agent_profile_id":"`+profiles[0]+`"},`+
			`{"type":"profile","agent_profile_id":"`+profiles[1]+`"},`+
			`{"type":"profile","agent_profile_id":"`+profiles[2]+`"}],`+
			`"edits":[{"type":"set","path":"/model/cache_retention","value":"long"}]}`,
		"idem-agent-config-edit-replay-error", http.StatusOK, headers)
	var statuses []any
	for _, item := range testutil.RequireType[[]any](t, response["data"]) {
		result := testutil.RequireType[map[string]any](t, item)
		statuses = append(statuses, result["status"], result["code"])
	}
	require.Equal(t, []any{"updated", nil, "failed", "internal_error", "updated", nil}, statuses)
}

type failProfileReplayTracer struct {
	profileID uuid.UUID
}

func (tracer failProfileReplayTracer) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.HasPrefix(data.SQL, "-- name: GetAgentProfileVersionByIdempotencyKey ") &&
		slices.Contains(data.Args, any(tracer.profileID)) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		return canceled
	}
	return ctx
}

func (failProfileReplayTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestEditAgentConfigsConcurrentReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "agent-config-edit-race")
	headers := authHeaders(project.AdminToken)
	config := createPublicHTTPAgentConfig(t, handler, project, "agent-config-edit-race", "yaml",
		"instruction: Be direct.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
		project.AdminToken, http.StatusCreated)
	profile := createPublicHTTPAgentProfile(t, handler, project, "agent-config-edit-race", "Race",
		testutil.RequireType[string](t, config["id"]), project.AdminToken, http.StatusCreated)
	profileID := testutil.RequireType[string](t, profile["id"])
	launched := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agents",
		`{"profile":"`+profileID+`"}`, "idem-agent-config-edit-race-launch", http.StatusCreated, headers)
	agentID := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launched["agent"])["id"])
	edits := func(replace string) string {
		return `"edits":[{"type":"replace_text","path":"/instruction","find":"Be direct.","replace":"` + replace + `"}]}`
	}
	race := func(key string, bodies ...string) {
		t.Helper()
		gate := integrationdb.BeginTx(t, ctx, pool)
		_, err := gate.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('project_lifecycle:' || $1::uuid::text, 0))`,
			project.ProjectUUID)
		require.NoError(t, err)
		var done []<-chan integrationdb.AsyncResult[*httptest.ResponseRecorder]
		for _, body := range bodies {
			req := httptest.NewRequest(http.MethodPost, project.ProjectPath+"/agent-configs/edit", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", key)
			for name, value := range headers {
				req.Header.Set(name, value)
			}
			done = append(done, integrationdb.RunAsync(func() (*httptest.ResponseRecorder, error) {
				return performRequest(handler, req), nil
			}))
		}
		integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectLifecycleShared", len(bodies))
		require.NoError(t, gate.Rollback(ctx))
		var results []map[string]any
		for _, response := range done {
			recorder := integrationdb.AwaitSuccess(t, response, "concurrent edit request")
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var page map[string]any
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &page))
			results = append(results, testutil.RequireType[map[string]any](t, testutil.RequireType[[]any](t, page["data"])[0]))
		}
		var applied []bool
		for _, result := range results {
			_, hasEdits := result["edits"]
			_, hasPrevious := result["previous_config_id"]
			require.Equal(t, hasEdits, hasPrevious, result)
			applied = append(applied, hasEdits)
		}
		require.NotEqual(t, applied[0], applied[1], results)
		require.Equal(t, results[0]["current_config_id"], results[1]["current_config_id"])
	}

	agentTarget := `{"targets":[{"type":"agent","agent_id":"` + agentID + `"}],`
	race("idem-agent-config-edit-race-agent", agentTarget+edits("Be kind."), agentTarget+edits("Be calm."))
	profileBody := `{"targets":[{"type":"profile","agent_profile_id":"` + profileID + `"}],` + edits("Be brief.")
	race("idem-agent-config-edit-race-profile", profileBody, profileBody)
}
