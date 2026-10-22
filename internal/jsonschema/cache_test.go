package jsonschema

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompiledValidatorCacheIdentityEvictionAndConcurrency(t *testing.T) {
	schema := json.RawMessage(`{"type":"integer"}`)
	validator, err := Compile(schema)
	require.NoError(t, err)
	again, err := Compile(append(json.RawMessage(nil), schema...))
	require.NoError(t, err)
	require.Same(t, validator, again)
	copy(schema, `{"type":"boolean"}`)
	changed, err := Compile(schema)
	require.NoError(t, err)
	require.NotSame(t, validator, changed)
	require.NoError(t, changed.Validate(json.RawMessage(`true`)))
	require.Error(t, changed.Validate(json.RawMessage(`1`)))
	require.NoError(t, validator.Validate(json.RawMessage(`1`)))
	require.Error(t, validator.Validate(json.RawMessage(`true`)))
	for i := range 257 {
		_, err := Compile(json.RawMessage(fmt.Sprintf(`{"type":"integer","minimum":%d}`, i)))
		require.NoError(t, err)
	}
	again, err = Compile(json.RawMessage(`{"type":"integer"}`))
	require.NoError(t, err)
	require.NotSame(t, validator, again)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 20 {
				if err := Validate(json.RawMessage(`{"type":"integer"}`), json.RawMessage(`1`)); err != nil {
					t.Error(err)
				}
				if err := Validate(json.RawMessage(`{"type":"boolean"}`), json.RawMessage(`1`)); err == nil {
					t.Error("different schema reused")
				}
			}
		})
	}
	workers.Wait()
}
