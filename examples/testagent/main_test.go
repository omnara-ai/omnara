package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestChatBootstrapsAgentAndSendsInput(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var secretBody map[string]any
	var providerBody map[string]any
	var modelBody map[string]any
	var grantBody map[string]any
	var configBody map[string]any
	var launchBody map[string]any
	var inputBody map[string]any
	var configAttempts int
	t.Setenv("OPENAI_API_KEY", "sk-test-local")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer omnara_pat_test" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs":
			_, _ = w.Write([]byte(`{"org":{"id":"org_1"},"project":{"id":"prj_1"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/org_1/model-provider-configs":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/org_1/secrets":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/org_1/secrets":
			if err := json.NewDecoder(r.Body).Decode(&secretBody); err != nil {
				t.Fatalf("decode secret body: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"sec_1"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/org_1/model-provider-configs":
			if err := json.NewDecoder(r.Body).Decode(&providerBody); err != nil {
				t.Fatalf("decode provider body: %v", err)
			}
			_, _ = w.Write([]byte(`{"config":{"id":"mpc_1"},"model_discovery":{"status":"failed","error":"test"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/org_1/model-provider-configs/mpc_1/models":
			if err := json.NewDecoder(r.Body).Decode(&modelBody); err != nil {
				t.Fatalf("decode model body: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"mdl_1"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/org_1/projects/prj_1/model-grants":
			if err := json.NewDecoder(r.Body).Decode(&grantBody); err != nil {
				t.Fatalf("decode grant body: %v", err)
			}
			_, _ = w.Write([]byte(`{"grant":{"id":"pmg_1"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/org_1/projects/prj_1/agent-configs":
			configAttempts++
			if configAttempts == 1 {
				http.Error(w, `{"error":"model.provider_config \"openai-prod\" was not found"}`, http.StatusBadRequest)
				return
			}
			if err := json.NewDecoder(r.Body).Decode(&configBody); err != nil {
				t.Fatalf("decode config body: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"acfg_1"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/org_1/projects/prj_1/agents":
			if err := json.NewDecoder(r.Body).Decode(&launchBody); err != nil {
				t.Fatalf("decode launch body: %v", err)
			}
			_, _ = w.Write([]byte(`{"agent":{"id":"ses_1"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/org_1/projects/prj_1/agents/ses_1/events/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write(
				[]byte(
					"data: {\"sequence\":1,\"event_kind\":\"model_output\",\"content_blocks\":[{\"type\":\"text\",\"text\":\"ready\"}]}\n\n",
				),
			)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/org_1/projects/prj_1/agents/ses_1/inputs":
			if err := json.NewDecoder(r.Body).Decode(&inputBody); err != nil {
				t.Fatalf("decode input body: %v", err)
			}
			_, _ = w.Write([]byte(`{"agent_input":{"id":"inp_1"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(
		yamlPath,
		[]byte("name: Test Agent\ninstruction: Help.\nmodel:\n  provider_config: openai-prod\n  name: gpt-5.5\n"),
		0o600,
	); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run(
		context.Background(),
		[]string{"--api-url", server.URL, "--token", "omnara_pat_test", "chat", yamlPath},
		strings.NewReader("hello\n"),
		&stdout,
		&stderr,
		server.Client(),
	)
	if err != nil {
		t.Fatalf("run chat: %v", err)
	}
	if !strings.Contains(stdout.String(), "\n"+ansiCyan+">"+ansiReset+" \n") {
		t.Fatalf("stdout did not add newline before and after prompt input: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "org: org_1") || !strings.Contains(stderr.String(), "agent: ses_1") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if secretBody["name"] != "openai-prod-api-key" || secretBody["kind"] != "generic" {
		t.Fatalf("unexpected secret body: %+v", secretBody)
	}
	if payload, ok := secretBody["payload"].(map[string]any); !ok || payload["value"] != "sk-test-local" {
		t.Fatalf("unexpected secret payload: %+v", secretBody)
	}
	if providerBody["name"] != "openai-prod" || providerBody["preset"] != "openai" ||
		providerBody["credential_secret_id"] != "sec_1" {
		t.Fatalf("unexpected provider body: %+v", providerBody)
	}
	if modelBody["name"] != "gpt-5.5" || modelBody["provider_model_slug"] != "gpt-5.5" ||
		modelBody["context_window_tokens"] != float64(128000) ||
		modelBody["supports_tools"] != true {
		t.Fatalf("unexpected model body: %+v", modelBody)
	}
	if grantBody["configured_model_id"] != "mdl_1" {
		t.Fatalf("unexpected grant body: %+v", grantBody)
	}
	sourceYAML, ok := configBody["source"].(string)
	if !ok || configBody["source_format"] != "yaml" || !strings.Contains(sourceYAML, "gpt-5.5") {
		t.Fatalf("unexpected config body: %+v", configBody)
	}
	if _, ok := launchBody["profile"]; ok || launchBody["config"] != "acfg_1" {
		t.Fatalf("unexpected launch body: %+v", launchBody)
	}
	blocks, ok := inputBody["content_blocks"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("unexpected input body: %+v", inputBody)
	}
	block, ok := blocks[0].(map[string]any)
	if !ok || block["text"] != "hello" {
		t.Fatalf("unexpected input body: %+v", inputBody)
	}
	mu.Lock()
	gotPaths := append([]string(nil), paths...)
	mu.Unlock()
	wantPaths := []string{
		"POST /api/v1/orgs",
		"POST /api/v1/orgs/org_1/projects/prj_1/agent-configs",
		"GET /api/v1/orgs/org_1/model-provider-configs",
		"GET /api/v1/orgs/org_1/secrets",
		"POST /api/v1/orgs/org_1/secrets",
		"POST /api/v1/orgs/org_1/model-provider-configs",
		"POST /api/v1/orgs/org_1/model-provider-configs/mpc_1/models",
		"POST /api/v1/orgs/org_1/projects/prj_1/model-grants",
		"POST /api/v1/orgs/org_1/projects/prj_1/agent-configs",
		"POST /api/v1/orgs/org_1/projects/prj_1/agents",
		"POST /api/v1/orgs/org_1/projects/prj_1/agents/ses_1/inputs",
	}
	if !containsSubsequence(gotPaths, wantPaths) {
		t.Fatalf("paths = %#v", gotPaths)
	}
}

func TestChatUsesProvidedOrgAndProject(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer omnara_pat_test" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/org_existing/projects/prj_existing/agent-configs":
			_, _ = w.Write([]byte(`{"id":"acfg_1"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/org_existing/projects/prj_existing/agents":
			_, _ = w.Write([]byte(`{"agent":{"id":"ses_1"}}`))
		case r.Method == http.MethodGet &&
			r.URL.Path == "/api/v1/orgs/org_existing/projects/prj_existing/agents/ses_1/events/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write(
				[]byte(
					"data: {\"sequence\":1,\"event_kind\":\"model_output\"," +
						"\"content_blocks\":[{\"type\":\"text\",\"text\":\"ready\"}]}\n\n",
				),
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(
		yamlPath,
		[]byte("name: Test Agent\ninstruction: Help.\nmodel:\n  provider_config: openai-prod\n  name: gpt-5.5\n"),
		0o600,
	); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run(
		context.Background(),
		[]string{
			"--api-url",
			server.URL,
			"--token",
			"omnara_pat_test",
			"--org-id",
			"org_existing",
			"--project-id",
			"prj_existing",
			"chat",
			yamlPath,
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
		server.Client(),
	)
	if err != nil {
		t.Fatalf("run chat: %v", err)
	}
	if !strings.Contains(stderr.String(), "org: org_existing") ||
		!strings.Contains(stderr.String(), "project: prj_existing") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	mu.Lock()
	gotPaths := append([]string(nil), paths...)
	mu.Unlock()
	for _, path := range gotPaths {
		if path == "POST /api/v1/orgs" {
			t.Fatalf("unexpected bootstrap org request in paths = %#v", gotPaths)
		}
		if strings.Contains(path, "model-provider-configs") || strings.Contains(path, "model-grants") {
			t.Fatalf("unexpected model provider bootstrap request in paths = %#v", gotPaths)
		}
	}
	wantPaths := []string{
		"POST /api/v1/orgs/org_existing/projects/prj_existing/agent-configs",
		"POST /api/v1/orgs/org_existing/projects/prj_existing/agents",
	}
	if !containsSubsequence(gotPaths, wantPaths) {
		t.Fatalf("paths = %#v", gotPaths)
	}
}

func TestPromptContentBlocksAttachesImageReferences(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "pixel.png")
	imageBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 1, 2, 3}
	if err := os.WriteFile(imagePath, imageBytes, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	blocks, err := promptContentBlocks("review @[ " + imagePath + " ] after")
	if err != nil {
		t.Fatalf("prompt content blocks: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("blocks = %+v", blocks)
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "review" {
		t.Fatalf("unexpected leading text block: %+v", blocks[0])
	}
	if blocks[1]["type"] != "media" || blocks[1]["media_type"] != "image/png" || blocks[1]["filename"] != "pixel.png" {
		t.Fatalf("unexpected attachment block: %+v", blocks[1])
	}
	if blocks[1]["data"] != base64.StdEncoding.EncodeToString(imageBytes) {
		t.Fatalf("unexpected attachment data: %+v", blocks[1])
	}
	if blocks[2]["type"] != "text" || blocks[2]["text"] != "after" {
		t.Fatalf("unexpected trailing text block: %+v", blocks[2])
	}
}

func TestPromptContentBlocksAttachesBareYAMLReference(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "example.yaml")
	yamlBytes := []byte("name: Test Agent\ninstruction: Help.\n")
	if err := os.WriteFile(yamlPath, yamlBytes, 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	blocks, err := promptContentBlocks("whats in this file @" + yamlPath)
	if err != nil {
		t.Fatalf("prompt content blocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %+v", blocks)
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "whats in this file" {
		t.Fatalf("unexpected text block: %+v", blocks[0])
	}
	if blocks[1]["type"] != "media" || blocks[1]["media_type"] != "text/plain" || blocks[1]["filename"] != "example.yaml" {
		t.Fatalf("unexpected yaml attachment block: %+v", blocks[1])
	}
	if blocks[1]["data"] != base64.StdEncoding.EncodeToString(yamlBytes) {
		t.Fatalf("unexpected yaml attachment data: %+v", blocks[1])
	}
}

func TestAttachmentMediaTypeStripsDetectedCharset(t *testing.T) {
	mediaType, kind, err := attachmentMediaType("notes.unknown", []byte("plain utf-8 text\n"))
	if err != nil {
		t.Fatalf("attachment media type: %v", err)
	}
	if mediaType != "text/plain" || kind != "document" {
		t.Fatalf("media type=%q kind=%q, want text/plain document", mediaType, kind)
	}
}

func TestPromptContentBlocksRejectsUnsupportedAttachment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(path, []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	_, err := promptContentBlocks("attach @[" + path + "]")
	if err == nil || !strings.Contains(err.Error(), "unsupported media type") {
		t.Fatalf("prompt content blocks error = %v, want unsupported media type", err)
	}
}

func containsSubsequence(got, want []string) bool {
	next := 0
	for _, item := range got {
		if next < len(want) && item == want[next] {
			next++
		}
	}
	return next == len(want)
}

func TestGlobalConfigReadsEnvironment(t *testing.T) {
	t.Setenv("OMNARA_API_URL", "http://api.example.test")
	t.Setenv("OMNARA_TOKEN", "omnara_pat_test")
	t.Setenv("OMNARA_ORG_ID", "org_env")
	t.Setenv("OMNARA_PROJECT_ID", "prj_env")

	cfg, err := parseGlobalFlags(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse global flags: %v", err)
	}
	if cfg.APIURL != "http://api.example.test" || cfg.Token != "omnara_pat_test" || cfg.OrgID != "org_env" ||
		cfg.ProjectID != "prj_env" {
		t.Fatalf("unexpected config from env: %+v", cfg)
	}
}

func TestGlobalConfigRequiresOrgAndProjectTogether(t *testing.T) {
	t.Setenv("OMNARA_TOKEN", "omnara_pat_test")

	_, err := parseGlobalFlags([]string{"--org-id", "org_1"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "must be specified together") {
		t.Fatalf("parse global flags error = %v, want paired org/project error", err)
	}
}

func TestGlobalConfigRejectsNonOmnaraPAT(t *testing.T) {
	t.Setenv("OMNARA_TOKEN", "plain-secret")

	_, err := parseGlobalFlags(nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `must start with "omnara_pat_"`) ||
		!strings.Contains(err.Error(), "create an Omnara PAT") {
		t.Fatalf("parse global flags error = %v, want PAT prefix error", err)
	}
}

func TestReadSSEForwardsAgentEvents(t *testing.T) {
	stream := "event: model_output\nid: 1\n" +
		"data: {\"sequence\":1,\"event_kind\":\"model_output\"," +
		"\"content_blocks\":[{\"type\":\"text\",\"text\":\"hi\"}]}\n\n"
	events := make(chan agentEvent, 1)
	if err := readSSE(context.Background(), strings.NewReader(stream), events); err != nil {
		t.Fatalf("read SSE: %v", err)
	}
	event := <-events
	if event.Sequence != 1 || event.ContentBlocks[0].Text != "hi" {
		t.Fatalf("unexpected event: %+v", event)
	}
}

func TestReadSSEAcceptsLargeAgentEvents(t *testing.T) {
	text := strings.Repeat("linear issue ", 10000)
	payload, err := json.Marshal(map[string]any{
		"sequence":     2,
		"event_kind":   "tool_result",
		"tool_call_id": "tcl_large",
		"content_blocks": []map[string]string{{
			"type": "text",
			"text": text,
		}},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	stream := "data: " + string(payload) + "\n\n"
	events := make(chan agentEvent, 1)
	if err := readSSE(context.Background(), strings.NewReader(stream), events); err != nil {
		t.Fatalf("read large SSE: %v", err)
	}
	event := <-events
	if event.Sequence != 2 || event.ContentBlocks[0].Text != text {
		t.Fatalf("unexpected large event: sequence=%d text_len=%d", event.Sequence, len(event.ContentBlocks[0].Text))
	}
}

func TestPrintEventSuppressesUserEchoAndFormatsAgent(t *testing.T) {
	var userOut bytes.Buffer
	printed := printEvent(&userOut, agentEvent{
		EventKind: "agent_input",
		ContentBlocks: []contentBlock{
			{Type: "text", Text: "hello"},
		},
	})
	if printed || userOut.Len() != 0 {
		t.Fatalf("user event printed=%v out=%q", printed, userOut.String())
	}

	var agentOut bytes.Buffer
	printed = printEvent(&agentOut, agentEvent{
		EventKind: "model_output",
		ContentBlocks: []contentBlock{
			{Type: "text", Text: "hi"},
		},
	})
	if !printed || !strings.Contains(stripANSI(agentOut.String()), "[agent] hi") {
		t.Fatalf("agent event printed=%v out=%q", printed, agentOut.String())
	}
}

func TestPrintPromptAddsLeadingNewline(t *testing.T) {
	var out bytes.Buffer
	printPrompt(&out, "")
	if out.String() != "\n"+ansiCyan+">"+ansiReset+" " {
		t.Fatalf("prompt = %q", out.String())
	}

	out.Reset()
	printPrompt(&out, "answer question")
	if out.String() != "\nanswer question "+ansiCyan+">"+ansiReset+" " {
		t.Fatalf("question prompt = %q", out.String())
	}
}

func TestPrintEventFormatsToolCallAndResult(t *testing.T) {
	var toolCallOut bytes.Buffer
	printed := printEvent(&toolCallOut, agentEvent{
		EventKind: "model_output",
		ContentBlocks: []contentBlock{
			{
				Type:       "tool_call",
				ToolName:   "run_command",
				ToolCallID: "tcall_123",
				Input:      json.RawMessage(`{"command":"echo hi"}`),
			},
		},
	})
	if !printed || !strings.Contains(stripANSI(toolCallOut.String()), "[tool call]") ||
		!strings.Contains(stripANSI(toolCallOut.String()), "run_command") ||
		!strings.Contains(stripANSI(toolCallOut.String()), `{"command":"echo hi"}`) {
		t.Fatalf("tool call event printed=%v out=%q", printed, toolCallOut.String())
	}

	toolCallOut.Reset()
	printed = printEvent(&toolCallOut, agentEvent{
		EventKind: "model_output",
		ContentBlocks: []contentBlock{
			{
				Type:       "tool_call",
				ToolName:   "ask_question",
				ToolCallID: "tcall_456",
				Input: json.RawMessage(
					`{"questions":[{"prompt":"Ship it?","options":[{"label":"Yes"}]}]}`,
				),
			},
		},
	})
	if !printed ||
		!strings.Contains(stripANSI(toolCallOut.String()), "[tool call] ask_question tcall_456\n  question Ship it?") {
		t.Fatalf("ask question call event printed=%v out=%q", printed, toolCallOut.String())
	}

	var resultOut bytes.Buffer
	printed = printEvent(&resultOut, agentEvent{
		EventKind:  "tool_result",
		ToolCallID: "tcall_123",
		ContentBlocks: []contentBlock{
			{Type: "text", Text: "hi"},
		},
	})
	if !printed || !strings.Contains(stripANSI(resultOut.String()), "[tool result]") ||
		!strings.Contains(stripANSI(resultOut.String()), "tcall_123") ||
		!strings.Contains(stripANSI(resultOut.String()), "hi") {
		t.Fatalf("tool result event printed=%v out=%q", printed, resultOut.String())
	}

	resultOut.Reset()
	printed = printEvent(&resultOut, agentEvent{
		EventKind:  "tool_result",
		ToolCallID: "tcall_456",
		ContentBlocks: []contentBlock{{
			Type:  "structured_data",
			Value: json.RawMessage(`{"answers":[{"question":"Ship it?","selected_options":["Yes"]}]}`),
		}},
	})
	if !printed || !strings.Contains(stripANSI(resultOut.String()), "[tool result] tcall_456\n  answer   Ship it?: Yes") {
		t.Fatalf("answer result event printed=%v out=%q", printed, resultOut.String())
	}

	resultOut.Reset()
	printed = printEvent(&resultOut, agentEvent{
		EventKind:  "tool_result",
		ToolCallID: "tcall_789",
		Outcome:    "canceled",
	})
	if !printed || !strings.Contains(stripANSI(resultOut.String()), "[tool result] tcall_789 (canceled)") {
		t.Fatalf("empty result event printed=%v out=%q", printed, resultOut.String())
	}
}

func TestSendConsoleInputsAnswersPendingQuestion(t *testing.T) {
	var resolveBody map[string]any
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		if gotPath != "POST /api/v1/orgs/org_1/projects/prj_1/agents/ses_1/interactions/int_1/resolve" {
			t.Fatalf("unexpected request: %s", gotPath)
		}
		if err := json.NewDecoder(r.Body).Decode(&resolveBody); err != nil {
			t.Fatalf("decode resolve body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"int_1","state":"resolved"}`))
	}))
	defer server.Close()

	client := apiClient{baseURL: server.URL, token: "tok", orgID: "org_1", projectID: "prj_1", httpClient: server.Client()}
	interactions := newInteractionState()
	interactions.replaceOpen(
		[]pendingInteraction{
			{
				ID:              "int_1",
				InteractionKind: "question",
				State:           "open",
				Request: json.RawMessage(
					`{"title":"Question","questions":[{"prompt":"Ship?",` +
						`"options":[{"label":"Yes"},{"label":"No"}]}]}`,
				),
			},
		},
	)

	var stdout bytes.Buffer
	if err := sendConsoleInputs(
		context.Background(),
		client,
		"ses_1",
		interactions,
		strings.NewReader("yes\n"),
		&stdout,
	); err != nil {
		t.Fatalf("send input: %v", err)
	}
	if gotPath == "" {
		t.Fatal("expected resolve request")
	}
	answers, answersOK := resolveBody["answers"].([]any)
	var answer map[string]any
	answerOK := false
	if len(answers) == 1 {
		answer, answerOK = answers[0].(map[string]any)
	}
	if !answersOK || len(answers) != 1 || !answerOK ||
		!reflect.DeepEqual(answer["option_indices"], []any{float64(0)}) {
		t.Fatalf("unexpected resolve body: %+v", resolveBody)
	}
}

func TestSendConsoleInputsResolvesPendingApproval(t *testing.T) {
	var resolveBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/orgs/org_1/projects/prj_1/agents/ses_1/interactions/int_1/resolve" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&resolveBody); err != nil {
			t.Fatalf("decode resolve body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"int_1","state":"resolved"}`))
	}))
	defer server.Close()

	client := apiClient{baseURL: server.URL, token: "tok", orgID: "org_1", projectID: "prj_1", httpClient: server.Client()}
	interactions := newInteractionState()
	interactions.replaceOpen(
		[]pendingInteraction{
			{
				ID:              "int_1",
				InteractionKind: "permission",
				State:           "open",
				Request: json.RawMessage(
					`{"title":"Permission requested for run_command","context":[` +
						`{"label":"Command","value":"echo hi"}],"questions":[{` +
						`"prompt":"Allow this tool call?","options":[{"label":"Allow"},` +
						`{"label":"Deny","allows_text":true}]}]}`,
				),
			},
		},
	)

	if err := sendConsoleInputs(
		context.Background(),
		client,
		"ses_1",
		interactions,
		strings.NewReader("1: unsafe, destructive command\n"),
		&bytes.Buffer{},
	); err != nil {
		t.Fatalf("send input: %v", err)
	}
	answers, answersOK := resolveBody["answers"].([]any)
	var answer map[string]any
	answerOK := false
	if len(answers) == 1 {
		answer, answerOK = answers[0].(map[string]any)
	}
	if !answersOK || len(answers) != 1 || !answerOK ||
		!reflect.DeepEqual(answer["option_indices"], []any{float64(1)}) ||
		answer["text"] != "unsafe, destructive command" {
		t.Fatalf("unexpected resolve body: %+v", resolveBody)
	}
}

func TestInteractionFormAnswerAcceptsTextOnlyForCapableOptions(t *testing.T) {
	question := interactionQuestion{
		Options: []interactionQuestionOption{
			{Label: "Allow"},
			{Label: "Deny", AllowsText: true},
		},
	}
	answer, err := interactionFormAnswer(question, "1: unsafe command")
	if err != nil {
		t.Fatalf("parse denial reason: %v", err)
	}
	if !reflect.DeepEqual(answer.OptionIndices, []int{1}) ||
		answer.Text != "unsafe command" {
		t.Fatalf("answer = %+v", answer)
	}
	if _, err := interactionFormAnswer(question, "0: unnecessary"); err == nil {
		t.Fatal("text was accepted for an option that does not allow it")
	}
}

func TestInteractionFormAnswersParsesEachQuestionIndependently(t *testing.T) {
	form := interactionForm{
		Questions: []interactionQuestion{
			{Options: []interactionQuestionOption{{Label: "Yes"}, {Label: "No"}}},
			{
				Options: []interactionQuestionOption{
					{Label: "Use default"},
					{Label: "Other", AllowsText: true},
				},
			},
		},
	}
	answers, err := interactionFormAnswers(
		form,
		[]string{"0", "1: US West, with failover"},
	)
	if err != nil {
		t.Fatalf("parse interaction answers: %v", err)
	}
	want := []interactionAnswer{
		{OptionIndices: []int{0}},
		{OptionIndices: []int{1}, Text: "US West, with failover"},
	}
	if !reflect.DeepEqual(answers, want) {
		t.Fatalf("answers = %+v, want %+v", answers, want)
	}
}

func TestPrintInteractionUsesStructuredPrompt(t *testing.T) {
	var out bytes.Buffer
	printInteraction(
		&out,
		pendingInteraction{
			ID:              "int_1",
			InteractionKind: "question",
			Request: json.RawMessage(
				`{"title":"Question","questions":[{"prompt":"Ship it?",` +
					`"options":[{"label":"Yes"},{"label":"No"}]}]}`,
			),
		},
	)
	if !strings.Contains(
		stripANSI(out.String()),
		"[question] Question\n  0: Ship it? [0=Yes, 1=No]",
	) {
		t.Fatalf("question output = %q", out.String())
	}

	out.Reset()
	printInteraction(
		&out,
		pendingInteraction{
			ID:              "int_2",
			InteractionKind: "permission",
			Request: json.RawMessage(
				`{"title":"Permission requested for run_command","context":[` +
					`{"label":"Command","value":"echo hi"}],"questions":[{` +
					`"prompt":"Allow this tool call?","options":[{"label":"Allow"},` +
					`{"label":"Deny","allows_text":true}]}]}`,
			),
		},
	)
	if !strings.Contains(
		stripANSI(out.String()),
		"[approval] Permission requested for run_command\n  Command: echo hi",
	) {
		t.Fatalf("approval output = %q", out.String())
	}
}

func stripANSI(s string) string {
	for _, code := range []string{
		ansiReset, ansiDim, ansiGray, ansiCyan, ansiGreen, ansiYellow, ansiBlue, ansiMagenta, ansiRed,
	} {
		s = strings.ReplaceAll(s, code, "")
	}
	return s
}
