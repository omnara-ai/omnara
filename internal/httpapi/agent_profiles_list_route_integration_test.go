//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestListAgentProfiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)

	project := bootstrapPublicHTTPProject(t, handler, "list-profiles")

	configB := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"list-profiles-b",
		"yaml",
		"instruction: Help with B.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
		project.AdminToken,
		http.StatusCreated,
	)
	configA := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"list-profiles-a",
		"yaml",
		"instruction: Help with A.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
		project.AdminToken,
		http.StatusCreated,
	)

	profileB := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		"list-profiles-b",
		"Beta",
		configB["id"].(string),
		project.AdminToken,
		http.StatusCreated,
	)
	profileA := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		"list-profiles-a",
		"Alpha",
		configA["id"].(string),
		project.AdminToken,
		http.StatusCreated,
	)

	profilesPath := project.ProjectPath + "/agent-profiles"

	listed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		profilesPath,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	data := listed["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("expected 2 agent profiles, got %d: %+v", len(data), data)
	}
	if _, ok := listed["next_cursor"]; !ok {
		t.Fatalf("response missing next_cursor: %+v", listed)
	}
	if listed["next_cursor"] != nil {
		t.Fatalf("single full page should have null next_cursor, got %v", listed["next_cursor"])
	}

	first := data[0].(map[string]any)
	second := data[1].(map[string]any)
	if first["name"] != "Alpha" || second["name"] != "Beta" {
		t.Fatalf("expected newest-first ordering Alpha, Beta, got %q, %q", first["name"], second["name"])
	}
	if first["id"] != profileA["id"] || second["id"] != profileB["id"] {
		t.Fatalf("unexpected profile ids: %+v", data)
	}
	assertProfilesNewestFirst(t, data)

	firstConfig, ok := first["current_config"].(map[string]any)
	if !ok {
		t.Fatalf("expected embedded current_config on profile: %+v", first)
	}
	if firstConfig["id"] != configA["id"] {
		t.Fatalf("expected Alpha current_config %v, got %v", configA["id"], firstConfig["id"])
	}
	firstConfigModel := firstConfig["model"].(map[string]any)
	configAModel := configA["model"].(map[string]any)
	if firstConfigModel["configured_model_id"] != configAModel["configured_model_id"] {
		t.Fatalf(
			"expected Alpha current_config configured_model_id %v, got %v",
			configAModel["configured_model_id"],
			firstConfigModel["configured_model_id"],
		)
	}
	if first["current_config_id"] != configA["id"] {
		t.Fatalf("expected Alpha current_config_id %v, got %v", configA["id"], first["current_config_id"])
	}
	if secondConfig, ok := second["current_config"].(map[string]any); !ok || secondConfig["id"] != configB["id"] {
		t.Fatalf("expected Beta current_config %v, got %+v", configB["id"], second["current_config"])
	}

	const extraProfiles = 4
	for i := 0; i < extraProfiles; i++ {
		seed := "list-profiles-page-" + string(rune('a'+i))
		cfg := createPublicHTTPAgentConfig(
			t,
			handler,
			project,
			seed,
			"yaml",
			"instruction: Help with page "+seed+".\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
			project.AdminToken,
			http.StatusCreated,
		)
		createPublicHTTPAgentProfile(
			t,
			handler,
			project,
			seed,
			"Page"+string(rune('A'+i)),
			cfg["id"].(string),
			project.AdminToken,
			http.StatusCreated,
		)
	}
	wantTotal := 2 + extraProfiles
	pagedIDs := make([]string, 0, wantTotal)
	pagedCreatedAt := make([]time.Time, 0, wantTotal)
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		path := profilesPath + "?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page := requestJSONWithHeaders(
			t,
			handler,
			http.MethodGet,
			path,
			"",
			"",
			http.StatusOK,
			authHeaders(project.AdminToken),
		)
		rows := page["data"].([]any)
		if len(rows) > 2 {
			t.Fatalf("page returned %d profiles, want <= limit 2", len(rows))
		}
		for _, raw := range rows {
			row := raw.(map[string]any)
			id := row["id"].(string)
			if seen[id] {
				t.Fatalf("cursor paging returned duplicate profile %s", id)
			}
			seen[id] = true
			pagedIDs = append(pagedIDs, id)
			if _, ok := row["current_config"].(map[string]any); !ok {
				t.Fatalf("paged profile missing embedded current_config: %+v", row)
			}
			ts, err := time.Parse(time.RFC3339Nano, row["created_at"].(string))
			if err != nil {
				t.Fatalf("parse profile created_at: %v", err)
			}
			pagedCreatedAt = append(pagedCreatedAt, ts)
		}
		if pages > wantTotal+2 {
			t.Fatalf("pagination did not terminate; got=%v", pagedIDs)
		}
		next, ok := page["next_cursor"]
		if !ok {
			t.Fatalf("response missing next_cursor: %+v", page)
		}
		if next == nil {
			break
		}
		cursor = next.(string)
	}
	if len(pagedIDs) != wantTotal {
		t.Fatalf("paged %d profiles, want %d: %v", len(pagedIDs), wantTotal, pagedIDs)
	}
	for i := 1; i < len(pagedCreatedAt); i++ {
		if pagedCreatedAt[i].After(pagedCreatedAt[i-1]) {
			t.Fatalf(
				"profile %d created_at %s newer than predecessor %s; want newest-first",
				i,
				pagedCreatedAt[i],
				pagedCreatedAt[i-1],
			)
		}
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		profilesPath+"?cursor=not-a-valid-cursor",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		profilesPath+"?limit=0",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)

	otherProjectCreated := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/projects",
		`{"name":"List Profiles Other Project"}`,
		"idem-list-profiles-other-project",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	otherProjectID := otherProjectCreated["id"].(string)
	otherProjectPath := "/api/v1/orgs/" + project.OrgID + "/projects/" + otherProjectID
	otherList := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		otherProjectPath+"/agent-profiles",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if other := otherList["data"].([]any); len(other) != 0 {
		t.Fatalf("expected sibling project to have no profiles, got %+v", other)
	}
	if otherList["next_cursor"] != nil {
		t.Fatalf("empty page should have null next_cursor, got %v", otherList["next_cursor"])
	}

	otherOrg := bootstrapPublicHTTPProject(t, handler, "list-profiles-other-org")
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		profilesPath,
		"",
		"",
		http.StatusNotFound,
		authHeaders(otherOrg.AdminToken),
	)
}

func assertProfilesNewestFirst(t *testing.T, data []any) {
	t.Helper()
	var prev time.Time
	for i, raw := range data {
		ts, err := time.Parse(time.RFC3339Nano, raw.(map[string]any)["created_at"].(string))
		if err != nil {
			t.Fatalf("parse created_at for profile %d: %v", i, err)
		}
		if i > 0 && ts.After(prev) {
			t.Fatalf("profile %d created_at %s is newer than predecessor %s; want newest-first", i, ts, prev)
		}
		prev = ts
	}
}
