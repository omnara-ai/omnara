# Custom tools over signed webhooks

A small TypeScript receiver runs a `text_length` custom tool with the SDK's
`createCustomToolWorker` and posts the result back to Omnara. Webhooks deliver
ready calls right away, with periodic recovery for missed notifications. The
receiver verifies Standard Webhooks signatures before parsing the payload and
subscribes only to `tool_call_update`.

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

The receiver acknowledges with 204 before tool handling finishes. This is safe for the repeatable text-length calculation: after a delivery failure or process restart, recovery can compute it again. Recovery runs at startup, follows pages sequentially without a delay between them, and pauses one minute between complete passes. A failed list request keeps its cursor for retry.

For an action such as sending an email, make the action idempotent by tool-call ID across processes and keep any required results in your own durable storage. Save a verified event to your job queue before acknowledging it if acceptance must survive process exit. Serverless runtimes may stop background work after an HTTP response; use an awaited job supported by that platform instead.

The receiver reports processing and delivery failures. Permanent payload rejections require correcting the result; rerunning the calculation cannot repair them. On SIGTERM/SIGINT, it stops accepting work, signals cancellation, and awaits accepted webhook work.

See the [custom-tool guide](https://docs.omnara.com/tools/custom#typescript-helper) for preserving computed results, deferred jobs, cancellation, and recovery contracts, and the [SDK retry policy](../../frontend/packages/sdk/README.md#retries-and-deadlines) for request configuration. When adapting this example to long-running jobs, submit results when those jobs finish and use recovery as a fallback.

## Check

```sh
pnpm test
pnpm run typecheck
```
