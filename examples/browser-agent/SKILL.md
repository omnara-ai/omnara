---
name: browser-agent
description: Build and deploy a Browser Agent on Omnara, a long-running agent that signs in to web apps with stored credentials and carries out multi-step tasks in a real browser, overnight if needed. Interviews the user about the job, sign-ins, schedule, and Slack first. Use when the user asks to build, deploy, set up, update, or remove a browser agent.
---

# Build the Browser Agent

You are building an Omnara agent with the user, using `agent.yaml` in this
folder as the starting point. The goal: an agent profile that does the
user's job in their web apps, which the user has watched sign in to every
account once, reachable where they want it, and on a schedule if they want
one.

Interview first, then build. Don't create anything in Omnara until the user
has confirmed your summary of their answers in step 2.

`agent.yaml` is a guide, not a form. Its browser, sign-in, and machine
settings are tested together, so keep them unless you have a reason: the
agent-browser commands and vault sign-in, the startup script, the
`AGENT_BROWSER_*` variables, and the machine settings that keep the machine
and its browser alive. Read its comments before you edit it. Everything else
should fit the job:

- The "Your job" section comes from the interview (step 3).
- If the job needs more than the browser, add it: an MCP server for an API
  the user already has, `web_search` limits, or more instruction sections.
- If the user wants something the template doesn't do, change it and tell
  them what you changed.
- If a site blocks the agent, or the user wants to watch the browser live,
  switch it to a cloud browser as described in this folder's `README.md`
  under "When to use a cloud browser instead".
- If a command fails, read the error and fix it; `npx omnara <command> --help`
  and [docs.omnara.com](https://docs.omnara.com) have the details. If you're
  stuck, show the user the error.

The commands use the Omnara CLI (`npx omnara`) because its login handles auth
in one step; add `--json` when you need to read IDs from the output. The
Omnara MCP tools, the
[REST API](https://docs.omnara.com/api-reference/openapi.yaml), or the SDK work
too, and the flags map directly to API fields.

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

## 2. Interview the user

Ask these together in one message, in plain language, and say a rough answer
is fine. Don't ask for passwords here; step 4 collects them.

1. **The job.** What should the agent do, in their words? If there's a set
   way it has to be done, walk through the steps. What does "done" look like,
   and what should it hand back (a message, a CSV, a report)?
2. **Sign-ins.** Which sites or accounts does it sign in to? For each: a
   short name (like `crm` or `billing`) and the sign-in page URL. Ask whether
   any two are accounts on the same site, whether any sign in through SSO
   (Google, Okta, Microsoft), and whether any ask for a one-time code at sign-in.
3. **When it runs.** Started by someone whenever there's work, or on a
   schedule, such as every night at a set time? For a schedule, get the time
   and timezone.
4. **Where they follow it.** Suggest Slack: the team mentions the bot to
   start a task, and progress updates, questions, and results arrive in the
   thread. Other options are the Omnara console, which is always available,
   and their own app through the API.
5. **What it may do on its own.** Overnight there's no one to approve
   anything. Which actions may it take without asking, such as submitting
   forms, sending messages, or deleting records? Everything else it will ask
   about first and wait for an answer.

Follow up only where an answer is too vague to act on. Then summarize the
answers back in a few lines and get a yes before building. Two answers need
care:

- **SSO:** an SSO sign-in is a sign-in to the identity provider. Store the
  identity provider's sign-in page and credentials for it. If it requires a
  device prompt or a passkey, the agent can't complete it on its own;
  suggest Kernel's Managed Auth from `README.md`.
- **Two accounts on one site:** this works, but the agent switches between
  them by signing out, so it can't be in both at once. Tell the user.

## 3. Write the job section

Turn answer 1 and answer 5 into the `{{TASK_INSTRUCTIONS}}` text. Use the
user's wording and add nothing they didn't say. A good shape:

```text
  Every night, reconcile yesterday's orders in the billing portal against
  the CRM.

  1. Sign in to billing and export yesterday's orders as CSV.
  2. Sign in to crm and, for each order, find the matching deal by order
     number.
  3. Mark the deal "Paid" if the amounts match. List mismatches; don't fix
     them.

  Done means every order is checked. Hand back a summary with counts and a
  CSV of the mismatches.

  You may update deal status in crm without asking. Ask before changing
  anything in billing.
```

Refer to sign-ins by the names from answer 2. Indent every line by two
spaces, as above, so it nests inside the instruction. Show the text to the
user and adjust it until they're happy.

## 4. Store the sign-ins

For each sign-in from answer 2, choose a `LOGIN_KEY`: its name in uppercase,
with dashes turned into underscores (`crm` becomes `CRM`, `ops-portal`
becomes `OPS_PORTAL`). Recommend accounts made for the agent rather than
someone's personal login, with only the access the job needs, since everything
the agent does appears under that account.

1. Check for existing secrets:
   `npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name browser-agent-<name>-username --json`
   and the same with `-password`, for each name. If they exist, ask whether
   to reuse them. To change a value, have the user update it on the
   **Secrets** page of the Omnara dashboard (the ID stays the same).
2. Ask the user to add the missing sign-ins to a `.env` file in the current
   directory and tell you when it's saved. Discourage pasting passwords into
   the chat.

   ```sh
   LOGIN_CRM_USERNAME=agent@acme.com
   LOGIN_CRM_PASSWORD=...
   LOGIN_BILLING_USERNAME=agent@acme.com
   LOGIN_BILLING_PASSWORD=...
   ```

3. Create two secrets per sign-in, without printing the values:

   ```sh
   set -a && . ./.env && set +a
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name browser-agent-crm-username --material-kind generic \
     --material-value "$LOGIN_CRM_USERNAME" --json
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name browser-agent-crm-password --material-kind generic \
     --material-value "$LOGIN_CRM_PASSWORD" --json
   ```

   Note each `id` (`sec_…`).

## 5. Pick the model, machine pool, and machine size

1. `npx omnara grant models list --json`: each item has
   `model.provider_config` and `model.name`. The agent looks at screenshots,
   so the model must accept images. Suggest `anthropic/claude-opus-5.5` on
   `omnara-openrouter` if it's granted; otherwise suggest the strongest
   image-capable model on the list. The choice gives
   `MODEL_PROVIDER_CONFIG` and `MODEL_NAME`.
2. `npx omnara grant pools list --json`; each grant's `machine_pool.name` is a
   candidate `MACHINE_POOL`. Suggest `default-pool` if it's granted: its
   machines come with Node.js, which the startup script uses to install
   agent-browser and Chrome. Otherwise use the only pool, or ask if there are
   several. If there are none, tell the user the project needs a machine
   pool. On a custom pool, the image needs Node.js and npm. On a Daytona,
   Modal, or Tenki pool, delete the `sleep_after_ms` line; those providers
   don't support it and their machines don't sleep.
3. Size the machine large: reliability matters more than cost for this
   agent. Chrome is memory hungry, and on Blaxel the file system is in memory
   too. Use `8192` MB for `MACHINE_MEMORY_MB`, or the largest
   `max_machine_memory_mb` the pool and its project grant allow if that's
   lower (both appear in the `grant pools list` output). On Blaxel, CPU scales
   with memory. If the user needs bigger machines than the pool allows, they
   can raise its limits on the **Machines** page or create a custom pool.

## 6. Build the config

1. Copy `agent.yaml` from this folder to `./browser-agent.yaml` in the current
   directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/browser-agent-example/examples/browser-agent/agent.yaml
   instead.
2. Replace `{{TASK_INSTRUCTIONS}}` with the text from step 3, and fill in the
   first sign-in's `{{LOGIN_KEY}}`, `{{LOGIN_URL}}`,
   `{{LOGIN_USERNAME_SECRET_ID}}`, and `{{LOGIN_PASSWORD_SECRET_ID}}`. For each
   further sign-in, add its own lines next to the first. With two sign-ins:

   ```yaml
   env_overlay:
     LOGIN_CRM_URL: "https://crm.acme.com/login"
     LOGIN_BILLING_URL: "https://billing.acme.com/signin"
     # AGENT_BROWSER_… lines unchanged
   secret_env_overlay:
     LOGIN_CRM_USERNAME: "sec_…"
     LOGIN_CRM_PASSWORD: "sec_…"
     LOGIN_BILLING_USERNAME: "sec_…"
     LOGIN_BILLING_PASSWORD: "sec_…"
   ```

3. Leave the machine settings as they are, with one exception. The machine
   is never deleted while the agent exists (`delete_after_idle_minutes: 0`),
   so its browser and sign-ins carry over between tasks. It sleeps after two
   hours with no command running (`sleep_after_ms: 7200000`) and wakes in
   well under a second with Chrome, memory, and files intact. A working agent
   runs commands every few seconds, so it never sleeps mid-task.

   The exception: if any site from answer 2 asks for a one-time code at
   sign-in, set `sleep_after_ms: 0`. A sleeping browser can't keep a session
   alive, and a sign-in after waking would need a person to answer the code.
4. Fill in `{{MACHINE_MEMORY_MB}}`, `{{MODEL_PROVIDER_CONFIG}}`,
   `{{MODEL_NAME}}`, and `{{MACHINE_POOL}}` from step 5, then confirm nothing
   is left: `grep -n '{{' browser-agent.yaml` must print nothing.
5. Ask the user for a profile name if they want one other than
   `browser-agent`. Check for an existing profile:
   `npx omnara profiles list --name browser-agent --json`.
   - None: `npx omnara profiles create --name browser-agent --file ./browser-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./browser-agent.yaml --json`

   If it rejects the reasoning effort, the chosen model doesn't support
   `high`: remove the `reasoning` block and retry. Note the profile's `id`
   and `current_config_id`.

## 7. Sign in once

Launch a first task that changes nothing:

```sh
npx omnara agents launch --profile <agent-profile-id> --config <current-config-id> \
  --message "Don't do your job yet. Sign in to each of your saved sign-ins, and for each one tell me what's on the first page you land on and send a screenshot of it." --json
```

Give the user the link to watch it work:
`https://app.omnara.com/projects/<project-id>/agents/<agent-id>`. The first
reply takes a few minutes while the machine starts and installs agent-browser
and Chrome. If a site asks for a one-time code, the agent asks for it in the
conversation; the user answers there.

If a sign-in fails, read the agent's messages before changing anything. The
usual causes are a sign-in URL that redirects to a different form (store the
URL the form actually lives on), a two-page form that asks for the email and
then the password, or a site that blocks headless browsers from datacenter
IPs (switch to a cloud browser; see `README.md`).

If the user wants a schedule without Slack, keep this agent: step 9 sends it
the nightly runs, and it's already signed in. Otherwise archive it in the
console once every sign-in works.

## 8. Connect Slack (if chosen)

1. The user creates an app configuration token at
   [api.slack.com/apps](https://api.slack.com/apps) under **Your App
   Configuration Tokens** → **Generate Token**. It expires after about 12
   hours.
2. Have the user add it to `.env` as `SLACK_APP_CONFIG_TOKEN=...`, then run:

   ```sh
   set -a && . ./.env && set +a
   npx omnara profiles slack <agent-profile-id> --app-name "Browser Agent" \
     --app-configuration-token "$SLACK_APP_CONFIG_TOKEN"
   ```

   Ask before choosing a different app name; it's how the team will mention
   the bot. It opens a Slack authorization page; the user approves within 10
   minutes.
3. Tell the user to invite the bot to a channel (`/invite @Browser Agent`). A
   private channel suits it, since its updates can include data from the
   sites.

Each mention in a new thread starts a new agent with its own machine;
replies in the thread go to the same agent. Machines aren't deleted while
their agent exists, and every one counts toward the pool's machine limit, so
tell the user to archive a thread's agent in the console when its work is
done. Ongoing work belongs in one thread.

## 9. Schedule it (if chosen)

Use the time and timezone from answer 3, as a five-field cron expression. The
schedule sends each night's message to one standing agent, so every run uses
the same machine and starts already signed in. Pick the agent:

- **With Slack:** have the user mention the bot once in the channel where
  the nightly results should go, for example "@Browser Agent this thread is
  for your nightly runs; reply 'ready' and wait". Its runs post into that
  thread. Get that conversation's agent ID from its URL in the console.
- **Without Slack:** use the agent from step 7. Its runs show up in its
  conversation in the console.

```sh
npx omnara crons create --name browser-agent-nightly \
  --target-type agent --target-agent-id <agent-id> \
  --cron '0 22 * * *' --timezone America/Los_Angeles \
  --message-template 'Start your job for tonight.' --json
```

If a run is still going when the next one fires, the message waits in the
queue. Don't target the profile: that launches a new agent, and a new
machine that's never deleted, every night.

A standing agent keeps the config it was launched with. After updating the
profile, start a new standing agent and point the trigger at it with
`npx omnara crons update`, then archive the old agent.

If `npx omnara crons list --name browser-agent-nightly --json` already shows a
trigger, change it with `npx omnara crons update <cron-trigger-id>` instead of
creating a second one.

## 10. Wrap up

Summarize what you created: the secrets, the profile, the Slack app, and the
schedule, if any. Then tell the user:

- **Machines are kept.** Each agent's machine lasts until the agent is
  archived, sleeping after two idle hours (or never, if a site uses one-time
  codes) and waking with its browser intact. Archive agents in the console when their work is done;
  idle machines still count toward the pool's machine limit.
- **Change the job, sign-ins, or anything else:** ask a coding agent with
  this skill. It reuses the secrets and updates the profile in place.
  Running agents keep their config; new ones use the update.
- **Rotate a password:** update its secret on the **Secrets** page. New
  machines pick it up; for a standing agent, archive it and start a new one.
- **Remove it:** delete the cron trigger (`npx omnara crons delete <id>`),
  remove the Slack integration from the profile in the dashboard, then
  `npx omnara profiles delete <agent-profile-id>` and
  `npx omnara secrets delete <secret-id>` for each secret.
