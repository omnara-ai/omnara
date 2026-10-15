package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestReadFileDefaults(t *testing.T) {
	for _, input := range []string{
		`{"path":"/memory/team/notes.md"}`,
		`{"path":"/memory/team/notes.md","offset_char":0}`,
		`{"path":"/memory/team/notes.md","limit_chars":512}`,
		`{"path":"/tmp/notes.md"}`,
		`{"path":"/tmp/notes.md","offset_char":0}`,
	} {
		request, err := resolveReadFileRequest(json.RawMessage(input))
		if err != nil {
			t.Fatal(err)
		}
		if request.OffsetChar != nil {
			if *request.OffsetChar != 0 || *request.LimitChars != 512 {
				t.Fatalf("incorrect character defaults: %+v", request)
			}
		} else if *request.OffsetLine != 1 || *request.LimitLines != 100 {
			t.Fatalf("incorrect line defaults: %+v", request)
		}
	}
}

func TestReadFileRoutesOmnaraAndMachinePaths(t *testing.T) {
	artifactID, err := publicid.Encode(publicid.KindArtifact, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	machineUUID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	machineID := "mch_aaaaaaaaaaaaaaaaaaaaaaaaae"
	for _, test := range []struct {
		name      string
		input     map[string]any
		onMachine bool
		machineID uuid.UUID
	}{
		{name: "memory", input: map[string]any{"path": "/memory/team/notes.md"}},
		{name: "artifact", input: map[string]any{"path": "/artifacts/" + artifactID}},
		{name: "absolute machine path", input: map[string]any{"path": "/tmp/shot.png"}, onMachine: true},
		{name: "relative machine path", input: map[string]any{"path": "notes/today.md"}, onMachine: true},
		{name: "home machine path", input: map[string]any{"path": "~/shot.png"}, onMachine: true},
		{name: "other root", input: map[string]any{"path": "/skills/test"}, onMachine: true},
		{name: "root prefix only", input: map[string]any{"path": "/memoryless/notes.md"}, onMachine: true},
		{
			name: "explicit machine", input: map[string]any{"path": "/tmp/shot.png", "machine_id": machineID},
			onMachine: true, machineID: machineUUID,
		},
		{
			name:  "machine id wins over Omnara roots",
			input: map[string]any{"path": "/artifacts/" + artifactID, "machine_id": machineID}, onMachine: true,
			machineID: machineUUID,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.input)
			if err != nil {
				t.Fatal(err)
			}
			request, err := resolveReadFileRequest(raw)
			if err != nil {
				t.Fatal(err)
			}
			if request.onMachine != test.onMachine || request.machineID != test.machineID {
				t.Fatalf("request routed to machine=%t id=%s, want machine=%t id=%s",
					request.onMachine, request.machineID, test.onMachine, test.machineID)
			}
		})
	}
}

func TestFileRetrievalRejectsInvalidInputs(t *testing.T) {
	id, err := publicid.Encode(publicid.KindArtifact, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	path := "/artifacts/" + id
	for _, input := range []map[string]any{
		{"path": "/artifacts"}, {"path": path + "/"},
		{"path": path, "offset_line": 1, "offset_char": 0}, {"path": path, "limit_chars": 0},
		{"path": path, "offset_char": -1}, {"path": path, "limit_chars": 4097},
		{"path": path, "offset_byte": 0}, {"path": path, "limit_bytes": 4},
		{"path": "/memory/team"}, {"path": "/memory/team/../other/file"},
		{"path": "/memory/team/file", "limit_lines": 1, "limit_chars": 4},
		{"path": path, "limit_lines": 201}, {"path": path, "unexpected": true},
		{"path": "bad\x00path"}, {"path": "/tmp/shot.png", "machine_id": "bad"},
		{"path": "/tmp/shot.png", "machine_id": nil}, {"path": "", "machine_id": "mch_aaaaaaaaaaaaaaaaaaaaaaaaae"},
		{"path": "/tmp/notes.md", "offset_line": 1, "limit_chars": 4},
		{"path": "/tmp/notes.md", "expected_digest": "bad"},
	} {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if validateReadFileInput(raw) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestReadFileRejectsExpectedDigest(t *testing.T) {
	tool, ok, err := toolImplementationFor(toolcatalog.ToolNameReadFile)
	if err != nil || !ok {
		t.Fatalf("read_file registration: %v", err)
	}
	input := `{"path":"/memory/team/file","expected_digest":"sha256:` + strings.Repeat("a", 64) + `"}`
	if err := tool.validateInput(json.RawMessage(input)); err == nil {
		t.Fatal("expected_digest accepted")
	}
}

func TestArtifactFilenameGuidance(t *testing.T) {
	for _, name := range []string{"read_file", "search_files", "download_file"} {
		t.Run(name, func(t *testing.T) {
			tool, ok, err := toolImplementationFor(name)
			if err != nil || !ok {
				t.Fatalf("tool registration: %v", err)
			}
			input := map[string]any{"path": "/artifacts/report.pdf"}
			switch name {
			case "search_files":
				input["args"] = []string{"-e", "hello"}
			case "download_file":
				input["destination"] = "report.pdf"
			}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if err := tool.validateInput(raw); err == nil || !strings.Contains(err.Error(), "artifacts are read by ID") ||
				!strings.Contains(err.Error(), "use list_files") {
				t.Fatalf("filename error = %v", err)
			}
		})
	}
	t.Run("integration attachment", func(t *testing.T) {
		_, _, err := (Executor{}).loadMessageAttachment(t.Context(), Turn{}, "/artifacts/report.pdf")
		if err == nil || !strings.Contains(err.Error(), "artifacts are read by ID") ||
			!strings.Contains(err.Error(), "use list_files") {
			t.Fatalf("filename error = %v", err)
		}
	})
}

func TestCompleteImageReadReturnsMetadataAndMediaRef(t *testing.T) {
	artifactID := uuid.New()
	digest := "sha256:" + strings.Repeat("a", 64)
	result, err := completeImageRead("/artifacts/example", "image/png", digest, 12, artifactID, "")
	if err != nil {
		t.Fatal(err)
	}
	var parts []map[string]any
	if err := json.Unmarshal(asyncCompletionContent(t, result), &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0]["type"] != "structured_data" || parts[1]["type"] != "media_ref" ||
		parts[1]["artifact_id"] != artifactID.String() || parts[1]["exclude_from_model_context"] != nil {
		t.Fatalf("image read parts = %v", parts)
	}
	metadata, _ := parts[0]["value"].(map[string]any)
	if metadata["path"] != "/artifacts/example" || metadata["content_type"] != "image/png" ||
		metadata["size_bytes"] != float64(12) || metadata["digest"] != digest {
		t.Fatalf("image read metadata = %v", metadata)
	}
}
