# Omnara

The control plane for agents.

Take your agents from prototype to production — one open-source API to deploy,
run, and control them. Self-host it, or use our managed cloud.

## Deploy agents, not infrastructure

Define an agent once — in YAML or the console — with its model, tools, policies,
and machines. Launch it from the API, console, or Slack. Omnara owns the agent
loop and its durable state, so agents can run for minutes or days, survive
crashes and disconnects, pause for a human, and resume exactly where they left
off.

A production agent needs more than a prompt and a model call. It needs reliable
hosting, persistent history, secure access to models, machines, and tools, and a
way for people to stay in control. Omnara provides those pieces as one system.

- **Agents outlive workers** — every turn, tool call, approval, checkpoint, and
  artifact is durable state. Workers can restart without restarting the agent.
- **Machines are tools** — let agents use managed pool machines or connect your
  own through an outbound daemon. The agent is not bound to one machine's
  lifecycle.
- **Models stay replaceable** — configure providers centrally and grant each
  project only the models it should use, without coupling agent definitions to
  one vendor.
- **Humans stay in control** — scope access by organization and project, encrypt
  secrets, require approval for risky tools, and retain an append-only history
  of agent activity.

## Architecture

The repository contains a TypeScript web console, four long-running Go service
entrypoints, and one migration entrypoint:

- `frontend/` contains the web console.
- `cmd/api` serves the HTTP API.
- `cmd/worker` claims ready agent work, calls models, and executes tools.
- `cmd/maintenance` performs global background maintenance.
- `cmd/daemon` runs agent processes on connected machines.
- `cmd/migrate` applies database migrations as a one-shot process.

Postgres is the source of truth. Valkey/Redis provides best-effort wakeups and
cancellation signals, while S3-compatible object storage holds artifacts.
Model and machine-provider adapters sit behind first-party contracts, so the
durable agent state does not depend on a single vendor.

See the [architecture documentation](https://docs.omnara.com/self-hosting/architecture)
for the full request and execution flow.

## Quickstart

### Requirements

- Docker with Compose

### Run published images

```sh
git clone https://github.com/omnara-ai/omnara.git
cd omnara
docker compose -f compose.yaml --profile app up -d
```

### Build from source

```sh
git clone https://github.com/omnara-ai/omnara.git
cd omnara
docker compose --profile app up -d --build
```

Open [http://localhost:8000](http://localhost:8000). Local authentication email
is written to `docker compose logs api`, so you can complete signup without
configuring an email provider.

Add a model-provider credential in the console, create an agent profile, and
launch an agent. The [hosted quickstart](https://docs.omnara.com/quickstart)
walks through the same product flow in more detail.

Local development uses intentionally insecure defaults. Do not use those
defaults in a deployed environment. Follow the
[self-hosting guide](https://docs.omnara.com/self-hosting/deployment) and
[configuration reference](https://docs.omnara.com/self-hosting/configuration)
for production setup.

## API

The public API is defined in [`api/openapi/openapi.yaml`](api/openapi/openapi.yaml)
and served from `/api/v1`. Use it to manage organizations, projects, agent
profiles, agents, inputs, events, interactions, models, machines, pools, skills,
and secrets.

- [API overview](https://docs.omnara.com/api/overview)
- [Published documentation](https://docs.omnara.com)

Generated Go and TypeScript API code is committed and checked for drift in CI.
Change the OpenAPI document first, then regenerate the affected clients and
server boundaries.

## Development

Source development requires the Go version declared in [`go.mod`](go.mod),
Node.js 22 or newer with Corepack, and Docker with Compose.

Run the fast repository gate:

```sh
make verify
```

Run database-backed integration tests:

```sh
make test-integration
```

Run deterministic service end-to-end tests:

```sh
make test-service-e2e
```

Provider-backed live tests are available through the `make test-live-*` targets
and require the corresponding credentials.

See [CONTRIBUTING.md](CONTRIBUTING.md) for generated-code workflows and pull
request expectations.

## Community

- Use [GitHub Issues](https://github.com/omnara-ai/omnara/issues) for bugs and
  feature requests.
- Join the [Omnara Discord](https://discord.gg/Dc46sYk6e3) for questions and
  discussion.
- Report vulnerabilities privately by following [SECURITY.md](SECURITY.md).

## License

Omnara is licensed under the [Apache License 2.0](LICENSE).
