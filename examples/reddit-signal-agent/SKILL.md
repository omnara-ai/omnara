---
name: reddit-signal-agent
description: Deploy or update the Reddit Signal Agent on Omnara, an agent that scrapes Reddit for posts about a topic and delivers a daily digest. Use when the user asks to deploy, set up, update, or remove the Reddit signal agent.
---

# Deploy the Reddit Signal Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: an agent profile the user has watched run once, reachable
wherever they want it (their own app, Slack, Discord, or the Omnara
console), and
optionally running on a daily schedule.

The steps below are the usual path and have the exact commands. Skip anything
that's already done (for example, the token and profile exist and the user
only wants a new topic), follow the user's lead if they want a different
order, and narrate briefly as you go.

- Start from `agent.yaml` as written. If the user wants something different
  (the instruction, tools, model, or how they reach the agent), change it and
  tell them what you changed.
- If a command fails, read the error and fix it; `npx omnara <command> --help`
  and [docs.omnara.com](https://docs.omnara.com) have the details. If you're
  stuck, show the user the error.

The commands use the Omnara CLI (`npx omnara`) because its login handles auth
in one step; add `--json` when you need to read IDs from the output. They're a
reference, not a requirement: the Omnara MCP tools, the
[REST API](https://docs.omnara.com/api-reference/openapi.yaml), or the SDK work
too, and the flags map directly to API fields. What matters is the result:
a secret with the token, a profile from the filled-in `agent.yaml`, and
whatever the user picks in steps 7 and 8.

## 1. Connect to Omnara

1. Run `npx omnara whoami`. If it reports that you aren't logged in, run
   `npx omnara login` in an interactive terminal, share the approval link it
   prints, and wait until the user approves.
2. Pick the org and project. `npx omnara whoami --json` lists the user's orgs,
   and `npx omnara projects list --org <org-id> --json` lists an org's
   projects.
   - If there's only one org and one project, use them.
   - If there are several of either, ask the user which to use. Suggest the
     current defaults from `npx omnara config` if they're set, otherwise the
     project named `Default`.

   Save the choice with `npx omnara config --org <org-id> --project <project-id>`.

Remember the project ID; later steps need it.

## 2. Choose what to track

Ask the user what they want to hear about on Reddit: their product category,
competitors, customers' pain points, or any topic. Then write three values:

- `TOPIC`: a short phrase the agent judges relevance against, for example
  `managed-agent infrastructure`.
- `REDDIT_SEARCHES`: phrases searched across all of Reddit, as a JSON array on
  one line, for example `["managed agents", "agent infrastructure"]`. Reddit
  search has no boolean operators, so use one plain phrase per entry; three to
  six is plenty.
- `SUBREDDIT_URLS`: communities whose newest posts are scanned even when
  search would miss them, as a JSON array of `/new/` URLs on one line, for
  example
  `[{"url": "https://www.reddit.com/r/AI_Agents/new/"}, {"url": "https://www.reddit.com/r/LLMDevs/new/"}]`.
  Suggest subreddits where the user's audience gathers; up to about ten.

Show all three to the user (as plain lists, not JSON) and adjust until they're
happy.

## 3. Store the Apify token

Reddit's own API requires manual approval, so the agent scrapes through
[Apify's hosted MCP server](https://mcp.apify.com) running the
[trudax/reddit-scraper-lite](https://apify.com/trudax/reddit-scraper-lite)
scraper. The user needs a free Apify account
([console.apify.com/sign-up](https://console.apify.com/sign-up), no credit
card); the API token is on the **API & Integrations** page under
**Settings**. The scraper is billed per result, and each scan is capped at 120
results, so it costs cents.

1. Check for an existing secret:
   `npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name reddit-signal-agent-apify-token --json`.
   If one exists, ask whether to reuse it. To reuse it, note its `id` and skip
   to step 4. To change its value, have the user update it on the
   **Secrets** page of the Omnara dashboard (the ID stays the same) and skip to
   step 4.
2. Ask the user to add the token to a `.env` file in the current directory
   and tell you when it's saved (pasting it in the chat also works):

   ```sh
   APIFY_TOKEN=...
   ```

3. Create the secret:

   ```sh
   set -a && . ./.env && set +a
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name reddit-signal-agent-apify-token --material-kind generic \
     --material-value "$APIFY_TOKEN" --json
   ```

   Note the `id` (`sec_…`) from the output.

## 4. Pick the model

`npx omnara grant models list --json`: each item has `model.provider_config`
and `model.name`. Ask the user which model to use. Suggest `openai/gpt-6.1-sol`
on `omnara-openrouter` if it's granted (it replaces GPT-6 Sol, which the
instruction was tested with); otherwise suggest a strong general-purpose model from the list. Name a couple
of alternatives rather than the whole list, and show everything if they ask.
The choice gives `MODEL_PROVIDER_CONFIG` and `MODEL_NAME`.

This agent needs no machine pool: the Apify MCP server does all the fetching.

## 5. Create the agent

1. Copy `agent.yaml` from this folder to `./reddit-signal-agent.yaml` in the
   current directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/reddit-signal-agent/agent.yaml
   instead.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{TOPIC}}` | from step 2 |
   | `{{REDDIT_SEARCHES}}` | from step 2, the JSON array |
   | `{{SUBREDDIT_URLS}}` | from step 2, the JSON array |
   | `{{APIFY_TOKEN_SECRET_ID}}` | the `sec_…` ID from step 3 |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 4 |
   | `{{MODEL_NAME}}` | from step 4 |

   Then confirm nothing is left: `grep -n '{{' reddit-signal-agent.yaml` must
   print nothing.
3. Check for an existing profile: `npx omnara profiles list --name reddit-signal-agent --json`.
   - None: `npx omnara profiles create --name reddit-signal-agent --file ./reddit-signal-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./reddit-signal-agent.yaml --json`

   Note the profile's `id` and `current_config_id` from the output.

## 6. Run a first scan

```sh
npx omnara agents launch --profile <agent-profile-id> --config <current-config-id> \
  --message "Run the Reddit scan now." --json
```

Give the user the link to watch it work:
`https://app.omnara.com/projects/<project-id>/agents/<agent-id>`. The scrape
runs a real browser behind the MCP server, so a scan takes several minutes,
mostly a quiet stretch of `get-actor-run` calls while the agent waits. The
user can reply in the console to ask for reply drafts or push back on the
filtering.

## 7. Choose where to use it

Ask where the user wants to talk to the agent. Any combination works, and the
Omnara console always does: every agent launched from the profile shows up
there.

- **Their own app (recommended):** inside their product or internal tool, or a
  small UI built for it.
- **Slack or Discord:** the team mentions the bot, and replies in the thread go
  to the same agent.

Set these up with [`integrations.md`](../integrations.md); if it isn't next to
this folder, download
https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/integrations.md.
For this agent, name the integrations `reddit-signal-agent-slack` and
`reddit-signal-agent-discord`, call the bot "Reddit Signal Agent", and have the
user try it with "@Reddit Signal Agent run the scan".

## 8. Run it daily (optional)

A schedule works with any of the above. Ask whether the user wants a daily
scan, and for the time and timezone (default: weekdays at 9am in the user's
timezone). Set `--cron` and `--timezone` from their answer:

```sh
npx omnara crons create --name reddit-signal-agent-daily \
  --target-type profile --target-agent-profile-id <agent-profile-id> \
  --cron '0 9 * * 1-5' --timezone America/Los_Angeles \
  --message-template 'Run the daily Reddit scan.' --json
```

If `npx omnara crons list --name reddit-signal-agent-daily --json` already
shows a trigger, change it with `npx omnara crons update <cron-trigger-id>`
(same flags) instead of creating a second one.

Each firing launches a fresh agent from the profile. It shows up in the console,
and the user's app can pick it up through the SDK or API. To post every digest
in one Slack or Discord thread instead, see "Scheduled runs in a channel" in
`integrations.md`.

## 9. Wrap up

Summarize what you created: the secret name, the profile, and the Slack or
Discord integration or cron trigger if any. Then tell the user:

- **Change the topic or anything else:** ask a coding agent with this skill.
  It reuses the secret and updates the profile in place.
- **Remove it:** delete the cron trigger (`npx omnara crons delete <id>`),
  remove its integrations, if any (see "Removing" in `integrations.md`),
  then run `npx omnara profiles delete <agent-profile-id>` and
  `npx omnara secrets delete <secret-id>` (skip the secret if another agent
  shares it).
