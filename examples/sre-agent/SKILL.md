---
name: sre-agent
description: Deploy or update the SRE Agent on Omnara, a production investigator that diagnoses issues across AWS and source code and reports the likely cause with evidence. Use when the user asks to deploy, set up, update, or remove the SRE agent.
---

# Deploy the SRE Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: an agent profile the user has watched run once, reachable
wherever they want it (their own app, Slack, or the Omnara console), and
optionally running a daily health check.

The steps below are the usual path and have the exact commands. Skip anything
that's already done (for example, the secrets and profile exist and the user
only wants to add code access), follow the user's lead if they want a different
order, and narrate briefly as you go.

- Start from `agent.yaml` as written. If the user wants something different
  (the instruction, data sources, model, or how they reach the agent), change
  it and tell them what you changed.
- If a command fails, read the error and fix it; `npx omnara <command> --help`
  and [docs.omnara.com](https://docs.omnara.com) have the details. If you're
  stuck, show the user the error.

The commands use the Omnara CLI (`npx omnara`) because its login handles auth
in one step; add `--json` when you need to read IDs from the output. They're a
reference, not a requirement: the Omnara MCP tools, the
[REST API](https://docs.omnara.com/api-reference/openapi.yaml), or the SDK work
too, and the flags map directly to API fields. What matters is the result:
secrets for the data sources, a profile from the filled-in `agent.yaml`, and
whatever the user picks in steps 8 and 9. Slack setup is simplest with the
CLI.

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

## 2. Describe the system

Ask the user about the production system the agent will investigate:

- `COMPANY_NAME`: the company or product name, for example `Acme`.
- What it is and its main services, and how they run on AWS (ECS, Lambda,
  EKS, EC2), plus where logs live. Write this as `SYSTEM_CONTEXT`: two to four
  sentences on a single line, for example
  `Acme is a checkout API for online stores. It runs an API and a worker on ECS Fargate behind an ALB, with RDS Postgres and SQS, primarily on AWS. Logs go to CloudWatch Logs under /ecs/acme-*.`
  If the user has AWS CLI access and wants you to, you can draft it from
  `aws ecs list-services`, `aws lambda list-functions`, and similar
  read-only calls.
- `AWS_REGION`: the region production runs in, for example `us-west-2`.
- Whether they want the agent to read their code (step 4). It's optional and
  makes answers sharper.

Show `SYSTEM_CONTEXT` to the user and adjust until they're happy. If they
can say more about their logs or metrics (for example, every request writes
one structured log line with its errors and database queries), add a short
section for each under `# Available Data` in step 6, like the `## AWS` one.

## 3. Create read-only AWS credentials

The agent reaches AWS through the
[AWS MCP Server](https://docs.aws.amazon.com/aws-mcp/latest/userguide/what-is-mcp-server.html),
which signs each call with these credentials, so their IAM policy is what
keeps the agent read-only. Use a dedicated IAM user with only the AWS managed
policies `ViewOnlyAccess` and `CloudWatchReadOnlyAccess`. They cover
describing resources, metrics, alarms, and reading logs, but not S3 object
contents or secret values.

Skip this step if the secret already exists:
`npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name sre-agent-aws --json`.
If it does, ask whether to reuse it and note its `id`.

1. Create the IAM user, either way:
   - **You do it**, if the user has the AWS CLI signed in to the production
     account with IAM permissions and says go ahead. This writes the keys
     straight to `.env` without showing them:

     ```sh
     aws iam create-user --user-name omnara-sre-agent
     aws iam attach-user-policy --user-name omnara-sre-agent \
       --policy-arn arn:aws:iam::aws:policy/job-function/ViewOnlyAccess
     aws iam attach-user-policy --user-name omnara-sre-agent \
       --policy-arn arn:aws:iam::aws:policy/CloudWatchReadOnlyAccess
     aws iam create-access-key --user-name omnara-sre-agent \
       --query 'AccessKey.[AccessKeyId,SecretAccessKey]' --output text \
       | awk '{print "AGENT_AWS_ACCESS_KEY_ID=" $1 "\nAGENT_AWS_SECRET_ACCESS_KEY=" $2}' >> .env
     ```

   - **The user does it** in the IAM console: **Users** → **Create user** →
     **Attach policies directly** (`ViewOnlyAccess`,
     `CloudWatchReadOnlyAccess`), then on the user's **Security credentials**
     tab, **Create access key**. Ask them to add the keys to a `.env` file in
     the current directory and tell you when it's saved (pasting them in the
     chat also works):

     ```sh
     AGENT_AWS_ACCESS_KEY_ID=...
     AGENT_AWS_SECRET_ACCESS_KEY=...
     ```

2. Create the secret:

   ```sh
   set -a && . ./.env && set +a
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name sre-agent-aws --material-kind aws_credentials \
     --material-access-key-id "$AGENT_AWS_ACCESS_KEY_ID" \
     --material-secret-access-key "$AGENT_AWS_SECRET_ACCESS_KEY" --json
   ```

   Note the `id` (`sec_…`) as `AWS_SECRET_ID`. If the user prefers a role,
   add `--material-role-arn` (and `--material-external-id` if the role's
   trust policy requires one); Omnara assumes it with these keys.

## 4. Let the agent read the code (optional)

Recommended: with the repository cloned on a machine, the agent can trace an
error to the code behind it and cite the exact lines. Ask which repository
(`GITHUB_REPO`, as `owner/name`). One repository is supported as written.

- **Public repository:** nothing to store.
- **Private repository:** the user creates a
  [fine-grained token](https://github.com/settings/personal-access-tokens/new)
  with read-only **Contents** access to that repository and adds it to `.env`
  as `AGENT_GITHUB_TOKEN=...`. Check for an existing secret named
  `sre-agent-github` as in step 3, otherwise create it:

  ```sh
  set -a && . ./.env && set +a
  npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
    --name sre-agent-github --material-kind generic \
    --material-value "$AGENT_GITHUB_TOKEN" --json
  ```

  Note the `id` as `GITHUB_TOKEN_SECRET_ID`.

## 5. Pick the model (and machine pool)

1. `npx omnara grant models list --json`: each item has
   `model.provider_config` and `model.name`. Ask the user which model to use.
   Suggest `openai/gpt-6-astra` on `omnara-openrouter` if it's granted (the
   instruction was tested with it); otherwise suggest the strongest reasoning
   model on the list. Name a couple of alternatives rather than the whole
   list, and show everything if they ask. The choice gives
   `MODEL_PROVIDER_CONFIG` and `MODEL_NAME`.
2. Only if the agent reads the code (step 4):
   `npx omnara grant pools list --json`; each grant's `machine_pool.name` is a
   candidate `MACHINE_POOL`. Use the only one, or ask if there are several. If
   there are none, tell the user the project needs a machine pool for code
   access, and continue without it if they prefer.

## 6. Create the agent

1. Copy `agent.yaml` from this folder to `./sre-agent.yaml` in the current
   directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/sre-agent/agent.yaml
   instead.
2. Remove what the user isn't using:
   - No code access: delete the `## Source Code` section of the instruction,
     `machine_sources`, and the `tools:` block at the end.
   - Public repository: delete the `secret_env_overlay` block.
3. Replace every remaining placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{COMPANY_NAME}}` | from step 2 |
   | `{{SYSTEM_CONTEXT}}` | from step 2 |
   | `{{AWS_REGION}}` | from step 2 |
   | `{{AWS_SECRET_ID}}` | the `sec_…` ID from step 3 |
   | `{{GITHUB_REPO}}` | from step 4 |
   | `{{GITHUB_TOKEN_SECRET_ID}}` | from step 4 |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 5 |
   | `{{MODEL_NAME}}` | from step 5 |
   | `{{MACHINE_POOL}}` | from step 5 |

   Then confirm nothing is left: `grep -n '{{' sre-agent.yaml` must print
   nothing.
4. Check for an existing profile: `npx omnara profiles list --name sre-agent --json`.
   - None: `npx omnara profiles create --name sre-agent --file ./sre-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./sre-agent.yaml --json`

   Note the profile's `id` and `current_config_id` from the output.

## 7. Run a first health check

```sh
npx omnara agents launch --profile <agent-profile-id> --config <current-config-id> \
  --message "Run a production health check." --json
```

Give the user the link to watch it work:
`https://app.omnara.com/projects/<project-id>/agents/<agent-id>`. It takes a
few minutes, longer on the first run while the machine clones the repository.
The user can reply in the console with a real question, such as a recent
alert or "why was checkout slow yesterday at 3pm?". If the agent reports an
AWS call as denied, that's the read-only policy working; only widen it if the
user wants that data.

## 8. Choose where to use it

Ask where the user wants to talk to the agent, offering these in this order.
Any combination is fine.

**Their own app, existing or new (recommended).** Put the agent where the
user's team already works: inside their existing product or internal tool, or, if there's no
natural home, a small UI built for it. Both are the same integration: launch
agents from the profile and stream their replies with the TypeScript SDK
(`@omnara/sdk`) or the REST API; see [docs.omnara.com](https://docs.omnara.com).
Anything else that can call the API can start the agent the same way; for
example, their alerting webhook can start an investigation with the alert text
as the message. The app needs an org API key: the user creates one on the
dashboard's **API Tokens** page (**Organization** tab → **New token**), then
grants it a role on this project from the key's detail panel. Help them wire
it in, or build the UI with them.

**Slack.** The bot answers wherever it's mentioned, and thread replies become
instructions to the agent. An incidents or alerts channel works well.

1. Open the project's **Integrations** page. Create a **Slack bot** integration
   with a descriptive name, or reuse the integration already created for this
   example. In its launch settings, select this agent profile. Preserve other
   selected profiles and settings when updating an existing integration.
2. Connect the Slack app from that page, using an app configuration token from
   [Slack's app settings](https://api.slack.com/apps), and complete OAuth. For CLI
   setup of that saved integration, put `SLACK_APP_CONFIG_TOKEN=...` in `.env` and run:

   ```sh
   set -a && . ./.env && set +a
   npx omnara integrations slack <integration-id> --app-name "SRE Agent" \
     --app-configuration-token "$SLACK_APP_CONFIG_TOKEN"
   ```

   Use the integration's `itg_…` ID, not the profile ID. The user approves the
   returned OAuth URL before it expires. Setup reconnects that integration;
   selecting the profile in its launcher enables new Slack conversations.
3. Tell the user to invite the bot to a channel (`/invite @SRE Agent`) and
   mention it: "@SRE Agent why are checkout requests failing?"

**Omnara console.** Already done: every agent launched from the profile shows
up in the console.

## 9. Run a daily health check (optional)

A schedule works with any of the above. Ask whether the user wants a daily
health check, and for the time and timezone (default: weekdays at 9am in the
user's timezone). Set `--cron` and `--timezone` from their answer:

```sh
npx omnara crons create --name sre-agent-daily \
  --target-type profile --target-agent-profile-id <agent-profile-id> \
  --cron '0 9 * * 1-5' --timezone America/Los_Angeles \
  --message-template 'Run the daily production health check.' --json
```

If `npx omnara crons list --name sre-agent-daily --json` already shows a
trigger, change it with `npx omnara crons update <cron-trigger-id>` (same
flags) instead of creating a second one.

Each firing launches a fresh agent from the profile. It shows up in the
console, and the user's app can pick it up through the SDK or API. To get
each check in a Slack channel, mention the bot in that channel once, then
create the trigger with `--target-type agent --target-agent-id <agent-id>`
(the agent ID from that conversation's console URL), so every check posts to
that thread.

## 10. Wrap up

Summarize what you created: the secrets, the IAM user if you made it, the
profile, and the Slack app or cron trigger if any. Remind the user to delete
`.env` or move the keys somewhere safe. Then tell them:

- **Add a data source or change anything else:** ask a coding agent with this
  skill. It reuses the secrets and updates the profile in place.
- **Remove it:** delete the cron trigger (`npx omnara crons delete <id>`),
  remove this profile from the integration's launcher on the project's Integrations page, then
  `npx omnara profiles delete <agent-profile-id>`, delete each secret with
  `npx omnara secrets delete <secret-id>`, and delete the
  `omnara-sre-agent` IAM user's access key (or the user) in AWS.
