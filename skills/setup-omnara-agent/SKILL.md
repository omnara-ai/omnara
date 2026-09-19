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
   - Select ordinary built-in tools from GET /tool-catalog, excluding create_machine and delete_machine. Do not add provider tools or interaction-destination tools through this blanket selection.
   - For app capabilities, follow https://docs.omnara.com/integrations/apps and the provider's setup guide. Config `app_resources` contribute their selected tools automatically; do not duplicate them under top-level `tools` merely to enable them. Provider tools require a connection and concrete scope. Top-level tool entries may override permission or enablement, but do not grant provider access.
   - An enabled `interaction_handler` implicitly adds `list_interaction_destinations` and `set_interaction_destination`, subject to explicit tool policy. A handler, ordinary listener, send tool and reply-follow policy are independently selected; none implies the others. Do not add the destination tools manually just because the user chose Slack or another provider.
   - The granted model and machine pool
   - Relevant secrets or startup scripts for the machine pool env override. For example, if the user wants to clone a Github repository, you may setup a script which clones the repo upon starting the machine. If needed, you can pipe a Github PAT via a secret into the env var overlay

3. Launch an agent from the profile using `npx omnara agents create` with a simple first message. Provide the user with a link to the agent at https://app.omnara.com/projects/{project_id}/agents/{agent_id}

4. (optional: interact via Slack) If the user wants to setup Omnara to interact via Slack, guide them through the setup process to connect their agent profile to a slack bot. Tell them how to fetch their app configuration token at the following URL: https://api.slack.com/apps . Then tell them to run `npx omnara profiles slack {agent_profile_id} ...` (use `-h` to fetch the help parameters you need) to execute the authentication flow.

   Successful Slack setup creates a connection and project app with a mention launcher. Its newly launched agents receive scoped Slack tools, a message listener and the Slack interaction handler; the profile's base config is unchanged. It does not retroactively attach the earlier dashboard-launched agent to Slack. For GitHub or Discord, use the current provider guide and respect its documented setup/availability limits.

5. (optional: interact via SDK or REST API) Help the user setup their own custom application to interact with Omnara. Omnara can be used via a Typescript SDK, install the `@omnara/sdk` package to use it. Alternatively, in non-Typescript environments, Omnara can be used directly via the API, see the openapi specification at https://docs.omnara.com/api-reference/openapi.yaml. In the case of a frontend application, these APIs may need to be proxied through the user's API in order to avoid CORS errors. Help the user generate an organization-level API token to interact with the API through the CLI or API.

   If you're building your own integration using Omnara's API, follow https://docs.omnara.com/integrations/custom-integrations. Give its API key the required project role. Your service decides which agent receives each message and sends ordinary inputs with actor attribution and an Idempotency-Key; no Omnara app or connection registration is needed. Custom tools use the ordinary tool-call/result lifecycle. Your service also chooses where to display questions and approvals, then submits the answers through the API. The dashboard can answer those same interactions.
