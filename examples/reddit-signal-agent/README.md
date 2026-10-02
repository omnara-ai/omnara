# Reddit Signal Agent

An agent that listens to Reddit for you. Each run it scrapes the last 24
hours of posts about a topic you pick, across Reddit search and the
subreddits where your audience gathers, filters out the noise, and delivers a
digest of at most five threads, each with why it matters and a suggested
action:

> **1. Team asking how to run agents that survive restarts**\
> Their LangGraph agents lose state on every deploy; they're comparing a managed runtime against rolling their own queue.\
> **Why it matters:** a concrete pain point this category solves, with no answer in the thread yet.\
> **Author:** u/jdoe in r/LLMDevs\
> **Suggested action:** reply with how you handle durable agent state.

Reply to it wherever you use it (your own app, Slack, or the Omnara console)
to ask for reply drafts or push back on the filtering. It never posts to
Reddit.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com))
- A free Apify account ([console.apify.com](https://console.apify.com/sign-up),
  no credit card). Reddit's own API requires manual approval, so the agent
  scrapes through Apify's hosted MCP server instead. The scraper is billed
  per result and each scan is capped at 120 results, about $0.25 at most;
  Apify's free plan includes $5 of usage a month.

## Deploy

Open Claude Code, Codex, Cursor, or any other coding agent and say:

```text
Read https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/reddit-signal-agent/SKILL.md and follow it to deploy the Reddit signal agent.
```

Your coding agent follows [SKILL.md](SKILL.md), which is plain steps with the
exact `npx omnara` commands if you'd rather run them yourself. Run it again
any time to change the topic; it updates the agent in place.

## How it works

[agent.yaml](agent.yaml) is the whole agent. Its only fetch layer is Apify's
MCP server, pinned to the
[trudax/reddit-scraper-lite](https://apify.com/trudax/reddit-scraper-lite)
scraper, so it needs no machine pool. A scan is three tool calls: start the
scrape, wait for it to finish (several minutes), and read the results. Omnara
sends the Apify token as the bearer token on each MCP request; it never
appears in the config or the event log. The filter rules, the five-thread
cap, and "nothing worth your time today" as a valid result are plain
instruction text you can read and edit.
