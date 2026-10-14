# Contributing to Omnara

Thanks for helping improve Omnara. Bug reports, documentation fixes, feature
ideas, and code contributions are welcome.

Follow the README [self-host steps](README.md#self-host) for local setup. Report
vulnerabilities through [SECURITY.md](SECURITY.md), not a public issue.

## Pull requests

- Open an issue before starting a substantial change so we can agree on the
  direction.
- Keep each pull request focused on one problem.
- Use a conventional title accepted by
  [the PR title configuration](.github/pr-title-config.json), such as
  `fix: handle expired sessions` or `feat(api): add agent filtering`.
- Run the relevant checks described in [Development](#development).

## Development

Source development requires the Go version declared in [`go.mod`](go.mod),
Node.js 24 or newer with Corepack, ripgrep (`rg` on `PATH`), and Docker with
Compose.

`make run-worker` builds the sandbox launcher. File search and scripted
edits require a Linux worker; see the
[worker prerequisites](https://docs.omnara.com/self-hosting/configuration#memory-storage).

Run the fast repository gate:

```sh
make verify
```

To include React Doctor against the current branch changes, run
`make web-check-all`.

Run database-backed integration tests:

```sh
make test-integration
```

Run deterministic service end-to-end tests:

```sh
make test-service-e2e
```

Provider-backed live tests are available through the `make test-live-*` targets
and require the corresponding credentials. CI runs them on every push to `main`;
to run them on a pull request, ask a maintainer to add the `live-tests` label.
Pull requests from forks do not receive the provider credentials.

## Third-party integrations

We welcome sandbox providers, model providers, and integrations with services
such as Slack, Discord, or GitHub.

### Before you build one

- **Model providers.** Omnara already works with any model behind an OpenAI
  Responses, OpenAI Chat Completions, or Anthropic Messages compatible
  endpoint; users set the base URL, endpoint path, and authentication (see
  [Model providers](https://docs.omnara.com/organization/model-providers)).
  Code changes are only needed for an API format or preset that does not fit.
- **Integrations.** Your service can connect through the public API as a
  [custom integration](https://docs.omnara.com/integrations/custom-integrations),
  with nothing to merge here.
- **Not sure it fits?** Open an issue to ask first, or go straight to a pull
  request.

### Opening the pull request

- Follow the structure of existing integrations of the same kind, such as
  `internal/machinepool/providers/<name>/` for sandbox providers or
  `internal/integration/<name>/` for integrations. Sandbox providers include a
  `Test<Name>ProviderLiveSmoke` test in `make test-live-sandbox-providers`.
- Enable "Allow edits by maintainers".
- Share test credentials or a test account with the maintainers privately,
  never in the pull request, so live tests can run.

### How we review and merge

Maintainers handle the last mile. We either comment that the pull request is
not close to what we can merge, or finish it ourselves on your branch: tests,
docs pages, and fixes. Maintainers decide whether to merge. When we do, you
remain the commit author, and maintainers who contributed are added as
co-authors. After merging, we maintain the integration, list it in the docs
and the README, and announce it in the changelog.

### What we ask of you

Publish an Omnara setup page in your own docs or guides, linking to the
matching Omnara docs page, and keep it current.

### Keeping integrations up to date

We keep integrations working as long as the service is available and we can
test it. If an integration stops working, test access runs out, or the setup
page on your side moves or comes down, we will reach out before changing
anything, and we announce any deprecation in the changelog.

## Generated files

Do not edit generated files by hand. After changing the OpenAPI contract, run:

```sh
make openapi-generate
make docs-openapi
make web-generate
```

After changing SQL queries, run:

```sh
make sqlc-generate
```

Commit the resulting generated changes with the source change.

## Licensing

By submitting a contribution, you agree that it may be distributed under the
[Apache License 2.0](LICENSE).
