package agentconfig

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const editYAMLSource = `# support agent
instruction: |
  Be direct.
  Escalate when unsure. Be direct.
model:
  provider_config: openai-prod
  name: gpt-test
tools:
  web_search: {}
  run_command:
    permission:
      mode: always_ask # keep
skills:
  - skl_one
  - skl_two
`

func setEdit(path string, value string) Edit {
	return Edit{Type: "set", Path: path, Value: json.RawMessage(value)}
}

func decodeEditedSource(t *testing.T, source string) map[string]any {
	t.Helper()
	var value map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(source), &value))
	return value
}

func TestApplyEditsOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		edit    Edit
		outcome string
		changed bool
	}{
		{
			"replace text everywhere",
			Edit{Type: "replace_text", Path: "/instruction", Find: "Be direct.", Replace: "Be kind."},
			"applied", true,
		},
		{"text not found", Edit{Type: "replace_text", Path: "/instruction", Find: "missing"}, "text_not_found", false},
		{
			"replace text missing path",
			Edit{Type: "replace_text", Path: "/mcp/linear/url", Find: "a"},
			"path_not_found", false,
		},
		{"set existing", setEdit("/model/name", `"gpt-next"`), "applied", true},
		{"set same value", setEdit("/model/name", `"gpt-test"`), "applied", false},
		{"set new key", setEdit("/tools/web_fetch", `{}`), "applied", true},
		{
			"set creates missing top-level section",
			setEdit("/mcp/linear", `{"url":"https://mcp.linear.app/mcp"}`),
			"applied", true,
		},
		{
			"set with missing deeper parent",
			setEdit("/tools/web_fetch/permission/mode", `"always_ask"`),
			"path_not_found", false,
		},
		{"set list index", setEdit("/skills/1", `"skl_three"`), "applied", true},
		{"set past list end", setEdit("/skills/2", `"skl_three"`), "path_not_found", false},
		{"remove existing", Edit{Type: "remove", Path: "/tools/web_search"}, "applied", true},
		{"remove missing", Edit{Type: "remove", Path: "/tools/web_fetch"}, "path_not_found", false},
		{"add item", Edit{Type: "add_item", Path: "/skills", Value: json.RawMessage(`"skl_three"`)}, "applied", true},
		{"add existing item", Edit{Type: "add_item", Path: "/skills", Value: json.RawMessage(`"skl_one"`)}, "item_exists", false},
		{"add item creates missing top-level list", Edit{Type: "add_item", Path: "/memory_stores", Value: json.RawMessage(`{"name":"notes","access":"read"}`)}, "applied", true},
		{"remove item", Edit{Type: "remove_item", Path: "/skills", Value: json.RawMessage(`"skl_one"`)}, "applied", true},
		{"remove missing item", Edit{Type: "remove_item", Path: "/skills", Value: json.RawMessage(`"skl_three"`)}, "item_not_found", false},
		{"remove item from missing list", Edit{Type: "remove_item", Path: "/memory_stores", Value: json.RawMessage(`"x"`)}, "item_not_found", false},
		{"replace text in null", Edit{Type: "replace_text", Path: "/mcp", Find: "a"}, "path_not_found", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ApplyEdits(SourceFormatYAML, editYAMLSource, []Edit{tt.edit})
			require.NoError(t, err)
			require.Equal(t, []string{tt.outcome}, result.Edits)
			require.Equal(t, tt.changed, result.Changed)
			if !tt.changed {
				require.Equal(t, editYAMLSource, result.Source)
			}
		})
	}
}

func TestApplyEditsJSONSource(t *testing.T) {
	source := `{"instruction":"Use <revision>a</revision> & go.","model":{"provider_config":"p","name":"m"},"tools":{"a/b":{}}}`
	result, err := ApplyEdits(SourceFormatJSON, source, []Edit{
		{Type: "replace_text", Path: "/instruction", Find: "<revision>a</revision>", Replace: "<revision>b</revision>"},
		{Type: "remove", Path: "/tools/a~1b"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"applied", "applied"}, result.Edits)
	require.JSONEq(t,
		`{"instruction":"Use <revision>b</revision> & go.","model":{"provider_config":"p","name":"m"},"tools":{}}`,
		result.Source,
	)
	require.Contains(t, result.Source, "<revision>b</revision>")
}

func TestApplyEditsRejectsEditsThatCannotFit(t *testing.T) {
	tests := []struct {
		name   string
		source string
		edit   Edit
	}{
		{"replace text on an object", editYAMLSource, Edit{Type: "replace_text", Path: "/model", Find: "a"}},
		{"add item to an object", editYAMLSource, Edit{Type: "add_item", Path: "/model", Value: json.RawMessage(`"x"`)}},
		{"path without leading slash", editYAMLSource, setEdit("model", `{}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ApplyEdits(SourceFormatYAML, tt.source, []Edit{tt.edit})
			require.ErrorIs(t, err, ErrInvalidEdit)
		})
	}
}

func TestApplyEditsTreatsNullSectionsAsEmpty(t *testing.T) {
	source := "instruction: x\nmodel:\n  provider_config: p\n  name: m\nmcp:\nskills:\n"
	result, err := ApplyEdits(SourceFormatYAML, source, []Edit{
		setEdit("/mcp/linear", `{"url":"https://mcp.linear.app/mcp"}`),
		{Type: "add_item", Path: "/skills", Value: json.RawMessage(`"skl_one"`)},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"applied", "applied"}, result.Edits)
	edited := decodeEditedSource(t, result.Source)
	require.Equal(t, map[string]any{"linear": map[string]any{"url": "https://mcp.linear.app/mcp"}}, edited["mcp"])
	require.Equal(t, []any{"skl_one"}, edited["skills"])
}

func TestApplyEditsKeepsAnchoredEditsLocal(t *testing.T) {
	source := "instruction: x\nmodel:\n  provider_config: p\n  name: m\ntools:\n" +
		"  run_command:\n    permission: &ask\n      mode: always_ask # ask first\n" +
		"  write_process:\n    permission: *ask\nskills:\n  - &first skl_one\nmemory_stores:\n  - {name: *first, access: read}\n"
	permissionMode := func(edited map[string]any, tool string) any {
		tools := testutil.RequireType[map[string]any](t, edited["tools"])
		entry := testutil.RequireType[map[string]any](t, tools[tool])
		return testutil.RequireType[map[string]any](t, entry["permission"])["mode"]
	}
	tests := []struct {
		name  string
		edit  Edit
		check func(map[string]any)
	}{
		{"set inside the anchored value", setEdit("/tools/run_command/permission/mode", `"always_allow"`),
			func(edited map[string]any) {
				require.Equal(t, "always_allow", permissionMode(edited, "run_command"))
				require.Equal(t, "always_ask", permissionMode(edited, "write_process"))
			}},
		{"replace text through the alias",
			Edit{Type: "replace_text", Path: "/tools/write_process/permission/mode", Find: "ask", Replace: "allow"},
			func(edited map[string]any) {
				require.Equal(t, "always_ask", permissionMode(edited, "run_command"))
				require.Equal(t, "always_allow", permissionMode(edited, "write_process"))
			}},
		{"replace the anchored value's parent", setEdit("/tools/run_command", `{}`),
			func(edited map[string]any) {
				require.Equal(t, "always_ask", permissionMode(edited, "write_process"))
			}},
		{"remove an anchored list item",
			Edit{Type: "remove_item", Path: "/skills", Value: json.RawMessage(`"skl_one"`)},
			func(edited map[string]any) {
				require.Equal(t, []any{}, edited["skills"])
				require.Equal(t, []any{map[string]any{"name": "skl_one", "access": "read"}}, edited["memory_stores"])
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ApplyEdits(SourceFormatYAML, source, []Edit{tt.edit})
			require.NoError(t, err)
			require.Equal(t, []string{"applied"}, result.Edits)
			require.NotContains(t, result.Source, "*")
			tt.check(decodeEditedSource(t, result.Source))
		})
	}
	_, err := ApplyEdits(SourceFormatYAML, "a: &x\n  b: *x\n", []Edit{setEdit("/c", `1`)})
	require.ErrorIs(t, err, ErrInvalidEdit)
}

func TestApplyEditsCapsConfigSize(t *testing.T) {
	model := "model:\n  provider_config: p\n  name: m\n"
	doubling := make([]Edit, 100)
	for i := range doubling {
		doubling[i] = Edit{Type: "replace_text", Path: "/instruction", Find: "a", Replace: "aa"}
	}
	_, err := ApplyEdits(SourceFormatYAML, "instruction: "+strings.Repeat("a", 4096)+"\n"+model, doubling)
	require.ErrorIs(t, err, ErrInvalidEdit)

	source := "instruction: x\n" + model + "tools:\n"
	var acrossFields []Edit
	for i := range 50 {
		name := fmt.Sprintf("tool_%d", i)
		source += "  " + name + ":\n    type: custom\n    description: " + strings.Repeat("a", 1000) + "\n"
		acrossFields = append(acrossFields, Edit{
			Type: "replace_text", Path: "/tools/" + name + "/description", Find: "a", Replace: strings.Repeat("b", 100),
		})
	}
	_, err = ApplyEdits(SourceFormatYAML, source, acrossFields)
	require.ErrorIs(t, err, ErrInvalidEdit)
	require.Regexp(t, `^edit \d+: `, err.Error())

	_, err = ApplyEdits(SourceFormatYAML, "instruction: x\n"+model,
		[]Edit{setEdit("/instruction", `"`+strings.Repeat("z", maxEditedSourceBytes)+`"`)})
	require.ErrorIs(t, err, ErrInvalidEdit)

	shrinkThenGrow, err := ApplyEdits(SourceFormatYAML, "instruction: "+strings.Repeat("a", 600_000)+"\n"+model, []Edit{
		{Type: "replace_text", Path: "/instruction", Find: "aa", Replace: "a"},
		{Type: "replace_text", Path: "/instruction", Find: "a", Replace: "aaa"},
	})
	require.NoError(t, err)
	grown := testutil.RequireType[string](t, decodeEditedSource(t, shrinkThenGrow.Source)["instruction"])
	require.Len(t, grown, 900_000)

	expandRemoveExpand, err := ApplyEdits(SourceFormatYAML, "instruction: x\n"+model+
		"tools:\n  a:\n    type: custom\n    description: a\n  b:\n    type: custom\n    description: b\n", []Edit{
		{Type: "replace_text", Path: "/tools/a/description", Find: "a", Replace: strings.Repeat("a", 600_000)},
		{Type: "remove", Path: "/tools/a"},
		{Type: "replace_text", Path: "/tools/b/description", Find: "b", Replace: strings.Repeat("b", 600_000)},
	})
	require.NoError(t, err)
	require.True(t, expandRemoveExpand.Changed)

	oversized := "instruction: " + strings.Repeat("a", maxEditedSourceBytes) + "\n" + model
	shrunk, err := ApplyEdits(SourceFormatYAML, oversized,
		[]Edit{{Type: "replace_text", Path: "/instruction", Find: "aaaa", Replace: "a"}})
	require.NoError(t, err)
	require.Less(t, len(shrunk.Source), len(oversized))
}

func TestApplyEditsSetsNull(t *testing.T) {
	result, err := ApplyEdits(SourceFormatYAML, "instruction: x\nmodel:\n  provider_config: p\n  name: m\n",
		[]Edit{setEdit("/mcp", `null`)})
	require.NoError(t, err)
	require.True(t, result.Changed)
	edited := decodeEditedSource(t, result.Source)
	value, ok := edited["mcp"]
	require.True(t, ok)
	require.Nil(t, value)
}

func TestApplyEditsHandlesNullListItems(t *testing.T) {
	source := "instruction: x\nmodel:\n  provider_config: p\n  name: m\ntools:\n  pick:\n    type: custom\n" +
		"    description: d\n    input_schema:\n      type: object\n      properties:\n        mode:\n          enum: [foo]\n"
	const path = "/tools/pick/input_schema/properties/mode/enum"
	addNull := Edit{Type: "add_item", Path: path, Value: json.RawMessage(`null`)}
	added, err := ApplyEdits(SourceFormatYAML, source, []Edit{addNull})
	require.NoError(t, err)
	require.Equal(t, []string{"applied"}, added.Edits)
	again, err := ApplyEdits(SourceFormatYAML, added.Source, []Edit{addNull})
	require.NoError(t, err)
	require.Equal(t, []string{"item_exists"}, again.Edits)
	removed, err := ApplyEdits(SourceFormatYAML, added.Source,
		[]Edit{{Type: "remove_item", Path: path, Value: json.RawMessage(`null`)}})
	require.NoError(t, err)
	require.Equal(t, []string{"applied"}, removed.Edits)
	require.Equal(t, decodeEditedSource(t, source), decodeEditedSource(t, removed.Source))
}

func TestApplyEditsKeepsYAMLText(t *testing.T) {
	setLong := setEdit("/model/cache_retention", `"long"`)
	tests := []struct {
		name, source string
		edit         Edit
		want         string
	}{
		{"nested set adds one line",
			"# agent\ninstruction: |\n  Be direct.\nmodel:\n  provider_config: p # provider\n  name: m\ntools:\n  web_search: {} # keep\n",
			setLong,
			"# agent\ninstruction: |\n  Be direct.\nmodel:\n  provider_config: p # provider\n  name: m\n  cache_retention: long\ntools:\n  web_search: {} # keep\n"},
		{"folded prompt stays as written",
			"instruction: >\n  Intro.\n\n   - indented line\nmodel:\n  name: m\n",
			setLong,
			"instruction: >\n  Intro.\n\n   - indented line\nmodel:\n  name: m\n  cache_retention: long\n"},
		{"literal prompt edit changes one line",
			"instruction: |\n    Be direct.  \n    Ask before deleting.\nmodel:\n  name: m\n",
			Edit{Type: "replace_text", Path: "/instruction", Find: "deleting.", Replace: "deleting. Log every command."},
			"instruction: |\n    Be direct.  \n    Ask before deleting. Log every command.\nmodel:\n  name: m\n"},
		{"prompt starting with a blank line",
			"instruction: |\n\n  ## Role\nmodel:\n  name: m\n",
			Edit{Type: "replace_text", Path: "/instruction", Find: "Role", Replace: "Role and rules"},
			"instruction: |\n\n  ## Role and rules\nmodel:\n  name: m\n"},
		{"quoted prompt becomes literal",
			"instruction: \"Be direct.\\nAsk first.\"\nmodel:\n  name: m\n",
			Edit{Type: "replace_text", Path: "/instruction", Find: "first.", Replace: "first. Log."},
			"instruction: |-\n  Be direct.\n  Ask first. Log.\nmodel:\n  name: m\n"},
		{"removed entry keeps nearby comments",
			"tools:\n  # built-ins\n  web_search: {}\n  run_command: {} # ask\ninstruction: x\n",
			Edit{Type: "remove", Path: "/tools/web_search"},
			"tools:\n  # built-ins\n  run_command: {} # ask\ninstruction: x\n"},
		{"crlf",
			"instruction: hi\r\nmodel:\r\n  name: m\r\n",
			setLong,
			"instruction: hi\r\nmodel:\r\n  name: m\r\n  cache_retention: long\r\n"},
		{"flow document is rewritten",
			"{instruction: hi, model: {name: m}}\n",
			setLong,
			"instruction: hi\nmodel:\n  cache_retention: long\n  name: m\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ApplyEdits(SourceFormatYAML, tt.source, []Edit{tt.edit})
			require.NoError(t, err)
			require.Equal(t, []string{"applied"}, result.Edits)
			require.Equal(t, tt.want, result.Source)
		})
	}
}

func TestApplyEditsReadsJSONLikeTheCompiler(t *testing.T) {
	source := `{"instruction":"See https:\/\/x.dev \u0085 now","model":{"provider_config":"p","name":"m"}}`
	result, err := ApplyEdits(SourceFormatJSON, source, []Edit{
		{Type: "replace_text", Path: "/instruction", Find: "now", Replace: "today"},
		setEdit("/model/name", `"a\/b"`),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"applied", "applied"}, result.Edits)
	require.JSONEq(t, `{"instruction":"See https://x.dev \u0085 today","model":{"provider_config":"p","name":"a/b"}}`,
		result.Source)
}

func TestApplyEditsComparesNumbersByValue(t *testing.T) {
	source := "instruction: x\nmax_subagents: 2\ntools:\n  pick:\n    enum: [1, 2]\n"
	result, err := ApplyEdits(SourceFormatYAML, source, []Edit{
		{Type: "remove_item", Path: "/tools/pick/enum", Value: json.RawMessage(`1.0`)},
		{Type: "add_item", Path: "/tools/pick/enum", Value: json.RawMessage(`2e0`)},
		setEdit("/max_subagents", `2.0`),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"applied", "item_exists", "applied"}, result.Edits)
	require.Equal(t, []any{2}, testutil.RequireType[map[string]any](t,
		testutil.RequireType[map[string]any](t, decodeEditedSource(t, result.Source)["tools"])["pick"])["enum"])
}

func TestApplyEditsMatchAcrossFormats(t *testing.T) {
	edits := []Edit{
		{Type: "replace_text", Path: "/instruction", Find: "direct", Replace: "kind"},
		setEdit("/model/cache_retention", `"long"`),
		{Type: "remove", Path: "/tools/web_search"},
		{Type: "add_item", Path: "/skills", Value: json.RawMessage(`"skl_three"`)},
		{Type: "remove_item", Path: "/skills", Value: json.RawMessage(`"skl_one"`)},
	}
	yamlResult, err := ApplyEdits(SourceFormatYAML, editYAMLSource, edits)
	require.NoError(t, err)
	jsonSource, _, err := sourceJSON(SourceFormatYAML, []byte(editYAMLSource))
	require.NoError(t, err)
	jsonResult, err := ApplyEdits(SourceFormatJSON, string(jsonSource), edits)
	require.NoError(t, err)
	require.Equal(t, yamlResult.Edits, jsonResult.Edits)
	yamlValue, err := sourceValue(SourceFormatYAML, []byte(yamlResult.Source))
	require.NoError(t, err)
	jsonValue, err := sourceValue(SourceFormatJSON, []byte(jsonResult.Source))
	require.NoError(t, err)
	require.Equal(t, yamlValue, jsonValue)
}

func TestApplyEditsWritesAnyText(t *testing.T) {
	for _, text := range []string{"\n\n", "\n", "  indented\nnext", "tail  \n\n\n", "a b\nc"} {
		raw, err := json.Marshal(text)
		require.NoError(t, err)
		result, err := ApplyEdits(SourceFormatYAML, "instruction: |\n  old\n  text\nmodel:\n  name: m\n",
			[]Edit{setEdit("/instruction", string(raw))})
		require.NoError(t, err)
		require.Equal(t, text, decodeEditedSource(t, result.Source)["instruction"])
	}
}

func TestApplyEditsChecksWrittenYAML(t *testing.T) {
	result, err := ApplyEdits(SourceFormatYAML, "instruction: |+\n  keep\n\n\nmodel:\n  name: m\n",
		[]Edit{{Type: "replace_text", Path: "/instruction", Find: "keep", Replace: "kept"}})
	require.NoError(t, err)
	require.Equal(t, "kept\n\n\n", decodeEditedSource(t, result.Source)["instruction"])
}

func TestApplyEditsRewritesNumbersLikeTheCompiler(t *testing.T) {
	source := "{instruction: x, model: {provider_config: p, name: m}, tools: {pick: {type: custom, description: d, " +
		"input_schema: {type: object, properties: {n: {const: 18446744073709551616}, s: {const: \"1e400\"}}}}}}\n"
	result, err := ApplyEdits(SourceFormatYAML, source,
		[]Edit{{Type: "replace_text", Path: "/instruction", Find: "x", Replace: "y"}})
	require.NoError(t, err)
	want, err := sourceValue(SourceFormatYAML, []byte(strings.Replace(source, "instruction: x", "instruction: y", 1)))
	require.NoError(t, err)
	got, err := sourceValue(SourceFormatYAML, []byte(result.Source))
	require.NoError(t, err)
	require.Equal(t, want, got)
}
