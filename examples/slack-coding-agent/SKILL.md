---
name: slack-coding-agent
description: Deploy or update the Slack Coding Agent on Omnara, a coding agent people mention in Slack, in Discord, or on a GitHub pull request that works on a GitHub repository and opens pull requests. Use when the user asks to deploy, set up, update, or remove the Slack coding agent.
---

# Deploy the Slack Coding Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: a coding agent the user's team can mention in Slack, in
Discord, or on a GitHub pull request, which works on one of their GitHub
repositories and opens pull requests, and which the user has watched answer
once.

The steps below are the usual path, with the exact commands. Skip anything
that's already done (for example, the token and profile exist and the user only
wants a different model), and follow the user's lead if they want a different
order.

- Start from `agent.yaml` as written. If the user wants something different
  (the instruction, tools, or model), change it and tell them what you
  changed.
- If the user wants the agent to read their issue tracker (Linear, Jira, and
  so on), add the tracker's MCP server in an `mcp:` block, store its
  credential with `npx omnara secrets mcp-oauth` (OAuth) or as a `generic`
  secret (API key), and add a short instruction section on how to use it.
  Tell the user that an OAuth connection acts as whoever approves it, so its
  writes appear under that person's name.
- If a command fails, `npx omnara <command> --help` and
  [docs.omnara.com](https://docs.omnara.com) have the details.

The commands use the Omnara CLI (`npx omnara`); add `--json` to read IDs.
They're a reference, not a requirement: the Omnara MCP tools, the
[REST API](https://docs.omnara.com/api-reference/openapi.yaml), and the SDK work
too, and the flags map directly to API fields.

## 1. Connect to Omnara

Run `npx omnara whoami`. If you aren't logged in, run `npx omnara login` in an
interactive terminal, share the approval link it prints, and wait for the user
to approve. Then pick the org and project (`npx omnara whoami --json` lists
orgs, `npx omnara projects list --org <org-id> --json` their projects): use them
if there's one of each, otherwise ask, suggesting the defaults from
`npx omnara config` or the project named `Default`. Save the choice with
`npx omnara config --org <org-id> --project <project-id>`; later steps need the
project ID.

## 2. Choose the repository and store a GitHub token

Ask which repository the agent works on (`GITHUB_REPO`, as `owner/name`). One
repository is supported as written.

The agent pushes branches and opens pull requests, so it needs a token with
write access. Recommend a
[fine-grained token](https://github.com/settings/personal-access-tokens/new)
limited to that repository, with **Contents** and **Pull requests** set to
read and write. Optional extras: **Issues** read (to read linked issues),
**Actions** read (to read CI logs), and **Workflows** read and write (only if
it should edit `.github/workflows`). Commits and pull requests appear under
the token owner's account, so a dedicated bot account keeps them separate
from a person's. If the repository belongs to an organization, its owners
may need to approve the token.

If a secret named `slack-coding-agent-github` already exists
(`npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name slack-coding-agent-github --json`),
ask whether to reuse it and note its `id`; if the value changed, the user
updates it on the dashboard's **Secrets** page (the ID stays the same).
Otherwise have the user add `AGENT_GITHUB_TOKEN=github_pat_...` to a `.env` file
in the current directory (pasting it in the chat also works), then create the
secret and note its `id` (`sec_…`) as `GITHUB_TOKEN_SECRET_ID`:

```sh
set -a && . ./.env && set +a
npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
  --name slack-coding-agent-github --material-kind generic \
  --material-value "$AGENT_GITHUB_TOKEN" --json
```

## 3. Pick the model and machine pool

1. `npx omnara grant models list --json`: each item has `model.provider_config`
   and `model.name`. Ask the user which model to use, suggesting
   `anthropic/claude-opus-5.5` on `omnara-openrouter` if it's granted
   (`agent.yaml` runs it at high reasoning effort), otherwise the strongest
   coding model on the list. The choice gives `MODEL_PROVIDER_CONFIG` and
   `MODEL_NAME`.
2. `npx omnara grant pools list --json`; each grant's `machine_pool.name` is a
   candidate `MACHINE_POOL`. Suggest `default-pool` if it's granted: its
   machines come with `git`, `gh`, and common language toolchains. Otherwise
   use the only pool, or ask if there are several. If there are none, tell
   the user the project needs a machine pool; the agent can't work on code
   without one. On a custom pool, the image needs `git` and `gh`.

## 4. Create the agent

1. Copy `agent.yaml` from this folder to `./slack-coding-agent.yaml` in the
   current directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/slack-coding-agent/agent.yaml
   instead.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{GITHUB_REPO}}` | from step 2 (it appears twice) |
   | `{{GITHUB_TOKEN_SECRET_ID}}` | from step 2 |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 3 |
   | `{{MODEL_NAME}}` | from step 3 |
   | `{{MACHINE_POOL}}` | from step 3 |

   Then confirm nothing is left: `grep -n '{{' slack-coding-agent.yaml` must
   print nothing.
3. Check for an existing profile: `npx omnara profiles list --name slack-coding-agent --json`.
   - None: `npx omnara profiles create --name slack-coding-agent --file ./slack-coding-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./slack-coding-agent.yaml --json`

   If it rejects the reasoning effort, the chosen model doesn't support
   `high`: remove the `reasoning` block and retry. Note the profile `id`.

## 5. Choose where the team reaches it

Ask where the team should mention the agent: Slack, Discord, GitHub pull
requests, or any combination. Wherever it's mentioned, it answers in that
thread or pull request, and replies there go to the same agent.

Set up whichever the team picks with [`integrations.md`](../integrations.md)
(read it from
https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/integrations.md
if you don't have the repo). For this agent, name the integrations
`slack-coding-agent-slack`, `slack-coding-agent-discord`, and
`slack-coding-agent-github`, and call the Slack and Discord bot "Coding Agent".
A private channel suits it, since threads often include internal details.

On GitHub, register the App for the account that owns `GITHUB_REPO`, grant it
only that repository, and give it **Contents** write access so it can push
fixes. The first mention is a comment on an open pull request, such as
"@acme-coding-agent summarize this PR" with the App's slug.

## 6. Give it a first task

Have the user mention the bot in Slack or Discord with a question that doesn't
change anything:

> @Coding Agent what does this repo do, and how do I run its tests?

The first reply takes a minute or two while the machine starts and clones the
repository. The conversation also shows up as an agent in the Omnara console
([app.omnara.com](https://app.omnara.com)), where the user can watch every
command it runs. Once the answer looks right, suggest a small real fix as the
next message, such as a known bug or a typo, so they see it open a pull
request. With only GitHub connected, the comment from step 5 is the first
task.

## 7. Wrap up

Summarize what you created: the secret, the profile, and the integrations. Then
tell the user:

- **Start it from elsewhere too:** anything that can call the API (a Linear
  webhook, a CI job) can launch an agent from the profile with the task as
  the message; see [docs.omnara.com](https://docs.omnara.com). The
  instruction assumes a Slack or Discord thread or a pull request, so for
  those entry points adjust its Reporting Guidelines.
- **Change the instruction, model, or anything else:** ask a coding agent
  with this skill. It reuses the secret and updates the profile in place.
- **Remove it:** remove its integrations (see "Removing" in `integrations.md`),
  then run `npx omnara profiles delete <agent-profile-id>` and
  `npx omnara secrets delete <secret-id>` for each secret. Revoke the GitHub
  token in GitHub's settings as well.
