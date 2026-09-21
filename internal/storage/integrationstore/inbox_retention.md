# Integration inbox retention

The integration inbox stores transient verified provider callbacks, frozen
recipient plans, and admission progress. It is separate from accepted agent
inputs, events, artifacts, conversation targets, and app selections.

`cmd/maintenance.runCoreMaintenanceTick` performs cleanup last, after runtime,
tool, authentication, and profile-choice maintenance, including the first tick
at startup. Each cleanup operation has a 250 ms soft budget checked between
batches and its own five-second hard child deadline:

- Completed and explicitly discarded receipts become eligible seven days after
  `completed_at`. `CleanupTerminalIntegrationInbox` deletes at most 100 per
  transaction, oldest completion first. Full batches continue until the backlog
  drains or the soft budget elapses. The store accepts a retention duration of at
  least one second; SQL computes the cutoff from `statement_timestamp()`.
  Host clock skew cannot shorten durable retention.
- Receipts whose app, project, or organization is soft-deleted are removed
  in separate transactions of at most 100, within their own deadline. This
  includes failed receipts under deleted scopes. Disconnected apps are not
  deleted scopes.

Both queries use `FOR UPDATE SKIP LOCKED`; busy rows are deferred to a later tick.
Each statement commits independently. Normal soft-budget exhaustion stops new
batches and logs `budget_exhausted=true`; the current batch finishes without
cancellation, avoiding connection churn on a healthy backlog. A stalled pass hits
the five-second hard deadline and is logged as an error. Each statement is atomic,
but cancellation can race commit: a failed call may have an uncertain result.
Counts include confirmed deletions only. Earlier commits remain and the next
maintenance tick safely continues from remaining rows. Parent cancellation stops work immediately. Other cleanup failures
are also logged in the existing maintenance outcome. A backlog or held lock can
extend retention. Each pass may overrun its soft budget by one batch; two stalled
cleanup passes can delay the next core tick by about ten seconds, not 500 ms.

Terminal cleanup never removes pending, processing, or failed receipts. Failed
receipts on active or disconnected apps retain their exact payload, frozen plan,
preparation, and committed-slot progress for explicit operator recovery. Neither
cleanup operation deletes agent history or causes provider mutations.

Receipt-level deduplication lasts until the receipt is deleted, normally at least
seven days after completion or explicit discard. A sufficiently late provider replay can then be
accepted as a new receipt and evaluated against current routing. Existing agent
input semantic keys still provide their own deduplication while that history
exists; this is not a permanent, account-wide exactly-once delivery guarantee.

The policy is exercised by
`cmd/maintenance/inbox_retention_integration_test.go` through the actual core tick,
including rollback of a timed-out partial batch and progress on the next tick,
and by `inbox_retention_integration_test.go` for completion age, busy rows,
preserved failed plans, and the transport replay window. Query access-path tests
bind `retention_milliseconds` and verify that empty cleanup passes do not scan
retained inbox history.

## Worker operation

`OMNARA_WORKER_INBOX_CAPACITY` bounds concurrent receipt processing per worker:
1–100, default 4. It shares the worker's ordinary database pool (maximum 10
connections by default) with other work; concurrency does not resize that pool.

Recovery gives expired leases and inactive pending receipts separate 100-row
allowances in independently committed statements. A one-second soft budget is
checked between recovery calls; the current call (two SQL statements) finishes
without cancellation. A five-second hard deadline bounds the entire pass, with
hard timeouts returned as errors. Normal soft-budget exhaustion schedules another
pass after one second. Empty or short passes return to the 30-second cadence. This
keeps inactive frontiers moving without spending expired leases' allowance.

Every 30 seconds, the recovery loop also samples oldest-ready lag with a separate
one-second deadline, even when consumers are all occupied. A single ordered
`LIMIT 1` probe uses `integration_inbox_ready_idx`; no counts, scope joins,
payload reads, or scrape-time SQL are needed. Valid transitions never leave a
pending receipt at attempt 8: retry/expiry fails it and operator retry resets its
attempt count to zero. The lag probe therefore needs no residual attempt filter.
Lag is database time minus
`available_at`, excluding future retries and in-flight leases. Inactive pending
receipts remain visible until recovery drains them, since they can block the
bounded ready-discovery frontier.

`omnara_app_inbox_oldest_ready_lag_seconds` has no app/project labels: zero means
a successful empty sample; `NaN` means no sample or a failed sample.
`omnara_app_inbox_lag_sample_last_success_timestamp_seconds` remains unchanged
on failure and lets operators detect a stalled sampler. Samples describe the
global queue, so use the maximum across worker replicas, not their sum.

## Operator recovery

The maintenance binary accepts operator commands using `OMNARA_DATABASE_URL`
from the normal operator environment. They do not start maintenance loops,
connect to Redis, or call provider channels. Discard can use the configured blob
store after its database commit. Commands have a one-minute deadline and emit
JSON on stdout, with errors/help and cleanup warnings on stderr.

```
maintenance inbox list --project proj_... --state failed --limit 50
maintenance inbox list --project proj_... --app app_... --after <next-cursor>
maintenance inbox show --project proj_... --receipt <receipt-UUID>
maintenance inbox retry --project proj_... --receipt <receipt-UUID>
maintenance inbox discard --project proj_... --receipt <receipt-UUID> --reason "operator explanation"
```

`list` defaults to failed receipts, accepts every inbox state or `all`, and returns
at most 100 metadata records plus an optional `next` cursor. `show` adds frozen
agent/base-config/artifact identities, app selection addresses and prepared/committed
stage flags. Neither command prints the verified payload, messages, compiled
configuration, provider credentials, or raw preparation/progress. Failure diagnostics
are those already redacted by the producing workflow before persistence.

Retry transitions only `failed` to `pending` and resets the bounded attempt budget.
It preserves the receipt, frozen plan, planned UUIDs, preparation, committed-slot
results and last failure. It never recompiles source or reselects app membership.
The receipt app must be active; admission independently checks the current
profile, model/config, agent and referenced app ownership when work resumes.
Only the receipt app and apps authorizing concrete actions must be active.

Discard transitions only `failed` to `discarded`. It rejects any committed launch slot
and any retained selection target for a reserved app/conversation, including a
retired target. Partial launch admission therefore remains retry-only. Committed
existing-agent input slots do not block discard: their original progress and
semantic input deduplication remain intact while failed launch reservations are
released. Discard works on disconnected live apps without reactivating them.
The required operator reason
is emitted with the successful action's project/receipt IDs for operator audit;
it does not replace the preserved last failure or create a new audit ledger.

Both operations lock project/app, receipt, then sorted frozen conversations.
They serialize with selection and empty-plan decisions. Before explicit retry,
failed reservations do not delay plain follow-ups with no recipient; after retry,
those follow-ups wait for pending/processing initial admission. Failed reservations
always block replacement launches. Discard releases that reservation for a new
event, but keeps the original receipt terminal and deduplicated for seven days.
The recovery transaction has no external effects. Concurrent retry/discard has
one winner; the other receives a state conflict. Inspect the receipt if a command's
response is lost. Retry never rewrites a now-pending or terminal receipt.

After successful discard, the command calls
`integration.CleanupDiscardedAppInboxArtifacts` with a 30-second cleanup deadline
within the command's deadline. It initializes the existing S3 backend only when
`OMNARA_BLOB_S3_BUCKET` is explicitly configured, using the normal
`OMNARA_BLOB_S3_*` settings. Missing configuration is harmless for plans without
prepared keys; otherwise cleanup reports a warning. Only uncommitted frozen keys
without durable artifact references are eligible. Committed input artifacts are
preserved. See `internal/integration/app_discard_cleanup.md` for safety checks
and the limits of this best-effort cleanup.

Cleanup failure leaves the successful discard JSON and zero exit status intact,
and prints a warning with the receipt ID and retry instructions. Repeat the same
`discard` command while the receipt remains retained to retry cleanup. An already
discarded receipt returns `already_discarded: true` and runs only cleanup; it does
not rewrite the plan, progress, selection state or terminal timestamp. If output
fails after the database commit, inspect the receipt and repeat discard to finish
cleanup. No broad blob scan or automatic garbage collection is performed.

The recovery invariants and lock order are exercised by
`inbox_recovery_integration_test.go`, with actual CLI parsing/semantic-store dispatch
and metadata projection in `cmd/maintenance/inbox_integration_test.go`.
`cmd/maintenance/inbox_cleanup_integration_test.go` exercises the actual command
with a local S3 endpoint, proving commit precedes blob I/O and failed cleanup can
be retried without changing the receipt.

## Local capacity experiment

`TestAppInboxLocalLoad` is an opt-in, isolated-database experiment. Run with
`OMNARA_INBOX_LOAD=1` and the normal local test database URL; it skips ordinary
CI runs. It measures durable admission, claim/completion, cleanup, pool waits,
and completed-receipt latency against 100,000 historical receipts and concurrent
metadata reads, with a separate long-lived-snapshot scenario.

A September 2026 local PostgreSQL 18 run completed about 289 receipts/s with four
consumers and ten connections, and 639/s with sixteen consumers and twenty
connections. No scenario reached 1,000 completions/s; admission exceeded
completion and left a backlog. The consumer used
an empty recipient plan, so these are optimistic local receipt-processing results,
not full agent-delivery capacity or a production sizing guarantee. Sustained
1,000 receipts/s at seven-day retention means approximately 605 million rows;
the experiment does not test a database of that size. Measure representative
routing, payloads, retained history, and competing workloads before sizing a
production deployment for that rate.
