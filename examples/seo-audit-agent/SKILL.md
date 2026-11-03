---
name: seo-audit-agent
description: Deploy or update the SEO Audit Agent on Omnara, an agent that audits a web page for SEO, answer engines, and AI search citations and delivers a PDF report. Use when the user asks to deploy, set up, update, or remove the SEO audit agent.
---

# Deploy the SEO Audit Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: an agent profile the user has watched audit one page and
deliver its report, reachable wherever the user wants it.

The steps below are the usual path, with the exact commands. Skip anything
that's already done (for example, the profile exists and the user only
wants an add-on), and follow the user's lead if they want a different
order.

- Start from `agent.yaml` as written. If the user wants something different
  (the checks, the report, the model, or how they reach the agent), change
  it and tell them what you changed.
- If a command fails, `npx omnara <command> --help` and
  [docs.omnara.com](https://docs.omnara.com) have the details.
- Stop only where a step says to ask the user. Ask in a sentence or two of
  plain language, together with the next step's question if one comes right
  after, then end your turn and wait for the reply.
- Where a step gives a default, use it without asking and say in a line what
  you picked, so the user can change it.
- When a step needs a key, the user can paste it in the chat or add it to a
  `.env` file in the current directory, whichever they prefer; offer both. If
  they paste it, write it to `.env` yourself so the commands work as written.

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
there's one of each, otherwise the defaults from `npx omnara config` or the
project named `Default`; ask only if there's still no clear choice. Save the
choice with `npx omnara config --org <org-id> --project <project-id>`; later
steps need the project ID.

## 2. Choose the first audit and any add-ons

Ask the user these in one message, with the defaults, so they can answer
"defaults" or change only what they care about. Wait for the reply.

1. **A page to audit first**, and optionally the search query it should
   win. Default: the homepage of the domain in the user's email address
   (from `npx omnara whoami --json`), with the query worked out from the
   page. If that's a personal address, such as gmail.com, there's no
   default; ask for a URL.
2. **Add-ons.** Every check works without keys. Default: none. Two keys add
   measured data:
   - **Google API key** (free): real-user speed data (Core Web Vitals) from
     the Chrome UX Report, instead of lab numbers alone.
   - **OpenAI API key** (OpenAI bills each search and its tokens): asks the
     buyer questions through OpenAI's web search, the closest measurable
     stand-in for ChatGPT, and records which sites it cites, instead of a
     simulated check.

## 3. Store the add-on keys (if chosen)

For each add-on, reuse a secret that already exists under its name
(`npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name <secret-name> --json`);
if the value changed, the user updates it on the dashboard's **Secrets** page
(the ID stays the same). Otherwise get the key from the user, then create the
secret and note its `id` (`sec_…`).

- **Google:** in the [Google Cloud console](https://console.cloud.google.com),
  the user enables the **Chrome UX Report API** (**APIs & Services** →
  **Library**), then creates a key under **APIs & Services** →
  **Credentials** → **Create credentials** → **API key**, ideally restricted
  to that API. They give it to you as `GOOGLE_API_KEY`.

  ```sh
  set -a && . ./.env && set +a
  npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
    --name seo-audit-agent-google-api-key --material-kind generic \
    --material-value "$GOOGLE_API_KEY" --json
  ```

- **OpenAI:** a key from
  [platform.openai.com/api-keys](https://platform.openai.com/api-keys), given
  to you as `OPENAI_API_KEY`. Create the secret the same way, named
  `seo-audit-agent-openai-api-key`.

## 4. Pick the model and machine pool

1. `npx omnara grant models list --json`: each item has `model.provider_config`
   and `model.name`. Use `openai/gpt-6.1-sol` on `omnara-openrouter` if it's
   granted (`agent.yaml` runs it at high reasoning effort), otherwise the
   strongest model on the list. The choice gives `MODEL_PROVIDER_CONFIG` and
   `MODEL_NAME`.
2. `npx omnara grant pools list --json`; each grant's `machine_pool.name` is a
   candidate `MACHINE_POOL`. Use `default-pool` if it's granted: its machines
   come with Node.js, Python, and Playwright's headless Chromium, which the
   startup script builds on. Otherwise use the only pool, or ask if there are
   several. If there are none, tell the user the project needs a machine
   pool; the agent can't render pages without one. On a custom pool, the image
   needs root, Node.js 22.19 or later with npm, `jq`, Python 3.11 or later
   with Playwright and with pip allowed to install packages system-wide
   (`break-system-packages = true` in `/etc/pip.conf`), and Playwright's
   Chromium headless shell
   (`playwright install --with-deps --only-shell chromium`).

## 5. Create the agent

1. Copy `agent.yaml` from this folder to `./seo-audit-agent.yaml` in the
   current directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/seo-audit-agent/agent.yaml
   instead.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 4 |
   | `{{MODEL_NAME}}` | from step 4 |
   | `{{MACHINE_POOL}}` | from step 4 |

   Then confirm nothing is left: `grep -n '{{' seo-audit-agent.yaml` must
   print nothing.
3. For each add-on from step 3, add its secret under the machine source, next
   to `env_overlay`:

   ```yaml
   secret_env_overlay:
     GOOGLE_API_KEY: "sec_…"
     OPENAI_API_KEY: "sec_…"
   ```

4. Check for an existing profile: `npx omnara profiles list --name seo-audit-agent --json`.
   - None: `npx omnara profiles create --name seo-audit-agent --file ./seo-audit-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./seo-audit-agent.yaml --json`

   If it rejects the reasoning effort, the chosen model doesn't support
   `high`: remove the `reasoning` block and retry. Note the profile `id`.

## 6. Run a first audit

```sh
npx omnara agents launch --profile <agent-profile-id> \
  --message "Audit https://acme.com/pricing for the query 'invoice software for small business'." --json
```

Use the page and query from step 2; leave out the query if the user didn't
give one. Give the user the link to watch it work:
`https://app.omnara.com/projects/<project-id>/agents/<agent-id>`. An audit
takes about 10 minutes: the machine starts and installs Lighthouse,
then the agent fetches and renders the page, runs Lighthouse, searches the
buyer questions, and writes the report. The PDF and markdown copy arrive as
files in the conversation, where the user can ask follow-up questions or
for changes to the report.

## 7. Choose where to use it

Right after sharing the link, ask where the user wants to talk to the agent.
Wait for the reply; don't assume the console. Any combination works:

- **Their own app (recommended):** inside their agency's tools or client
  portal, or a small UI built for it. Anything that can call Omnara's API,
  like a form or a CRM workflow, can start an audit with the URL as the
  message.
- **Slack or Discord:** the team mentions the bot with a URL, and the report
  arrives in the thread, where replies go to the same agent.
- **Only the Omnara console:** nothing to set up; every agent launched from the
  profile shows up there.

Set up whichever the user picks with [`integrations.md`](../integrations.md)
(read it from
https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/integrations.md
if you don't have the repo). For this agent, name the integrations
`seo-audit-agent-slack` and `seo-audit-agent-discord`, call the bot "SEO Audit
Agent", and have the user try it with "@SEO Audit Agent audit
https://acme.com/pricing".

## 8. Wrap up

Summarize what you created: any secrets, the profile, and any integrations.
If there's a `.env`, remind the user to delete it or move what's in it
somewhere safe. Then tell them:

- **Change the checks, the report, or anything else:** ask a coding agent
  with this skill. It reuses the secrets and updates the profile in place,
  and the next audit uses it.
- **Remove it:** remove its integrations, if any (see "Removing" in
  `integrations.md`), then run `npx omnara profiles delete <agent-profile-id>`
  and `npx omnara secrets delete <secret-id>` for each secret.
