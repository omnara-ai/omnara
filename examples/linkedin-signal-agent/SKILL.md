---
name: linkedin-signal-agent
description: Deploy or update the LinkedIn Signal Agent on Omnara, an agent that scrapes LinkedIn posts (keyword search plus tracked company and profile feeds) and delivers a daily digest. Use when the user asks to deploy, set up, update, or remove the LinkedIn signal agent.
---

# Deploy the LinkedIn Signal Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: an agent profile the user has watched run once, reachable
wherever they want it (their own app, Slack, Discord, or the Omnara
console), and
optionally running on a daily schedule.

The steps below are the usual path and have the exact commands. Skip anything
that's already done (for example, the token and profile exist and the user
only wants new queries), follow the user's lead if they want a different
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

LinkedIn keyword search returns plenty of adjacent hype, so the agent's
precision comes from knowing who the user is. Ask about their product, their
audience, and what kind of post would make them want to reply, then write
three values:

- `TEAM_CONTEXT`: one paragraph, on a single line, covering what the product
  does, who it's for, its main competitors, and what a strong demand signal
  looks like (for example, "a team describing building this in-house").
  This matters more than the queries.
- `LINKEDIN_SEARCH_QUERIES`: LinkedIn post searches as a JSON array on one
  line. Boolean syntax works (up to 5 operators and 500 characters per
  query), and cost scales per query, so prefer one boolean query over
  several. Escape inner double quotes, for example
  `["\"managed agents\" OR \"agent infrastructure\" OR \"durable agents\""]`.
  Use `[]` to skip keyword search.
- `LINKEDIN_TARGET_URLS`: company pages or profiles whose last-day posts are
  scanned even without a keyword match, such as competitors and category
  voices, as a JSON array on one line, for example
  `["https://www.linkedin.com/company/langchain/"]`. Use `[]` to skip tracked
  feeds.

At least one of the two lists must be nonempty. Show all three to the user (as
plain text and lists, not JSON) and adjust until they're happy.

## 3. Store the Apify token

LinkedIn's API has no public post search, so the agent scrapes through
[Apify's hosted MCP server](https://mcp.apify.com) running two scrapers that
need no LinkedIn account or cookie:
[harvestapi/linkedin-post-search](https://apify.com/harvestapi/linkedin-post-search)
and
[harvestapi/linkedin-profile-posts](https://apify.com/harvestapi/linkedin-profile-posts).
The user needs a free Apify account
([console.apify.com/sign-up](https://console.apify.com/sign-up), no credit
card); the API token is on the **API & Integrations** page under
**Settings**. Both scrapers are billed per post, and a scan is capped at 200
search posts plus 10 per tracked feed, about $0.50 at most.

1. Check for an existing secret:
   `npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name linkedin-signal-agent-apify-token --json`.
   If one exists, ask whether to reuse it. To reuse it, note its `id` and skip
   to step 4. To change its value, have the user update it on the
   **Secrets** page of the Omnara dashboard (the ID stays the same) and skip to
   step 4. If only `reddit-signal-agent-apify-token` exists, it holds the
   same kind of Apify token; offer to reuse its `id`.
2. Ask the user to add the token to a `.env` file in the current directory
   and tell you when it's saved (pasting it in the chat also works):

   ```sh
   APIFY_TOKEN=...
   ```

3. Create the secret:

   ```sh
   set -a && . ./.env && set +a
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name linkedin-signal-agent-apify-token --material-kind generic \
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

1. Copy `agent.yaml` from this folder to `./linkedin-signal-agent.yaml` in
   the current directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/linkedin-signal-agent/agent.yaml
   instead.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{TEAM_CONTEXT}}` | from step 2, on one line |
   | `{{LINKEDIN_SEARCH_QUERIES}}` | from step 2, the JSON array |
   | `{{LINKEDIN_TARGET_URLS}}` | from step 2, the JSON array |
   | `{{APIFY_TOKEN_SECRET_ID}}` | the `sec_…` ID from step 3 |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 4 |
   | `{{MODEL_NAME}}` | from step 4 |

   Then confirm nothing is left: `grep -n '{{' linkedin-signal-agent.yaml`
   must print nothing.
3. Check for an existing profile: `npx omnara profiles list --name linkedin-signal-agent --json`.
   - None: `npx omnara profiles create --name linkedin-signal-agent --file ./linkedin-signal-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./linkedin-signal-agent.yaml --json`

   Note the profile's `id` and `current_config_id` from the output.

## 6. Run a first scan

```sh
npx omnara agents launch --profile <agent-profile-id> --config <current-config-id> \
  --message "Run the LinkedIn scan now." --json
```

Give the user the link to watch it work:
`https://app.omnara.com/projects/<project-id>/agents/<agent-id>`. The scrapes
take a few minutes, mostly a quiet stretch of `get-actor-run` calls while the
agent waits. The user can reply in the console to ask for reply drafts or
push back on the filtering.

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
For this agent, name the integrations `linkedin-signal-agent-slack` and
`linkedin-signal-agent-discord`, call the bot "LinkedIn Signal Agent", and have
the user try it with "@LinkedIn Signal Agent run the scan".

## 8. Run it daily (optional)

A schedule works with any of the above. Ask whether the user wants a daily
scan, and for the time and timezone (default: weekdays at 9am in the user's
timezone). Set `--cron` and `--timezone` from their answer:

```sh
npx omnara crons create --name linkedin-signal-agent-daily \
  --target-type profile --target-agent-profile-id <agent-profile-id> \
  --cron '0 9 * * 1-5' --timezone America/Los_Angeles \
  --message-template 'Run the daily LinkedIn scan.' --json
```

If `npx omnara crons list --name linkedin-signal-agent-daily --json` already
shows a trigger, change it with `npx omnara crons update <cron-trigger-id>`
(same flags) instead of creating a second one.

Each firing launches a fresh agent from the profile. It shows up in the console,
and the user's app can pick it up through the SDK or API. To post every digest
in one Slack or Discord thread instead, see "Scheduled runs in a channel" in
`integrations.md`.

## 9. Wrap up

Summarize what you created: the secret name, the profile, and the Slack or
Discord integration or cron trigger if any. Then tell the user:

- **Change the queries or anything else:** ask a coding agent with this
  skill. It reuses the secret and updates the profile in place.
- **Remove it:** delete the cron trigger (`npx omnara crons delete <id>`),
  remove its integrations, if any (see "Removing" in `integrations.md`),
  then run `npx omnara profiles delete <agent-profile-id>` and
  `npx omnara secrets delete <secret-id>` (skip the secret if another agent
  shares it).
