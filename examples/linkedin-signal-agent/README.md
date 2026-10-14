# LinkedIn Signal Agent

An agent that listens to LinkedIn for you. Each run it scrapes the last 24
hours of posts matching your keyword searches, plus new posts from the
companies and people you track, filters out the noise, and delivers a digest
of at most five posts, each with why it matters and a suggested action:

> **1. Platform lead describing their in-house agent runtime**\
> Their team runs customer-facing agents on Kubernetes and spent a quarter on retries and sandbox provisioning; asks how others handle it.\
> **Why it matters:** a demand signal: exactly the infrastructure this team replaces.\
> **Author:** Jane Doe, Head of Platform at a Series B logistics startup\
> **Suggested action:** reply with how you handle machine failures, disclosing the affiliation.

Reply to it wherever you use it (your own app, Slack, Discord, or the Omnara console)
to ask for reply drafts or push back on the filtering. It never posts to
LinkedIn.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com))
- A free Apify account ([console.apify.com](https://console.apify.com/sign-up),
  no credit card). LinkedIn's API has no public post search, so the agent
  scrapes through Apify's hosted MCP server, using scrapers that need no
  LinkedIn account or cookie. A scan is capped at about 260 posts, roughly
  $0.50 at most; Apify's free plan includes $5 of usage a month.

## Deploy

Open Claude Code, Codex, Cursor, or any other coding agent and say:

```text
Read https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/linkedin-signal-agent/SKILL.md and follow it to deploy the LinkedIn signal agent.
```

Your coding agent follows [SKILL.md](SKILL.md), which is plain steps with the
exact `npx omnara` commands if you'd rather run them yourself. Run it again
any time to change what it tracks; it updates the agent in place.

## How it works

[agent.yaml](agent.yaml) is the whole agent. Its only fetch layer is Apify's
MCP server, pinned to two scrapers:
[harvestapi/linkedin-post-search](https://apify.com/harvestapi/linkedin-post-search)
for keywords and
[harvestapi/linkedin-profile-posts](https://apify.com/harvestapi/linkedin-profile-posts)
for tracked feeds, so it needs no machine pool. A scan starts each scraper
once, waits for both, and pools the results. Omnara sends the Apify token as
the bearer token on each MCP request; it never appears in the config or the
event log. The team context, the filter rules, the five-post cap, and
"nothing worth your time today" as a valid result are plain instruction text
you can read and edit.

Scraping avoids LinkedIn's API restrictions, not its terms of service, so keep
this agent internal-facing.
