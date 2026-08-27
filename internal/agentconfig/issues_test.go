package agentconfig

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func compileIssues(t *testing.T, format SourceFormat, source string, opts CompileOptions) []Issue {
	t.Helper()
	_, err := Compile(format, []byte(source), opts)
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("Compile error = %v, want ValidationError", err)
	}
	return validationErr.Issues
}

func describeIssues(issues []Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, fmt.Sprintf("%d:%d %s -> %s", issue.Line, issue.Column, issue.Path, issue.Message))
	}
	return strings.Join(parts, "; ")
}

func TestCompileReportsSchemaIssuesWithYAMLLocations(t *testing.T) {
	issues := compileIssues(t, SourceFormatYAML, `instruction: Help the user.
model:
  provider_config: openai-prod
tools:
  run_command:
    enabled: "yes"
bogus: 1
`, CompileOptions{})
	want := []Issue{
		{Path: "/model/name", Message: "required field is missing", Line: 2, Column: 1},
		{Path: "/tools/run_command/enabled", Message: "value is string but should be boolean or null", Line: 6, Column: 5},
		{Path: "/bogus", Message: "unknown field", Line: 7, Column: 1},
	}
	if got, wanted := describeIssues(issues), describeIssues(want); got != wanted {
		t.Fatalf("issues = %s, want %s", got, wanted)
	}
}

func TestCompileReportsYAMLSyntaxErrorLine(t *testing.T) {
	issues := compileIssues(
		t,
		SourceFormatYAML,
		"instruction: x\nmodel:\n  provider_config: a\n  name: [\n",
		CompileOptions{},
	)
	if len(issues) != 1 || issues[0].Line != 4 || issues[0].Path != "" || issues[0].Message == "" {
		t.Fatalf("issues = %+v, want one root issue on line 4", issues)
	}
}

func TestCompileReportsTrailingYAMLDocument(t *testing.T) {
	issues := compileIssues(
		t,
		SourceFormatYAML,
		"instruction: x\nmodel:\n  provider_config: a\n  name: b\n---\nmore: 1\n",
		CompileOptions{},
	)
	if len(issues) != 1 || issues[0].Message != "trailing document" || issues[0].Line != 6 {
		t.Fatalf("issues = %+v, want trailing document on line 6", issues)
	}
}

func TestCompilePicksTheClosestAlternativeSchema(t *testing.T) {
	issues := compileIssues(t, SourceFormatYAML, `instruction: x
model:
  provider_config: a
  name: b
machine_sources:
  - machine_name: primary
    max_machines: many
`, CompileOptions{})
	want := []Issue{
		{Path: "/machine_sources/0/max_machines", Message: "value is string but should be integer", Line: 7, Column: 5},
	}
	if got, wanted := describeIssues(issues), describeIssues(want); got != wanted {
		t.Fatalf("issues = %s, want %s", got, wanted)
	}
}

func TestCompileReportsCompilerIssuesWithYAMLLocations(t *testing.T) {
	issues := compileIssues(t, SourceFormatYAML, `instruction: x
model:
  provider_config: a
  name: b
machine_sources:
  - machine_pool_name: pool
    max_machines: 1
    initial_num_machines: 2
`, CompileOptions{})
	want := []Issue{
		{Path: "/machine_sources/0/initial_num_machines", Message: "cannot exceed max_machines", Line: 8, Column: 5},
	}
	if got, wanted := describeIssues(issues), describeIssues(want); got != wanted {
		t.Fatalf("issues = %s, want %s", got, wanted)
	}
}

func TestCompileWrapsResolverIssuesUnderTheReferencedField(t *testing.T) {
	notFound := errors.New("no such model")
	source := "instruction: x\nmodel:\n  provider_config: a\n  name: b\n"
	issues := compileIssues(t, SourceFormatYAML, source, CompileOptions{
		ResolveModelSelection: func(string, string) (ResolvedModelSelection, error) {
			return ResolvedModelSelection{}, NewIssue("/model/name", notFound)
		},
	})
	if len(issues) != 1 || issues[0].Path != "/model/name" || issues[0].Line != 4 || issues[0].Message != "no such model" {
		t.Fatalf("issues = %+v, want resolver issue at /model/name line 4", issues)
	}
}

func TestCompileJSONIssuesHaveNoLocation(t *testing.T) {
	issues := compileIssues(t, SourceFormatJSON, `{"instruction":"x","model":{"provider_config":"a"}}`, CompileOptions{})
	want := []Issue{{Path: "/model/name", Message: "required field is missing"}}
	if got, wanted := describeIssues(issues), describeIssues(want); got != wanted {
		t.Fatalf("issues = %s, want %s", got, wanted)
	}
}

func TestValidationErrorMessageJoinsIssues(t *testing.T) {
	err := &ValidationError{Issues: []Issue{
		{Path: "/model/name", Message: "required field is missing"},
		{Path: "", Message: "trailing document"},
	}}
	want := "agent config is invalid: model.name: required field is missing; trailing document"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestDisplayPathUsesDotsAndIndexes(t *testing.T) {
	tests := map[string]string{
		"":                                       "",
		"/model/name":                            "model.name",
		"/machine_sources/0/max_machines":        "machine_sources[0].max_machines",
		"/mcp/docs/tools/search~1all/permission": `mcp.docs.tools["search/all"].permission`,
		"/machine_sources/0/env_overlay/API_KEY": "machine_sources[0].env_overlay.API_KEY",
		"/tools/mcp__docs__search":               "tools.mcp__docs__search",
		"/tools/needs quoting":                   `tools["needs quoting"]`,
	}
	for pointer, want := range tests {
		if got := DisplayPath(pointer); got != want {
			t.Errorf("DisplayPath(%q) = %q, want %q", pointer, got, want)
		}
	}
}

func TestJSONPointerEscapesSegments(t *testing.T) {
	if got := jsonPointer("mcp", "a/b~c", 0, "url"); got != "/mcp/a~1b~0c/0/url" {
		t.Fatalf("jsonPointer = %q", got)
	}
	if got := pointerSegments("/mcp/a~1b~0c/0"); !reflect.DeepEqual(got, []string{"mcp", "a/b~c", "0"}) {
		t.Fatalf("pointerSegments = %q", got)
	}
}
