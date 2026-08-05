//go:build integration

package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
)

func TestPublicSkillsUseFlatOwnerAwareRoutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServerWithStoreOptions(
		pool,
		[]storage.Option{storage.WithBlobStore(integrationblob.MustOpen(t, ctx))},
	)
	project := bootstrapPublicHTTPProject(t, handler, "flat-skills")
	basePath := "/api/v1/orgs/" + project.OrgID + "/skills"

	created := requestSkillUpload(t, handler, basePath,
		`{"kind":"project","project_id":"`+project.ProjectID+`"}`,
		"project-skill", project.AdminToken, http.StatusCreated)
	skillID, ok := created["id"].(string)
	if !ok || skillID == "" {
		t.Fatalf("created skill missing id: %+v", created)
	}
	if revisionID, ok := created["revision_id"].(string); !ok || revisionID == "" {
		t.Fatalf("created skill missing revision_id: %+v", created)
	}
	assertPublicSkillOwner(t, created, "project", project.ProjectID)

	got := requestJSONWithHeaders(t, handler, http.MethodGet, basePath+"/"+skillID,
		"", "", http.StatusOK, authHeaders(project.AdminToken))
	assertPublicSkillOwner(t, got, "project", project.ProjectID)
	if got["skill_md"] == "" {
		t.Fatalf("get skill omitted skill_md: %+v", got)
	}
	secondProjectSkill := requestSkillUpload(t, handler, basePath,
		`{"kind":"project","project_id":"`+project.ProjectID+`"}`,
		"project-skill-two", project.AdminToken, http.StatusCreated)
	secondProjectSkillID, ok := secondProjectSkill["id"].(string)
	if !ok || secondProjectSkillID == "" {
		t.Fatalf("second project skill missing id: %+v", secondProjectSkill)
	}

	page := requestJSONWithHeaders(t, handler, http.MethodGet,
		basePath+"?owner_kind=project&owner_project_id="+project.ProjectID+"&limit=1",
		"", "", http.StatusOK, authHeaders(project.AdminToken))
	data, ok := page["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("first skill page = %+v", page)
	}
	firstPageID, _ := data[0].(map[string]any)["id"].(string)
	nextCursor, ok := page["next_cursor"].(string)
	if !ok || nextCursor == "" {
		t.Fatalf("first skill page missing next_cursor: %+v", page)
	}
	secondPage := requestJSONWithHeaders(t, handler, http.MethodGet,
		basePath+"?owner_kind=project&owner_project_id="+project.ProjectID+"&limit=1&cursor="+
			url.QueryEscape(nextCursor),
		"", "", http.StatusOK, authHeaders(project.AdminToken))
	secondData, ok := secondPage["data"].([]any)
	if !ok || len(secondData) != 1 {
		t.Fatalf("second skill page = %+v", secondPage)
	}
	secondPageID, _ := secondData[0].(map[string]any)["id"].(string)
	if firstPageID == "" || secondPageID == "" || firstPageID == secondPageID {
		t.Fatalf("skill pages overlap: first=%q second=%q", firstPageID, secondPageID)
	}
	wantIDs := map[string]bool{skillID: true, secondProjectSkillID: true}
	if !wantIDs[firstPageID] || !wantIDs[secondPageID] || secondPage["next_cursor"] != nil {
		t.Fatalf("paginated skill ids first=%q second=%q response=%+v", firstPageID, secondPageID, secondPage)
	}

	userSkill := requestSkillUpload(t, handler, basePath, `{"kind":"user"}`,
		"user-skill", project.AdminToken, http.StatusCreated)
	userSkillID, ok := userSkill["id"].(string)
	if !ok || userSkillID == "" {
		t.Fatalf("created user skill missing id: %+v", userSkill)
	}
	grantPath := basePath + "/" + userSkillID + "/grants"
	grant := requestJSONWithHeaders(t, handler, http.MethodPost, grantPath,
		`{"target_project_id":"`+project.ProjectID+`"}`, "application/json", http.StatusCreated,
		authHeaders(project.AdminToken))
	grantID, ok := grant["id"].(string)
	if !ok || grantID == "" || grant["skill_id"] != userSkillID || grant["target_project_id"] != project.ProjectID {
		t.Fatalf("created skill grant = %+v", grant)
	}
	grants := requestJSONWithHeaders(t, handler, http.MethodGet, grantPath,
		"", "", http.StatusOK, authHeaders(project.AdminToken))
	grantData, ok := grants["data"].([]any)
	if !ok || len(grantData) != 1 ||
		grantData[0].(map[string]any)["grant"].(map[string]any)["id"] != grantID {
		t.Fatalf("skill grants = %+v", grants)
	}
	available := requestJSONWithHeaders(t, handler, http.MethodGet, project.ProjectPath+"/skills",
		"", "", http.StatusOK, authHeaders(project.AdminToken))
	availableData, ok := available["data"].([]any)
	if !ok {
		t.Fatalf("project available skills = %+v", available)
	}
	var grantedAccess map[string]any
	for _, item := range availableData {
		access, itemOK := item.(map[string]any)
		if !itemOK {
			continue
		}
		skill, skillOK := access["skill"].(map[string]any)
		if skillOK && skill["id"] == userSkillID {
			grantedAccess = access
			break
		}
	}
	availability, ok := grantedAccess["availability"].(map[string]any)
	if grantedAccess == nil || !ok || availability["source"] != "grant" || availability["grant_id"] != grantID {
		t.Fatalf("granted project skill access = %+v, project skills = %+v", grantedAccess, available)
	}
	requestJSONWithHeaders(t, handler, http.MethodDelete, grantPath+"/"+grantID,
		"", "", http.StatusNoContent, authHeaders(project.AdminToken))

	for _, legacy := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: project.ProjectPath + "/skills"},
		{method: http.MethodGet, path: "/api/v1/orgs/" + project.OrgID + "/users/me/skills"},
		{method: http.MethodPost, path: "/api/v1/orgs/" + project.OrgID + "/users/me/skills"},
	} {
		requestJSONWithHeaders(t, handler, legacy.method, legacy.path,
			"", "", http.StatusNotFound, authHeaders(project.AdminToken))
	}

	requestJSONWithHeaders(t, handler, http.MethodDelete, basePath+"/"+skillID,
		"", "", http.StatusNoContent, authHeaders(project.AdminToken))
}

func requestSkillUpload(
	t *testing.T,
	handler http.Handler,
	path, owner, name, token string,
	wantStatus int,
) map[string]any {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	ownerHeader := make(textproto.MIMEHeader)
	ownerHeader.Set("Content-Disposition", `form-data; name="owner"`)
	ownerHeader.Set("Content-Type", "application/json")
	ownerPart, err := writer.CreatePart(ownerHeader)
	if err != nil {
		t.Fatalf("create owner part: %v", err)
	}
	if _, err := ownerPart.Write([]byte(owner)); err != nil {
		t.Fatalf("write owner part: %v", err)
	}
	archivePart, err := writer.CreateFormFile("archive", name+".zip")
	if err != nil {
		t.Fatalf("create archive part: %v", err)
	}
	if _, err := archivePart.Write(buildPublicSkillZip(t, name)); err != nil {
		t.Fatalf("write archive part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("POST %s status=%d want=%d body=%s", path, rec.Code, wantStatus, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		return nil
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode skill response: %v body=%s", err, rec.Body.String())
	}
	return response
}

func buildPublicSkillZip(t *testing.T, name string) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create(name + "/SKILL.md")
	if err != nil {
		t.Fatalf("create SKILL.md: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: integration skill\n---\n\n# Integration skill\n"
	if _, err := file.Write([]byte(body)); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close skill zip: %v", err)
	}
	return archive.Bytes()
}

func assertPublicSkillOwner(t *testing.T, skill map[string]any, kind, ownerID string) {
	t.Helper()
	owner, ok := skill["owner"].(map[string]any)
	if !ok || owner["kind"] != kind {
		t.Fatalf("skill owner = %+v, want kind %s", skill["owner"], kind)
	}
	if kind == "project" && owner["project_id"] != ownerID {
		t.Fatalf("skill project owner = %+v, want %s", owner, ownerID)
	}
}
