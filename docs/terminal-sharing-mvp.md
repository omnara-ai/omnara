# Terminal Sharing MVP

## Purpose
Give an `omnara` user the experience of running their local agent CLI while simultaneously mirroring that terminal session to web and mobile clients. The MVP should be production-friendly, use the same authentication as the rest of Omnara, and leave room to introduce a remote sandbox hop later without rewriting the foundations.

## Scope for the MVP
- Wrap an agent CLI (Claude, AMP, etc.) in a pseudo-terminal so it behaves exactly as if it were running in the user’s shell.
- Stream that PTY over a single outbound WebSocket connection to the relay.
- Let web/mobile viewers attach over WebSockets, replay buffered output, and send keystrokes back through the relay.
- Authenticate every hop with the existing JWT API keys; never rely on plain prefixes or unauthenticated session IDs.

## Why PTY + WebSockets?
**PTY wrapper** — Most agent CLIs expect a real terminal: they emit ANSI control sequences, set raw mode, handle window resizing, and sometimes launch full-screen TUIs. A pseudo-terminal lets us capture the exact byte stream they expect without patching the agent binary.

**WebSocket tunnel** — A single outbound WebSocket connection keeps the local machine behind NAT/firewalls, rides over standard HTTPS infrastructure (works on Render), and uses the same framing + authentication as our viewer channel. We still get full-duplex streaming and window-resize signaling without requiring raw TCP exposure.

## High-Level Architecture
```
Local Agent           Relay (MVP)                   Web / Mobile Client
──────────────        ────────────────              ───────────────────
Agent CLI             WebSocket server             Browser / App
│                     SessionManager                WebSocket link
├─Spawned in PTY      │                            │
├─WebSocket client ───┼▶ Auth JWT, create Session  │
│                     ├▶ Buffer + broadcast bytes  ├▶ Replay history
│                     └▶ WebSocket router          └▶ Send keystrokes
```

### Component Responsibilities
- **Local session wrapper (`omnara.session_sharing`)**
  - Resolve agent executable, set env (e.g. `OMNARA_API_KEY`).
  - Spawn the agent inside a PTY.
  - Open an outbound WebSocket connection to the relay using the API key for authentication.
  - Copy output from PTY → local stdout + relay socket; copy input from viewers + local keyboard → PTY.

- **Relay WebSocket server (`relay_server`)**
  - Authenticate the API key JWT (`decode_api_key`) and hash it for session lookups.
  - Maintain per-user sessions in memory, including bounded history buffers and heartbeat cleanup.
  - Fan PTY output to all registered WebSocket clients and pipe WebSocket input back to the upstream agent socket.

- **WebSocket router**
  - Accept either a hashed API key (CLI clients) or the user's Supabase access token (dashboard/mobile viewers).
  - List active sessions for the authenticated user.
  - When a client joins a session, replay buffered history, stream live output, and accept `{"type": "input"}` events.

## Auth & Identity Flow
1. User runs `run_omnara_session` → we fetch/store an API key via the normal CLI auth flow.
2. The local wrapper opens an authenticated WebSocket connection using that API key. The relay validates the JWT, extracts `user_id`, and stores a hash of the key alongside the session.
3. Web/mobile clients present their Supabase access token (via `Authorization: Bearer` or negotiated WebSocket subprotocol). The relay verifies the token with the shared Supabase helper, checks that the `user_id` matches the session owner, and authorizes the viewer without prompting for an API key.

This keeps the MVP simple while aligning with the broader platform security model—no new key formats or storage schemes.

## Data Flow Summary
1. Local agent writes bytes → PTY master captures them.
2. Wrapper echoes bytes to local stdout and forwards them to the upstream WebSocket.
3. Relay appends bytes to the session ring buffer and broadcasts them to connected WebSockets.
4. Web client sends input (`{"type": "input", "data": "ls\n"}`) → relay forwards it to the upstream socket → PTY receives it and the agent reacts.
5. Heartbeats and exit handling ensure the session cleans up when the agent terminates.

## Extension Path: Remote Sandbox (Future)
The same primitives support the longer-term goal where the relay can fail over to a hosted sandbox when the local machine disconnects:
```
Local PTY  ←WebSocket→  Relay  ←WebSocket→  Sandbox PTY  ←WebSocket→  Clients
```
- **State sync**: when both local and sandbox are connected, rsync project state through the relay using an authenticated WebSocket tunnel.
- **Session promotion**: relay designates one PTY as the authoritative source; when the local machine drops, the sandbox session continues broadcasting to clients without interruption.
- **Input routing**: relay forwards client keystrokes to whichever PTY is active (local or sandbox) using the same `SessionManager` abstraction.

Because the relay already stores user identity alongside the session and all transports use the same frame encoding, adding the sandbox hop later primarily means standing up another outbound WebSocket client within the relay and swapping which PTY the viewer channel broadcasts.

## Current Status
### Local wrapper
- PTY output is framed into binary messages (`FRAME_TYPE_OUTPUT`) before being shipped over the upstream WebSocket connection, which avoids echo loops and preserves Ink control sequences.
- Viewer input is forwarded as JSON (`{"type":"input","data":...}`) so keystrokes work in both directions.
- Log output is persisted to `src/logs/session_sharing.log` with per-frame tracing to make debugging easier.

### Relay
- The aiohttp-based relay terminates authenticated WebSocket connections from both agents (`/agent`) and viewers (`/terminal`), decoding frames via `SessionManager` before broadcasting them.
- `Session.forward_input` wraps keystrokes in `FRAME_TYPE_INPUT` frames before writing back to the agent socket, so control flows mirror the viewer protocol.
- Extensive tracing lives in `logs/relay.log` so we can follow each frame end-to-end.

### Web app integration
- `agent_instances` rows now expose an optional `instance_metadata.transport` flag. Non-streaming runs leave metadata `null`; terminal sessions set it to `"ws"` so the dashboard can switch to a live terminal view.
- The local CLI registers its agent instance against the unified server before dialing the relay and persists the returned id in `OMNARA_AGENT_INSTANCE_ID`, avoiding duplicate rows on reconnect.
- The Python SDK calls `POST /api/v1/agent-instances` in create-only mode. When the API returns `409 Conflict` for an existing id, the SDK fetches the row via `GET /api/v1/agent-instances/{id}` and reuses it without mutating metadata.
- The dashboard keeps using the existing `/api/v1/agent-instances` + detail endpoints; when `instance_metadata.transport === "ws"` the page swaps the chat transcript for an xterm-based terminal component that connects directly to the relay.
- Web clients present their Supabase access token and the target `agent_instance_id` to the relay. The relay verifies the token with the shared helper, checks permissions via the shared SQLAlchemy session, and attaches the viewer to the right PTY stream—no extra backend hop required.
- `LiveTerminal` (React) encapsulates the full browser flow:
  1. Resolve relay host/port via `VITE_RELAY_HOST`, `VITE_RELAY_PORT`, `VITE_RELAY_SECURE`; `/api/v1/sessions` shares the same HTTP origin, `/terminal` is the WS path.
  2. Fetch the Supabase access token (`supabase.auth.getSession()`), send it as `Authorization: Bearer <token>` to list sessions (`{ relayHttpBase }/api/v1/sessions`).
  3. Instantiate xterm.js + `FitAddon`; register a CSI `J` handler to clear scrollback for Ink full-screen wipes.
  4. Hook `term.onData` and `term.onResize` to emit `{type:'input', data}` and `{type:'resize_request', cols, rows}` JSON payloads.
  5. Open `ws://{relayHost}:{relayPort}/terminal` with subprotocol `omnara-supabase.<token>` (avoids custom headers in browsers). Immediately send `{type:'join_session', session_id}`.
  6. Decode binary frames (1 byte type + 4 byte length). Type `0` → UTF-8 PTY output, type `2` → PTY resize (`!HH` rows/cols). Write text into xterm; apply remote resize frames to keep Ink in sync.
  7. Use a `ResizeObserver` + `fitAddon.fit()` so the terminal adapts when the dashboard layout changes; the web client only calls `fit()` when container dimensions actually change to reduce caret flicker.
- No API key ever hits the browser—everything is authenticated with the Supabase session so mobile can reuse the same mechanism.

### Mobile app integration
- Follow the same authentication and streaming protocol implemented in the web dashboard:
  1. Obtain the Supabase access token (React Native Supabase SDK or equivalent) and call `GET {relayHttpBase}/api/v1/sessions` with `Authorization: Bearer <token>` to discover active terminal sessions.
  2. Connect to `ws(s)://{relayWsBase}/terminal` using a WebSocket client that supports custom subprotocols; supply `omnara-supabase.<token>` so the relay authenticates the viewer without headers, then send `{type:'join_session', session_id}`.
  3. Reuse the relay frame protocol (1 byte type + 4 byte length). Type `0` → UTF-8 PTY output chunk, type `2` → resize payload (`!HH` rows/cols). JSON messages deliver `error`, `resize`, and `session_ended` notifications.
  4. Render terminal output either by embedding the existing `LiveTerminal` component inside a `react-native-webview` (bridge keystrokes and resize via `postMessage`) or by implementing a native terminal emulator that handles ANSI escape sequences and scrollback.
  5. Forward user keystrokes as `{type:'input', data}` and propagate viewport changes (orientation, keyboard open) as `{type:'resize_request', cols, rows}`.
  6. Observe the same configuration surface as web (`RELAY_HOST`, `RELAY_PORT`, `RELAY_SECURE`) so dev/prod relay targets can be swapped without code changes.
- Disconnection behaviour matches web: the relay retains the session while the CLI is connected, and the mobile client can reconnect by reusing the same `agent_instance_id`.

### Unified server + CLI wiring (done)
- Unified server endpoint: `POST /api/v1/agent-instances` (served by the write-focused agent API) accepts friendly names and an optional transport override. Supplying an existing id now returns `409 Conflict`—the route never mutates prior metadata.
- The Python SDK exposes `register_agent_instance()` and `update_agent_instance_status()` helpers (async + sync). The register helper handles conflicts by fetching the current row when the API reports that the id already exists.
- For local development, point the CLI at the unified server (default `http://127.0.0.1:9000`) using the `--base-url` flag so registration reaches the write service.
- `run_agent_with_relay` calls the SDK before opening the relay WebSocket, persists `OMNARA_AGENT_INSTANCE_ID` in the child env, and marks the instance `COMPLETED` when the agent exits cleanly. Reconnects reuse `--agent-instance-id` or the stored env var without re-registering.
- Agent instance rows now rely on the optional `instance_metadata.transport` flag to give the dashboard the context it needs to show live terminal streams alongside chat transcripts.

### Mobile client considerations
- Quickest React Native path is to embed the existing viewer inside `react-native-webview`, reuse the same WebSocket + frame protocol, and bridge keystrokes/resize events with `postMessage`. This keeps xterm.js and the fit addon intact so mobile mirrors web behavior.
- Longer-term native option: pair a headless terminal emulator (e.g., `xterm-headless`) with a custom React Native renderer for tighter UI integration, though this requires reimplementing scrollback, cursor, and resize math.

## Implementation Checklist (MVP)
- [x] PTY + WebSocket wrapper on the local machine (`session_sharing.py`).
- [x] Relay WebSocket server authenticates JWTs and tags sessions with `user_id` + `api_key_hash`.
- [x] WebSocket router enforces the same auth and routes input/output.
- [x] Lightweight viewer for manual testing (`apps/relay-viewer`).
- [ ] Production viewer in `apps/web` with polished Ink rendering.
- [ ] Tests around session lifecycle, auth failures, and history buffering.

## Next steps: dashboard terminal view
- Update `apps/web/src/components/dashboard/instances/InstanceDetail.tsx` so it inspects `instance.instance_metadata?.transport`. When the value is `"ws"`, skip mounting the chat interface and render a dedicated terminal panel instead.
- Port the existing viewer implementation into the dashboard: (1) add `xterm` and `xterm-addon-fit` to the web app (`cd apps/web && npm install xterm xterm-addon-fit`), (2) create a reusable `LiveTerminal` component that mirrors the logic in `apps/relay-viewer/public/index.html` (instantiate `Terminal`, load the fit addon, register the CSI clear handler, forward keystrokes/resize events), and (3) encapsulate the relay WebSocket handshake in a helper that reuses the proxy behaviour from `apps/relay-viewer/server.js` (fetch `/api/v1/sessions`, then open `/ws` with `join_session`).
- Make the relay host/port configurable for the dashboard (for example `VITE_RELAY_HOST`/`VITE_RELAY_PORT`) and honour them when connecting so local dev can hit `127.0.0.1` while production points at the managed relay.
- Surface connection state in the UI (connecting/streaming/disconnected) and provide an easy fallback back to the transcript for runs without terminal streaming.
- Leave the existing chat rendering path untouched for instances without a transport flag so legacy conversations continue to work.

## Known Issue
- **Ink repaint duplication**: Claude Code redraws the entire screen on each update. We now stream PTY resize events to web viewers so xterm mirrors the agent’s actual columns/rows, eliminating the duplicated blocks that appeared when the remote terminal wrapped differently than the source. Remaining rough edge: when the host terminal is dramatically larger than the viewer’s initial viewport, the viewer needs a manual resize (or a future “fit on connect”) to catch up before a remote keystroke arrives.
- **Control arbitration**: whichever viewer sends input last now drives the PTY size. This is ideal for mobile typing, but we may eventually want a “take control” toggle if multiple viewers edit simultaneously.
- **Relay persistence**: in-memory ring buffers vanish on relay restart. Sessions continue streaming live data, but historical terminal output is lost until we add optional persistence.

## Future Considerations
- Persistent storage (Redis or Postgres) if we need multi-process relay workers or durable session history.
- End-to-end encryption assertions if we introduce hosted sandboxes.
- Rate limiting on WebSocket input to prevent abuse.
- Optional mTLS or token rotation for the agent WebSocket endpoint.

## Transport Configuration
- CLI agents stream over WebSockets by default. Environment overrides exist for host, port, and protocol (`OMNARA_RELAY_HOST`, `OMNARA_RELAY_WS_HOST`, `OMNARA_RELAY_WS_PORT`, `OMNARA_RELAY_WS_SCHEME`, `OMNARA_RELAY_WS_PATH`, and `OMNARA_RELAY_WS_SKIP_VERIFY` for self-signed TLS).
- Setting `OMNARA_RELAY_DISABLED=1` skips session sharing entirely; no alternative transport is supported.
- Both agent and viewer sockets use the same framing (`FRAME_TYPE_*`) and JWT authentication helpers, so dashboards and mobile clients receive identical output regardless of platform.

This document should evolve as we harden the relay and begin the sandbox work, but the fundamentals above describe the plan we are executing right now.

## Mobile Terminal Enhancement: Arrow Keys & Escape

### Problem
Mobile keyboards lack physical arrow keys (←, →, ↑, ↓) and escape key, which are essential for terminal navigation, command history, and text editing. Users need virtual buttons to send these key sequences.

### Solution
Add a keyboard accessory toolbar with arrow keys and escape button below the terminal view. These buttons will inject the appropriate ANSI escape sequences into the terminal input stream.

### ANSI Escape Sequences
- **Arrow Up**: `\x1b[A`
- **Arrow Down**: `\x1b[B`
- **Arrow Right**: `\x1b[C`
- **Arrow Left**: `\x1b[D`
- **Escape**: `\x1b`

### Implementation Plan

#### 1. Create TerminalKeyboardAccessory Component
**Location**: `apps/mobile/src/components/terminal/TerminalKeyboardAccessory.tsx`

**Features**:
- Horizontal row of buttons: Escape, ←, ↓, ↑, →
- Use lucide-react-native icons for visual clarity
- Styled to match the terminal's dark theme
- Compact design that doesn't take excessive space
- Callbacks to send key sequences to terminal

**Props**:
```typescript
interface TerminalKeyboardAccessoryProps {
  onKeyPress: (sequence: string) => void;
  disabled?: boolean;
}
```

**Design**:
- Dark background (#1a1a1a or similar) matching terminal
- Buttons with subtle borders and hover states
- Icon-based buttons for arrows
- Text label for "Esc" key
- Spacing between buttons for touch targets
- Responsive sizing for different screen sizes

#### 2. Update TerminalMobileTerminal Component
**Location**: `apps/mobile/src/components/terminal/TerminalMobileTerminal.tsx`

**Changes**:
- Add method `sendKeySequence(sequence: string)` that posts a message to the WebView
- Expose this method through a ref or prop callback
- The method should construct an input message: `{type: 'keySequence', data: sequence}`

**WebView HTML Updates** (within `buildTerminalHtml()`):
- Add handler for `keySequence` message type in `handleInboundMessage`
- When received, call `sendInput(data)` with the sequence
- This reuses existing input infrastructure

#### 3. Update TerminalInstancePanel Component
**Location**: `apps/mobile/src/components/terminal/TerminalInstancePanel.tsx`

**Changes**:
- Import and render `TerminalKeyboardAccessory` below the terminal
- Create ref to `TerminalMobileTerminal` to access `sendKeySequence` method
- Connect accessory's `onKeyPress` to terminal's `sendKeySequence`
- Adjust layout to accommodate the accessory bar

**Layout Structure**:
```
<View style={styles.container}>
  <TerminalMobileTerminal ref={terminalRef} ... />
  <TerminalKeyboardAccessory onKeyPress={handleKeyPress} />
</View>
```

#### 4. Update Index Export
**Location**: `apps/mobile/src/components/terminal/index.ts`

Add export for the new component:
```typescript
export * from './TerminalKeyboardAccessory';
```

### Visual Design Mockup
```
┌─────────────────────────────────────┐
│                                     │
│         Terminal Output             │
│         (WebView)                   │
│                                     │
└─────────────────────────────────────┘
┌─────────────────────────────────────┐
│  [Esc] [←] [↓] [↑] [→]             │ <- Keyboard Accessory
└─────────────────────────────────────┘
```

### Testing Checklist
- [ ] Arrow keys navigate command history (Up/Down)
- [ ] Arrow keys move cursor in command line (Left/Right)
- [ ] Escape key cancels current input
- [ ] Escape key works in vi/vim mode
- [ ] Buttons work on both iOS and Android
- [ ] Touch targets are adequate (minimum 44x44 points)
- [ ] Works in both portrait and landscape
- [ ] Disabled state works correctly
- [ ] Integrates cleanly with terminal resize

### Future Enhancements
- Add Tab key for autocompletion
- Add Ctrl/Alt modifier keys
- Add F1-F12 function keys (collapsible)
- Add common key combinations (Ctrl+C, Ctrl+Z)
- Make keyboard customizable per user preferences
- Support swipe gestures on the terminal for quick navigation
