---
name: video-generation-agent
description: Deploy or update the Video Generation Agent on Omnara, an agent that turns a brief into a short video with Remotion, reviews its own frames, and delivers the rendered MP4. Use when the user asks to deploy, set up, update, or remove the video generation agent.
---

# Deploy the Video Generation Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: an agent profile the user has watched produce one video,
reachable wherever the user wants it.

The steps below are the usual path, with the exact commands. Skip anything
that's already done (for example, the skill and profile exist and the user only
wants a different model), and follow the user's lead if they want a different
order.

- Start from `agent.yaml` as written. If the user wants something different
  (the instruction, tools, model, or how they reach the agent), change it and
  tell them what you changed.
- If a command fails, `npx omnara <command> --help` and
  [docs.omnara.com](https://docs.omnara.com) have the details.

The commands use the Omnara CLI (`npx omnara`); add `--json` to read IDs.
They're a reference, not a requirement: the Omnara MCP tools, the
[REST API](https://docs.omnara.com/api-reference/openapi.yaml), and the SDK work
too, and the flags map directly to API fields.

## 1. Connect to Omnara

Run `npx omnara whoami`. If you aren't logged in, run `npx omnara login` without
piping its output, and keep it running: it waits until the user approves. It
opens the approval page in the user's browser and prints the page's link and a
code. Show the user both right away, since you may be on a different machine
than their browser, and ask them to approve once the code matches. If the
request expires, run it again. If login says the account has no organization
yet, have the user create one at the link it prints. Then `npx omnara whoami`
should succeed.

Pick the org and project (`npx omnara whoami --json` lists orgs,
`npx omnara projects list --org <org-id> --json` their projects): use them if
there's one of each, otherwise ask, suggesting the defaults from
`npx omnara config` or the project named `Default`. Save the choice with
`npx omnara config --org <org-id> --project <project-id>`; later steps need the
project ID.

## 2. Upload the Remotion skill

The agent writes videos with [Remotion](https://www.remotion.dev) and follows
Remotion's own [agent skills](https://github.com/remotion-dev/skills).
`remotion-best-practices` is a router: its `SKILL.md` points to reference
folders next to it, so upload the whole folder, not just `SKILL.md`.

If an upload named `remotion-best-practices` already exists
(`npx omnara skills list --owner-kind project --owner-project-id <project-id> --name remotion-best-practices --json`),
ask whether to reuse it as is or refresh it with the latest Remotion release.
Otherwise, or to refresh it, clone the skills and upload the folder; the CLI
packs the directory itself:

```sh
git clone --depth 1 https://github.com/remotion-dev/skills /tmp/remotion-skills
npx omnara skills create --owner-kind project --owner-project-id <project-id> \
  --archive /tmp/remotion-skills/skills/remotion-best-practices --json
```

The `id` (`skl_…`) is `REMOTION_SKILL_ID`. A refresh adds a revision under the
same ID, and the agent uses it from its next model call, so the profile doesn't
change.

## 3. Pick the model and machine pool

1. `npx omnara grant models list --json`: each item has `model.provider_config`
   and `model.name`. Suggest Claude Opus 5.5 (the instruction was tested with
   it, and it reviews its own frames well): the user's own Anthropic provider
   config if they have one with Opus 5.5, otherwise `anthropic/claude-opus-5.5`
   on `omnara-openrouter`. If Opus 5.5 isn't granted, suggest another strong
   model that accepts images. The choice gives `MODEL_PROVIDER_CONFIG` and
   `MODEL_NAME`.
2. `npx omnara grant pools list --json`; each grant's `machine_pool.name` is a
   candidate `MACHINE_POOL`. Suggest `default-pool` if it's granted: its
   machines come with Node.js and headless Chromium, which Remotion needs.
   Otherwise use the only pool, or ask if there are several. If there are
   none, tell the user the project needs a machine pool; the agent can't
   render without one.

## 4. Create the agent

1. Copy `agent.yaml` from this folder to `./video-generation-agent.yaml` in
   the current directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/video-generation-agent/agent.yaml
   instead.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{REMOTION_SKILL_ID}}` | the `skl_…` ID from step 2 |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 3 |
   | `{{MODEL_NAME}}` | from step 3 |
   | `{{MACHINE_POOL}}` | from step 3 |

   Then confirm nothing is left: `grep -n '{{' video-generation-agent.yaml`
   must print nothing.
3. Check for an existing profile: `npx omnara profiles list --name video-generation-agent --json`.
   - None: `npx omnara profiles create --name video-generation-agent --file ./video-generation-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./video-generation-agent.yaml --json`

   If it rejects the reasoning effort, the chosen model doesn't support
   `xhigh`: change it to `high`, or remove the `reasoning` block, and retry.
   Note the profile's `id` and `current_config_id` from the output.

## 5. Make a first video

Ask the user for a brief. If they don't have one in mind, use this:

```sh
npx omnara agents launch --profile <agent-profile-id> --config <current-config-id> \
  --message "A 10-second 16:9 title card for a product launch: the words 'Launch day' animate in over a slow-moving starfield, hold, then fade to black." --json
```

Give the user the link to watch it work:
`https://app.omnara.com/projects/<project-id>/agents/<agent-id>`. The first
video usually takes several minutes: the agent sets up a Remotion project,
renders, checks its own frames, and renders again if it finds problems. The
MP4 arrives as a file in the conversation. The user can reply there to ask
for changes ("make the text bigger", "make it 9:16"), or attach a logo or
music for the agent to use.

## 6. Choose where to use it

Ask where the user wants to talk to the agent. Any combination works, and the
Omnara console always does: every agent launched from the profile shows up
there.

- **Their own app (recommended):** inside their product or internal tool, or a
  small UI built for it. Anything that can call the API, like a CMS hook or a
  script, can start one with the brief as the message.
- **Slack or Discord:** the team mentions the bot, and replies in the thread go
  to the same agent. It posts the MP4 there, so replies are revision requests.

Set up whichever the user picks with [`integrations.md`](../integrations.md)
(read it from
https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/integrations.md
if you don't have the repo). For this agent, name the integrations
`video-generation-agent-slack` and `video-generation-agent-discord`, call the
bot "Video Agent", and have the user try it with "@Video Agent a 15-second
teaser for our new pricing page".

## 7. Wrap up

Summarize what you created: the skill, the profile, and any integrations. Then
tell the user:

- **Change the instruction, model, or anything else:** ask a coding agent with
  this skill. It reuses the Remotion skill and updates the profile in place.
- **Remove it:** remove its integrations, if any (see "Removing" in
  `integrations.md`), then run `npx omnara profiles delete <agent-profile-id>`
  and `npx omnara skills delete <skill-id>`.
