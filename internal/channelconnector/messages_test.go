package channelconnector

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestMessageAllowsFilesWithoutTextAndPreservesContents(t *testing.T) {
	t.Parallel()
	id, err := publicid.Encode(publicid.KindArtifact, uuid.New())
	require.NoError(t, err)
	require.NoError(t, (Message{ArtifactIDs: []string{id}}).Validate())
	message := Message{Text: "  literal text\n", ArtifactIDs: []string{id}}
	require.NoError(t, message.Validate())
	require.Equal(t, "  literal text\n", message.Text)
	require.Equal(t, []string{id}, message.ArtifactIDs)
}

func TestMessageRejectsInvalidContent(t *testing.T) {
	t.Parallel()
	id, err := publicid.Encode(publicid.KindArtifact, uuid.New())
	require.NoError(t, err)
	wrongKind, err := publicid.Encode(publicid.KindAgent, uuid.New())
	require.NoError(t, err)
	for name, message := range map[string]Message{
		"empty":              {},
		"whitespace":         {Text: " \t\n"},
		"NUL":                {Text: "hello\x00"},
		"invalid UTF-8":      {Text: string([]byte{0xff})},
		"UTF-8 byte limit":   {Text: strings.Repeat("界", MaxMessageTextBytes/3+1)},
		"wrong ID kind":      {ArtifactIDs: []string{wrongKind}},
		"duplicate artifact": {ArtifactIDs: []string{id, id}},
		"too many artifacts": {ArtifactIDs: make([]string, MaxOperationArtifacts+1)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Error(t, message.Validate())
		})
	}
}

func TestSendParamsPreserveExactNumbersAndDoNotApplySchemaDefaults(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"},"draft":{"type":"boolean","default":true}},"additionalProperties":false}`)
	params := json.RawMessage(`{"id":9007199254740993}`)
	before := string(params)
	normalized, err := ValidateSendParams(schema, params)
	require.NoError(t, err)
	require.Equal(t, before, string(normalized))
	require.Equal(t, before, string(params))
	normalized, err = ValidateSendParams(schema, nil)
	require.NoError(t, err)
	require.Equal(t, `{}`, string(normalized))
}

func TestSendParamsValidateCurrentConditionalSchema(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{
		"type":"object","properties":{"review":{"type":"boolean"},"line":{"type":"integer","minimum":1}},
		"if":{"required":["review"],"properties":{"review":{"const":true}}},
		"then":{"required":["line"]},"additionalProperties":false
	}`)
	_, err := ValidateSendParams(schema, json.RawMessage(`{"review":true,"line":5}`))
	require.NoError(t, err)
	for _, raw := range []string{
		`null`, `[]`, `{"review":true}`, `{"review":true,"line":0}`,
		`{"review":false,"review":true,"line":5}`, `{"review":false,"unknown":1}`,
		`{"line":1e1000000}`, `{"\u0000":true}`, `{} {}`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			_, err := ValidateSendParams(schema, json.RawMessage(raw))
			require.Error(t, err)
		})
	}
	_, err = ValidateSendParams(json.RawMessage(`{"type":"object","required":["new_field"]}`), json.RawMessage(`{}`))
	require.Error(t, err, "changing the registered schema must change validation")
}

func TestSendParamsRequirePairedOptionalFields(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{"start_line":{"type":"integer"},"start_side":{"type":"string"}},
		"dependentRequired":{"start_line":["start_side"],"start_side":["start_line"]},
		"additionalProperties":false
	}`)
	for raw, valid := range map[string]bool{
		`{}`:                                    true,
		`{"start_line":1,"start_side":"RIGHT"}`: true,
		`{"start_line":1}`:                      false,
		`{"start_side":"RIGHT"}`:                false,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			_, err := ValidateSendParams(schema, json.RawMessage(raw))
			require.Equal(t, valid, err == nil, "%v", err)
		})
	}
}
