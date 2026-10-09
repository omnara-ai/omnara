---
name: email-agent
description: Deploy or update the Email Agent on Omnara, an agent with its own email address that people can email, Cc, or @mention, built on Primitive's hosted email functions and MCP server. Use when the user asks to deploy, set up, update, customize, or remove the email agent.
---

# Deploy the Email Agent

You are setting up an Omnara agent with its own email address, from the
files in this folder. The goal: the user emails the agent, watches it reply,
and knows how to change who can wake it.

There are two parts. `agent.yaml` is the agent: it reads and sends mail
through [Primitive](https://www.primitive.dev)'s hosted MCP server.
`function/handler.js` is a small Primitive Function, hosted by Primitive,
that runs on every inbound email and decides which agent to wake. Each
email thread becomes its own Omnara conversation. Neither part needs a
server or a machine.

The steps below are the usual path and have the exact commands. Skip anything
that's already done, follow the user's lead if they want a different order,
and narrate briefly as you go.

- Start from the files as written. The defaults are deliberately open: anyone
  can email the agent, and it replies without asking for approval. If the user
  wants something different, step 9 lists the settings; change them and tell
  the user what you changed.
- If a command fails, read the error and fix it; `npx omnara <command> --help`,
  `npx primitive@latest <command> --help`, [docs.omnara.com](https://docs.omnara.com),
  and [docs.primitive.dev](https://docs.primitive.dev) have the details. If
  you're stuck, show the user the error.

The commands use the Omnara CLI (`npx omnara`) and the Primitive CLI
(`npx primitive@latest`); add `--json` when you need to read IDs from the
output. They're a reference: the REST APIs work too.

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

Remember the org and project IDs; later steps need them.

## 2. Set up Primitive and pick the address

[Primitive](https://www.primitive.dev) hosts the agent's inbox and the
function. Everything after this step uses one Primitive API key, kept in a
`.env` file in the current directory as `PRIMITIVE_API_KEY`. If `.env`
already has one, skip to item 4.

1. Ask whether the user already has a Primitive account. Most people don't,
   and you can create one for them in about a minute.
2. **No account:** create one.
   1. Primitive's [Terms of Service](https://www.primitive.dev/terms) and
      [Privacy Policy](https://www.primitive.dev/privacy) must be accepted
      to create an account. Link them and ask the user whether they accept.
      Don't continue without a yes.
   2. Create the account. The API key is shown only once, so this saves it
      straight to `.env` and prints only the mail domain:

      ```sh
      npx primitive@latest agent create --terms-accepted \
        --device-name "Omnara email agent" --json \
        | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const a=JSON.parse(s);if(!a.api_key){console.error(s);process.exit(1)}require("fs").appendFileSync(".env",`PRIMITIVE_API_KEY=${a.api_key}\n`,{mode:0o600});console.log(a.address)})'
      ```

   3. The new account can only reply to mail and can't run functions until
      an email is confirmed. Ask the user which email to confirm; it must
      not already belong to a Primitive account. Then send the code:

      ```sh
      set -a && . ./.env && set +a
      npx primitive@latest agent claim --email <their-email>
      ```

   4. Ask the user for the 6-digit code from that email, then:
      `npx primitive@latest agent claim-verify --verification-code <code>`.
      If they'd rather not share the code, `npx primitive@latest agent claim-link`
      prints a link they can open to confirm in the browser instead.
      If the claim reports `email_in_use`, that email already has a
      Primitive account: switch to the existing-account path below.
3. **Existing account:** ask the user to create an API key in the Primitive
   dashboard under **Settings → API keys** and add it to `.env` (pasting it
   in the chat also works):

   ```sh
   PRIMITIVE_API_KEY=prim_...
   ```

4. Check the account: `set -a && . ./.env && set +a && npx primitive@latest whoami --json`.
   `entitlements` must include `functions_enabled`. If it doesn't, the email
   claim above hasn't finished. `managed_inbox_address` is the account's mail
   domain, for example `brave-crow.primitive.email` (a domain, not a full
   address). If the user has verified their own domain in Primitive, they can
   use that instead.
5. Ask what the agent should be called. The name is the part before the `@`
   and is what people type to @mention it. Suggest `assistant`. The agent's
   address is `<name>@<domain>`, for example `assistant@brave-crow.primitive.email`.

## 3. Store the Primitive key

The agent reads and sends mail through Primitive's MCP server, using the
Primitive API key from step 2. Store it as an Omnara secret.

1. Check for an existing secret:
   `npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name email-agent-primitive-key --json`.
   If one exists, ask whether to reuse it. To reuse it, note its `id` and skip
   to step 4.
2. Create the secret:

   ```sh
   set -a && . ./.env && set +a
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name email-agent-primitive-key --material-kind generic \
     --material-value "$PRIMITIVE_API_KEY" --json
   ```

   Note the `id` (`sec_…`) from the output.

## 4. Pick the model

`npx omnara grant models list --json`: each item has `model.provider_config`
and `model.name`. Ask the user which model to use. Suggest a strong
general-purpose model with reliable tool calling, such as
`openai/gpt-6.1-sol` on `omnara-openrouter` if it's granted. Name a couple of
alternatives rather than the whole list. The choice gives
`MODEL_PROVIDER_CONFIG` and `MODEL_NAME`.

## 5. Create the agent profile

1. Copy `agent.yaml` from this folder to `./email-agent.yaml` in the current
   directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/email-agent/agent.yaml
   instead.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{AGENT_ADDRESS}}` | the address from step 2 |
   | `{{PRIMITIVE_KEY_SECRET_ID}}` | the `sec_…` ID from step 3 |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 4 |
   | `{{MODEL_NAME}}` | from step 4 |

   Then confirm nothing is left: `grep -n '{{' email-agent.yaml` must print
   nothing.
3. Check for an existing profile: `npx omnara profiles list --name email-agent --json`.
   - None: `npx omnara profiles create --name email-agent --file ./email-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./email-agent.yaml --json`

   Note the profile's `id` (`aprf_…`).

## 6. Create an Omnara key for the function

The function starts conversations and delivers each email through the Omnara
API, so it needs an org API key. Keys can only be created in the dashboard:

1. Ask the user to open the Omnara dashboard with **All projects** selected,
   go to **API Tokens** → **Organization** tab → **New token**, name it
   `email-agent`, and choose the `member` org role.
2. In the new key's detail panel, grant it the `operator` role on this
   project. That lets it run agents and send them messages, but not change
   profiles or secrets.
3. Have the user add the token to `.env`:

   ```sh
   OMNARA_API_KEY=omnara_org_v1_...
   ```

## 7. Deploy the function

The function is one self-contained file with no dependencies, so there's
nothing to install or build.

1. Copy `function/handler.js` from this folder to
   `./email-agent-handler.js`. If this folder isn't available locally,
   download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/email-agent/function/handler.js
   instead.
2. Check whether it's already deployed:
   `npx primitive@latest functions list --json`, looking for the name
   `omnara-email-agent`.
   - Not deployed: deploy it with its settings. `AGENTS` maps the agent's
     name to its profile:

     ```sh
     set -a && . ./.env && set +a
     export OMNARA_ORG_ID=<org-id> OMNARA_PROJECT_ID=<project-id>
     export AGENTS='{"<agent-name>":"<agent-profile-id>"}'
     npx primitive@latest functions deploy --name omnara-email-agent \
       --file email-agent-handler.js \
       --secret-from-env OMNARA_API_KEY --secret-from-env OMNARA_ORG_ID \
       --secret-from-env OMNARA_PROJECT_ID --secret-from-env AGENTS --wait
     ```

   - Already deployed: note its `id` and run
     `npx primitive@latest functions redeploy --id <function-id> --file email-agent-handler.js`.
     To change a setting, use
     `npx primitive@latest functions set-secret --id <function-id> --key <KEY> --value '<value>' --redeploy`.

   Note the function `id` from the output.
3. Route inbound mail to it:
   `npx primitive@latest functions route-set --id <function-id> --fallback`.
   The fallback route receives mail for every domain in the account that has
   no function of its own. Mail that doesn't address an agent is ignored and
   stays in the Primitive inbox. If the command reports a conflict, another
   function already holds the route: ask the user before re-running it with
   `--takeover`.

## 8. Try it

1. Ask the user to email the agent's address from their own email account
   with a question, for example "What's the population of Lisbon?".
2. A conversation named `<agent-name>: <subject>` appears in the console
   within a few seconds:
   `https://app.omnara.com/projects/<project-id>/agents`. The mail calls show
   up as `mcp__primitive__…` tools, and the reply lands in the user's inbox.
   Replying to it continues the same conversation.
3. Then show off tagging: have the user email someone else with the agent in
   Cc and `@<agent-name>` in the body. The agent wakes because it's mentioned
   and replies to the thread. A Cc without the mention doesn't wake it.

If nothing arrives, check the function's logs with
`npx primitive@latest functions logs --id <function-id>`. Each email logs
which agents it woke or why it skipped.

Primitive's sending rules affect replies. Out of the box, an account can
send to its own domains and to people who have emailed it first. Replying to
the sender always works. A reply-all that includes someone who has never
written to the agent is refused, so the agent replies to the sender only and
says who it couldn't copy. Emailing new people on its own, when the operator
asks it to, needs wider sending enabled on the Primitive account.

## 9. Customize (optional)

Tell the user the defaults and that each one is a single change:

| Setting | Default | Change it with |
| --- | --- | --- |
| Who can wake the agent | Anyone | Function setting `ALLOWED_SENDERS`: comma-separated addresses or `@domain` entries. Senders must also pass DMARC as that domain, so a forged From is rejected. |
| When a Cc wakes it | Only when the new text mentions `@<agent-name>` or the full address | Function setting `CC_MODE`: `addressed`, `always`, or `never`. To and Bcc always wake it. |
| Automated mail | Skipped (Auto-Submitted, mailing lists, bulk, no-reply senders, bounces) | Function setting `SKIP_AUTOMATED`: `false` to deliver it anyway |
| Spam | Skipped at SpamAssassin score 5 or above | Function setting `MAX_SPAM_SCORE` |
| Approval before sending | None | In `email-agent.yaml`, set `permission.mode: always_ask` on `sendEmail` or `replyToEmail`, then `npx omnara profiles update` |
| What the agent does | Answers questions, with web search | Edit the instruction's "Your role" section, then update the profile |

Function settings change with `functions set-secret ... --redeploy` (step 7).

**More agents.** Each agent is its own profile with its own address. For each
one, make a copy of `agent.yaml` with that address and role, create a profile
from it, and add it to `AGENTS`, for example
`{"assistant":"aprf_…","sales":"aprf_…","support":"aprf_…"}`. People can
then email or @mention any of them, and several can be on one thread. A
`"*"` entry is a catch-all: mail to any other address on the domain goes to
that profile, and the agent takes on the address it was sent to.

## 10. Wrap up

Summarize what you created: the secret, the profile, the Omnara org key, the
Primitive function, and the agent's address. Then tell the user:

- **Change anything:** ask a coding agent with this skill. It reuses the
  secret, updates the profile in place, and redeploys the function.
- **Remove it:** `npx primitive@latest functions route-unset --id <function-id>`
  and `npx primitive@latest functions delete --id <function-id>`, then
  `npx omnara profiles delete <agent-profile-id>` and
  `npx omnara secrets delete <secret-id>`. Revoke the Omnara org key on the
  **API Tokens** page and the Primitive key in Primitive's settings.
