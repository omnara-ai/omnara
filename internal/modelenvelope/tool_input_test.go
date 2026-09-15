package modelenvelope

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/stretchr/testify/require"
)

func TestToolInputRejectsAmbiguityBeforeNormalization(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, input, diagnostic string }{
		{"decoded root key", `{"a":1,"\u0061":2}`, `duplicate JSON object key "a"`},
		{"decoded nested key", `{"outer":[{"a":1,"\u0061":2}]}`, "/outer/0/a"},
		{"routing duplicate", `{"omnara_channel":"one","\u006fmnara_channel":"two"}`, "duplicate"},
		{"trailing value", `{} {}`, "trailing JSON"},
		{"trailing data", `{} garbage`, "trailing JSON"},
		{"invalid UTF8", "{\"value\":\"\xff\"}", "invalid UTF-8"},
		{
			"depth",
			`{"v":` + strings.Repeat("[", jsoncanonical.MaxDepth) + "0" + strings.Repeat("]", jsoncanonical.MaxDepth) + "}",
			"depth",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			raw := json.RawMessage(test.input)
			require.ErrorContains(t, ValidateToolInput(raw), test.diagnostic)
			normalized, err := NormalizeToolInput(raw)
			require.ErrorContains(t, err, test.diagnostic)
			require.Nil(t, normalized)
			require.Equal(t, test.input, string(raw))
		})
	}
}

func TestToolInputKeepsNumbersAndLiteralRoutingField(t *testing.T) {
	t.Parallel()
	const body = `{"count":9007199254740993,"decimal":1.2300,"exponent":1e400,` +
		`"omnara_channel":"unchanged","nested":{"omnara_channel":true}}`
	raw := json.RawMessage(" \n" + body + "\t")
	normalized, err := NormalizeToolInput(raw)
	require.NoError(t, err)
	require.Equal(t, body, string(normalized))
	normalized[0] = '['
	require.Equal(t, " \n"+body+"\t", string(raw), "normalized bytes must not alias their source")
}

func TestToolInputBudgetIncludesWhitespaceBeforeNormalization(t *testing.T) {
	raw := bytes.Repeat([]byte(" "), DefaultMaxProviderResponseBytes+1)
	copy(raw, "{}")
	_, err := NormalizeToolInput(raw)
	require.ErrorContains(t, err, "byte limit")
}
