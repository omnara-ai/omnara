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

## Project integrations

Each project integration owns its provider identity, credentials, and optional launcher.
Create a disconnected draft, connect its account, then choose launch settings.
Names are immutable: 1–32 letters, numbers, or hyphens, starting with a letter.

With an organization and project selected:

```sh
omnara integrations definitions
omnara integrations create --body '{"name":"engineering","integration_type":"slack_thread","settings":{}}'
omnara integrations list
```

Use the returned integration ID as `INTEGRATION_ID`; keep it as `SLACK_INTEGRATION_ID` for the launcher
example below. Connect Slack through browser authorization using an existing
Slack app:

```sh
omnara integrations slack "$INTEGRATION_ID" --client-id "$SLACK_CLIENT_ID" \
  --client-secret "$SLACK_CLIENT_SECRET" --signing-secret "$SLACK_SIGNING_SECRET"
omnara integrations slack --help
omnara integrations get "$INTEGRATION_ID"
```

`integrations slack` also supports automatic Slack app creation with `--app-name` and
`--app-configuration-token`. Reconnecting preserves the saved launcher settings.

For GitHub or Discord, create a project credential with `secrets create`, or reuse
an existing credential of the matching provider kind. Read `setup_revision` from
`integrations get` before configuring the integration. The server verifies the credentials and
rejects a stale revision.

```sh
omnara integrations create --body '{"name":"reviewer","integration_type":"github_pr","settings":{}}'
# Set INTEGRATION_ID to this new integration's ID and SETUP_REVISION to its current setup_revision.
omnara integrations configure "$INTEGRATION_ID" --expected-setup-revision "$SETUP_REVISION" \
  --provider-tenant-id "$GITHUB_APP_ID" --provider-account-ref "$GITHUB_INSTALLATION_ID" \
  --credential-secret-id "$SECRET_ID"
omnara integrations configure --help
```

GitHub uses its numeric App ID and Installation ID; the credential's App ID must
match. Discord uses its Application ID and discovers the bot User ID from its token. Configure Discord's
interaction public key with `--provider-config '{"public_key":"YOUR_64_HEX_PUBLIC_KEY"}'`
before using a launcher or interaction handler. Discord launchers include a handler by default. Set its
Interactions Endpoint URL to
`https://YOUR_OMNARA_HOST/api/integrations/discord/APPLICATION_ID/interactions`.
Omnara manages one Gateway connection per saved Discord integration.

`integrations update` replaces launcher settings and requires the unchanged integration name and
`integration_type`. For the Slack integration above, replace the example workspace and profile
IDs with your own:

```sh
omnara integrations update "$SLACK_INTEGRATION_ID" --body '{
  "name": "engineering",
  "integration_type": "slack_thread",
  "settings": {
    "launcher": {
      "trigger": "mention",
      "scope_kind": "workspace",
      "scope_ref": "T123",
      "slots": [{"key": "default", "agent_profile_id": "aprf_aaaaaaaaaaaaaaaaaaaaaaaaaa"}]
    }
  }
}'
omnara integrations profiles "$SLACK_INTEGRATION_ID" --profile-ids "$PROFILE_ID" --profile-ids "$SECOND_PROFILE_ID"
```

Slack workspace scopes must match the connected workspace. GitHub launchers can use
`repository` with a numeric repository ID and `mention` or `pull_request_opened`.
Discord launchers use `mention` with `scope_kind` and `scope_ref` omitted;
they have no configured server or channel filter.
`integrations profiles` edits offered Slack or Discord profiles while preserving existing
agent slots. One profile launches immediately; several offer a selection menu.

Select tools and interaction handlers independently in agent configurations:
tools use keys such as `int__engineering__post_message`, and interaction handlers
use `engineering: {}`. Tool entries accept permissions, enabled state and deferral.
For the shipped thread and PR tools, destinations come from the integration-agent
conversation context assigned by provider/scheduled launches. These tools fail
without that context; their arguments cannot choose another destination. Integrations can
also define standalone tools that use their credentials without a conversation.
Shipped interaction handlers use the assigned conversation and accept empty arguments.
Incoming subscriptions belong to the integration and are attached via
launch requests or the integration subscriptions API; configs have no `listeners` block.
`integrations get` and `integrations definitions` show optional
`capabilities.subscription.conversation_schema`. Attachments specify the integration
and conversation; the integration determines which activity is forwarded. Tools and
handlers expose static `input_schema`. A launcher adds its provider's missing tools and
handler to future agents; editing settings does not rewrite existing agents.

```sh
omnara integrations disconnect "$INTEGRATION_ID"
omnara integrations delete "$INTEGRATION_ID"
```

Disconnect stops provider access for that integration and keeps its setup for reconnecting.
Delete removes that integration and revokes its capabilities. Other integrations keep their own
lifecycle, including integrations sharing a credential. Existing agents and history remain
usable through the dashboard and API.
