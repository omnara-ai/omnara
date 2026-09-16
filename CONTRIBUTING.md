# Contributing to Omnara

Thanks for helping improve Omnara. Bug reports, documentation fixes, feature
ideas, and code contributions are welcome.

Follow the README [Quickstart](README.md#quickstart) for local setup. Report
vulnerabilities through [SECURITY.md](SECURITY.md), not a public issue.

## Pull requests

- Open an issue before starting a substantial change so we can agree on the
  direction.
- Keep each pull request focused on one problem.
- Use a conventional title accepted by
  [the PR title configuration](.github/pr-title-config.json), such as
  `fix: handle expired sessions` or `feat(api): add agent filtering`.
- Run the relevant checks described in the README's
  [Development section](README.md#development).

The HTTP integration suite also runs the TypeScript provider code against local
PostgreSQL, the Go API, and fake Slack, Discord and GitHub APIs, using Node from
`.node-version`. Run these journeys with `make test-channel-journey` after
`make db-up`. They need no provider credentials and exercise durable receipt,
agent creation, duplicate callbacks, thread continuation and PR conversations,
including sending through a migrated Slack DM.

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
