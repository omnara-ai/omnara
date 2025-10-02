"""WebSocket broadcasting for the omnara terminal relay."""

from __future__ import annotations

import json
import re
import struct
from dataclasses import dataclass
from typing import Any

from aiohttp import WSCloseCode, web

from .auth import (
    RelayAuthError,
    RelayCredentials,
    build_credentials_from_api_key,
    build_credentials_from_supabase,
)
from .protocol import (
    FRAME_TYPE_INPUT,
    FRAME_TYPE_METADATA,
    FRAME_TYPE_OUTPUT,
    FRAME_TYPE_RESIZE,
    iter_frames,
    pack_frame,
)
from .sessions import SessionManager


API_KEY_PROTOCOL_PREFIX = "omnara-key."
SUPABASE_PROTOCOL_PREFIX = "omnara-supabase."
RESIZE_STRUCT = struct.Struct("!HH")
SANITIZE_HISTORY_AGENTS: set[str] = {"codex"}


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
            await ws.close(code=WSCloseCode.POLICY_VIOLATION, message=b"auth")
            return ws

        ws_kwargs: dict[str, Any] = {}
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
        if not session_id:
            await ws.send_json({"error": "Missing session_id"})
            return

        session = await self._manager.get_session(
            credentials.user_id, session_id, credentials.api_key_hash
        )
        if not session:
            await ws.send_json({"error": "Session not found"})
            return

        session.register_websocket(ws)

        try:
            await ws.send_json(
                {
                    "type": "resize",
                    "session_id": session.session_id,
                    "cols": session.cols,
                    "rows": session.rows,
                }
            )
            await ws.send_json(
                {
                    "type": "agent_metadata",
                    "session_id": session.session_id,
                    "metadata": dict(session.metadata),
                }
            )
            # Stream buffered history. Some TUI agents (e.g., Codex) emit destructive clears as part
            # of their redraw loop; we strip those while replaying history to avoid wiping the freshly
            # reconstructed scrollback, but only for agents that opt-in via metadata or known
            # defaults.
            for chunk in session.iter_history():
                if not chunk:
                    continue

                sanitized = chunk
                history_policy = session.metadata.get("history_policy")
                agent_name = session.metadata.get("agent")
                app_name = session.metadata.get("app")
                if (
                    history_policy == "strip_esc_j"
                    or (
                        agent_name is not None and agent_name in SANITIZE_HISTORY_AGENTS
                    )
                    or app_name == "codex"
                ):
                    sanitized = re.sub(rb"\x1b\[[0-3]?J", b"", chunk)
                    if not sanitized:
                        continue

                frame = pack_frame(FRAME_TYPE_OUTPUT, sanitized)
                await ws.send_bytes(frame)

            await ws.send_json(
                {"type": "history_complete", "session_id": session.session_id}
            )

            async for msg in ws:
                if msg.type == web.WSMsgType.TEXT:
                    data = json.loads(msg.data)
                    if data.get("type") == "input":
                        session.forward_input(data.get("data", ""))
                    elif data.get("type") == "resize_request":
                        session.request_resize(data.get("cols"), data.get("rows"))
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
            await ws.close(code=WSCloseCode.POLICY_VIOLATION, message=b"auth")
            return ws

        credentials = bundle.credentials
        if credentials.api_key_hash is None:
            ws = web.WebSocketResponse()
            await ws.prepare(request)
            await ws.send_json({"error": "API key credentials required"})
            await ws.close(code=WSCloseCode.POLICY_VIOLATION, message=b"auth")
            return ws

        session_id = request.query.get("session_id") or request.match_info.get(
            "session_id"
        )
        if not session_id:
            ws = web.WebSocketResponse()
            await ws.prepare(request)
            await ws.send_json({"error": "Missing session_id"})
            await ws.close(code=WSCloseCode.POLICY_VIOLATION, message=b"session_id")
            return ws

        ws_kwargs: dict[str, Any] = {}
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
                        elif frame_type == FRAME_TYPE_METADATA:
                            try:
                                metadata = json.loads(payload.decode("utf-8"))
                            except (UnicodeDecodeError, json.JSONDecodeError) as exc:
                                print(
                                    f"[relay] metadata decode FAILED session={credentials.user_id}:{session_id} error={exc!r}",
                                    flush=True,
                                )
                                continue

                            if isinstance(metadata, dict):
                                session.apply_metadata(metadata)
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
