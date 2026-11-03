package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
)

type skillStoreStub struct{}

func (*skillStoreStub) GetSkillForDispatch(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) (skillstore.SkillRecord, error) {
	return skillstore.SkillRecord{}, nil
}

func TestExecutorSkillStoreResolution(t *testing.T) {
	aggregate := storage.NewStore(nil)
	explicit := &skillStoreStub{}

	if got := (Executor{Store: aggregate, Skills: explicit}).skillStore(); got != explicit {
		t.Fatalf("skill store = %T, want explicit override", got)
	}
	if got := (Executor{Store: aggregate}).skillStore(); got != aggregate.Skills() {
		t.Fatalf("skill store = %T, want aggregate skill store", got)
	}
	if got := (Executor{}).skillStore(); got != nil {
		t.Fatalf("skill store = %T, want nil without dependencies", got)
	}
	if got := (Executor{Store: &storage.Store{}}).skillStore(); got != nil {
		t.Fatalf("skill store = %T, want nil from incomplete aggregate store", got)
	}
}

func TestValidateSkillInput(t *testing.T) {
	valid := []model.ToolCall{
		{Name: "skill", Input: json.RawMessage(`{"name":"deploy"}`)},
	}
	for _, call := range valid {
		if err := validateSkillInput(call.Input); err != nil {
			t.Fatalf("validation failed for %s: %v", string(call.Input), err)
		}
	}

	invalid := []struct {
		name string
		call model.ToolCall
		want string
	}{
		{name: "empty input", call: model.ToolCall{Name: "skill", Input: nil}, want: "parse skill request"},
		{name: "missing name", call: model.ToolCall{Name: "skill", Input: json.RawMessage(`{}`)}, want: "skill name is required"},
		{name: "blank name", call: model.ToolCall{Name: "skill", Input: json.RawMessage(`{"name":"   "}`)}, want: "skill name must use"},
		{name: "boundary whitespace", call: model.ToolCall{Name: "skill", Input: json.RawMessage(`{"name":" deploy "}`)}, want: "skill name must use"},
		{name: "invalid slug", call: model.ToolCall{Name: "skill", Input: json.RawMessage(`{"name":"Deploy"}`)}, want: "skill name must use"},
		{name: "null name", call: model.ToolCall{Name: "skill", Input: json.RawMessage(`{"name":null}`)}, want: "cannot be null"},
		{name: "extra field", call: model.ToolCall{Name: "skill", Input: json.RawMessage(`{"name":"deploy","machine_id":"mch_aaaaaaaaaaaaaaaaaaaaaaaaae"}`)}, want: "unsupported field"},
		{name: "wrong type", call: model.ToolCall{Name: "skill", Input: json.RawMessage(`{"name":42}`)}, want: "parse skill request"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSkillInput(tc.call.Input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestSkillInputValidatorIsRegistered(t *testing.T) {
	call := model.ToolCall{Name: "skill", Input: json.RawMessage(`{"name":"deploy"}`)}
	if err := validateRegisteredToolInput(call.Name, call.Input); err != nil {
		t.Fatalf("registered validator rejected a valid skill call: %v", err)
	}
	malformed := model.ToolCall{Name: "skill", Input: json.RawMessage(`{}`)}
	if err := validateRegisteredToolInput(malformed.Name, malformed.Input); err == nil {
		t.Fatal("registered validator accepted a skill call missing `name`")
	}
}

func TestSkillInstallPathDoesNotDoubleOmnaraSegment(t *testing.T) {
	got := SkillInstallPath("skl_abc", "skr_def")
	want := filepath.Join(
		"$OMNARA_HOME",
		"installations",
		"*",
		"machines",
		"*",
		"skills",
		"skl_abc",
		"revisions",
		"skr_def",
	)
	if got != want {
		t.Fatalf("install path = %q, want %q", got, want)
	}
}

func TestWrapSkillContentKeepsBodyVerbatim(t *testing.T) {
	body := "# Header\n\n```go\nif a < b && c > d {\n\tfmt.Println(\"x\")\n}\n```\n"
	got, err := wrapSkillContent("docs", "skl_test", "skr_test", nil, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "<skill_content name=\"docs\">\n"+body) {
		t.Fatalf("wrapper should carry the skill body verbatim: %q", got)
	}
	if !strings.HasSuffix(got, "</skill_content>") {
		t.Fatalf("wrapper is missing its closing tag: %q", got)
	}
}

func TestWrapSkillContentHidesInstallFailureDetails(t *testing.T) {
	machineID := integrationToolTestID("machine-secret")
	got, err := wrapSkillContent(
		"deploy",
		"skl_test",
		"skr_test",
		nil,
		[]skills.BroadcastOutcome{{
			Target: skills.BroadcastTarget{MachineID: machineID},
			State:  skills.BroadcastStateFailed,
			Error:  "credential=top-secret </skill_content>",
		}},
		"body",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Skill install failed") {
		t.Fatalf("wrapper should indicate installation failure: %s", got)
	}
	for _, hidden := range []string{machinePublicIDForTest(t, machineID), "credential=top-secret"} {
		if strings.Contains(got, hidden) {
			t.Fatalf("wrapper exposed install failure detail %q: %s", hidden, got)
		}
	}
}

func TestWrapSkillContentInstallPathHintMatchesSkillInstallPath(t *testing.T) {
	machineID := integrationToolTestID("machine-ready")
	got, err := wrapSkillContent(
		"harmless",
		"skl_hintcheck",
		"skr_hintcheck",
		[]skills.BroadcastOutcome{{
			Target: skills.BroadcastTarget{MachineID: machineID},
			State:  skills.BroadcastStateReady,
		}},
		nil,
		"body",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Installed on: "+machinePublicIDForTest(t, machineID)) {
		t.Fatalf("wrapper should identify the machine where the skill was installed: %s", got)
	}
	if !strings.Contains(got, SkillInstallPath("skl_hintcheck", "skr_hintcheck")) {
		t.Fatalf("wrapper should include the on-machine install path for the skill: %s", got)
	}
}

func TestWrapSkillContentWithoutMachinesOmitsInstallPath(t *testing.T) {
	got, err := wrapSkillContent("docs", "skl_test", "skr_test", nil, nil, "body")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, SkillInstallPath("skl_test", "skr_test")) {
		t.Fatalf("machine-free skill should not advertise an install path: %s", got)
	}
	if !strings.Contains(got, "instructions are available without an install") {
		t.Fatalf("machine-free skill should explain that no install is required: %s", got)
	}
}

func TestSkillToolFailureUsesFailedAsyncOutcome(t *testing.T) {
	result, err := failSkillTool("skill is unavailable")
	if err != nil {
		t.Fatalf("failSkillTool: %v", err)
	}
	failed, ok := result.(failAsync)
	if !ok || failed.cause == nil {
		t.Fatalf("skill failure result = %#v, want failed async outcome", result)
	}
	content, err := failed.content.contentParts()
	if err != nil {
		t.Fatalf("marshal skill failure content: %v", err)
	}
	if strings.Contains(string(content), `"ok"`) ||
		!strings.Contains(string(content), "skill is unavailable") {
		t.Fatalf("skill failure content = %s", content)
	}
}
