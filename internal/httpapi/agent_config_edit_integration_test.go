//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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

	edit := func(query, body, key string, want int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agent-configs/edit"+query,
			body, key, want, headers)
	}
	results := func(response map[string]any) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, item := range testutil.RequireType[[]any](t, response["data"]) {
			out = append(out, testutil.RequireType[map[string]any](t, item))
		}
		return out
	}
	source := func(configID string) string {
		t.Helper()
		config := requestJSONWithHeaders(t, handler, http.MethodGet, project.ProjectPath+"/agent-configs/"+configID,
			"", "", http.StatusOK, headers)
		return testutil.RequireType[string](t, config["source"])
	}

	body := `{"agent_profile_ids":["` + profileID + `"],"agent_ids":["` + shared + `"],"edits":[` +
		`{"type":"replace_text","path":"/instruction","find":"Be direct.","replace":"Be direct and kind."},` +
		`{"type":"set","path":"/model/cache_retention","value":"long"}]}`
	response := edit("", body, "", http.StatusOK)
	require.Nil(t, response["next_cursor"])
	data := results(response)
	require.Len(t, data, 4)
	require.Equal(t, []any{"profile", profileID, "updated", float64(2)},
		[]any{data[0]["type"], data[0]["agent_profile_id"], data[0]["status"], data[0]["current_generation"]})
	require.Equal(t, configID, data[0]["previous_config_id"])
	for i, agentID := range []string{shared, personal, other} {
		require.Equal(t, []any{"agent", agentID, "updated"},
			[]any{data[i+1]["type"], data[i+1]["agent_id"], data[i+1]["status"]})
	}
	require.Equal(t, []any{"applied", "applied"}, data[1]["edits"])
	require.Equal(t, []any{"text_not_found", "applied"}, data[3]["edits"])
	require.Contains(t, source(testutil.RequireType[string](t, data[2]["current_config_id"])),
		"Hi Maya. Be direct and kind.")

	for _, result := range results(edit("", body, "", http.StatusOK)) {
		require.Equal(t, "unchanged", result["status"])
	}

	walk := func(body string) []any {
		t.Helper()
		var walked []any
		for cursor := ""; ; {
			query := "?limit=1"
			if cursor != "" {
				query += "&cursor=" + url.QueryEscape(cursor)
			}
			page := edit(query, body, "", http.StatusOK)
			for _, result := range results(page) {
				if id, ok := result["agent_id"]; ok {
					walked = append(walked, id)
				} else {
					walked = append(walked, result["agent_profile_id"])
				}
			}
			next, _ := page["next_cursor"].(string)
			if next == "" {
				return walked
			}
			cursor = next
		}
	}
	require.Equal(t, []any{profileID, shared, personal, other}, walk(body))
	firstAgents, secondAgents := []any{shared, personal, other}, []any{secondEarly, secondLate}
	if testPublicIDOrder(t, publicid.KindAgentProfile, secondProfileID, profileID) {
		firstAgents, secondAgents = secondAgents, firstAgents
	}
	require.Equal(t, append(firstAgents, secondAgents...), walk(`{"agent_profile_ids":["`+profileID+
		`","`+secondProfileID+`"],"apply_to":["agent"],"edits":[{"type":"set","path":"/model/cache_retention","value":"long"}]}`))

	insert := `{"agent_profile_ids":["` + profileID + `"],"edits":[{"type":"replace_text","path":"/instruction",` +
		`"find":"Ask before deleting.","replace":"Ask before deleting. Log every command."}]}`
	var pages []map[string]any
	var queries []string
	cursor := ""
	for page := 0; ; page++ {
		query := "?limit=1"
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}
		queries = append(queries, query)
		key := fmt.Sprintf("idem-agent-config-edit-insert-%d", page)
		first := edit(query, insert, key, http.StatusOK)
		retry := edit(query, insert, key, http.StatusOK)
		firstData, retryData := results(first), results(retry)
		require.Len(t, firstData, 1)
		require.Equal(t, firstData[0]["current_config_id"], retryData[0]["current_config_id"])
		pages = append(pages, firstData[0])
		next, _ := retry["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	require.Len(t, pages, 4)
	require.Equal(t, []any{"profile", "updated"}, []any{pages[0]["type"], pages[0]["status"]})
	for _, page := range pages {
		require.Equal(t, 1, strings.Count(source(testutil.RequireType[string](t, page["current_config_id"])),
			"Log every command."))
	}

	intervening := results(edit("", `{"agent_profile_ids":["`+profileID+`"],"edits":[{"type":"replace_text",`+
		`"path":"/instruction","find":"Log every command.","replace":"Log every command!"}]}`, "", http.StatusOK))
	countConfigs := func() int {
		var count int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM agent_configs WHERE project_id = $1`,
			project.ProjectUUID).Scan(&count))
		return count
	}
	configsBefore := countConfigs()
	for i := range 2 {
		replayed := results(edit(queries[i], insert, fmt.Sprintf("idem-agent-config-edit-insert-%d", i), http.StatusOK))
		require.Equal(t, []any{"updated", pages[i]["current_config_id"]},
			[]any{replayed[0]["status"], replayed[0]["current_config_id"]})
		require.NotContains(t, replayed[0], "edits")
		require.Contains(t, source(testutil.RequireType[string](t, intervening[i]["current_config_id"])),
			"Log every command!")
	}
	require.Equal(t, configsBefore, countConfigs())

	nulled := results(edit("", `{"agent_ids":["`+shared+`"],"edits":[{"type":"set","path":"/mcp","value":null}]}`,
		"", http.StatusOK))
	require.Equal(t, "updated", nulled[0]["status"])
	nullItem := results(edit("", `{"agent_ids":["`+shared+`"],"edits":[`+
		`{"type":"add_item","path":"/skills","value":null}]}`, "", http.StatusOK))
	require.Equal(t, []any{"failed", "invalid_request"}, []any{nullItem[0]["status"], nullItem[0]["code"]})

	missing := testPublicID(t, publicid.KindAgent, httpTestID("agent-config-edit-missing"))
	setShort := `"edits":[{"type":"set","path":"/model/cache_retention","value":"short"}]`
	edit("", `{"agent_ids":["`+missing+`"],`+setShort+`}`, "", http.StatusNotFound)
	edit("", `{"agent_ids":["`+shared+`"],"apply_to":["profile"],`+setShort+`}`, "", http.StatusBadRequest)
	profileOnly := results(edit("", `{"agent_profile_ids":["`+profileID+`"],"apply_to":["profile"],`+setShort+`}`,
		"", http.StatusOK))
	require.Len(t, profileOnly, 1)
	require.Equal(t, []any{"profile", "updated"}, []any{profileOnly[0]["type"], profileOnly[0]["status"]})

	setOther := `{"agent_ids":["` + other + `"],` + setShort + `}`
	beforeArchive := results(edit("", setOther, "idem-agent-config-edit-before-archive", http.StatusOK))
	require.Equal(t, "updated", beforeArchive[0]["status"])
	requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agents/"+other+"/archive", "",
		"idem-agent-config-edit-archive", http.StatusOK, headers)
	afterArchive := results(edit("", setOther, "idem-agent-config-edit-before-archive", http.StatusOK))
	require.Equal(t, []any{"updated", beforeArchive[0]["current_config_id"]},
		[]any{afterArchive[0]["status"], afterArchive[0]["current_config_id"]})
	archived := results(edit("", setOther, "", http.StatusOK))
	require.Equal(t, []any{"failed", "state_transition_conflict"}, []any{archived[0]["status"], archived[0]["code"]})
}

func TestEditAgentConfigsRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "agent-config-edit-restart")
	headers := authHeaders(project.AdminToken)
	config := func(seed, instruction string) string {
		created := createPublicHTTPAgentConfig(t, handler, project, "agent-config-edit-restart-"+seed, "yaml",
			"instruction: "+instruction+"\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
			project.AdminToken, http.StatusCreated)
		return testutil.RequireType[string](t, created["id"])
	}
	profile := func(seed, configID string) string {
		created := createPublicHTTPAgentProfile(t, handler, project, "agent-config-edit-restart-"+seed, seed,
			configID, project.AdminToken, http.StatusCreated)
		return testutil.RequireType[string](t, created["id"])
	}
	launch := func(seed, profileID string) string {
		launched := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agents",
			`{"profile":"`+profileID+`"}`, "idem-agent-config-edit-restart-"+seed, http.StatusCreated, headers)
		return testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launched["agent"])["id"])
	}
	inserted := func(agentID string) int {
		agent := testutil.RequireType[map[string]any](t, requestJSONWithHeaders(t, handler, http.MethodGet,
			project.ProjectPath+"/agents/"+agentID, "", "", http.StatusOK, headers)["agent"])
		config := requestJSONWithHeaders(t, handler, http.MethodGet, project.ProjectPath+"/agent-configs/"+
			testutil.RequireType[string](t, agent["current_config_id"]), "", "", http.StatusOK, headers)
		return strings.Count(testutil.RequireType[string](t, config["source"]), "Log every command.")
	}
	calm := config("calm", "Be calm.")
	unchanged, edited := profile("a", calm), profile("b", calm)
	if !testPublicIDOrder(t, publicid.KindAgentProfile, unchanged, edited) {
		unchanged, edited = edited, unchanged
	}
	requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agent-profiles/"+edited+"/config",
		`{"config":"`+config("ask", "Ask before deleting.")+`","expected_current_config_id":"`+calm+`"}`,
		"idem-agent-config-edit-restart-retarget", http.StatusOK, headers)
	first, second := launch("first", edited), launch("second", edited)
	targets := `{"agent_profile_ids":["` + unchanged + `","` + edited + `"],"edits":`
	insert := targets + `[{"type":"replace_text","path":"/instruction",` +
		`"find":"Ask before deleting.","replace":"Ask before deleting. Log every command."}]}`
	page := func(body, cursor, key string) map[string]any {
		query := "?limit=1"
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}
		return requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agent-configs/edit"+query,
			body, key, http.StatusOK, headers)
	}
	finish := func(body string, response map[string]any, run string, after func(int)) []map[string]any {
		var results []map[string]any
		for i := 1; ; i++ {
			for _, item := range testutil.RequireType[[]any](t, response["data"]) {
				results = append(results, testutil.RequireType[map[string]any](t, item))
			}
			if after != nil {
				after(i)
			}
			next, _ := response["next_cursor"].(string)
			if next == "" {
				return results
			}
			response = page(body, next, fmt.Sprintf("idem-agent-config-edit-restart-%s-%d", run, i))
		}
	}

	page(insert, "", "idem-agent-config-edit-restart-rollout")
	lost := launch("lost", edited)
	var late string
	finish(insert, page(insert, "", "idem-agent-config-edit-restart-rollout"), "first", func(i int) {
		if i == 2 {
			late = launch("late", edited)
		}
	})
	requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agents/"+first+"/archive", "",
		"idem-agent-config-edit-restart-archive", http.StatusOK, headers)
	finish(insert, page(insert, "", "idem-agent-config-edit-restart-rollout"), "restart", nil)
	for _, agentID := range []string{first, second, lost, late} {
		require.Equal(t, 1, inserted(agentID), agentID)
	}

	set := targets + `[{"type":"set","path":"/model/cache_retention","value":"long"}]}`
	deleted := finish(set, page(set, "", "idem-agent-config-edit-restart-delete"), "delete", func(i int) {
		if i == 1 {
			requestJSONWithHeaders(t, handler, http.MethodDelete, project.ProjectPath+"/agent-profiles/"+edited, "",
				"", http.StatusNoContent, headers)
		}
	})
	require.Equal(t, []any{edited, "failed", "not_found"},
		[]any{deleted[1]["agent_profile_id"], deleted[1]["status"], deleted[1]["code"]})
	require.Len(t, deleted, 5)
}

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

	race("idem-agent-config-edit-race-agent",
		`{"agent_ids":["`+agentID+`"],`+edits("Be kind."), `{"agent_ids":["`+agentID+`"],`+edits("Be calm."))
	profileBody := `{"agent_profile_ids":["` + profileID + `"],"apply_to":["profile"],` + edits("Be brief.")
	race("idem-agent-config-edit-race-profile", profileBody, profileBody)
}

func testPublicIDOrder(t *testing.T, kind publicid.Kind, a, b string) bool {
	t.Helper()
	first, err := publicid.Decode(kind, a)
	require.NoError(t, err)
	second, err := publicid.Decode(kind, b)
	require.NoError(t, err)
	return bytes.Compare(first[:], second[:]) < 0
}
