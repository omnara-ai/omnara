---
name: setup-omnara-agent
description: Install and set up Omnara. Use when the user asks to install Omnara or run /setup-omnara-agent.
---

# Set up an Omnara agent

Get the user a launched Omnara agent and its link. The commands below are
complete; read other docs only if one fails or the user wants Slack, Discord,
GitHub, or their own app.

If the Omnara MCP server is attached (tools such as `whoami`,
`profiles_create`, and `agents_launch`), use the Omnara MCP tools for every
step they cover. Do not install or log in to the CLI for those steps. Call
`whoami`; if it fails with an authorization error, ask the user to complete the
Omnara browser authorization for the MCP server in their client, then call
`whoami` again. Do not continue setup until it succeeds.

## 1. Sign in

```sh
npx omnara whoami
```

If it says you're not logged in, start `npx omnara login` without piping its
output, in the background if you can, and keep it running while the user
approves. It opens the approval page in the user's browser and prints a link
and a code. Before you wait for it, send the user a message with both, since
their browser may be on another machine, and ask them to approve once the code
matches. Do not continue setup until login completes and `npx omnara whoami`
succeeds.

## 2. Ask what to build

Ask two questions before creating anything:

1. What should the agent do? Examples include a coding agent, deep research
   agent, etc.
2. How will you interact with the agent (Omnara web console, Slack, GitHub,
   Discord, or your own app using the public API/TypeScript SDK)?

## 3. Pick the project, model, and pool

Look up the user's org and its default project
(`npx omnara projects list --org <org-id> --json` lists projects). Then:

```sh
npx omnara config --org <org-id> --project <project-id> &&
npx omnara grant models list --json | node -e 'for (const g of JSON.parse(require("fs").readFileSync(0)).data) console.log(g.model.provider_config, g.model.name)' &&
npx omnara grant pools list --json | node -e 'for (const g of JSON.parse(require("fs").readFileSync(0)).data) console.log(g.machine_pool.name)'
```

Use a listed model, and `default-pool` if it's listed, otherwise the first
pool.

## 4. Create the agent and launch it

Fill in an instruction for what the user asked for, a short profile name, and
a simple first message. The machine tools come with `machine_sources`; don't
list them, or other built-in tools the user doesn't need.

Add relevant secrets or startup scripts for the machine pool env override. For
example, if the user wants to clone a GitHub repository, you may set up a script
(`machine_provider_options_overlay.startup_script` on the machine source) which
clones the repo upon starting the machine. If needed, you can pipe a GitHub PAT
via a secret into the env var overlay.

Then create the profile and launch the agent:

```sh
cat > omnara-agent.yaml <<'EOF'
version: v1
instruction: |
  <what the agent does, for whom, and how it reports back>
model:
  provider_config: <provider>
  name: <model>
tools:
  web_search: {}
  web_fetch: {}
  ask_question: {}
machine_sources:
  - machine_pool_name: <pool>
EOF
npx omnara profiles create --name <profile-name> --file omnara-agent.yaml --json > omnara-profile.json &&
npx omnara agents launch --profile "$(node -p 'require("./omnara-profile.json").id')" --message "<first message>" --json | node -e 'const r=JSON.parse(require("fs").readFileSync(0)); console.log(r.agent.id)'
```

Give the user the link:
`https://app.omnara.com/projects/<project-id>/agents/<agent-id>`.

## 5. Other places to talk to it

Only if the user chose them:

- **Slack, Discord, or GitHub:** follow
  https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/integrations.md
  with this profile in the integration's launcher. Agents it launches use the
  profile; the agent above stays in the web console. The MCP server has no
  integration tools, so sign in to the CLI (step 1) first.
- **Their own app:** help the user set it up with the TypeScript SDK
  (`npm i @omnara/sdk`) or the REST API
  (https://docs.omnara.com/api-reference/openapi.yaml), and an org API key
  from **API Tokens** in the dashboard that has the required project role.
  Browser apps must call the API through their own backend; it doesn't allow
  cross-origin requests. To route their users' messages to agents, follow
  https://docs.omnara.com/integrations/custom-integrations.md.
