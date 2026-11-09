# Custom tools over signed webhooks

A small TypeScript receiver runs a `text_length` custom tool with the SDK's
`createCustomToolWorker` and posts the result back to Omnara. Webhooks deliver
ready calls right away, and the application scans for missed notifications at startup and once a minute,
following pages promptly until each pass finishes. It verifies Standard Webhooks signatures
before parsing the payload and subscribes only to `tool_call_update`.

## Run

Requires Node 22+ and pnpm. From the repository root:

```sh
pnpm --dir frontend install --frozen-lockfile
cd examples/custom-tool-webhook
pnpm install --frozen-lockfile
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
4. In the Omnara dashboard, create a profile using `agent.yaml`. Open the project's
   **Webhooks** tab and add the public URL ending in `/webhook`. Under
   **Receive events from**, choose **Agents using one profile**, then select
   that profile in **Agent profile**. Select **Tool-call updates** and choose
   the signing secret.
5. Launch the profile and send: **Count the characters in "hello 🌍".**
   The tool returns 7 Unicode code points.

`OMNARA_AGENT_PROFILE_ID` optionally limits the worker to agents launched from
that profile. `OMNARA_API_URL` optionally overrides `https://api.omnara.com/v1`;
`PORT` optionally overrides 3000.

## Delivery and recovery

The receiver verifies each webhook's signature and timestamp with `verifyEventWebhook` before using it. The helper checks project/profile scope and reads current readiness before invoking the handler, so a delayed ready event cannot start a call already observed as canceled.

The receiver returns 204 after starting handling in this process. It tracks the promise and reports failures explicitly. This example computes text length, which is safe to repeat: after a delivery failure or process restart, the application's scheduled check can compute it again. The startup recovery pass and later passes follow pages of up to 50 calls sequentially, without waiting between pages. The application pauses one minute after finishing a pass, then starts again from the beginning. The SDK does not schedule those checks or keep a result backlog.

For an action such as sending an email, make the action idempotent by tool-call ID across processes and keep any required results in your own durable storage. Save a verified event to your job queue before acknowledging it if acceptance must survive process exit. Serverless runtimes may stop background work after an HTTP response; use an awaited job supported by that platform instead.

## Delivery failures keep the computed result

`await worker.handle(update)` resolves with `completed`, `deferred`, or `ignored`. It rejects when processing or delivery cannot finish. `CustomToolDeliveryError` means the handler has already produced a result: use `error.call` and `error.result` to retry delivery without executing the business operation again. `error.cause` carries the underlying error, and optional `error.retryAt` is the earliest retry time in epoch milliseconds from `Retry-After`.

```typescript
try {
  await worker.handle(update, { signal })
} catch (error) {
  if (!(error instanceof CustomToolDeliveryError)) throw error
  await deliveries.save({ call: error.call, result: error.result, retryAt: error.retryAt })
}

// Your delivery job honors retryAt and submits the saved result.
await sdk.submitToolCallResult({
  client,
  path: { orgID, projectID, agentID: saved.call.agentID, toolCallID: saved.call.toolCallID },
  body: saved.result,
})
```

Import `CustomToolDeliveryError` and `sdk` from `@omnara/sdk`. Here `deliveries` is your existing application storage. No storage adapter or queue is required for the repeatable calculation in this example.

One helper submission uses the client's existing finite request policy. It retries network failures and HTTP 408, 429, 500, 502, 503 and 504 with backoff, jitter and `Retry-After`, without rerunning the tool. Configure `maxRetries` and `timeoutMs` on the client; this receiver uses a 30-second request deadline and the default two retries. Exhaustion returns the failure to the application. There is no background resubmission afterward. Set `timeoutMs` or pass a cancellation signal: a finite retry count alone does not stop a stalled HTTP request.

A `409` with `tool_call_already_completed` means the call is terminal, including cancellation or a lost successful response. The helper treats it as `completed`; direct delivery jobs must handle it too. It does not prove this result won. Other conflicts, 404s and invalid-request responses remain delivery errors with the original result.

For authentication or visibility failures, repair credentials/access before resubmitting. For a permanent HTTP 400, 413 or 422 payload rejection, inspect and correct/reduce the saved result or deliberately submit an explicit failed result explaining the reporting problem. Do not repeat a successful external action to repair its result delivery. The example logs payload rejections as an application repair task; repeatedly recomputing text length cannot fix them.

Local handler exceptions or invalid/unserializable output instead become a sanitized failed business result. Return an explicit failed result to give the model an intentional explanation.

## Finite checks and cancellation

The example follows pages sequentially without a polling delay between them, then waits one minute after a complete pass. A failed list request keeps its cursor for retry. A bounded application job can checkpoint and arrange prompt continuation instead.

`check({ cursor, limit, signal })` uses the full calls already returned by the list response, without per-call GETs. It awaits one page and returns `{ results, nextCursor }`. Consume every `failed` outcome before saving the next cursor; its `error` can be a `CustomToolDeliveryError` whose result needs durable recovery. Listing failures reject without a new cursor. Once listing succeeds, per-call cancellation/failures are returned as outcomes so other calls' computed results are not lost.

For long-running application jobs, return `{ outcome: 'deferred' }` and have the job submit its result directly when it finishes. Start or find the job idempotently by tool-call ID and reuse its saved result during recovery. Recovery is a fallback for missed completion delivery, so completion does not wait for another pass through the backlog. The application owns the job's cancellation and persistence.

Concurrent local invocations for the same call share the first invocation. Only its signal cancels the handler; a duplicate caller can cancel its own wait. Handlers must cooperate with cancellation. The owning call waits for a handler to settle, then exposes any undelivered computed result rather than abandoning it. A completed notification also signals active work to stop; its caller can cancel its own wait without abandoning the original invocation. Discovery executes from the list snapshot; notifications still read current readiness. A completion or cancellation elsewhere can race either read. These local conveniences do not claim calls, coordinate processes, or make external actions exactly once.

On SIGTERM/SIGINT, the application stops its scheduler/server, aborts its operations, and awaits accepted webhook work. The optional `onError` and `onObserve` hooks provide diagnostics; they never replace consuming failures from operations.

The same recovery rule applies with SSE: connect and check for already-ready calls, handle incoming frames, and keep scheduling checks while connected. Lifecycle frames can be dropped without a disconnect and are not replayed. The helper checks the whole configured project, including subagents; a direct agent-scoped recovery query needs `include_subagents=true` to match a parent's stream.

## Check

```sh
pnpm test
pnpm run typecheck
```

The repository's routine checks also run this example suite.
