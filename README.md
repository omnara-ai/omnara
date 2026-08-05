# Omnara

**The API for production-grade agents.**

Omnara is an open source platform for building and running agents. It handles
execution and state while you choose the models, tools, machines, and how users
interact with each agent.

[Omnara Cloud](https://app.omnara.com) ·
[Documentation](https://docs.omnara.com/introduction) ·
[API](https://docs.omnara.com/api/overview) ·
[Discord](https://discord.gg/Dc46sYk6e3)

## Who it is for

- **Developers.** Build agents for internal use or customer-facing products with
  the API. Your application controls who can use each agent, what input it
  receives, and how its output reaches users. Omnara handles execution and state
  in between.
- **Teams.** Interact with agents directly through Omnara's dashboard or
  first-party Slack connector.

## Features

- **Durable agents.** Agent state is committed atomically to Postgres. Agents
  recover automatically from crashes, restarts, and temporary machine
  disconnects.
- **Machine access.** Use sandboxes from Blaxel, Daytona, or Unikraft (more
  coming soon), or connect your own laptop or VM. An agent can run with no
  machines or use several at once. These can be sandboxes, your own machines,
  or both. You can add or remove machines while the agent is running.
- **Models.** Bring your own API keys and use any model exposed through a
  compatible endpoint, including OpenRouter, LiteLLM, and Ollama. Omnara
  supports OpenAI Responses, OpenAI Chat Completions, and Anthropic Messages,
  with more API formats coming soon.
- **Tools.** Use built-in tools, custom tools, skills, and HTTP MCP servers.
- **RBAC.** Assign organization and project roles to users and API keys.
  Separate who can manage access, configure agents, operate them, or only view
  them.
- **Cloud or self-hosted.** Use Omnara Cloud or run it yourself under Apache 2.0.
  - **Queryable state.** Self-hosted deployments can query agent history
    directly in Postgres for analytics, evals, prompt analysis, and training
    datasets.

## Get started

### Omnara Cloud

Sign up at [app.omnara.com](https://app.omnara.com), add a model provider,
define an agent profile, and launch it from the console, Slack, or API.

See the [quickstart](https://docs.omnara.com/quickstart) for the complete flow.

### Self-host

#### Requirements

- Docker with Compose

#### Run published images

```sh
git clone https://github.com/omnara-ai/omnara.git
cd omnara
docker compose -f compose.yaml --profile app up -d
```

#### Build from source

```sh
git clone https://github.com/omnara-ai/omnara.git
cd omnara
docker compose --profile app up -d --build
```

Open [http://localhost:8000](http://localhost:8000). Local authentication email
is written to `docker compose logs api`, so you can complete signup without
configuring an email provider.

Add a model provider credential in the console, create an agent profile, and
launch an agent.

Local development uses intentionally insecure defaults. Do not use those
defaults in a deployed environment. Follow the
[self-hosting guide](https://docs.omnara.com/self-hosting/deployment) and
[configuration reference](https://docs.omnara.com/self-hosting/configuration)
for production setup.

## API

The API is defined in [`api/openapi/openapi.yaml`](api/openapi/openapi.yaml) and
served under `/api/v1`. See the
[API overview](https://docs.omnara.com/api/overview).

## Development

Source development requires the Go version declared in [`go.mod`](go.mod),
Node.js 22 or newer with Corepack, and Docker with Compose.

Run the fast repository gate.

```sh
make verify
```

Run database-backed integration tests.

```sh
make test-integration
```

Run deterministic service end-to-end tests.

```sh
make test-service-e2e
```

Provider-backed live tests are available through the `make test-live-*` targets
and require the corresponding credentials.

See [CONTRIBUTING.md](CONTRIBUTING.md) for generated-code workflows and pull
request expectations.

## Community

[GitHub Issues](https://github.com/omnara-ai/omnara/issues) ·
[Discord](https://discord.gg/Dc46sYk6e3) ·
[Security](SECURITY.md)

## License

[Apache License 2.0](LICENSE)
