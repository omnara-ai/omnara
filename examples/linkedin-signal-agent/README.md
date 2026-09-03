# LinkedIn Signal Agent

An agent that listens to LinkedIn for you. Once a day it searches the last 24
hours of posts about any topic you pick, filters out the noise, and delivers
a short digest — at most five posts, each with why it matters and a suggested
action. Reply in Slack (or the Omnara console) to ask for reply drafts or
push back on the filtering. It never posts to LinkedIn.

There is no LinkedIn API key involved — LinkedIn's official API has no
public post search — and **no LinkedIn account or session cookie** to risk:
the agent fetches through an Apify-hosted MCP server backed by two no-cookie
scrapers, [harvestapi/linkedin-post-search](https://apify.com/harvestapi/linkedin-post-search)
for keyword search and
[harvestapi/linkedin-profile-posts](https://apify.com/harvestapi/linkedin-profile-posts)
for tracked company/profile feeds. An Apify token is the only credential,
and like the [Reddit signal agent](../reddit-signal-agent) it needs no
machine pool: the entire fetch layer is the MCP server.

The whole example is one runnable TypeScript file, [demo.ts](demo.ts). The agent itself
is a single config object — there is no service to deploy. Run it top to
bottom and you have your own.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com)) with an API key
- A free Apify account ([console.apify.com](https://console.apify.com/sign-up))
  — no credit card; the API token is under **Settings → Integrations**. Both
  scrapers are pay-per-result (~$2 per 1,000 posts), so a capped scan costs
  about $0.52; a weekday cron runs ~$11/month, past the free plan's $5 credit
- A TypeScript runtime: [Deno](https://docs.deno.com/runtime/getting_started/installation/),
  Node 22.18+, or [Bun](https://bun.sh)

## Run it

```sh
brew install deno

cd examples/linkedin-signal-agent
cp .env.example .env      # set OMNARA_API_KEY and APIFY_TOKEN
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

One section holds the topic, the LinkedIn
search queries, and the tracked company/profile URLs — edit those lines to
point the agent at whatever you want to track. Every section is idempotent:
rerun after editing and the agent updates in place.

## Slack and scheduling (optional)

The demo can create a Slack app for you. Invite the bot to a channel and
mention it — the agent delivers its digest in that thread, and thread replies
become instructions to the agent. There is no default channel: it answers
wherever it is mentioned or DM'd.

The last section schedules a weekday-morning scan — opt-in: set
`SCHEDULE_DAILY=1` in `.env`. Scheduled runs launched from
the profile deliver to the Omnara console; to get them in a Slack channel,
mention the bot there once and point the trigger at that agent instead.
