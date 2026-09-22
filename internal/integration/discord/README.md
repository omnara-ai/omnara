# Discord protocol notes

This package owns REST, Gateway v10 and signed HTTP interactions. Routing, leases,
credentials and agent transactions belong to callers; see the
[app contribution guide](../CONTRIBUTING.md). Setup instructions belong in the
[Discord user guide](../../../docs/integrations/discord.mdx).

## Identity and Gateway recovery

Application ID and bot user ID are distinct. Discover both using
[`/applications/@me`](https://docs.discord.com/developers/resources/application#get-current-application)
and [`/users/@me`](https://docs.discord.com/developers/resources/user#get-current-user),
and check them again in READY. Guild ID belongs to a destination, not app identity.

The owned Gateway client uses `coder/websocket` and deliberately leaves reconnect
control to the worker. After every disconnect, reload the durable checkpoint;
received sequence is not proof that an event was committed. `CommitDispatch` must
atomically persist the dispatch or ignored-event decision with its checkpoint,
under the app runtime lease. This also covers READY/RESUMED. A callback failure
must not advance the checkpoint. A commit racing cancellation may have succeeded,
which is why reconnect cannot reuse only in-memory state.

Independent saved apps may share a physical bot but keep separate leases and
receipts. IDENTIFY budgets and concurrency buckets are bot-wide; RESUME does not
consume an IDENTIFY permit. Heartbeats continue while waiting for a permit.
Follow [Gateway session limits and resume rules](https://docs.discord.com/developers/events/gateway)
and [close codes](https://docs.discord.com/developers/topics/opcodes-and-status-codes).
Shutdown closes TCP rather than sending session-invalidating codes 1000/1001.

Dispatch commits are sequential while the control loop handles heartbeats. Bounded
read-ahead applies backpressure; paused reading permits one extra heartbeat
interval because an ACK may be buffered behind dispatches. Only an ACK renews that
grace. Sustained backlog can still reconnect. The finite provider replay window
can expire; this is not an unlimited event archive.

Frames are bounded at 2 MiB. An oversized replayed frame can repeatedly disconnect
the client and require intervention; it is never silently skipped. Only Guild
Messages and Message Content intents are requested, avoiding unused guild snapshots.
Changing intents takes effect on the next IDENTIFY, not a resumed session.

## Threads and sends

A [thread started from a message](https://docs.discord.com/developers/resources/channel#start-thread-from-message)
uses that message's ID. `EnsureThread` reconciles uncertain creation by GET at that
identity rather than blindly retrying POST. The caller must freeze authorized
recipients before creating the thread and recheck authority before every attempt.
Stopping an agent never archives, deletes or otherwise mutates its Discord thread.

Message creation uses the same logical-send nonce and payload for bounded retries
with `enforce_nonce`. Discord's nonce deduplication window is short; a final unknown
result does not authorize a fresh retry later. See
[Create Message](https://docs.discord.com/developers/resources/message#create-message).
Rate limits surface `RetryAfter` and `Global` to the caller; there is no bot-wide
REST scheduler. See
[Discord rate limits](https://docs.discord.com/developers/topics/rate-limits).

Attachment downloads refresh scoped message metadata, validate membership and CDN
origin/path, and send no bot authorization to the CDN. Upload/download byte limits
are enforced by the client; aggregate intake and artifact authorization belong to
the caller. Redirects must not forward credentials.

## Interaction callbacks

Ordinary messages use Gateway; questions, approvals and profile selections use
signed HTTP callbacks. Discord makes HTTP and Gateway interaction delivery
mutually exclusive. There is no fallback queue of expiring interaction tokens.
See [interaction transports and acknowledgement deadlines](https://docs.discord.com/developers/interactions/receiving-and-responding).

Callback owner IDs are untrusted routing hints. The captured app's public key must
verify the timestamp and raw body before its owner, live setup and conversation
can authorize an answer. PING has no captured owner and may use any matching active
setup. Independent apps sharing a bot cannot borrow one another's callback authority.
See [Ed25519 verification](https://docs.discord.com/developers/interactions/overview#validating-security-request-headers).

Commit the answer or selected-profile handoff before acknowledging, within the
two-second intake context that leaves room for Discord's three-second deadline.
Opening a modal is not an answer; replay must reproduce it without resolving the
interaction. Any verified participant in the captured conversation may answer.
Provider presentation failure leaves the dashboard/API interaction available.
