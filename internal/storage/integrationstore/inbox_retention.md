# Integration inbox retention

The integration inbox stores transient verified provider callbacks, frozen
recipient plans, and admission progress. It is separate from accepted agent
inputs, events, artifacts, conversation targets, and app selections.

`cmd/maintenance.runCoreMaintenanceTick` performs cleanup last, after runtime,
tool, authentication, and profile-choice maintenance, including the first tick
at startup. Each cleanup operation has a 250 ms soft budget checked between
batches and its own five-second hard child deadline:

- Completed and failed receipts become eligible seven days after
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

Terminal cleanup never removes pending or processing receipts. Failed receipts
retain their exact payload, plan and committed progress for seven days after
failure. They are not requeued. Neither cleanup operation deletes agent history
or causes provider mutations.

Receipt-level deduplication lasts until the receipt is deleted, normally at least
seven days after completion or failure. A sufficiently late provider replay can then be
accepted as a new receipt and evaluated against current routing. Existing agent
input semantic keys still provide their own deduplication while that history
exists; this is not a permanent, account-wide exactly-once delivery guarantee.

The policy is exercised by
`cmd/maintenance/inbox_retention_integration_test.go` through the actual core tick,
including rollback of a timed-out partial batch and progress on the next tick,
and by `inbox_retention_integration_test.go` for completion age, busy rows,
failed-receipt retention, and the transport replay window. Query access-path tests
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
pending receipt at attempt 8: retry/expiry makes it terminal. The lag probe therefore needs no residual attempt filter.
Lag is database time minus
`available_at`, excluding future retries and in-flight leases. Inactive pending
receipts remain visible until recovery drains them, since they can block the
bounded ready-discovery frontier.

`omnara_app_inbox_oldest_ready_lag_seconds` has no app/project labels: zero means
a successful empty sample; `NaN` means no sample or a failed sample.
`omnara_app_inbox_lag_sample_last_success_timestamp_seconds` remains unchanged
on failure and lets operators detect a stalled sampler. Samples describe the
global queue, so use the maximum across worker replicas, not their sum.

## Terminal failure

Every claim consumes one of eight attempts, including worker crashes and waits
for an unfinished launch. Retries start after five seconds and double up to five
minutes. The existing unrecoverable-choice and uncertain-scheduled-send errors
terminate early. Other failures exhaust the same finite budget. Ready receipts
are ordered by availability, not strict conversation FIFO: a newer message may
be admitted while an older one waits to retry.

Failure releases unfinished conversation launch reservations. A fresh mention
can try again, but already committed agents, inputs, subscriptions and selection
targets remain authoritative. A duplicate receipt or repeated profile-choice
button cannot restart failed work. Retained choices protect their original
source until the linked receipt expires; they no longer reserve the conversation.

After a confirmed worker failure commit, the consumer attempts a safe provider
notice and cleans unused prepared artifacts. Empty plans and fully committed
plans produce no failure notice. Human chat/comment requests get best-effort
feedback when their destination and credentials remain usable; scheduled failures
can notify an already saved opening thread. Automatic GitHub PR events do not
produce failure comments. No provider notice affects admission or retries.

A crash between the terminal commit and this pass can lose the notice or leave
unused uploads. Failures finalized by lease/inactive-app recovery also skip that
pass. Logs and the retained receipt remain available for diagnosis; conversations
are released in every failure path. There is no operator replay or discard CLI.
See [prepared-file cleanup](../../integration/app_failed_cleanup.md).

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
