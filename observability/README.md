# observability

`github.com/omnara-ai/omnara/observability` is the wide-event logging and
Prometheus infrastructure shared by Omnara services. It is a separate Go module
so other services can depend on it without importing the control plane.

- `wideevent` builds one structured log record per unit of work (an HTTP
  request, a job run), with attached database and outbound HTTP traces, level
  escalation, and secret scrubbing in the JSON handler.
- `metrics` provides the Prometheus registry and operator listener
  (`/metrics`, `/healthz`, `/readyz`) plus recorders for inbound HTTP,
  outbound HTTP and gRPC, pgx queries, and periodic jobs, and the control plane's
  daemon, memory, notification, and provider-runtime recorders.
  `metrics.WithNamespace` keeps each service's series distinct.
- `httprequest` completes each inbound HTTP request: it emits the request's
  wide event and its metrics observation from one final status, and recovers
  handler panics.

Service-specific event helpers stay in the consuming service. The
root module builds against this directory through a `replace` directive;
external consumers pin a version.

## Event delivery

Workers export `omnara_event_webhook_slots_used`, `omnara_event_webhook_capacity`, and
`omnara_event_webhook_attempts_total{outcome,cause}`. The bounded cause is `none`, `recipient`,
`configuration`, or `local`. Unavailable signing secrets and invalid signing keys are
configuration failures; database and key-management failures remain local.
Persistence failures take precedence over an earlier delivery failure.
Outcomes distinguish completed deliveries, deleted endpoints (`skipped`), scheduled retries, exhausted retry windows (`gave_up`),
retry/complete persistence failures, and cancellation. The
`omnara_event_webhook_delivery_age_seconds` histogram, labeled by
`attempt` (`first` or `retry`), measures enqueue-to-2xx
age, including retry waiting. It excludes skipped endpoints and failed attempts;
a receiver can acknowledge the same delivery again after a lost response or persistence failure.
The existing `event_webhook.delivery_attempt` wide event also includes
`event_webhook.age_seconds` at attempt start and `event_webhook.wait_seconds`
since the scheduled attempt time. IDs remain in logs, never metric labels.

Maintenance samples the writer's `event_webhook_deliveries` table every 15
seconds, with a five-second context deadline and three-second SQL statement
limit. Scrapes read in-memory gauges and never query PostgreSQL. The single
aggregate scans the pending delivery table, not event history. Exact state counts
show retry waiting, stranded claims, and cleanup backlog. Rows expire after ten
minutes and the existing cleanup removes them in bounded batches.
`omnara_event_webhook_queue_deliveries{state}` partitions rows into:

- `in_flight`: a live claim, even when its delivery window has just expired;
- `expired`: an expired delivery without a live claim, waiting for cleanup;
- `due`: an unexpired delivery whose retry time has arrived, without a live claim;
- `delayed`: an unexpired delivery scheduled for later, including given-up rows
  scheduled beyond their delivery window while awaiting expiry.

`oldest_due_lag_seconds` measures overdue scheduling time since `next_attempt_at`;
this drives backlog alerts without treating normal recipient retry delays as
platform backlog. `oldest_due_age_seconds` separately measures time **since
enqueue** for expiry dashboards. Due work includes organization-throttled
deliveries, including their wait for a claim. One organization with slow
recipients can raise lag by saturating its concurrency allowance. Empty successful
samples report zero. Before the first sample and after failed samples, queue and
age gauges report `NaN`, never a misleading zero. The
`omnara_event_webhook_backlog_sample_last_success_timestamp_seconds` gauge and
`omnara_event_webhook_backlog_sample_errors_total` counter expose stale or failing observation. Use
**max**, not sum, across maintenance replicas for queue gauges. Sum rates of
counters across replicas. `expired_total` counts actual cleanup deletions once.
A `gave_up` attempt does not increment it immediately, but its retained row is
counted when cleanup later deletes it. Expiry does not prove a receiver never
accepted the request: recording completion can fail after a 2xx response.
These process counters can lose an observation on a crash after a database
commit; they are operational signals, not an audit ledger.

API processes export `omnara_agent_stream_buffer_dropped_total{frame}` for full
SSE subscriber buffers (`tool_call_update` and `model_output_delta`). A frame sent
to multiple slow subscribers can be counted more than once. Durable agent events
recover through reconciliation; tool-call updates require ready-call recovery,
and token previews are best effort. All processes with a routed publisher expose
`omnara_notifications_publisher_queue_depth` and `publisher_queue_capacity`.
Depth reads local channel occupancy and excludes the coalesced batch currently
publishing (up to 256 intents). Existing
`omnara_notifications_total{result="dropped",reason="queue_full",intent}` counts
publisher overflow. Full buffers increment counters without a log per drop.
These counters cover Omnara's application queues; they do not count losses
inside go-redis's pub/sub channel or during Redis reconnects. Ready-call recovery
remains necessary.

### Alert rules and deployment boundary

[`prometheus/delivery.rules.yml`](prometheus/delivery.rules.yml) supplies warning
rules for stale/missing samples, sustained scheduling lag, local failures,
SSE tool-update loss, and publisher overflow. The thresholds are initial operator
thresholds, not a latency SLO. Validate them with:

```sh
make prometheus-check
```

Run this from the repository root; CI runs the same pinned tool image. All rules
start at warning severity. Expiry and best-effort token drops remain dashboard
metrics because recipient outages and slow subscribers can cause them.
The three-minute freshness threshold tolerates a 60-second scrape interval and
the 15-second sampler. The tests cover empty queues, duplicate maintenance
replicas, failed/missing sampling, minute scrapes, local versus recipient or
configuration failures, and tool buffer loss without token-drop alerts.

This repository does **not** provision Prometheus, Alertmanager, scrape targets,
or receiver routing. These files do not install live alerts. In the deployment's
monitoring configuration, scrape each service's operator `/metrics` endpoint,
add a stable `cluster` target label when one Prometheus watches multiple clusters,
load this file through `rule_files` (or the equivalent managed rule resource),
and route its `severity="warning"` alerts to the owning team. Keep existing
service/target-down alerts: an entirely missing cluster cannot be inferred from
other clusters' metric samples. The global absent-series rule catches a missing
metric family, and timestamp freshness catches stalled sampling in a visible
cluster. Reload and verify rule health in that monitoring deployment separately.
