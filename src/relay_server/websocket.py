"""WebSocket broadcasting for the omnara terminal relay."""

from __future__ import annotations

import json
import logging
import struct
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path

from aiohttp import WSCloseCode, web

from .auth import (
    RelayAuthError,
    RelayCredentials,
    build_credentials_from_api_key,
    build_credentials_from_supabase,
)
from .protocol import (
    FRAME_TYPE_INPUT,
    FRAME_TYPE_OUTPUT,
    FRAME_TYPE_RESIZE,
    FRAME_TYPE_SWITCH_TO_TMUX,
    FRAME_TYPE_SWITCH_TO_AGENT,
    FRAME_TYPE_MODE_CHANGED,
    iter_frames,
    pack_frame,
)
from .sessions import SessionManager


API_KEY_PROTOCOL_PREFIX = "omnara-key."
SUPABASE_PROTOCOL_PREFIX = "omnara-supabase."
RESIZE_STRUCT = struct.Struct("!HH")

# Setup logging for handoff debugging
HANDOFF_LOG_FILE = Path.home() / ".omnara" / "logs" / "terminal_handoff.log"

def _log_handoff(message: str) -> None:
    """Log handoff events to a dedicated file for debugging."""
    try:
        HANDOFF_LOG_FILE.parent.mkdir(parents=True, exist_ok=True)
        # Clear log file on first write if it's a new session
        timestamp = datetime.utcnow().isoformat()
        with HANDOFF_LOG_FILE.open("a", encoding="utf-8") as f:
            f.write(f"[{timestamp}] [RELAY] {message}\n")
            f.flush()
    except Exception as e:
        print(f"[RELAY] Failed to log: {e}", flush=True)


@dataclass(slots=True)
class CredentialBundle:
    """Container for parsed credentials and optional subprotocol."""

    credentials: RelayCredentials
    negotiated_protocol: str | None = None


def _extract_credentials(request: web.Request) -> CredentialBundle:
    """Parse incoming request headers/subprotocols for relay auth."""

    # Allow explicit API key header (legacy observers).
    header_key = request.headers.get("X-API-Key")
    if header_key:
        credentials = build_credentials_from_api_key(header_key)
        return CredentialBundle(credentials)

    # Use Supabase bearer token if provided.
    auth_header = request.headers.get("Authorization", "")
    if auth_header.lower().startswith("bearer "):
        token = auth_header.split(" ", 1)[1].strip()
        if token:
            credentials = build_credentials_from_supabase(token)
            return CredentialBundle(credentials)

    # Fall back to WebSocket subprotocol negotiation. We support both API keys
    # and Supabase tokens to cover browsers which cannot set custom headers.
    protocol_header = request.headers.get("Sec-WebSocket-Protocol", "")
    if protocol_header:
        for candidate in (part.strip() for part in protocol_header.split(",")):
            if candidate.startswith(API_KEY_PROTOCOL_PREFIX):
                api_key = candidate[len(API_KEY_PROTOCOL_PREFIX) :]
                credentials = build_credentials_from_api_key(api_key)
                return CredentialBundle(credentials, candidate)
            if candidate.startswith(SUPABASE_PROTOCOL_PREFIX):
                token = candidate[len(SUPABASE_PROTOCOL_PREFIX) :]
                credentials = build_credentials_from_supabase(token)
                return CredentialBundle(credentials, candidate)

    raise RelayAuthError("Missing authentication credentials")


class WebsocketRouter:
    """Routes websocket connections to active sessions."""

    def __init__(self, manager: SessionManager):
        self._manager = manager

    async def handle(self, request: web.Request) -> web.WebSocketResponse:
        try:
            bundle = _extract_credentials(request)
        except RelayAuthError as exc:
            ws = web.WebSocketResponse()
            await ws.prepare(request)
            await ws.send_json({"error": str(exc)})
            await ws.close(code=WSCloseCode.POLICY_VIOLATION, message="auth")
            return ws

        ws_kwargs: dict[str, object] = {}
        if bundle.negotiated_protocol:
            ws_kwargs["protocols"] = [bundle.negotiated_protocol]

        ws = web.WebSocketResponse(**ws_kwargs)
        await ws.prepare(request)

        credentials = bundle.credentials
        sessions = await self._manager.sessions_for_user(
            credentials.user_id, credentials.api_key_hash
        )
        await ws.send_json(
            {
                "type": "sessions",
                "sessions": [
                    {
                        "id": session.session_id,
                        "active": session.is_active,
                        "started_at": session.started_at,
                        "ended_at": session.ended_at,
                        "cols": session.cols,
                        "rows": session.rows,
                    }
                    for session in sessions
                ],
            }
        )

        async for msg in ws:
            if msg.type == web.WSMsgType.TEXT:
                payload = json.loads(msg.data)
                if payload.get("type") == "join_session":
                    await self._handle_join(
                        ws,
                        credentials,
                        payload.get("session_id"),
                    )
            elif msg.type == web.WSMsgType.ERROR:
                break

        await ws.close()
        return ws

    async def _handle_join(
        self,
        ws: web.WebSocketResponse,
        credentials: RelayCredentials,
        session_id: str | None,
    ) -> None:
        _log_handoff(f"[ENTRY] _handle_join called with session_id={session_id}")
        if not session_id:
            _log_handoff(f"[ERROR] Missing session_id")
            await ws.send_json({"error": "Missing session_id"})
            return

        _log_handoff(f"[LOOKUP] Getting session {session_id} for user {credentials.user_id}")
        session = await self._manager.get_session(
            credentials.user_id, session_id, credentials.api_key_hash
        )
        if not session:
            _log_handoff(f"[ERROR] Session {session_id} not found")
            await ws.send_json({"error": "Session not found"})
            return

        _log_handoff(f"[SUCCESS] Found session {session_id}, registering websocket")
        session.register_websocket(ws)

        # Automatically switch to tmux when web/mobile connects (if not already in tmux mode)
        _log_handoff(f"Web/mobile client connected to session {session_id}, current mode: {session.mode}")
        if session.mode == "agent":
            _log_handoff(f"Requesting switch to tmux for session {session_id}")
            session.request_switch_to_tmux()
        else:
            _log_handoff(f"Session {session_id} already in mode {session.mode}, not switching")

        try:
            await ws.send_json(
                {
                    "type": "resize",
                    "session_id": session.session_id,
                    "cols": session.cols,
                    "rows": session.rows,
                }
            )
            for chunk in session.iter_history():
                frame = pack_frame(FRAME_TYPE_OUTPUT, chunk)
                await ws.send_bytes(frame)

            async for msg in ws:
                if msg.type == web.WSMsgType.TEXT:
                    data = json.loads(msg.data)
                    if data.get("type") == "input":
                        session.forward_input(data.get("data", ""))
                        session.request_resize(data.get("cols"), data.get("rows"))
                    elif data.get("type") == "resize_request":
                        session.request_resize(data.get("cols"), data.get("rows"))
                    elif data.get("type") == "switch_to_tmux":
                        _log_handoff(f"Manual switch_to_tmux request for session {session_id}")
                        session.request_switch_to_tmux()
                    elif data.get("type") == "switch_to_agent":
                        _log_handoff(f"Manual switch_to_agent request for session {session_id}")
                        session.request_switch_to_agent()
                elif msg.type in (
                    web.WSMsgType.CLOSE,
                    web.WSMsgType.CLOSED,
                    web.WSMsgType.ERROR,
                ):
                    break
        finally:
            session.unregister_websocket(ws)

    async def list_sessions(self, request: web.Request) -> web.Response:
        try:
            bundle = _extract_credentials(request)
        except RelayAuthError as exc:
            return web.json_response(
                {"error": str(exc)},
                status=web.HTTPUnauthorized.status_code,
            )

        sessions = await self._manager.sessions_for_user(
            bundle.credentials.user_id, bundle.credentials.api_key_hash
        )
        return web.json_response(
            {
                "sessions": [
                    {
                        "id": session.session_id,
                        "active": session.is_active,
                        "started_at": session.started_at,
                        "ended_at": session.ended_at,
                        "cols": session.cols,
                        "rows": session.rows,
                    }
                    for session in sessions
                ]
            }
        )


class AgentWebsocketHandler:
    """Handles agent-side websocket connections used as transport."""

    def __init__(self, manager: SessionManager):
        self._manager = manager

    async def handle(self, request: web.Request) -> web.WebSocketResponse:
        try:
            bundle = _extract_credentials(request)
        except RelayAuthError as exc:
            ws = web.WebSocketResponse()
            await ws.prepare(request)
            await ws.send_json({"error": str(exc)})
            await ws.close(code=WSCloseCode.POLICY_VIOLATION, message="auth")
            return ws

        credentials = bundle.credentials
        if credentials.api_key_hash is None:
            ws = web.WebSocketResponse()
            await ws.prepare(request)
            await ws.send_json({"error": "API key credentials required"})
            await ws.close(code=WSCloseCode.POLICY_VIOLATION, message="auth")
            return ws

        session_id = request.query.get("session_id") or request.match_info.get(
            "session_id"
        )
        if not session_id:
            ws = web.WebSocketResponse()
            await ws.prepare(request)
            await ws.send_json({"error": "Missing session_id"})
            await ws.close(code=WSCloseCode.POLICY_VIOLATION, message="session_id")
            return ws

        ws_kwargs: dict[str, object] = {}
        if bundle.negotiated_protocol:
            ws_kwargs["protocols"] = [bundle.negotiated_protocol]

        ws = web.WebSocketResponse(**ws_kwargs)
        await ws.prepare(request)

        session = await self._manager.create_session(
            credentials.user_id, session_id, credentials.api_key_hash
        )
        session.attach_agent_socket(ws)

        await ws.send_json({"type": "ready", "session_id": session_id})

        try:
            async for msg in ws:
                if msg.type == web.WSMsgType.BINARY:
                    buffer = bytearray(msg.data)
                    for frame_type, payload in iter_frames(buffer):
                        if frame_type == FRAME_TYPE_OUTPUT:
                            session.append_output(payload)
                        elif frame_type == FRAME_TYPE_RESIZE:
                            if len(payload) == RESIZE_STRUCT.size:
                                rows, cols = RESIZE_STRUCT.unpack(payload)
                                if rows > 0 and cols > 0:
                                    session.update_size(cols, rows)
                        elif frame_type == FRAME_TYPE_MODE_CHANGED:
                            # Agent is reporting a mode change
                            if len(payload) >= 9:  # 1 byte mode + 4 bytes cols + 4 bytes rows
                                mode_byte = payload[0]
                                mode_map = {0: "agent", 1: "tmux"}
                                mode = mode_map.get(mode_byte, "agent")
                                cols = int.from_bytes(payload[1:5], "big")
                                rows = int.from_bytes(payload[5:9], "big")
                                _log_handoff(f"Agent reported mode change to {mode} ({cols}x{rows}) for session {session_id}")
                                session.broadcast_mode_change(mode, cols, rows)
                        elif frame_type == FRAME_TYPE_INPUT:
                            # Upstream should not send input frames, ignore.
                            continue
                elif msg.type == web.WSMsgType.TEXT:
                    # Reserved for future metadata exchange.
                    continue
                elif msg.type in (
                    web.WSMsgType.CLOSE,
                    web.WSMsgType.CLOSED,
                    web.WSMsgType.ERROR,
                ):
                    break
        finally:
            session.detach_agent_socket()
            await self._manager.end_session(credentials.user_id, session_id)

        return ws
