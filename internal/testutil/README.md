# Go test helpers

Use ordinary `testing` tests and local constructors. Reuse operations with the
same semantics; keep scenario-specific setup visible.

## Storage fixtures

`integrationdb.OpenMigratedPool` owns a migrated database per test, closes its
pool before dropping the database, and registers cleanup on the supplied test.
Open it inside each parallel test. Shared seed helpers receive that pool and
explicit identities; they do not open databases or own cleanup.

`storagefixture` can be imported by same-package storage tests because it uses
leaf stores and never imports the storage facade. Keep generated SQL types
inside the storage boundary. `storagetest` imports the facade and is intended
for callers outside same-package storage tests. External `executionstore_test`
integration tests already use `storagefixture`; same-package `executionstore`
unit tests cannot import it because it imports that leaf store.

The project seed creates storage rows with placeholder encrypted material. Use
real secret creation when credential decryption is the behavior under test.
`SeedAgentConfig` provisions the model/grant and persists the compiled config in
the supplied project. Keep compilation and persistence separate when testing
missing grants, validation, or other configuration contracts. Model compilation
helpers name their provisioning side effect. Keep claims,
execution, release, cancellation, configuration activation and clock advancement
visible when the test exercises those transitions. For new lock helpers,
distinguish strict release assertions from cleanup that tolerates an inactive
lock.

## Assertions and diagnostics

Use `require.NoError(t, err, "operation")` for prerequisites where continuing
would be invalid. New shared provisioning helpers use this form consistently;
existing contextual checks can remain when a conversion adds little clarity.
Keep independent result checks nonfatal when useful.
Fatal assertions, including `require`, belong in the goroutine running the test.
Send errors from worker goroutines to that goroutine before asserting them.

Use `cmp.Diff(want, got)` with a `(-want +got)` label for structural failures.
Inspect the compared type: cmp honors Equal methods, distinguishes nil from
empty containers, and panics on unexported fields without explicit handling.
Don't hide meaningful fields with broad ignore options. Preserve raw wire
assertions and the existing precise semantic JSON comparison where appropriate.
Use `errors.Is`/`errors.As` for error identity/classification; don't replace them
with message matching or generic non-nil checks.

Keep ordinary parallel tests and `testing/synctest` for deterministic concurrent
behavior. Testify's suite package does not support parallel tests. No suite,
mocking layer or assertion facade is needed for these helpers.

## Coverage boundaries

- Unit: arithmetic, validation, error identity and preparation order.
- Adapter: actual wire fields, omissions, serialization and parsing.
- Storage: constraints, immutable history and concurrent operations.
- Worker: durable scheduling, cancellation, retries, compaction and recovery.
- Service: real composition across API, adapter, worker, persistence and tools.

Similar assertions at these boundaries can protect different contracts. Remove
unused helpers and repeated provisioning, not distinct behavioral coverage.

## Tagged lint

`make golangci-lint` retains the untagged all-module gate. Use
`make golangci-lint-tagged` to lint the root module with `integration,servicee2e`.
The full tagged command currently reports an existing backlog, especially long
lines and unchecked type assertions. It is not a clean baseline.

PR and push CI additionally run `make golangci-lint-tagged-diff LINT_BASE_SHA=...`
against the fetched event base. This rejects reported issues on added/changed
lines; it does not certify untouched lines or detect every effect that a change
has on other files. The event base is a branch tip, not necessarily a merge
base; choose the base carefully for local runs. Manual workflow dispatch has
no event diff and retains the
regular gate. To finish the rollout, remediate the tagged backlog in focused
changes, then promote full tagged lint to the required gate. Live/blackbox tags
remain compile-checked separately and are outside this tagged-lint target.

## Further reading

- [Go test comments](https://go.dev/wiki/TestComments) and
  [testing lifecycle](https://pkg.go.dev/testing).
- [Deterministic time and synchronization](https://go.dev/blog/testing-time).
- [Testify require](https://pkg.go.dev/github.com/stretchr/testify/require).
- [Google go-cmp semantics](https://pkg.go.dev/github.com/google/go-cmp/cmp).
