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
   b. How will you interact with the agent (Omnara web console, first-party slack integration, custom frontend UI using the typescript SDK)

2. Look up the user's org, its default project, and the granted model and
   machine pool (`orgs_list`, `projects_list`, `grant_models_list`, `grant_pools_list` /
   the equivalent `npx omnara` commands). Tailor the instruction to the user's request.
   Create a reusable profile of the agent with `profiles_create` / `npx omnara profiles create`.
   - Attach every built-in tool except create_machine and delete_machine. List them with `tools_catalog` / GET /tool-catalog. Include send_integration_message and set_integration_target only if using the first party Slack integration; omit both if not
   - The granted model and machine pool
   - Relevant secrets or startup scripts for the machine pool env override. For example, if the user wants to clone a Github repository, you may setup a script which clones the repo upon starting the machine. If needed, you can pipe a Github PAT via a secret into the env var overlay

3. Launch an agent from the profile with `agents_launch` / `npx omnara agents launch` and a simple first message. Provide the user with a link to the agent at https://app.omnara.com/projects/{project_id}/agents/{agent_id}

4. (optional: interact via Slack) If the user wants to setup Omnara to interact via Slack, guide them through the setup process to connect their agent profile to a slack bot. Tell them how to fetch their app configuration token at the following URL: https://api.slack.com/apps . Then tell them to run `npx omnara profiles slack {agent_profile_id} ...` (use `-h` to fetch the help parameters you need) to execute the authentication flow. The MCP server has no Slack setup tool, so this step always uses the CLI; authenticate the CLI as described above first.

5. (optional: interact via SDK or REST API) Help the user setup their own custom application to interact with Omnara. Omnara can be used via a Typescript SDK, install the `@omnara/sdk` package to use it. Alternatively, in non-Typescript environments, Omnara can be used directly via the API, see the openapi specification at https://docs.omnara.com/api-reference/openapi.yaml. In the case of a frontend application, these APIs may need to be proxied through the user's API in order to avoid CORS errors. Help the user generate an organization-level API token to interact with the API through the CLI or API.
