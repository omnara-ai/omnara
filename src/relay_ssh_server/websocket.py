"""WebSocket broadcasting for omnara SSH relay."""

from __future__ import annotations

import json
from aiohttp import WSCloseCode, web

from .auth import RelayAuthError, decode_api_key, hash_api_key
from .protocol import FRAME_TYPE_OUTPUT, pack_frame
from .sessions import SessionManager


class WebsocketRouter:
    """Routes websocket connections to active sessions."""

    def __init__(self, manager: SessionManager):
        self._manager = manager

    async def handle(self, request: web.Request) -> web.WebSocketResponse:
        ws = web.WebSocketResponse()
        await ws.prepare(request)

        api_key = request.headers.get("X-API-Key", "")
        try:
            user_id = decode_api_key(api_key)
            api_key_hash = hash_api_key(api_key)
        except RelayAuthError:
            await ws.send_json({"error": "Authentication failed"})
            await ws.close(code=WSCloseCode.POLICY_VIOLATION, message="auth")
            return ws

        sessions = await self._manager.sessions_for_user(user_id, api_key_hash)
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
                        user_id,
                        api_key_hash,
                        payload.get("session_id"),
                    )
            elif msg.type == web.WSMsgType.ERROR:
                break

        await ws.close()
        return ws

    async def _handle_join(
        self,
        ws: web.WebSocketResponse,
        user_id: str,
        api_key_hash: str,
        session_id: str | None,
    ) -> None:
        if not session_id:
            await ws.send_json({"error": "Missing session_id"})
            return

        session = await self._manager.get_session(user_id, session_id, api_key_hash)
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
                elif msg.type in (
                    web.WSMsgType.CLOSE,
                    web.WSMsgType.CLOSED,
                    web.WSMsgType.ERROR,
                ):
                    break
        finally:
            session.unregister_websocket(ws)

    async def list_sessions(self, request: web.Request) -> web.Response:
        api_key = request.headers.get("X-API-Key", "")
        try:
            user_id = decode_api_key(api_key)
            api_key_hash = hash_api_key(api_key)
        except RelayAuthError:
            return web.json_response(
                {"error": "Authentication failed"}, status=web.HTTPUnauthorized.status_code
            )

        sessions = await self._manager.sessions_for_user(user_id, api_key_hash)
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
