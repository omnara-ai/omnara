# Custom tools over signed webhooks

A small TypeScript receiver runs a `text_length` custom tool with the SDK's
`createCustomToolWorker` and posts the result back to Omnara. Webhooks deliver
ready calls right away, and the worker also checks for missed ones every
minute. It verifies Standard Webhooks signatures before parsing the payload and
subscribes only to `tool_call_update`.

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
   the receiver.
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

The receiver verifies the original request bytes, signature, and timestamp,
then validates with `zEventWebhookPayload`, passes `tool_call_update` payloads
to `worker.handle`, and acknowledges right away. The worker uses the event's organization, project, and profile to decide whether
it belongs here, without fetching the agent. It fetches each matching call to
check that it is still ready and passes the current arguments to the registered
function. A `completed` notification signals the function to stop through its
`AbortSignal`; functions must cooperate with cancellation.

The worker submits the tool's result, retrying network errors and `408`,
`429`, and `5xx` responses. If the tool throws, it submits a `failed` result
with the error message. A `409` with code `tool_call_already_completed` means
the call already finished, so it counts as done. Other errors go to `onError` and the awaited operation's `failed` outcome.
Once a function returns, the helper keeps its result in memory if submission
fails. A later check retries submitting that result without running the function
again. Retained results count against the concurrency limit, so an API outage
cannot build an unbounded result queue.

`onError` observes failures; it does not control execution or delivery. Exceptions
and rejected promises from that callback are ignored, so a broken logger cannot
prevent another tool's result from being submitted.

`worker.run()` lists ready calls at startup and every minute, so a call still
runs if its webhook was missed or the receiver was down. That can repeat a
calculation, which is safe because this tool has no external side effects. For
a tool that changes external state, use its tool-call ID as an idempotency key
in that system.

## Use inside an existing application

You can use the same helper inside your backend, job queue, or scheduled task.
`run()` is optional; it supplies the continuous recovery loop.

```typescript
// In a queue job, after verifying the original webhook signature:
const outcome = await worker.handle(update)
if (outcome.status === 'failed') throw outcome.error
if (outcome.status === 'busy' || outcome.status === 'stopped') {
  throw new Error('Retry this job when the worker can accept it')
}
```

`completed` means the call has finished, including cancellation or a result from
another worker; it does not prove this worker's result won. `ignored` means the
update does not match the configured scope/tools or is not actionable. Handler
exceptions are submitted as failed tool results; `failed` means the helper could
not finish processing or submitting the call.

Without `run()`, your application owns retries: retry failed `handle()` calls or
schedule `check()`. Retained results keep their slots until delivery succeeds,
the call completes elsewhere, or the helper shuts down.

A single recovery check processes one page, waiting for slots as functions
finish. Page size (default 50, maximum 100) and concurrency (default 10) are independent:

```typescript
const page = await worker.check({ limit: 100 })
for (const { call, outcome } of page.results) {
  if (outcome.status === 'failed') console.error(call.toolCallID, outcome.error)
  if (outcome.status === 'busy' || outcome.status === 'stopped') {
    // Leave this call for a future recovery pass, or retry it through your queue.
  }
}
// Continue this pass with worker.check({ cursor: page.nextCursor }) when non-null.
```

The cursor advances over the page that was checked, including failed or busy
calls. Start a later recovery pass without a cursor to revisit unfinished calls.
The first page also retries results retained by this helper. Listing failures or
shutdown cancellation reject `check()`; calls it already started are settled
before it rejects. Per-call outcomes identify execution/submission failures.

## Shutdown and retries

Call `await worker.shutdown()` when your application stops accepting work, or
abort the `signal` supplied when creating it. The example wires SIGTERM and SIGINT
to shutdown. The helper stops intake, signals running functions to stop, and
retries retained results. Results produced during shutdown can still be submitted
within the same grace period (10 seconds by default, configurable with
`shutdownGraceMs`). Submission failures and unfinished work at the deadline go to
`onError`; shutdown then finishes. JavaScript cannot forcibly stop a function that
ignores its signal, but the helper will not submit a late result after the deadline.

Deduplication and retained results belong to this helper instance and do not
survive a process restart. Multiple instances can execute the same call. Use the
tool-call ID as an idempotency key for external side effects. The receiver
acknowledges promptly and relies on recovery for work lost before completion;
applications that require durable local acceptance can enqueue the verified
update before acknowledging it.

## Check

```sh
pnpm test
pnpm run typecheck
```
