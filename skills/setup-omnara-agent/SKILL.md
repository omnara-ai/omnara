---
name: setup-omnara-agent
description: Install and set up Omnara. Use when the user asks to install Omnara or run /setup-omnara-agent.
---

Read https://docs.omnara.com/llms.txt and the Quickstart guide first. Fetch other
documentation pages as you need them.

## Choose an interface

Check whether the Omnara MCP server is attached to this session: look for its tools,
such as `whoami`, `profiles_create`, and `agents_launch`.

- **MCP server attached:** use the Omnara MCP tools for every step they cover. Do not
  install or log in to the CLI for those steps.
- **No MCP server:** use the `npx omnara` CLI for every step.

Steps below name both forms as MCP tool / CLI command.

## Authenticate

- **MCP:** call `whoami`. If it fails with an authorization error, ask the user to
  complete the Omnara browser authorization for the MCP server in their client, then
  call `whoami` again. Do not continue setup until it succeeds.
- **CLI:** run `npx omnara whoami`. If not logged in, run `npx omnara login` in an
  interactive terminal session without piping output; add `--no-browser` on headless
  or remote machines. By default, login opens a browser for the user to sign in and
  approve access. Whenever login starts, immediately share the approval link and code
  and keep the command running while the user approves. Do not continue setup until
  login completes and `npx omnara whoami` succeeds.

## Set up the agent

Then, ask the user questions on what type of agent they'd like to create:

1. Ask two questions before creating anything.
   a. What should the agent do? Examples include a coding agent, deep research agent, etc.
   b. How will you interact with the agent (Omnara web console, Slack, GitHub, Discord, or your own app using the public API/TypeScript SDK)?

2. Look up the user's org, its default project, and the granted model and
   machine pool (`orgs_list`, `projects_list`, `grant_models_list`, `grant_pools_list` /
   the equivalent `npx omnara` commands). Tailor the instruction to the user's request.
   Create a reusable profile with `profiles_create` / `npx omnara profiles create`.
   - Select ordinary built-in tools from `tools_catalog` / GET /tool-catalog, excluding create_machine and delete_machine. Do not include integration tools through blanket selection; they require a saved integration.
   - For integration capabilities, follow https://docs.omnara.com/integrations/overview and the provider's setup guide. Tools use `int__<integration-name>__<tool-name>` under `tools`; handlers use `interaction_handlers: {integration-name: {}}`. Integration launchers add the integration's missing tools to a derived config, preserving existing profile entries; Slack and Discord launchers also add their interaction handler with `list_interaction_handlers` and `set_interaction_handler`. Slack and Discord tools use the thread assigned at launch; GitHub tools use the assigned PR. Adding tools alone does not assign a conversation.
   - For manual configs with an interaction handler, include `list_interaction_handlers` and `set_interaction_handler` under `tools`. These tools are not added to every config. The dashboard remains available for questions and approvals.
   - Incoming subscriptions belong to the integration, independently of config. Do not add a `listeners` block. Removing a tool does not stop forwarding; remove the specific subscription through the integration Conversations page or subscription API.
   - Validate source against the [source JSON Schema](https://github.com/omnara-ai/omnara/blob/main/internal/agentconfig/generated/agent_config.schema.json). The [OpenAPI contract](https://docs.omnara.com/api-reference/openapi.yaml) defines matching API/SDK schemas.
   - Existing native Slack deployments need the [coordinated maintenance cutover](https://docs.omnara.com/self-hosting/composable-integrations-cutover); profile edits alone do not migrate them.
   - The granted model and machine pool
   - Relevant secrets or startup scripts for the machine pool env override. For example, if the user wants to clone a Github repository, you may setup a script which clones the repo upon starting the machine. If needed, you can pipe a Github PAT via a secret into the env var overlay

3. Launch an agent from the profile with `agents_launch` / `npx omnara agents launch` and a simple first message. Provide the user with a link to the agent at https://app.omnara.com/projects/{project_id}/agents/{agent_id}

4. (optional: interact via Slack) Create a project integration with `integration_type: slack_thread` through `npx omnara integrations create`, and select the profile in its launcher with `npx omnara integrations profiles`. Then connect the saved integration with `npx omnara integrations slack {integration_id} ...`. Use `-h` for current arguments and follow the Slack guide for creating a Slack registration with a configuration token from https://api.slack.com/apps or connecting an existing registration. The MCP server has no integration setup tools, so authenticate the CLI as described above before this step.

   Newly launched agents receive the integration's thread tools, interaction handler and a subscription for replies; the profile's base config is unchanged. This does not attach the earlier dashboard-launched agent to Slack. To forward messages to an existing agent, use the integration subscription API. A normal API launch can supply `subscriptions` separately from config; subscriptions alone do not assign sending context. For GitHub or Discord, use the current provider guide.

5. (optional: interact via SDK or REST API) Help the user setup their own custom application to interact with Omnara. Omnara can be used via a Typescript SDK, install the `@omnara/sdk` package to use it. Alternatively, in non-Typescript environments, Omnara can be used directly via the API, see the openapi specification at https://docs.omnara.com/api-reference/openapi.yaml. In the case of a frontend application, these APIs may need to be proxied through the user's API in order to avoid CORS errors. Help the user generate an organization-level API token to interact with the API through the CLI or API.

   If you're building your own integration using Omnara's API, follow https://docs.omnara.com/integrations/custom-integrations. Give its API key the required project role. Your service decides which agent receives each message and sends ordinary inputs with actor attribution and an Idempotency-Key; no Omnara integration registration is needed. Custom tools use the ordinary tool-call/result lifecycle. Your service also chooses where to display questions and approvals, then submits the answers through the API. The dashboard can answer those same interactions.
