# Omnara CLI

Command-line interface for [Omnara](https://omnara.com): launch and manage agents, organizations, integrations, and MCP servers.

## Installation

```sh
npx omnara
```

## Usage

```sh
omnara login
omnara --help
```

Requires Node.js 22 or later.

## Documentation

See the [Omnara docs](https://docs.omnara.com) and the [GitHub repository](https://github.com/omnara-ai/omnara).

## License

Apache-2.0

## Project apps and connections

Connections hold provider identity, configuration, and credentials. Project apps select
capabilities and launch profiles. Disconnecting a connection affects every app and
agent using it; removing an app keeps the connection and existing agents.

With an organization and project selected, Slack setup supports either automatic app
creation or an existing Slack app. Reconnecting preserves saved app settings.

```sh
omnara profiles slack "$PROFILE_ID" --client-id "$SLACK_CLIENT_ID" \
  --client-secret "$SLACK_CLIENT_SECRET" --signing-secret "$SLACK_SIGNING_SECRET"
omnara profiles slack --help
```

For GitHub or Discord, first create a project credential (`secrets create`) and a
connection (`connections create`). GitHub's tenant is its numeric App ID and account
is the Installation ID; the credential's App ID must match. Discord's tenant is the
Application ID and account is the bot User ID, not the guild ID. Its provider config
accepts `public_key` and `shard_count` (1–4096, default 1).

```sh
omnara connections create --help
omnara connections list
omnara profiles github "$PROFILE_ID" --name review --connection "$CONNECTION_ID" \
  --repository-id "$REPOSITORY_ID" --tools github_read --tools github_discussion_comment
omnara profiles discord "$PROFILE_ID" --name support --connection "$CONNECTION_ID" \
  --channel-id "$CHANNEL_ID" --tools discord_read --tools discord_post_message
omnara apps list
omnara apps get "$APP_ID"
```

GitHub uses a numeric repository ID. Discord can enable questions and approvals with
`--interactions` once its public key and interaction endpoint are configured. Both
flows receive subsequent conversation events by default; use `--no-listen` to opt out.
`apps update` replaces saved settings; already compiled agent configs retain their
settings. Use `apps update --help` for the complete request shape.
