# X Signal Agent

An agent that listens to X (Twitter) for you. Each run it searches the last
24 hours of posts about a topic you pick, filters out the noise, and delivers
a digest of at most five posts, each with why it matters and a suggested
action:

> **1. Founder comparing agent runtimes for their support bot**\
> Asks whether to build on claude managed agents or another managed agent provider; 40 replies, no clear answer yet.\
> **Why it matters:** an active buyer weighing exactly this category.\
> **Author:** @jdoe, CTO at a 20-person fintech\
> **Suggested action:** reply with how you handle long-running agents.

Reply to it wherever you use it (your own app, Slack, or the Omnara console)
to ask for reply drafts or push back on the filtering. It never posts to X.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com))
- An X API bearer token with pay-per-use billing from
  [console.x.com](https://console.x.com). A daily scan costs on the order of
  cents.

## Deploy

Open Claude Code, Codex, Cursor, or any other coding agent and say:

```text
Read https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/x-signal-agent/SKILL.md and follow it to deploy the X signal agent.
```

Your coding agent follows [SKILL.md](SKILL.md): it logs you in to Omnara,
helps you write the X search for your topic, stores your X token as an Omnara
secret (you put it in a `.env` file), creates the agent from
[agent.yaml](agent.yaml), and runs a first scan. Then it helps you put the
agent in your own app (existing or new) or Slack, and optionally runs it on a daily schedule.
Ask it to change anything along the way, like the instruction or the model.

Prefer to do it yourself? SKILL.md is plain steps with the exact `npx omnara`
commands. Run this again any time to change the topic; it updates the agent
in place.

## How it works

[agent.yaml](agent.yaml) is the whole agent. It runs on a machine from your
project's machine pool, where it calls the X API with `curl`; the token is
injected as the `X_BEARER_TOKEN` environment variable and never appears in the
config or the event log. The filter rules, the five-post cap, and "nothing
worth your time today" as a valid result are plain instruction text you can
read and edit.
