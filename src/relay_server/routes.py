"""FastAPI WebSocket routes for terminal relay."""

from __future__ import annotations

import json
import struct
from dataclasses import dataclass

from fastapi import APIRouter, WebSocket, WebSocketDisconnect, Query, Header
from fastapi.responses import JSONResponse

from relay_server.auth import (
    RelayAuthError,
    RelayCredentials,
    build_credentials_from_api_key,
    build_credentials_from_supabase,
)
from relay_server.protocol import (
    FRAME_TYPE_OUTPUT,
    FRAME_TYPE_RESIZE,
    iter_frames,
    pack_frame,
)
from relay_server.sessions import SessionManager

API_KEY_PROTOCOL_PREFIX = "omnara-key."
SUPABASE_PROTOCOL_PREFIX = "omnara-supabase."
RESIZE_STRUCT = struct.Struct("!HH")


@dataclass(slots=True)
class CredentialBundle:
    """Container for parsed credentials and optional subprotocol."""

    credentials: RelayCredentials
    negotiated_protocol: str | None = None


def _extract_credentials(
    websocket: WebSocket,
    x_api_key: str | None = None,
    authorization: str | None = None,
) -> CredentialBundle:
    """Parse incoming WebSocket headers/subprotocols for relay auth."""

    # Allow explicit API key header
    if x_api_key:
        credentials = build_credentials_from_api_key(x_api_key)
        return CredentialBundle(credentials)

    # Use Supabase bearer token if provided
    if authorization and authorization.lower().startswith("bearer "):
        token = authorization.split(" ", 1)[1].strip()
        if token:
            credentials = build_credentials_from_supabase(token)
            return CredentialBundle(credentials)

    # Fall back to WebSocket subprotocol negotiation
    protocol_header = websocket.headers.get("sec-websocket-protocol", "")
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


def create_relay_router(manager: SessionManager) -> APIRouter:
    """Create FastAPI router for relay WebSocket endpoints."""
    router = APIRouter()

    @router.websocket("/terminal")
    async def terminal_viewer(
        websocket: WebSocket,
        x_api_key: str | None = Header(None, alias="X-API-Key"),
        authorization: str | None = Header(None, alias="Authorization"),
    ):
        """WebSocket endpoint for terminal viewers to watch sessions."""
        try:
            bundle = _extract_credentials(websocket, x_api_key, authorization)
        except RelayAuthError as exc:
            await websocket.accept()
            await websocket.send_json({"error": str(exc)})
            await websocket.close(code=1008)  # Policy violation
            return

        # Accept with negotiated subprotocol if present (required by browser WebSocket spec)
        await websocket.accept(subprotocol=bundle.negotiated_protocol)

        credentials = bundle.credentials
        sessions = await manager.sessions_for_user(
            credentials.user_id, credentials.api_key_hash
        )

        await websocket.send_json(
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

        try:
            while True:
                message = await websocket.receive()
                if message["type"] == "websocket.disconnect":
                    break
                if message["type"] == "websocket.receive":
                    if "text" in message:
                        payload = json.loads(message["text"])
                        if payload.get("type") == "join_session":
                            await _handle_join(
                                websocket,
                                manager,
                                credentials,
                                payload.get("session_id"),
                            )
                            # _handle_join loops until disconnect, so we're done after it returns
                            break
        except WebSocketDisconnect:
            pass

    async def _handle_join(
        websocket: WebSocket,
        manager: SessionManager,
        credentials: RelayCredentials,
        session_id: str | None,
    ) -> None:
        """Handle viewer joining a specific session."""
        if not session_id:
            await websocket.send_json({"error": "Missing session_id"})
            return

        session = await manager.get_session(
            credentials.user_id, session_id, credentials.api_key_hash
        )
        if not session:
            await websocket.send_json({"error": "Session not found"})
            return

        session.register_websocket(websocket)

        try:
            await websocket.send_json(
                {
                    "type": "resize",
                    "session_id": session.session_id,
                    "cols": session.cols,
                    "rows": session.rows,
                }
            )

            # Send history
            for chunk in session.iter_history():
                frame = pack_frame(FRAME_TYPE_OUTPUT, chunk)
                await websocket.send_bytes(frame)

            # Listen for input
            while True:
                message = await websocket.receive()
                print(
                    f"[relay] viewer message type={message['type']} session={session.session_id}",
                    flush=True,
                )
                if message["type"] == "websocket.disconnect":
                    print(
                        f"[relay] viewer disconnected session={session.session_id}",
                        flush=True,
                    )
                    break
                if message["type"] == "websocket.receive" and "text" in message:
                    msg = json.loads(message["text"])
                    if msg.get("type") == "input":
                        session.forward_input(msg.get("data", ""))
                        session.request_resize(msg.get("cols"), msg.get("rows"))
                    elif msg.get("type") == "resize_request":
                        session.request_resize(msg.get("cols"), msg.get("rows"))
        except WebSocketDisconnect as exc:
            print(
                f"[relay] viewer WebSocketDisconnect session={session.session_id} code={exc.code}",
                flush=True,
            )
        except Exception as exc:
            print(
                f"[relay] viewer exception session={session.session_id} error={exc!r}",
                flush=True,
            )
        finally:
            print(
                f"[relay] viewer cleanup session={session.session_id}",
                flush=True,
            )
            session.unregister_websocket(websocket)

    @router.websocket("/agent")
    async def agent_connection(
        websocket: WebSocket,
        session_id: str = Query(...),
        x_api_key: str | None = Header(None, alias="X-API-Key"),
        authorization: str | None = Header(None, alias="Authorization"),
    ):
        """WebSocket endpoint for agent CLI connections."""
        try:
            bundle = _extract_credentials(websocket, x_api_key, authorization)
        except RelayAuthError as exc:
            await websocket.accept()
            await websocket.send_json({"error": str(exc)})
            await websocket.close(code=1008)
            return

        credentials = bundle.credentials
        if credentials.api_key_hash is None:
            await websocket.accept()
            await websocket.send_json({"error": "API key credentials required"})
            await websocket.close(code=1008)
            return

        if not session_id:
            await websocket.accept()
            await websocket.send_json({"error": "Missing session_id"})
            await websocket.close(code=1008)
            return

        # Accept with negotiated subprotocol if present
        await websocket.accept(subprotocol=bundle.negotiated_protocol)

        session = await manager.create_session(
            credentials.user_id, session_id, credentials.api_key_hash
        )
        session.attach_agent_socket(websocket)

        await websocket.send_json({"type": "ready"})
        print(
            f"[relay] agent connected session={session_id}",
            flush=True,
        )

        try:
            while True:
                message = await websocket.receive()
                if message["type"] == "websocket.disconnect":
                    print(
                        f"[relay] agent disconnected session={session_id}",
                        flush=True,
                    )
                    break
                if message["type"] == "websocket.receive" and "bytes" in message:
                    data = message["bytes"]
                    for frame_type, payload in iter_frames(bytearray(data)):
                        if frame_type == FRAME_TYPE_OUTPUT:
                            session.append_output(payload)
                        elif frame_type == FRAME_TYPE_RESIZE:
                            rows, cols = RESIZE_STRUCT.unpack(payload)
                            session.update_size(cols, rows)
        except WebSocketDisconnect as exc:
            print(
                f"[relay] agent WebSocketDisconnect session={session_id} code={exc.code}",
                flush=True,
            )
        except Exception as exc:
            print(
                f"[relay] agent exception session={session_id} error={exc!r}",
                flush=True,
            )
        finally:
            print(
                f"[relay] agent cleanup session={session_id}",
                flush=True,
            )
            session.detach_agent_socket()
            # Mark session as ended so viewers know the agent is done
            session.end()

    @router.get("/api/v1/sessions")
    async def list_sessions(
        x_api_key: str | None = Header(None, alias="X-API-Key"),
        authorization: str | None = Header(None, alias="Authorization"),
    ):
        """REST endpoint to list active sessions."""
        # For HTTP endpoints, we extract credentials directly from headers
        # without needing a WebSocket object
        try:
            if x_api_key:
                credentials = build_credentials_from_api_key(x_api_key)
            elif authorization and authorization.lower().startswith("bearer "):
                token = authorization.split(" ", 1)[1].strip()
                credentials = build_credentials_from_supabase(token)
            else:
                raise RelayAuthError("Missing authentication credentials")
        except RelayAuthError as exc:
            return JSONResponse({"error": str(exc)}, status_code=401)

        sessions = await manager.sessions_for_user(
            credentials.user_id, credentials.api_key_hash
        )
        return {
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

    return router
