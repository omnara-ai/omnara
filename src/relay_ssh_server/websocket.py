"""WebSocket broadcasting for omnara SSH relay."""

from __future__ import annotations

import json
from dataclasses import dataclass

from aiohttp import WSCloseCode, web

from .auth import (
    RelayAuthError,
    RelayCredentials,
    build_credentials_from_api_key,
    build_credentials_from_supabase,
)
from .protocol import FRAME_TYPE_OUTPUT, pack_frame
from .sessions import SessionManager


API_KEY_PROTOCOL_PREFIX = "omnara-key."
SUPABASE_PROTOCOL_PREFIX = "omnara-supabase."


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

