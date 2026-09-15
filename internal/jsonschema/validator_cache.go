package jsonschema

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/hashicorp/golang-lru/v2"
)

const (
	maxCachedValidators  = 128
	maxCachedSchemaBytes = 256 * 1024
)

var compiledValidators, compiledValidatorsErr = newValidatorCache(maxCachedValidators, maxCachedSchemaBytes)

// Only successful, immutable validators are retained, keyed by exact source
// bytes. Hashing does not normalize away duplicate keys or numeric lexemes.
// This process-local cache contains no authorization/context state and exposes
// no schema lookup API. Large documents still compile, but are not retained.
type validatorCache struct {
	entries        *lru.Cache[[sha256.Size]byte, *Validator]
	maxSchemaBytes int
	// Serialize bounded cache misses, with a second lookup after acquiring the
	// lock. Concurrent requests for one schema compile it only once; cache hits
	// do not wait for unrelated compilation.
	compileMu sync.Mutex
}

func newValidatorCache(entries, maxSchemaBytes int) (*validatorCache, error) {
	cache, err := lru.New[[sha256.Size]byte, *Validator](entries)
	if err != nil {
		return nil, fmt.Errorf("create JSON schema validator cache: %w", err)
	}
	return &validatorCache{entries: cache, maxSchemaBytes: maxSchemaBytes}, nil
}

func (c *validatorCache) compile(raw json.RawMessage) (*Validator, error) {
	if len(raw) > c.maxSchemaBytes {
		return compileValidator(raw)
	}
	digest := sha256.Sum256(raw)
	if validator, found := c.entries.Get(digest); found {
		return validator, nil
	}
	c.compileMu.Lock()
	defer c.compileMu.Unlock()
	if validator, found := c.entries.Get(digest); found {
		return validator, nil
	}
	validator, err := compileValidator(raw)
	if err != nil {
		return nil, err
	}
	c.entries.Add(digest, validator)
	return validator, nil
}
