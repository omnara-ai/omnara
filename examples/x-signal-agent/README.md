# X Signal Agent

An agent that listens to X (Twitter) for you. Once a day it searches the last
24 hours of conversation about any topic you pick, filters out the noise, and
delivers a short digest — at most five posts, each with why it matters and a
suggested action. Reply in Slack (or the Omnara console) to ask for reply
drafts or push back on the filtering. It never posts to X.

The whole example is one runnable TypeScript file, [demo.ts](demo.ts). The agent itself
is a single config object — there is no service to deploy. Run it top to
bottom and you have your own.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com)) with an API key
- An X API bearer token with pay-per-use billing
  ([console.x.com](https://console.x.com)) — a daily scan costs on the order
  of cents
- A TypeScript runtime: [Deno](https://docs.deno.com/runtime/getting_started/installation/),
  Node 22.18+, or [Bun](https://bun.sh)

## Run it

```sh
brew install deno

cd examples/x-signal-agent
cp .env.example .env      # set OMNARA_API_KEY and X_BEARER_TOKEN
deno install              # fetch @omnara/sdk into node_modules
deno run --env-file --allow-all demo.ts
```

Nothing in `demo.ts` is Deno-specific — `.env` loading is native in every
runtime and `@omnara/sdk` is a normal npm package, so Node and Bun run the
same file:

```sh
npm install && node --env-file=.env demo.ts   # Node 22.18+
bun install && bun demo.ts                    # Bun (reads .env automatically)
```

Prefer notebook cells? `demo.ts` is in
[jupytext](https://jupytext.readthedocs.io/) percent format — open it in
Jupyter with jupytext and the Deno kernel (`deno jupyter --install`).

One section holds the topic and the X search
query — edit those two lines to point the agent at whatever you want to
track. Every section is idempotent: rerun after editing and the agent updates
in place.

## Slack and scheduling (optional)

The demo can create a Slack app for you. Invite the bot to a channel and
mention it — the agent delivers its digest in that thread, and thread replies
become instructions to the agent. There is no default channel: it answers
wherever it is mentioned or DM'd.

The last section schedules a weekday-morning scan — opt-in: set
`SCHEDULE_DAILY=1` in `.env`. Scheduled runs launched from
the profile deliver to the Omnara console; to get them in a Slack channel,
mention the bot there once and point the trigger at that agent instead.
