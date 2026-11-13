# Configuration representation

Source text and its declared format are the editing/history authority. The dashboard keeps the complete parsed document and applies edits only to paths its controls own. Its shared SDK parser and serializer also protect CLI edits. An unchanged edit returns the original text; an actual edit may normalize formatting. Source is never reconstructed from the Builder projection.

The backend parses and validates source, resolves resource names, and produces the immutable compiled runtime definition. Generated source is an export/fallback for definitions without authored source. It is not an intermediate step in normal editing.

## Values and validation

YAML and JSON use the same exact decimal number policy. Number spelling and object order do not affect definition hashes. Numbers are limited to 1000 significant decimal digits, 1000 fractional places, a written decimal exponent between -1000 and 1000, and 2000 characters per numeric literal. A nonzero number must have a finite, nonzero float64 approximation; the approximation is only a range/legacy-format check and never replaces the exact value. Overflow, underflow, and duplicate mapping keys are rejected before source normalization. Integer-valued forms such as `1.0` are accepted in integer fields, subject to the authoritative schema and Go field bounds.

YAML base-prefixed integers, octal integers, underscores, aliases, merge precedence, timestamps, and explicit strings retain their YAML meanings. Expansion is bounded at 100000 values. Numeric-looking plain strings outside the reader's numeric range must be quoted explicitly. Arbitrary input schemas remain raw JSON through compilation, storage, export, and runtime requests. Their schema validity is checked by the existing runtime validator during compilation.

`jsoncanonical.Normalize` continues to preserve numeric spelling. `Canonical` is the separate value-based representation for definition hashing. It normalizes negative zero to zero so PostgreSQL jsonb cannot invalidate a newly written hash. Runtime loading first verifies that hash, then permits the previous float-based hash only for existing definitions, preserving their previous runtime interpretation.

## Deployment compatibility

Existing definitions are not rewritten. Resaving old source with previously rounded numbers intentionally creates a new, more precise definition.

Before deployment, audit stored `agent_config_definitions` in a read-only transaction: verify every `compiled_definition` against its `effective_definition_hash` using `RuntimeContractFromCompiled`. Report identifiers and failure counts without logging source or definitions. A mismatch can indicate historical negative-zero loss or corruption; investigate it before permitting execution. Do not repair integrity failures by blindly rehashing stored content. If a repair is justified, create a replacement definition and repoint versions using the existing `omnara.agent_config_definition_rewrite` mechanism.

Old binaries cannot read new definitions containing numbers they used to round. Pause config writes during the coordinated reader/writer upgrade, and replace all API and worker readers before restoring writes. A rolling deployment that leaves old readers active while new writers accept exact definitions is unsupported. Once such definitions exist, rollback requires retaining the compatible reader.

The conformance tests cover equivalent source encodings, exact values, source export, legacy verification, schema errors, and a real jsonb-to-runtime round trip.
