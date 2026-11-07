# Custom tools over signed webhooks

A small TypeScript receiver runs a `text_length` custom tool with the SDK's
`createCustomToolWorker` and posts the result back to Omnara. Webhooks deliver
ready calls right away, and the worker also checks for missed ones, pausing
up to a minute between recovery passes. It verifies Standard Webhooks signatures
before parsing the payload and subscribes only to `tool_call_update`.

## Run

Requires Node 22+ and pnpm. From the repository root:

```sh
pnpm --dir frontend install --frozen-lockfile
cd examples/custom-tool-webhook
pnpm install
cp .env.example .env
```

This example uses the SDK from this checkout.

1. Generate a signing key with `openssl rand -base64 32`. Save it as a
   project-accessible **generic secret** in Omnara and keep the same value for
   the receiver. Keep this secret out of agent machines and tool inputs; anyone
   holding it can sign requests to your tool handlers.
2. Fill in `.env` with your personal access token, organization and project
   IDs, and the signing key from step 1.
3. Run `pnpm start`. Expose `http://127.0.0.1:3000` through an HTTPS tunnel.
   The config uses `omnara-openrouter` / `openai/gpt-6.1-sol`; choose another
   model if that one isn't available to your project.
4. In the Omnara dashboard, create a profile using `agent.yaml`. In its
   **Webhooks** tab, add the public URL ending in
   `/webhook`, select **Tool-call updates**, and choose the signing secret.
5. Launch the profile and send: **Count the characters in "hello 🌍".**
   The tool returns 7 Unicode code points.

`OMNARA_AGENT_PROFILE_ID` optionally limits the worker to agents launched from
that profile. `OMNARA_API_URL` optionally overrides `https://api.omnara.com/v1`;
`PORT` optionally overrides 3000.

## Delivery handling

The receiver awaits the SDK's `verifyEventWebhook` with the original request
body, headers, and signing secret. It verifies the signature and timestamp
before validating and returning the typed event. `worker.handle(payload.data)`
then checks the organization, project, profile, and tool name. Before invoking
the handler, it fetches the call's current state and input, even when the event
includes input. A delayed ready notification therefore cannot start a call that
the read already reports completed. Recovery reserves execution capacity before
reading a page and needs no per-call fetch.

The receiver acknowledges immediately after accepting the work into this
process. This example's tool is a repeatable calculation, and `run()` recovers
calls lost if the process exits. For durable acceptance, save the verified event
to your existing job queue before acknowledging it, then await `handle()` in
the queue consumer. On serverless platforms, don't assume work will continue
after sending the response; use the platform's supported job/lifecycle mechanism.

The helper submits results and retries network errors and `408`, `429`, `500`,
`502`, `503`, and `504` responses. With the default client policy, each API operation makes at most three attempts within 30
seconds (`requestTimeoutMs` can extend this for large uploads). It respects
`Retry-After`; if the wait would exceed that budget, it returns the failure
instead of retrying early. The continuous `run()` loop
reports listing failures through `onError` and tries again on a later pass,
including after authentication/configuration errors. It continues until shutdown,
so a failed recovery pass does not disable webhook handling.

Unexpected handler errors and invalid results become a generic failed tool
result. Their original details go only to `onError`. To intentionally explain a
failure to the model, return an explicit `{ outcome: 'failed', content_blocks: ... }`.
A `409` with code `tool_call_already_completed` counts as done. It means the call
has finished, including cancellation or another worker's result; it does not
prove that this worker's result won.

Once a function returns, the helper keeps its result in memory after access
errors (including `404`) and unknown submission failures. A later operation
retries the same result without running the function again. A typed not-ready
conflict releases the result without remembering the call as completed. An
invalid result is replaced with a generic failure once; if that fallback is also
rejected, the helper remembers the rejection locally to avoid rerunning it.
These results count against the concurrency limit, so an outage cannot grow an
unbounded result queue. Retries of retained results also respect `Retry-After`.
An unrecognized permanent failure can fill that capacity until it is resolved;
watch `onError` and the optional `onObserve` capacity observations.
The memory is lost on restart; use application-owned durable storage when that
would be unacceptable.

`onError` receives an error and the affected call, or `undefined` when a recovery
listing fails. It observes failures rather than controlling execution. Exceptions
and rejected promises from the callback are ignored.

## Use inside an existing application

The same helper works in a backend, job queue, or scheduled task. `run()` is
optional; it supplies a continuous recovery loop. A queue consumer can do:

```typescript
const outcome = await worker.handle(update, {
  signal: AbortSignal.timeout(25_000),
})
if (outcome.status === 'failed') throw outcome.error
if (outcome.status === 'busy' || outcome.status === 'stopped') {
  throw new Error('Retry this job when the helper can accept it')
}
```

`ignored` means the event does not match the scope/tools or is not actionable.
`deferred` means the function intentionally left the call waiting for a later
result. Handler exceptions that were successfully submitted as failed tool
results count as `completed`; `failed` means the helper could not finish
processing or submitting the call.

An operation's `signal` limits its requests, waits, and newly started handlers
without shutting down unrelated work. If the same call is already running,
the duplicate caller only stops waiting for it. JavaScript cannot forcibly stop
a function: an aborted handler still occupies its slot until it settles, and a
late result is retained for a future submission rather than posted after the
operation's deadline. Functions should cooperate with their `signal`. An aborted
`handle()` returns `stopped`; expected caller cancellation does not call `onError`.

A scheduled job can save its cursor between invocations so a large backlog
does not make it repeatedly process only the oldest calls:

```typescript
const signal = AbortSignal.timeout(25_000)
let cursor = (await checkpoints.load(recoveryKey)) ?? undefined
do {
  const page = await worker.check({ cursor, limit: 10, signal })
  for (const { call, outcome } of page.results) {
    if (outcome.status === 'failed') console.error(call.toolCallID, outcome.error)
  }
  await checkpoints.save(recoveryKey, page.nextCursor, { signal })
  if (page.blocked) break // Retained results need delivery before more work can start.
  cursor = page.nextCursor ?? undefined
} while (cursor !== undefined)
```

Each check reserves capacity before reading and processes up to `limit` rows
across smaller API pages. It waits for its work and returns the outcomes.
When retained results occupy all slots, `blocked` is true and the cursor stays
before any unread calls; stop the job and retry later. A blocked null cursor
means resume from the beginning, not that the pass finished.
Here `checkpoints` is your existing
job storage, keyed by this worker's project, profile, and tools. Save the cursor
after each completed page; reaching `null` resets the next invocation to the
beginning so deferred and newly approved calls are revisited. An interrupted
page keeps its previous checkpoint and can safely be repeated. Choose a page
size that fits your job's budget, and defer long-running work instead of waiting
for it inside a handler. The continuous `run()` loop manages its cursor in memory.

Each check also retries results retained by this helper. Listing failures and
an aborted check reject the operation; already-started handlers receive
cancellation when its signal is aborted.

## Tools that answer later

Return `{ outcome: 'deferred' }` when a durable application job is still running.
The helper submits nothing and releases the slot. Subsequent recovery checks
invoke the handler again; it reads the saved result when the job is finished.
For example, with your own database and job queue:

```typescript
const worker = createCustomToolWorker({
  client,
  path: { orgID, projectID },
  tools: {
    go_to: async ({ toolCallID, input, signal }) => {
      const job = await jobs.startOrGet(toolCallID, input, { signal })
      if (job.result === null) return { outcome: 'deferred' }
      return job.result
    },
  },
  onError: (error, call) => console.error(call?.toolCallID, error),
})
```

Here `jobs` is your application's durable storage. `startOrGet` must atomically
use the tool-call ID to avoid launching the same job twice. Alternatively, your
job can submit its result directly to Omnara when it completes. The helper does
not require another service or a particular queue/database. Enable `run()` or
schedule `check()` to revisit deferred calls; webhook handling alone does not
poll them. Recovery also revisits notifications that returned `busy` while all
slots were occupied or reserved; a durable queue can instead retry those jobs.
The application owns cancellation of a job once its handler has
returned `deferred`.

## Duplicates, cancellation, and shutdown

Delivery and recovery are at least once. The helper shares active attempts for
the same call and remembers the most recent 1,000 completed or permanently
rejected call IDs. That suppresses recent duplicates within this process, but does not survive restart
or coordinate instances. Use the tool-call ID as an idempotency key for external
side effects, including starting deferred jobs.

Events can arrive out of order. A `completed` event signals an active handler to
stop through its `AbortSignal` on the receiving instance. The readiness read
skips calls it observes as completed, but cancellation can happen after that
read, and two workers can both read `ready`. Cancellation cannot undo an
external action already performed.

Call `await worker.shutdown()` when the application stops accepting work, or
abort the `signal` passed at construction. Shutdown stops intake, signals active
handlers, and gives finished results a bounded opportunity to be submitted
(default ten seconds, configurable with `shutdownGraceMs`). Results remaining
at shutdown are not durably saved by the helper.

## Check

```sh
pnpm test
pnpm run typecheck
```
