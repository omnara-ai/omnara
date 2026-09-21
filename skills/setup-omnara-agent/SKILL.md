---
name: setup-omnara-agent
description: Install and set up Omnara. Use when the user asks to install Omnara or run /setup-omnara-agent.
---

Read https://docs.omnara.com/llms.txt and the Quickstart guide first. Fetch other
documentation pages as you need them.

Before setting up the agent, check authentication with `npx omnara whoami`. If not
logged in, run `npx omnara login` in an interactive terminal session without piping
output; add `--no-browser` on headless or remote machines. By default, login opens a
browser for the user to sign in and approve access. Whenever login starts,
immediately share the approval link and code and keep the command running while the
user approves. Do not continue setup until login completes and `npx omnara whoami`
succeeds.

Then, ask the user questions on what type of agent they'd like to create:

1. Ask two questions before creating anything.
   a. What should the agent do? Examples include a coding agent, deep research agent, etc.
   b. How will you interact with the agent (Omnara web console, Slack, GitHub, Discord, or your own app using the public API/TypeScript SDK)?

2. Look up the user's org, its default project, and the granted model and
   machine pool via the API. Tailor the instruction to the user's request. Use `npx omnara profiles create` to create a reusable profile of the agent.
   - Select ordinary built-in tools from GET /tool-catalog, excluding create_machine and delete_machine. Do not add app tools through this blanket selection; they require a saved app.
   - For app capabilities, follow https://docs.omnara.com/integrations/apps and the provider's setup guide. Create a project app with its own credentials. Select its tools under `tools` using `app__<app-name>__<tool-name>`, with permissions, enabled state and deferral as needed. Select handlers as `interaction_handlers: {app-name: {}}`. Catalog tools and handlers expose static `input_schema`. Provider/scheduled launches establish immutable app-agent context: omitted destination fields inherit it and supplied fields must stay within it. Slack channel/DM and Discord server channel contexts permit child threads; thread and GitHub contexts remain exact. Unbound tools require explicit complete destinations. Incoming subscriptions belong to the app, not the agent config; do not add a `listeners` block. App launchers add missing tools and handlers to a derived config and attach the conversation atomically with launch; existing profile entries win unchanged.
   - `list_interaction_handlers` and `set_interaction_handler` are implicit tools for tool-capable models. A handler, subscription and sending tool are independent. Selecting a handler requires complete destination `args` and is not restricted by sending context. Accepted provider input selects its full origin automatically, including on launch. Subscriptions and followed replies do not create sending context. Removing a sending tool keeps subscriptions; stop forwarding by deleting the specific subscription ID through the app Conversations page or subscription API. This keeps the agent and its sending tools. The dashboard remains available for questions and approvals.
   - Validate source against the [source JSON Schema](https://github.com/omnara-ai/omnara/blob/main/internal/agentconfig/generated/agent_config.schema.json). The [OpenAPI contract](https://docs.omnara.com/api-reference/openapi.yaml) defines matching API/SDK schemas.
   - For an existing native Slack deployment, follow the [coordinated maintenance cutover](https://docs.omnara.com/self-hosting/composable-apps-cutover); do not treat profile edits as a migration. It preserves exact old sending scopes as app-agent context and does not add subscriptions or handlers to old conversations.
   - The granted model and machine pool
   - Relevant secrets or startup scripts for the machine pool env override. For example, if the user wants to clone a Github repository, you may setup a script which clones the repo upon starting the machine. If needed, you can pipe a Github PAT via a secret into the env var overlay

3. Launch an agent from the profile using `npx omnara agents create` with a simple first message. Provide the user with a link to the agent at https://app.omnara.com/projects/{project_id}/agents/{agent_id}

4. (optional: interact via Slack) If the user wants to setup Omnara to interact via Slack, create a project app with `app_type: slack_thread` through `npx omnara apps create`, and select the profile in its launcher. Use `-h` for current command arguments. Then connect that saved app with `npx omnara apps slack {app_id} ...`. Follow the Slack guide for either creating a Slack registration with a configuration token from https://api.slack.com/apps or connecting an existing registration.

   Successful Slack setup connects the saved project app to the bot. Its newly launched agents receive scoped Slack tools and the Slack interaction handler, plus an app-owned subscription for thread replies; the profile's base config is unchanged. It does not retroactively attach the earlier dashboard-launched agent to Slack. To attach an existing agent, use the app subscription API with its agent ID, subscription type, concrete conversation and optional event selection. For an ordinary API launch, supply `subscriptions` separately from config; subscription-only attachments preserve the config. For GitHub or Discord, use the current provider guide and respect its documented setup/availability limits.

5. (optional: interact via SDK or REST API) Help the user setup their own custom application to interact with Omnara. Omnara can be used via a Typescript SDK, install the `@omnara/sdk` package to use it. Alternatively, in non-Typescript environments, Omnara can be used directly via the API, see the openapi specification at https://docs.omnara.com/api-reference/openapi.yaml. In the case of a frontend application, these APIs may need to be proxied through the user's API in order to avoid CORS errors. Help the user generate an organization-level API token to interact with the API through the CLI or API.

   If you're building your own integration using Omnara's API, follow https://docs.omnara.com/integrations/custom-integrations. Give its API key the required project role. Your service decides which agent receives each message and sends ordinary inputs with actor attribution and an Idempotency-Key; no Omnara app registration is needed. Custom tools use the ordinary tool-call/result lifecycle. Your service also chooses where to display questions and approvals, then submits the answers through the API. The dashboard can answer those same interactions.
