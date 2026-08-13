//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"
)

func TestCronTriggerRoutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)

	project := bootstrapPublicHTTPProject(t, handler, "cron-triggers")
	profile := createPublicHTTPAgent(t, handler, project, "cron-triggers-profile", project.AdminToken)
	profileID := profile["id"].(string)
	triggersPath := project.ProjectPath + "/cron-triggers"

	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		triggersPath,
		`{"name":"daily-triage","target":{"type":"profile","agent_profile_id":"`+profileID+`"},`+
			`"cron":"0 9 * * 1-5","message_template":"Triage issues."}`,
		"idem-cron-trigger-create",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	triggerID := created["id"].(string)
	if created["name"] != "daily-triage" {
		t.Fatalf("unexpected name: %+v", created)
	}
	if created["cron"] != "0 9 * * 1-5" {
		t.Fatalf("unexpected cron: %+v", created)
	}
	if created["timezone"] != "UTC" {
		t.Fatalf("expected default timezone UTC, got %+v", created)
	}
	if created["enabled"] != true {
		t.Fatalf("expected trigger enabled by default: %+v", created)
	}
	if created["last_fired_at"] != nil {
		t.Fatalf("expected null last_fired_at: %+v", created)
	}
	if created["next_fire_at"] == nil {
		t.Fatalf("expected next_fire_at for enabled trigger: %+v", created)
	}
	target := created["target"].(map[string]any)
	if target["type"] != "profile" || target["agent_profile_id"] != profileID {
		t.Fatalf("unexpected target: %+v", target)
	}

	replayed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		triggersPath,
		`{"name":"daily-triage","target":{"type":"profile","agent_profile_id":"`+profileID+`"},`+
			`"cron":"0 9 * * 1-5","message_template":"Triage issues."}`,
		"idem-cron-trigger-create",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if replayed["id"] != triggerID {
		t.Fatalf("idempotent replay returned different trigger: %v vs %v", replayed["id"], triggerID)
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		triggersPath,
		`{"name":"daily-triage","target":{"type":"profile","agent_profile_id":"`+profileID+`"},`+
			`"cron":"0 10 * * *","message_template":"Duplicate name."}`,
		"idem-cron-trigger-duplicate-name",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		triggersPath,
		`{"name":"bad-cron","target":{"type":"profile","agent_profile_id":"`+profileID+`"},`+
			`"cron":"not a cron","message_template":"Bad cron."}`,
		"idem-cron-trigger-bad-cron",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		triggersPath,
		`{"name":"bad-timezone","target":{"type":"profile","agent_profile_id":"`+profileID+`"},`+
			`"cron":"0 9 * * *","timezone":"Not/AZone","message_template":"Bad timezone."}`,
		"idem-cron-trigger-bad-timezone",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)

	otherProject := bootstrapPublicHTTPProject(t, handler, "cron-triggers-foreign")
	foreignProfile := createPublicHTTPAgent(
		t,
		handler,
		otherProject,
		"cron-triggers-foreign-profile",
		otherProject.AdminToken,
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		triggersPath,
		`{"name":"foreign-target","target":{"type":"profile","agent_profile_id":"`+
			foreignProfile["id"].(string)+`"},`+
			`"cron":"0 9 * * *","message_template":"Foreign target."}`,
		"idem-cron-trigger-foreign-target",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)

	launch := launchPublicHTTPAgent(t, handler, project, "cron-triggers-agent", project.AdminToken, http.StatusCreated)
	agentID := launch["agent"].(map[string]any)["id"].(string)
	agentTrigger := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		triggersPath,
		`{"name":"agent-nudge","target":{"type":"agent","agent_id":"`+agentID+`"},`+
			`"cron":"30 8 * * *","timezone":"America/New_York","message_template":"Check the queue.","enabled":false}`,
		"idem-cron-trigger-agent",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	agentTriggerID := agentTrigger["id"].(string)
	if agentTrigger["timezone"] != "America/New_York" {
		t.Fatalf("unexpected timezone: %+v", agentTrigger)
	}
	if agentTrigger["enabled"] != false {
		t.Fatalf("expected disabled trigger: %+v", agentTrigger)
	}
	if agentTrigger["next_fire_at"] != nil {
		t.Fatalf("disabled trigger should have null next_fire_at: %+v", agentTrigger)
	}
	agentTarget := agentTrigger["target"].(map[string]any)
	if agentTarget["type"] != "agent" || agentTarget["agent_id"] != agentID {
		t.Fatalf("unexpected agent target: %+v", agentTarget)
	}

	listed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		triggersPath,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if data := listed["data"].([]any); len(data) != 2 {
		t.Fatalf("expected 2 cron triggers, got %d: %+v", len(data), data)
	}
	if listed["next_cursor"] != nil {
		t.Fatalf("single full page should have null next_cursor, got %v", listed["next_cursor"])
	}

	byAgent := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		triggersPath+"?agent_id="+agentID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	agentRows := byAgent["data"].([]any)
	if len(agentRows) != 1 || agentRows[0].(map[string]any)["id"] != agentTriggerID {
		t.Fatalf("agent_id filter returned wrong rows: %+v", agentRows)
	}
	byProfile := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		triggersPath+"?agent_profile_id="+profileID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	profileRows := byProfile["data"].([]any)
	if len(profileRows) != 1 || profileRows[0].(map[string]any)["id"] != triggerID {
		t.Fatalf("agent_profile_id filter returned wrong rows: %+v", profileRows)
	}

	fetched := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		triggersPath+"/"+triggerID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if fetched["id"] != triggerID || fetched["message_template"] != "Triage issues." {
		t.Fatalf("unexpected trigger fetch: %+v", fetched)
	}

	renamed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		triggersPath+"/"+triggerID,
		`{"name":"daily-triage-renamed"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if renamed["next_fire_at"] != fetched["next_fire_at"] {
		t.Fatalf(
			"name-only patch must not reschedule: %v -> %v",
			fetched["next_fire_at"],
			renamed["next_fire_at"],
		)
	}

	updated := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		triggersPath+"/"+triggerID,
		`{"cron":"0 7 * * *","enabled":false}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if updated["cron"] != "0 7 * * *" {
		t.Fatalf("expected updated cron: %+v", updated)
	}
	if updated["enabled"] != false || updated["next_fire_at"] != nil {
		t.Fatalf("disabling should clear next_fire_at: %+v", updated)
	}
	if updatedTarget := updated["target"].(map[string]any); updatedTarget["agent_profile_id"] != profileID {
		t.Fatalf("update must not change the target: %+v", updatedTarget)
	}

	reenabled := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		triggersPath+"/"+triggerID,
		`{"enabled":true}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if reenabled["enabled"] != true || reenabled["next_fire_at"] == nil {
		t.Fatalf("re-enabling should schedule next_fire_at: %+v", reenabled)
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		triggersPath+"/"+triggerID,
		`{"cron":"still not a cron"}`,
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		triggersPath+"/"+triggerID,
		`{"name":"agent-nudge"}`,
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)

	otherOrg := bootstrapPublicHTTPProject(t, handler, "cron-triggers-other-org")
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		triggersPath+"/"+triggerID,
		"",
		"",
		http.StatusNotFound,
		authHeaders(otherOrg.AdminToken),
	)

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		triggersPath+"/"+triggerID,
		"",
		"",
		http.StatusNoContent,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		triggersPath+"/"+triggerID,
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		triggersPath+"/"+triggerID,
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)

	recreated := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		triggersPath,
		`{"name":"daily-triage","target":{"type":"profile","agent_profile_id":"`+profileID+`"},`+
			`"cron":"0 9 * * 1-5","message_template":"Triage issues."}`,
		"idem-cron-trigger-recreate",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	if recreated["id"] == triggerID {
		t.Fatalf("recreate after delete should mint a new trigger id")
	}
}
