---
name: browser-agent
description: Build and deploy a Browser Agent on Omnara, a long-running agent that signs in to web apps with stored credentials and works in a real browser, overnight if needed. Interviews the user about the job, sign-ins, schedule, and Slack or Discord first. Use when the user asks to build, deploy, set up, update, or remove a browser agent.
---

# Build the Browser Agent

You are building an Omnara agent with the user, starting from `agent.yaml` in
this folder. The result: an agent profile that does the user's work in their
web apps, has signed in to each account once while the user watched, and is
reachable where they want it, on a schedule if they want one.

Interview first, then build. Don't create anything in Omnara until the user has
confirmed your summary in step 2. When a step says to ask the user, ask in
plain language, then end your turn and wait for their reply. Where a step gives
a default, use it without asking and say in a line what you picked, so the user
can change it.

`agent.yaml` is a guide, not a form. Its browser, sign-in, and machine settings
were tested together: the agent-browser commands, the vault and two-factor
helper in the startup script, the `AGENT_BROWSER_*` variables, and the settings
that keep the machine alive. Keep them unless you have a reason, and read the
file's comments before editing. Fit everything else to the job: add instruction
sections, an MCP server for an API the user already has, or anything else the
job needs, and tell the user what you changed. If a command fails,
`npx omnara <command> --help` and [docs.omnara.com](https://docs.omnara.com)
have the details.

The commands use the Omnara CLI (`npx omnara`); add `--json` when you need to
read IDs. The Omnara MCP tools, the
[REST API](https://docs.omnara.com/api-reference/openapi.yaml), and the SDK
work too, and the flags map directly to API fields.

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
there's one of each, otherwise the defaults from `npx omnara config` or the
project named `Default`; ask only if there's still no clear choice. Save the
choice with `npx omnara config --org <org-id> --project <project-id>`; later
steps need the project ID.

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
4. **Where they follow it.** Suggest Slack or Discord, whichever the team uses:
   the team mentions the bot to start a task, and updates, screenshots,
   questions, and results arrive in the thread. The Omnara console always works
   too. Ask whether they want a screenshot with every update (the default), only
   with questions, or only with the result.
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
   Reuse any that exist. To change a value, have the user update it on the
   **Secrets** page of the Omnara dashboard; the ID stays the same.
2. Ask the user to put the missing values in a `.env` file in the current
   directory rather than pasting passwords into the chat. `TOTP` is the
   authenticator setup key, only for sites that use one. The file is read by the
   shell, so wrap each value in single quotes; otherwise spaces, `$`, or `#` in
   a value break it. Write a single quote inside a value as `'\''`.

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
   accept images. Use `anthropic/claude-opus-5.5` on `omnara-openrouter` if
   it's granted, otherwise the strongest image-capable model listed.
2. `npx omnara grant pools list --json` lists the pools as
   `machine_pool.name`. Use `default-pool` if it's granted; its machines
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

Set it up with [`integrations.md`](../integrations.md) (read it from
https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/integrations.md
if you don't have the repo). For this agent, name the integrations
`browser-agent-slack` and `browser-agent-discord`, and call the bot "Browser
Agent". A private channel suits it, since its updates show data from the sites.

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

Summarize what you created: the secrets, the profile, and any integrations or
schedule. Remind the user to delete `.env` or move what's in it somewhere safe.
Then tell them:

- **Machines are kept** until their agent is archived, sleeping when idle and
  waking with the browser intact. Archive agents whose work is done.
- **To change anything,** ask a coding agent with this skill. It reuses the
  secrets and updates the profile. Running agents keep the config they
  started with, so for a scheduled agent, start a new standing agent, point
  the trigger at it with `npx omnara crons update`, and archive the old one.
- **To rotate a password,** update its secret on the **Secrets** page, then
  start a new agent the same way; running machines keep the old value.
- **To remove it,** delete the cron trigger (`npx omnara crons delete <id>`),
  remove its integration (see "Removing" in `integrations.md`),
  then run `npx omnara profiles delete <agent-profile-id>` and
  `npx omnara secrets delete <secret-id>` for each secret.
