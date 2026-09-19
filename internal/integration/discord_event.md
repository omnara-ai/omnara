# Discord inbox adapter handoff

Construct `NewDiscordAppInboxProvider(discord.Config{HTTPClient: client}, secrets,
connections)` and register it as the `"discord"` `AppInboxProvider`. It consumes
the runtime's JSON-encoded `discord.Dispatch`; no envelope or checkpoint change
is needed. `Expand` and `DownloadFile` match the existing consumer interface.
Only trusted durable dispatches enter this adapter. Gateway ownership, leases,
checkpoints and signed interaction callbacks remain with their current owners.

`NormalizeDiscordAppEvent(connection, raw, channel)` is pure. Its channel argument
must come from authenticated REST `GetChannel`; expansion performs that lookup.
The dispatch's guild ID is mandatory and must match REST metadata. Connection
`ProviderTenantID` is the App ID, `ProviderAccountRef` is bot user ID, and neither
is a guild. Metadata includes source channel, parent channel, thread, message ID
and timestamp without credentials or CDN URLs. Message IDs give stable semantic
keys across Gateway sessions and sequence changes.

Normal human messages/replies steer and cancel open interactions. Self, bot,
webhook, system, DM and mutation events are excluded again even though runtime
intake already filters automation. Only native Discord mentions count; text
resembling a mention does not. An ordinary root-channel message yields no event.
A thread reply uses its REST-proven immutable parent and thread IDs.

## Recipient selection and thread preparation

Expansion makes no provider mutations. For a root mention it uses the documented
fact that a thread created from a message has that message's ID, producing the
stable future `parent:messageID` scope. After freezing a **nonempty authorized
plan**, before admitting an input/launch, `AppInboxConsumer` calls:

```go
provider.PrepareConversation(ctx, connection, raw, frozenScope, authority)
```

`frozenScope` is `appdefinition.DiscordScope` from the frozen plan.
`authority(context.Context) error` must revalidate the receipt lease and a live
recipient/listener/launcher that accepts that exact scope. It runs before every
HTTP attempt, including the eventual thread POST. The provider independently
rechecks connection identity/revision, current credential version and project
secret availability. A nil authority callback is rejected. Empty plans must not
invoke this method. An unconditional success callback is not valid worker wiring.

The existing provider `EnsureThread` reuses the one thread attached to the source
message and reconciles uncertain creation by that immutable ID. Re-running
preparation does not create another thread. Existing thread replies make no
provider mutations. There is no stop/delete/archive/join side effect.

For ordinary thread replies, routing must require an exact selected/listening or
followed thread. A channel-scoped tool resource containing a thread is not by
itself permission to receive every message in that thread. Parent/guild addresses
remain useful for explicit mention launchers; keep that separate from ordinary
reply listener matching in the router/admission checks.

## Media and credentials

The adapter uses the project's current generic bot-token secret, including org
grants. It checks `/users/@me` and `/applications/@me` so rotation cannot silently
substitute a different bot. Every REST/CDN attempt and final expansion publication
rechecks availability and captured connection/credential revisions. Calls share
one 15-second context and the provider's bounded retry/rate-limit behavior.

Attachments are refreshed by captured message and attachment ID, never fetched
from a raw dispatch URL. Downloads use the existing provider's restricted CDN
paths and carry no bot token. The captured ID, filename and declared size must
match refreshed metadata. Expansion caps 10 files, 8 MiB per file and 24 MiB total
downloaded bytes, including unsupported files. Model-context supported media
types and canonical MIME parsing determine admission. Empty/oversized/unsupported
files have explicit omission metadata/text; transient and rate-limit failures
retain the receipt for retry instead of silently dropping media.

Successful downloads produce `AppPlannedFile.Expected` digests and ephemeral file
bytes. `appRecipientContent` assigns recipient artifact IDs; the existing consumer
and artifactstore handle frozen-blob upload and atomic metadata/input admission.
Recovery rehydrates only IDs present in the original dispatch. The consumer
checks bytes, type, name and digest against the frozen plan and rejects drift.
The adapter does not write artifact rows or blobs itself.

Primary evidence checked September 18, 2026:

- [Gateway message-create fields](https://docs.discord.com/developers/events/gateway-events#message-create)
- [Thread identity and creation](https://docs.discord.com/developers/resources/channel#start-thread-from-message)
- [Attachment structure](https://docs.discord.com/developers/resources/message#attachment-object)

`discord_event_test.go` uses local HTTP/CDN fixtures and capability fakes to cover
scope/identity validation, human steering, replay keys, authorization loss before
mutation and after downloads, single-thread reuse, bounded files, rate limits,
and the existing consumer's artifact upload/recovery/digest contract. No live
provider calls, new dependencies, schema/store changes or runtime edits.
