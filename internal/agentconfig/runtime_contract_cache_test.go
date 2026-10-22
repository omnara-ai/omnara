package agentconfig

import (
	"bytes"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeContractCacheIdentityEvictionAndIsolation(t *testing.T) {
	compiled, err := Compile(SourceFormatYAML, []byte(`instruction: original
model:
  provider_config: benchmark
  name: test-model
tools:
  run_command: {}
`), CompileOptions{})
	require.NoError(t, err)
	first, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, compiled.Hash)
	require.NoError(t, err)
	want := cloneRuntimeContract(first)
	first.Tools[0].InputSchema[0] = '!'
	first.Tools[0].Permission.Parameters[0] = '!'
	cached, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, compiled.Hash)
	require.NoError(t, err)
	require.Equal(t, want, cached)
	cached.Tools[0].InputSchema[0] = '?'
	cached, err = RuntimeContractFromCompiled(compiled.CanonicalJSON, compiled.Hash)
	require.NoError(t, err)
	require.Equal(t, want, cached)
	_, err = RuntimeContractFromCompiled(compiled.CanonicalJSON, "wrong-hash")
	require.ErrorContains(t, err, "hash mismatch")
	rewritten := bytes.Clone(compiled.CanonicalJSON)
	_, err = RuntimeContractFromCompiled(rewritten, compiled.Hash)
	require.NoError(t, err)
	copy(rewritten, bytes.ReplaceAll(rewritten, []byte("original"), []byte("modified")))
	_, err = RuntimeContractFromCompiled(rewritten, compiled.Hash)
	require.ErrorContains(t, err, "hash mismatch")
	for i := range 129 {
		encoded, err := EncodeCompiled(Compiled{Instruction: fmt.Sprintf("cache eviction %d", i)})
		require.NoError(t, err)
		_, err = RuntimeContractFromCompiled(encoded.CanonicalJSON, encoded.Hash)
		require.NoError(t, err)
	}
	cached, err = RuntimeContractFromCompiled(compiled.CanonicalJSON, compiled.Hash)
	require.NoError(t, err)
	require.Equal(t, want, cached)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 20 {
				contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, compiled.Hash)
				if err != nil {
					t.Error(err)
					return
				}
				contract.Tools[0].InputSchema[0] = '!'
			}
		})
	}
	workers.Wait()
}

func TestCloneRuntimeContractDetachesEveryReference(t *testing.T) {
	var original RuntimeContract
	fillContractValue(reflect.ValueOf(&original).Elem())
	cloned := cloneRuntimeContract(original)
	require.Equal(t, original, cloned)
	assertDetachedContractValue(t, reflect.ValueOf(original), reflect.ValueOf(cloned))
}

func fillContractValue(value reflect.Value) {
	switch value.Kind() {
	case reflect.Struct:
		for i := range value.NumField() {
			fillContractValue(value.Field(i))
		}
	case reflect.Pointer:
		value.Set(reflect.New(value.Type().Elem()))
		fillContractValue(value.Elem())
	case reflect.Slice:
		value.Set(reflect.MakeSlice(value.Type(), 1, 1))
		fillContractValue(value.Index(0))
	case reflect.Array:
		for i := range value.Len() {
			fillContractValue(value.Index(i))
		}
	case reflect.Map:
		value.Set(reflect.MakeMap(value.Type()))
		key, item := reflect.New(value.Type().Key()).Elem(), reflect.New(value.Type().Elem()).Elem()
		fillContractValue(key)
		fillContractValue(item)
		value.SetMapIndex(key, item)
	case reflect.String:
		value.SetString("value")
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Int, reflect.Int64:
		value.SetInt(1)
	case reflect.Uint8:
		value.SetUint(1)
	default:
		panic(value.Type().String())
	}
}

func assertDetachedContractValue(t *testing.T, first, second reflect.Value) {
	t.Helper()
	switch first.Kind() {
	case reflect.Struct:
		for i := range first.NumField() {
			assertDetachedContractValue(t, first.Field(i), second.Field(i))
		}
	case reflect.Pointer:
		require.NotEqual(t, first.Pointer(), second.Pointer(), first.Type().String())
		assertDetachedContractValue(t, first.Elem(), second.Elem())
	case reflect.Slice:
		require.NotEqual(t, first.Pointer(), second.Pointer(), first.Type().String())
		for i := range first.Len() {
			assertDetachedContractValue(t, first.Index(i), second.Index(i))
		}
	case reflect.Array:
		for i := range first.Len() {
			assertDetachedContractValue(t, first.Index(i), second.Index(i))
		}
	case reflect.Map:
		require.NotEqual(t, first.Pointer(), second.Pointer(), first.Type().String())
		for _, key := range first.MapKeys() {
			assertDetachedContractValue(t, first.MapIndex(key), second.MapIndex(key))
		}
	default:
		require.Equal(t, first.Interface(), second.Interface())
	}
}
