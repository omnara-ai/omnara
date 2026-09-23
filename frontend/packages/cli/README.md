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

## Project apps

Each project app owns its provider identity, credentials, and optional launcher.
Create a disconnected draft, connect its account, then choose launch settings.
Names are immutable: 1–32 letters, numbers, or hyphens, starting with a letter.

With an organization and project selected:

```sh
omnara apps definitions
omnara apps create --body '{"name":"engineering","app_type":"slack_thread","settings":{}}'
omnara apps list
```

Use the returned app ID as `APP_ID`; keep it as `SLACK_APP_ID` for the launcher
example below. Connect Slack through browser authorization using an existing
Slack app:

```sh
omnara apps slack "$APP_ID" --client-id "$SLACK_CLIENT_ID" \
  --client-secret "$SLACK_CLIENT_SECRET" --signing-secret "$SLACK_SIGNING_SECRET"
omnara apps slack --help
omnara apps get "$APP_ID"
```

`apps slack` also supports automatic Slack app creation with `--app-name` and
`--app-configuration-token`. Reconnecting preserves the saved launcher settings.

For GitHub or Discord, create a project credential with `secrets create`, or reuse
an existing credential of the matching provider kind. Read `setup_revision` from
`apps get` before configuring the app. The server verifies the credentials and
rejects a stale revision.

```sh
omnara apps create --body '{"name":"reviewer","app_type":"github_pr","settings":{}}'
# Set APP_ID to this new app's ID and SETUP_REVISION to its current setup_revision.
omnara apps configure "$APP_ID" --expected-setup-revision "$SETUP_REVISION" \
  --provider-tenant-id "$GITHUB_APP_ID" --provider-account-ref "$GITHUB_INSTALLATION_ID" \
  --credential-secret-id "$SECRET_ID"
omnara apps configure --help
```

GitHub uses its numeric App ID and Installation ID; the credential's App ID must
match. Discord uses its Application ID and discovers the bot User ID from its token. Configure Discord's
interaction public key with `--provider-config '{"public_key":"YOUR_64_HEX_PUBLIC_KEY"}'`
before using a launcher or interaction handler. Discord launchers include a handler by default. Set its
Interactions Endpoint URL to
`https://YOUR_OMNARA_HOST/api/integrations/discord/APPLICATION_ID/interactions`.
Omnara manages one Gateway connection per Discord app.

`apps update` replaces launcher settings and requires the unchanged app name and
`app_type`. For the Slack app above, replace the example workspace and profile
IDs with your own:

```sh
omnara apps update "$SLACK_APP_ID" --body '{
  "name": "engineering",
  "app_type": "slack_thread",
  "settings": {
    "launcher": {
      "trigger": "mention",
      "scope_kind": "workspace",
      "scope_ref": "T123",
      "slots": [{"key": "default", "agent_profile_id": "aprf_aaaaaaaaaaaaaaaaaaaaaaaaaa"}]
    }
  }
}'
omnara apps profiles "$SLACK_APP_ID" --profile-ids "$PROFILE_ID" --profile-ids "$SECOND_PROFILE_ID"
```

Slack workspace scopes must match the connected workspace. GitHub launchers can use
`repository` with a numeric repository ID and `mention` or `pull_request_opened`.
Discord launchers use `mention` with `scope_kind` and `scope_ref` omitted;
they have no configured server or channel filter.
`apps profiles` edits offered Slack or Discord profiles while preserving existing
agent slots. One profile launches immediately; several offer a selection menu.

Select tools and interaction handlers independently in agent configurations:
tools use keys such as `app__engineering__post_message`, and interaction handlers
use `engineering: {}`. Tool entries accept permissions, enabled state and deferral.
Runtime destinations come from the app-agent conversation context assigned by
provider/scheduled launches. App tools fail without that context; tool arguments
cannot choose another destination. Handler selection always supplies a complete
destination independently.
Incoming subscriptions belong to the app and are attached via
launch requests or the app subscriptions API; configs have no `listeners` block.
`apps get` and `apps definitions` show `capabilities.subscriptions`, whose local
type names map to `conversation_schema` and supported `events`. Tools and handlers
expose static `input_schema`. A launcher adds its provider's missing tools and
handler to future agents; editing settings does not rewrite existing agents.

```sh
omnara apps disconnect "$APP_ID"
omnara apps delete "$APP_ID"
```

Disconnect stops provider access for that app and keeps its setup for reconnecting.
Delete removes that app and revokes its capabilities. Other apps keep their own
lifecycle, including apps sharing a credential. Existing agents and history remain
usable through the dashboard and API.
