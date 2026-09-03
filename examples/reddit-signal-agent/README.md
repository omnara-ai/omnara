# Reddit Signal Agent

An agent that listens to Reddit for you. Once a day it searches the last 24
hours of conversation about any topic you pick, filters out the noise, and
delivers a short digest — at most five threads, each with why it matters and
a suggested action. Reply in Slack (or the Omnara console) to ask for reply
drafts or push back on the filtering. It never posts to Reddit.

There is no Reddit API key involved. Reddit gates its Data API behind a
manual approval process, so this agent fetches through an Apify-hosted MCP
server backed by a maintained Reddit scraper — an Apify account token is the
only credential, and the free plan covers a daily scan. Unlike the
[X signal agent](../x-signal-agent), this agent needs no machine pool: its
entire fetch layer is the MCP server.

The whole example is one runnable TypeScript file, [demo.ts](demo.ts). The agent itself
is a single config object — there is no service to deploy. Run it top to
bottom and you have your own.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com)) with an API key
- A free Apify account ([console.apify.com](https://console.apify.com/sign-up))
  — no credit card; the API token is under **Settings → Integrations**. The
  scraper is pay-per-result ($3.40 per 1,000 results), so a capped daily scan
  costs a few cents against the free plan's $5/month
- [Deno](https://docs.deno.com/runtime/getting_started/installation/)

## Run it

```sh
brew install deno

cd examples/reddit-signal-agent
cp .env.example .env      # set OMNARA_API_KEY and APIFY_TOKEN
deno install              # fetch @omnara/sdk into node_modules
deno run --allow-all demo.ts
```

Deno runs the TypeScript directly (`deno install` fetches `@omnara/sdk` from
`package.json`). The SDK itself works on Node and Bun too — `demo.ts` is a
plain script you can lift into any project.

Prefer notebook cells? `demo.ts` is in
[jupytext](https://jupytext.readthedocs.io/) percent format — open it in
Jupyter with jupytext and the Deno kernel (`deno jupyter --install`).

One section holds the topic and the Reddit
search terms — edit those lines to point the agent at whatever you want to
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
