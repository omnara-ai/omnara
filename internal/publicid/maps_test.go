package publicid

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestIDMapJSON(t *testing.T) {
	id := uuid.New()
	public, err := Encode(KindSecret, id)
	require.NoError(t, err)
	for _, raw := range []string{`{}`, `null`, `{"TOKEN":"` + public + `","REMOVE":null}`} {
		internal, err := DecodeMapJSON(KindSecret, json.RawMessage(raw))
		require.NoError(t, err)
		if raw != `{}` && raw != `null` {
			require.JSONEq(t, `{"TOKEN":"`+id.String()+`","REMOVE":null}`, string(internal))
		}
		external, err := EncodeMapJSON(KindSecret, internal)
		require.NoError(t, err)
		require.JSONEq(t, raw, string(external))
	}
	got, err := DecodeMapJSON(KindSecret, nil)
	require.NoError(t, err)
	require.Nil(t, got)
	wrongKind, err := Encode(KindMachine, id)
	require.NoError(t, err)
	for _, value := range []string{id.String(), "invalid", wrongKind} {
		_, err := DecodeMapJSON(KindSecret, json.RawMessage(`{"TOKEN":"`+value+`"}`))
		require.Error(t, err)
	}
	for _, raw := range []string{`[]`, `{"TOKEN":42}`, `{"TOKEN":"` + uuid.Nil.String() + `"}`} {
		_, err := EncodeMapJSON(KindSecret, json.RawMessage(raw))
		require.Error(t, err)
	}
}
