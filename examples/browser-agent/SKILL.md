---
name: browser-agent
description: Build and deploy a Browser Agent on Omnara, a long-running agent that signs in to web apps with stored credentials and works in a real browser, overnight if needed. Interviews the user about the job, sign-ins, schedule, and Slack or Discord first. Use when the user asks to build, deploy, set up, update, or remove a browser agent.
---

# Build the Browser Agent

You are building an Omnara agent with the user, starting from `agent.yaml` in
this folder. The result: an agent profile that does the user's work in their
web apps, has signed in to each account once while the user watched, and is
reachable where they want it, on a schedule if they want one.

Interview first, then build. Don't create anything in Omnara until the user
has confirmed your summary in step 2.

`agent.yaml` is a guide, not a form. Its browser, sign-in, and machine
settings were tested together: the agent-browser commands, the vault and
two-factor helper in the startup script, the `AGENT_BROWSER_*` variables, and
the settings that keep the machine alive. Keep them unless you have a reason,
and read the file's comments before editing. Fit everything else to the job:
add instruction sections, an MCP server for an API the user already has, or
anything else the job needs, and tell the user what you changed. If a command
fails, read the error and fix it; `npx omnara <command> --help` and
[docs.omnara.com](https://docs.omnara.com) have the details.

The commands use the Omnara CLI (`npx omnara`); add `--json` when you need to
read IDs. The Omnara MCP tools, the
[REST API](https://docs.omnara.com/api-reference/openapi.yaml), and the SDK
work too, and the flags map directly to API fields.

## 1. Connect to Omnara

1. Run `npx omnara whoami`. If it reports that you aren't logged in, run
   `npx omnara login` in an interactive terminal, share the approval link it
   prints, and wait until the user approves.
2. Pick the org and project. `npx omnara whoami --json` lists the user's orgs,
   and `npx omnara projects list --org <org-id> --json` lists an org's
   projects. If there's one of each, use them. Otherwise ask, suggesting the
   defaults from `npx omnara config` or the project named `Default`. Save the
   choice with `npx omnara config --org <org-id> --project <project-id>`.

Remember the project ID; later steps need it.

## 2. Interview the user

Ask these together in one message, in plain language, and say a rough answer
is fine. Don't ask for passwords here; step 4 collects them.

1. **The work.** Is it one set job that runs the same way each time, or a
   helper the team hands different tasks to? For a set job: what it does,
   step by step if there's a set way, what "done" looks like, and what it
   hands back (a message, a CSV, a report). For a helper: the kinds of tasks
   it will get.
2. **Sign-ins.** Which sites does it sign in to? For each: a short name (like
   `crm`), the sign-in page URL, and how it handles two-factor sign-in, if at
   all: an authenticator app, or a code by text or email. Also ask whether any
   two are accounts on the same site, and whether any use SSO (Google, Okta,
   Microsoft).
3. **When it runs.** Whenever someone starts it, or on a schedule such as
   every night at a set time? For a schedule, get the time and timezone.
4. **Where they follow it.** Suggest Slack or Discord, whichever the team
   uses: the team mentions the bot to start a task, and updates, screenshots,
   questions, and results arrive in the thread. The Omnara console always
   works too. Ask whether they want a
   screenshot with every update (the default), only with questions, or only
   with the result.
5. **What it may do on its own.** Overnight no one is around to approve
   anything. Which actions may it take without asking, such as submitting
   forms, sending messages, or deleting records? It will ask about everything
   else and wait.

Follow up only where an answer is too vague to act on, then summarize the
answers in a few lines and get a yes. Explain these where they apply:

- **Authenticator app:** the agent can enter these codes itself from the
  account's setup key, the text code shown under "can't scan the QR code?"
  when the app is set up. If the account already has one, the user may need
  to set it up again to see the key, then add the same key to their phone.
- **Codes by text or email:** the agent asks a person each time it signs in,
  so it can't sign in unattended. Suggest switching that account to an
  authenticator app.
- **SSO:** store the identity provider's sign-in page and credentials. If it
  needs a device prompt or passkey, the agent can't do it alone; suggest
  a hosted browser, as described in `README.md` under "If a site blocks the
  agent".
- **Two accounts on one site:** works, but the agent signs out to switch, so
  it can't use both at once.
- **No sign-ins:** fine; step 4 is skipped.

## 3. Write the job section

Turn answers 1, 4, and 5 into the `{{TASK_INSTRUCTIONS}}` text, using the
user's wording and adding nothing they didn't say. Refer to sign-ins by their
names from answer 2. Put any extra sign-in details that aren't secret, such
as a company ID the form asks for, here too. For a set job:

```text
  Every night, reconcile yesterday's orders in billing against crm.

  1. Sign in to billing and export yesterday's orders as CSV.
  2. Sign in to crm and find the matching deal for each order by order
     number.
  3. Mark the deal "Paid" if the amounts match. List mismatches; don't fix
     them.

  Done means every order is checked. Hand back a summary with counts and a
  CSV of the mismatches. You may update deal status in crm without asking.
  Ask before changing anything in billing.
```

For a helper, describe the work instead of steps:

```text
  The team sends you tasks in the billing and crm web apps: pulling reports,
  looking up records, and updating fields. Confirm what a task needs before
  starting if it's ambiguous. You may update records in crm without asking.
  Ask before changing anything in billing.
```

If the user wants fewer screenshots than the default, add a line such as
"Attach screenshots only to questions." Indent every line by two spaces, as
above, so it nests inside the instruction. Show the text to the user and
adjust it until they're happy.

## 4. Store the sign-ins

For each sign-in, the `LOGIN_KEY` is its name in uppercase with dashes turned
into underscores (`crm` becomes `CRM`, `ops-portal` becomes `OPS_PORTAL`).
Recommend accounts made for the agent, with only the access the job needs,
since everything it does appears under that account.

1. Check for existing secrets named `browser-agent-<name>-username`,
   `-password`, and `-totp`:
   `npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name <secret-name> --json`.
   If they exist, ask whether to reuse them. To change a value, have the
   user update it on the **Secrets** page of the Omnara dashboard; the ID
   stays the same.
2. Ask the user to put the missing values in a `.env` file in the current
   directory and tell you when it's saved. Discourage pasting passwords into
   the chat. `TOTP` is the authenticator setup key, only for sites that use
   one. The file is read by the shell, so wrap each value in single quotes;
   otherwise spaces, `$`, or `#` in a value break it. Write a single quote
   inside a value as `'\''`.

   ```sh
   LOGIN_CRM_USERNAME='agent@acme.com'
   LOGIN_CRM_PASSWORD='...'
   LOGIN_CRM_TOTP='JBSW Y3DP EHPK 3PXP'
   ```

3. Create a secret for each value without printing it, and note each `id`
   (`sec_…`):

   ```sh
   set -a && . ./.env && set +a
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name browser-agent-crm-password --material-kind generic \
     --material-value "$LOGIN_CRM_PASSWORD" --json
   ```

## 5. Pick the model, machine pool, and machine size

1. `npx omnara grant models list --json` lists `model.provider_config` and
   `model.name` pairs. The agent looks at screenshots, so the model must
   accept images. Suggest `anthropic/claude-opus-5.5` on `omnara-openrouter`
   if it's granted, otherwise the strongest image-capable model listed.
2. `npx omnara grant pools list --json` lists the pools as
   `machine_pool.name`. Suggest `default-pool` if it's granted; its machines
   have the Node.js 24 the startup script needs. Otherwise use the only pool,
   or ask. A custom pool's image needs Node.js 24, npm, and root for
   installing Chrome's system packages. On a Daytona, Modal, or Tenki pool,
   delete the `sleep_after_ms` line; those providers don't support it.
3. Use `8192` MB for `MACHINE_MEMORY_MB`, or the largest
   `max_machine_memory_mb` the pool and its grant allow if lower (both are in
   the `grant pools list` output). Chrome needs memory, and on Blaxel the file
   system is in memory too. If the user needs more than the pool allows, they
   can raise its limits on the **Machines** page.

## 6. Build the config

1. Copy `agent.yaml` from this folder to `./browser-agent.yaml`. If this
   folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/browser-agent/agent.yaml
   instead.
2. Replace `{{TASK_INSTRUCTIONS}}` with the text from step 3.
3. Fill in the sign-ins. Each one is a `LOGIN_<KEY>_URL` line under
   `env_overlay` and its secrets under `secret_env_overlay`, plus
   `LOGIN_<KEY>_TOTP` if it has a setup key. With two sign-ins:

   ```yaml
   env_overlay:
     LOGIN_CRM_URL: "https://crm.acme.com/login"
     LOGIN_BILLING_URL: "https://billing.acme.com/signin"
     # AGENT_BROWSER_… lines unchanged
   secret_env_overlay:
     LOGIN_CRM_USERNAME: "sec_…"
     LOGIN_CRM_PASSWORD: "sec_…"
     LOGIN_CRM_TOTP: "sec_…"
     LOGIN_BILLING_USERNAME: "sec_…"
     LOGIN_BILLING_PASSWORD: "sec_…"
   ```

   With no sign-ins, delete the `LOGIN_` lines and the empty
   `secret_env_overlay:` key.
4. Leave the machine settings alone, except: if any site sends codes by text
   or email, set `sleep_after_ms: 0`, since a sign-in after waking would need
   a person. The machine is kept until the agent is archived
   (`delete_after_idle_minutes: 0`), so its browser and sign-ins carry over
   between tasks. It sleeps after two hours with no command running
   (`sleep_after_ms: 7200000`) and wakes in under a second with Chrome,
   memory, and files intact; a working agent runs commands every few
   seconds, so it never sleeps mid-task.
5. Fill in `{{MACHINE_MEMORY_MB}}`, `{{MODEL_PROVIDER_CONFIG}}`,
   `{{MODEL_NAME}}`, and `{{MACHINE_POOL}}` from step 5.
   `grep -n '{{' browser-agent.yaml` must print nothing.
6. Create or update the profile, named `browser-agent` unless the user wants
   another name. Check with `npx omnara profiles list --name browser-agent --json`:
   - None: `npx omnara profiles create --name browser-agent --file ./browser-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./browser-agent.yaml --json`

   If it rejects the reasoning effort, the model doesn't support `high`:
   remove the `reasoning` block and retry. Note the profile's `id` and
   `current_config_id`.

## 7. Test it

Launch a first task that changes nothing:

```sh
npx omnara agents launch --profile <agent-profile-id> --config <current-config-id> \
  --message "Don't start your job yet. Sign in to each of your saved sign-ins, and for each one tell me what's on the first page you land on, with a screenshot." --json
```

Give the user the link to watch:
`https://app.omnara.com/projects/<project-id>/agents/<agent-id>`. The first
reply takes a few minutes while the machine installs agent-browser and
Chrome. With no sign-ins, ask it to open a site from the job instead.

If a sign-in fails, read the agent's messages before changing anything. The
usual causes are a sign-in URL that redirects elsewhere (store the URL the
form actually lives on) or a site that blocks headless browsers from
datacenter IPs (see "If a site blocks the agent" in `README.md`).

Keep this agent if the user wants a schedule without Slack or Discord; step 9
uses it. Otherwise archive it in the console once every sign-in works.

## 8. Connect Slack or Discord (if chosen)

**Slack.** If `npx omnara integrations list` already shows an active
`slack_thread` integration for this agent, from an earlier run of this skill,
reuse it: skip to step 4, and if `npx omnara integrations get <integration-id>` doesn't list
the profile under `settings.launcher.profiles`, add it with
`npx omnara integrations profiles <integration-id> --profile-ids <agent-profile-id>`.
The flag replaces the offered profiles, so repeat it for each one already listed.

1. The user creates an app configuration token at
   [api.slack.com/apps](https://api.slack.com/apps) under **Your App
   Configuration Tokens** → **Generate Token**. It expires after about 12
   hours. Have the user add it to `.env` as `SLACK_APP_CONFIG_TOKEN=...`.
2. Create the integration with this profile in its launcher, so a mention
   starts this agent, and note the `itg_…` ID it returns:

   ```sh
   npx omnara integrations create --name browser-agent-slack \
     --integration-kind slack_thread \
     --settings '{"launcher":{"profiles":["<agent-profile-id>"]}}' --json
   ```

   The name is permanent and names the agent's Slack tools
   (`int__browser-agent-slack__post_message`).
3. Create the Slack app and connect it:

   ```sh
   set -a && . ./.env && set +a
   npx omnara integrations slack <integration-id> --app-name "Browser Agent" \
     --app-configuration-token "$SLACK_APP_CONFIG_TOKEN"
   ```

   Ask before choosing a different app name; it's how the team mentions the
   bot. It opens a Slack authorization page that the user approves before it
   expires.
4. Tell the user to invite the bot to a channel (`/invite @Browser Agent`). A
   private channel suits it, since its updates show data from the sites.

**Discord.** The bot answers in a new thread wherever it's mentioned in a
server channel; it doesn't answer direct messages. If
`npx omnara integrations list` already shows an active `discord_thread`
integration for this agent, reuse it the same way as for Slack: add the
profile with `npx omnara integrations profiles` if
`settings.launcher.profiles` doesn't list it, then skip to step 6.

1. The user creates an application named "Browser Agent" in the
   [Discord Developer Portal](https://discord.com/developers/applications);
   ask before choosing a different name, since it's how the team mentions
   the bot. They copy the **Application ID** and **Public Key** from
   **General Information**. Under **Bot**, they select **Reset Token** to copy
   the bot token, turn on **Message Content Intent**, and turn off **Public
   Bot** so only they can add it (if Discord refuses, first set
   **Installation** → **Install Link** to **None**). Have the user add the
   values to `.env`:

   ```sh
   DISCORD_APPLICATION_ID=...
   DISCORD_PUBLIC_KEY=...
   DISCORD_BOT_TOKEN=...
   ```

2. Store the bot token and note the `sec_…` ID:

   ```sh
   set -a && . ./.env && set +a
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name browser-agent-discord-bot-token --material-kind generic \
     --material-value "$DISCORD_BOT_TOKEN" --json
   ```

3. Create the integration with this profile in its launcher, and note the
   `itg_…` ID and `setup_revision` it returns:

   ```sh
   npx omnara integrations create --name browser-agent-discord \
     --integration-kind discord_thread \
     --settings '{"launcher":{"profiles":["<agent-profile-id>"]}}' --json
   ```

   The name is permanent and names the agent's Discord tools
   (`int__browser-agent-discord__post_message`).
4. Connect the bot. Omnara checks the token and finds the bot's user:

   ```sh
   npx omnara integrations configure <integration-id> \
     --expected-setup-revision <setup-revision> \
     --provider-tenant-id "$DISCORD_APPLICATION_ID" \
     --credential-secret-id <secret-id> \
     --provider-config "{\"public_key\":\"$DISCORD_PUBLIC_KEY\"}" --json
   ```

5. Under **General Information**, the user sets **Interactions Endpoint URL**
   to `https://app.omnara.com/api/integrations/discord/<application-id>/interactions`
   and saves; the buttons on the bot's questions need it. Discord checks the
   URL when it's saved, so this has to come after step 4.
6. The user adds the bot to their server by opening this link, which asks
   only for the permissions the bot needs:
   `https://discord.com/oauth2/authorize?client_id=<application-id>&scope=bot&permissions=309237746752&integration_type=0`.
   A private channel suits it, since its updates show data from the sites;
   the user adds the bot to it under the channel's **Permissions**.

Each mention in a new thread starts a new agent with its own machine, and
replies in the thread go to the same agent. Kept machines count toward the
pool's machine limit, so tell the user to archive a thread's agent in the
console when its work is done.

## 9. Schedule it (if chosen)

A schedule sends each run's message to one standing agent, so every run uses
the same machine and starts signed in:

- **With Slack or Discord:** have the user mention the bot once in the
  channel where the results should go, for example "@Browser Agent this
  thread is for your nightly runs; reply 'ready' and wait". Get that agent's
  ID from the conversation's URL in the console.
- **Without either:** use the agent from step 7.

Convert the time and timezone from answer 3 to a five-field cron expression:

```sh
npx omnara crons create --name browser-agent-nightly \
  --target-type agent --target-agent-id <agent-id> \
  --cron '0 22 * * *' --timezone America/Los_Angeles \
  --message-template 'Start your job for tonight.' --json
```

If `npx omnara crons list --name browser-agent-nightly --json` already shows a
trigger, use `npx omnara crons update <cron-trigger-id>` instead. If a run is
still going when the next fires, the message waits in the queue. Don't target
the profile, and don't use the Slack or Discord integration's **Add
schedule**: both start a new agent, with a new kept machine, every run.

## 10. Wrap up

Summarize what you created: the secrets, the profile, and the Slack or Discord
integration and schedule if any. Then tell the user:

- **Machines are kept** until their agent is archived, sleeping when idle and
  waking with the browser intact. Archive agents whose work is done.
- **To change anything,** ask a coding agent with this skill. It reuses the
  secrets and updates the profile. Running agents keep the config they
  started with, so for a scheduled agent, start a new standing agent, point
  the trigger at it with `npx omnara crons update`, and archive the old one.
- **To rotate a password,** update its secret on the **Secrets** page, then
  start a new agent the same way; running machines keep the old value.
- **To remove it,** delete the cron trigger (`npx omnara crons delete <id>`),
  delete the Slack or Discord integration
  (`npx omnara integrations delete <integration-id>`), or, if other agents
  share it, rerun `npx omnara integrations profiles` with only their
  profiles. Then run `npx omnara profiles delete <agent-profile-id>` and
  `npx omnara secrets delete <secret-id>` for each secret.
  With Discord, also delete the application in the Discord Developer Portal.
