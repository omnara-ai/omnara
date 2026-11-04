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
to `worker.handle`, and acknowledges right away. The worker runs `ready` calls
to the tools it registers and ignores everything else. When a payload leaves
out an input over 64 KiB, the worker fetches the call first.

The worker submits the tool's result, retrying network errors and `408`,
`429`, and `5xx` responses. If the tool throws, it submits a `failed` result with the
error message. A `409` with code `tool_call_already_completed` means another
worker already submitted a result, so it counts as done. Other errors go to
`onError`.

`worker.run()` lists ready calls at startup and every minute, so a call still
runs if its webhook was missed or the receiver was down. That can repeat a
calculation, which is safe because this tool has no external side effects. For
a tool that changes external state, use its tool-call ID as an idempotency key
in that system.

## Check

```sh
pnpm test
pnpm run typecheck
```
