package jsonschema

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidatorCacheUsesExactContentAndPreservesOlderValidator(t *testing.T) {
	t.Parallel()
	cache, err := newValidatorCache(2, 1024)
	require.NoError(t, err)
	raw := json.RawMessage(`{"type":"integer","const":1}`)
	first, err := cache.compile(raw)
	require.NoError(t, err)
	again, err := cache.compile(append(json.RawMessage(nil), raw...))
	require.NoError(t, err)
	require.Same(t, first, again)
	raw[len(raw)-2] = '2'
	second, err := cache.compile(raw)
	require.NoError(t, err)
	require.NotSame(t, first, second)
	require.NoError(t, first.Validate(json.RawMessage(`1`)))
	require.Error(t, first.Validate(json.RawMessage(`2`)))
	require.NoError(t, second.Validate(json.RawMessage(`2`)))
	require.Error(t, second.Validate(json.RawMessage(`1`)))
	_, err = cache.compile(json.RawMessage(`{"type":"integer","type":"string","const":1}`))
	require.ErrorContains(t, err, "duplicate")
	require.Equal(t, 2, cache.entries.Len(), "invalid schemas are never cached")
}

func TestValidatorCacheEvictsLeastRecentlyUsedAndBypassesLargeDocuments(t *testing.T) {
	t.Parallel()
	cache, err := newValidatorCache(2, 128)
	require.NoError(t, err)
	a := json.RawMessage(`{"const":1}`)
	b := json.RawMessage(`{"const":2}`)
	c := json.RawMessage(`{"const":3}`)
	first, err := cache.compile(a)
	require.NoError(t, err)
	second, err := cache.compile(b)
	require.NoError(t, err)
	_, err = cache.compile(a) // Keep a, evict b next.
	require.NoError(t, err)
	_, err = cache.compile(c)
	require.NoError(t, err)
	require.Equal(t, 2, cache.entries.Len())
	again, err := cache.compile(a)
	require.NoError(t, err)
	require.Same(t, first, again)
	recompiled, err := cache.compile(b)
	require.NoError(t, err)
	require.NotSame(t, second, recompiled)
	require.NoError(t, second.Validate(json.RawMessage(`2`)), "eviction must not invalidate outstanding callers")

	smallCache, err := newValidatorCache(2, 8)
	require.NoError(t, err)
	large, err := smallCache.compile(a)
	require.NoError(t, err, "cache eligibility must not impose new schema rejection")
	largeAgain, err := smallCache.compile(a)
	require.NoError(t, err)
	require.NotSame(t, large, largeAgain)
	require.Zero(t, smallCache.entries.Len())
}

func TestValidatorCacheConcurrentCompileAndValidation(t *testing.T) {
	t.Parallel()
	cache, err := newValidatorCache(2, 1024)
	require.NoError(t, err)
	schema := json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer","minimum":2}},"required":["count"],"additionalProperties":false}`)
	const callers = 16
	var wg sync.WaitGroup
	validators := make([]*Validator, callers)
	errors := make([]error, callers)
	for i := range callers {
		wg.Go(func() {
			validators[i], errors[i] = cache.compile(schema)
			if errors[i] == nil {
				errors[i] = validators[i].Validate(json.RawMessage(`{"count":9007199254740993}`))
			}
		})
	}
	wg.Wait()
	for i := range callers {
		require.NoError(t, errors[i])
		require.Same(t, validators[0], validators[i])
	}
	require.Equal(t, 1, cache.entries.Len())
	require.Error(t, validators[0].Validate(json.RawMessage(`{"count":1}`)))
}
