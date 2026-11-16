# Configuration representation

Source text and its declared format are the editing/history authority. The dashboard keeps the complete parsed document and applies edits only to paths its controls own. Its shared SDK parser and serializer also protect CLI edits. An unchanged edit returns the original text; an actual edit may normalize formatting. Source is never reconstructed from the Builder projection.

The backend parses and validates source, resolves resource names, and produces the immutable compiled runtime definition. Generated source is an export/fallback for definitions without authored source. It is not an intermediate step in normal editing.

## Values and validation

YAML and JSON use the same exact decimal number policy. Number spelling and object order do not affect definition hashes. Numbers are limited to 1000 significant decimal digits, 1000 fractional places, a written decimal exponent between -1000 and 1000, and 2000 characters per numeric literal. A nonzero number must have a finite, nonzero float64 approximation; the approximation is only a range and compact-format check and never replaces the exact value. Overflow, underflow, and duplicate mapping keys are rejected before source normalization. Integer-valued forms such as `1.0` are accepted in integer fields, subject to the authoritative schema and Go field bounds.

YAML base-prefixed integers, octal integers, underscores, aliases, merge precedence, timestamps, and explicit strings retain their YAML meanings. The existing YAML alias-expansion limit is retained, and nesting is limited to 10000 levels. Numeric-looking plain strings outside the reader's numeric range must be quoted explicitly. Arbitrary input schemas remain raw JSON through compilation, storage, export, and runtime requests. Their schema validity is checked by the existing runtime validator during compilation.

`jsoncanonical.Normalize` continues to preserve numeric spelling. `Canonical` is the separate value-based representation for definition hashing. It normalizes negative zero to zero so PostgreSQL jsonb cannot invalidate a newly written hash. Compilation and runtime verification use the same canonicalization algorithm. Runtime loading rejects definitions whose canonical hash does not match the stored hash.

## Coordinated deployment

Deploy the API, workers, and maintenance services as one coordinated cutover. Pause configuration writes and background configuration derivation, drain or stop the previous processes, and replace every reader and writer before resuming activity. Older binaries cannot safely read new definitions containing numbers they would round; do not mix old readers with new writers.

Before resuming activity, verify stored definitions against their hashes using the new canonicalization algorithm and check runtime loading separately. Investigate mismatches rather than rewriting their hashes to make verification pass. Existing definitions are not automatically rewritten. Resaving source with previously rounded numbers can create a new, more precise definition.

Once the new compiler has written definitions, any rollback must retain the exact-number reader. A deployment of pre-change binaries is no longer a safe rollback.

The conformance tests cover equivalent source encodings, exact values, source export, hash integrity, schema errors, and a real jsonb-to-runtime round trip.
