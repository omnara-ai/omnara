//go:build integration && servicee2e

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"
)

func buildSkillTarGz(t *testing.T, skillName, canaryToken string) []byte {
	t.Helper()
	files := map[string]string{
		skillName + "/SKILL.md":   "---\nname: " + skillName + "\ndescription: Service E2E canary skill that drops a CANARY.txt file.\n---\n\n# " + skillName + "\n\nThis skill exists only to be installed; tests verify CANARY.txt is present.\n",
		skillName + "/CANARY.txt": canaryToken + "\n",
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("skill tar header %q: %v", name, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("skill tar write %q: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("skill tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("skill gzip close: %v", err)
	}
	return buf.Bytes()
}

func (p deterministicProject) uploadProjectSkill(
	t *testing.T,
	ctx context.Context,
	idemSeed, skillName string,
	archive []byte,
) uploadedProjectSkill {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	ownerHeader := make(textproto.MIMEHeader)
	ownerHeader.Set("Content-Disposition", `form-data; name="owner"`)
	ownerHeader.Set("Content-Type", "application/json")
	owner, err := writer.CreatePart(ownerHeader)
	if err != nil {
		t.Fatalf("create multipart owner: %v", err)
	}
	ownerJSON := `{"kind":"project","project_id":"` + p.projectID + `"}`
	if _, err := owner.Write([]byte(ownerJSON)); err != nil {
		t.Fatalf("write multipart owner: %v", err)
	}
	part, err := writer.CreateFormFile("archive", skillName+".tar.gz")
	if err != nil {
		t.Fatalf("create multipart archive part: %v", err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatalf("write multipart archive: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	orgPath := strings.Split(p.projectPath, "/projects/")[0]
	req, err := p.env.newAPIRequest(ctx, http.MethodPost, orgPath+"/skills", &body)
	if err != nil {
		t.Fatalf("new upload request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+p.adminToken)
	req.Header.Set("Idempotency-Key", "idem-"+idemSeed+"-skill")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload skill: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read upload response: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload skill status=%d body=%s", resp.StatusCode, string(data))
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode upload response: %v body=%s", err, string(data))
	}
	id, ok := out["id"].(string)
	if !ok || id == "" {
		t.Fatalf("upload response missing id: %s", string(data))
	}
	revisionID, ok := out["revision_id"].(string)
	if !ok || revisionID == "" {
		t.Fatalf("upload response missing revision_id: %s", string(data))
	}
	revision, ok := out["revision"].(float64)
	if !ok || revision < 1 {
		t.Fatalf("upload response missing revision: %s", string(data))
	}
	return uploadedProjectSkill{ID: id, RevisionID: revisionID, Revision: int(revision)}
}

type uploadedProjectSkill struct {
	ID         string
	RevisionID string
	Revision   int
}

func (p *deterministicProject) updateAgentProfileConfigWithMachineAndSkill(
	t *testing.T,
	ctx context.Context,
	seed string,
	providerConfig string,
	configuredModelName string,
	machineName string,
	cwd string,
	skillID string,
	tools ...string,
) {
	t.Helper()
	lines := []string{
		"instruction: Help the user make progress.",
		"model:",
		"  provider_config: " + providerConfig,
		"  name: " + configuredModelName,
		"machine_sources:",
		"  - machine_name: " + machineName,
		"    cwd: " + cwd,
		"skills:",
		"  - " + skillID,
	}
	if len(tools) > 0 {
		lines = append(lines, "tools:")
		for _, name := range tools {
			lines = append(lines, "  "+name+": {}")
		}
	}
	sourceYAML := strings.Join(lines, "\n") + "\n"
	sum := sha256.Sum256([]byte(sourceYAML))
	config := p.env.requestJSON(t, ctx, http.MethodPost, p.projectPath+"/agent-configs", map[string]any{"source_format": "yaml", "source": sourceYAML}, "", p.adminToken, http.StatusCreated)
	updated := p.env.requestJSON(t, ctx, http.MethodPost, p.projectPath+"/agent-profiles/"+p.agentID+"/config", map[string]any{
		"config":                     config["id"].(string),
		"expected_current_config_id": p.configID,
	}, "idem-"+seed+"-config-"+hex.EncodeToString(sum[:8]), p.adminToken, http.StatusOK)
	p.configID = updated["current_config"].(map[string]any)["id"].(string)
}
